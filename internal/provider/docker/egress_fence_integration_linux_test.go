//go:build linux

package docker

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	"github.com/crewship-ai/crewship/internal/provider"
)

// TestEgressFenceIntegration is the red-team proof for #1368 against a real
// daemon: inside a fenced restricted crew, the agent UID (1001) and root
// cannot open a socket to a peer on the same Docker network or query Docker's
// resolver, while the sidecar UID (1002) can. A restart behind the provider's
// back drops the fence, and the next EnsureCrewRuntime puts it back.
//
// The target is a peer container, not the internet, so the test needs no
// external network. Linux only (build tag); skips when Docker is unavailable.
func TestEgressFenceIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	// Not t.TempDir: the crew container writes as uid 1001 under it and the
	// strict cleanup would fail the test on files it cannot unlink.
	tmp, err := os.MkdirTemp("", "crewship-fence-it-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmp)
	sidecarPath := filepath.Join(tmp, "crewship-sidecar")
	build := exec.CommandContext(ctx, "go", "build", "-o", sidecarPath, "github.com/crewship-ai/crewship/cmd/crewship-sidecar")
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("cannot build crewship-sidecar: %v\n%s", err, out)
	}
	entrypointPath := filepath.Join(tmp, "entrypoint.sh")
	if err := os.WriteFile(entrypointPath, []byte("#!/bin/sh\nexec sleep infinity\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	network := "egresspilot-it-" + time.Now().Format("150405")
	t.Setenv("CREWSHIP_RUNTIME", "runc")
	var p *Provider
	p, err = New(ctx, Config{
		RuntimeImage:      "alpine:3",
		DefaultRuntime:    "runc",
		Network:           network,
		OutputBasePath:    tmp,
		SidecarBinaryPath: sidecarPath,
		EntrypointPath:    entrypointPath,
		EgressFenceCrews:  []string{"fence-it"},
	}, nil)
	if err != nil {
		// SKIP-WAIVER(#1368): the fence is installed by a real daemon into a
		// real network namespace; there is nothing to fake. Same guard as
		// TestResilienceNetworkRecreate for runners without Docker.
		t.Skipf("Docker not available: %v", err)
	}
	defer p.Close()
	defer func() { _, _ = p.client.NetworkRemove(context.Background(), network, client.NetworkRemoveOptions{}) }()

	// Peer listening on the crew network: reachable unless the fence holds.
	peer, err := p.client.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config:     &container.Config{Image: "alpine:3", Cmd: []string{"nc", "-lk", "-p", "7000", "-e", "echo", "hi"}},
		HostConfig: &container.HostConfig{NetworkMode: container.NetworkMode(network)},
	})
	if err != nil {
		t.Fatalf("create peer: %v", err)
	}
	defer func() {
		_, _ = p.client.ContainerRemove(context.Background(), peer.ID, client.ContainerRemoveOptions{Force: true})
	}()
	if _, err := p.client.ContainerStart(ctx, peer.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatalf("start peer: %v", err)
	}
	// A second listener that holds each connection open, for the
	// opened-before-the-fence case.
	sink, err := p.client.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config:     &container.Config{Image: "alpine:3", Cmd: []string{"sh", "-c", "while :; do nc -l -p 7001 | while read l; do date +%s%N > /tmp/last; done; done"}},
		HostConfig: &container.HostConfig{NetworkMode: container.NetworkMode(network)},
	})
	if err != nil {
		t.Fatalf("create sink: %v", err)
	}
	defer func() {
		_, _ = p.client.ContainerRemove(context.Background(), sink.ID, client.ContainerRemoveOptions{Force: true})
	}()
	if _, err := p.client.ContainerStart(ctx, sink.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatalf("start sink: %v", err)
	}
	si, err := p.client.ContainerInspect(ctx, sink.ID, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	sinkIP := si.Container.NetworkSettings.Networks[network].IPAddress.String()
	pi, err := p.client.ContainerInspect(ctx, peer.ID, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	peerIP := pi.Container.NetworkSettings.Networks[network].IPAddress.String()

	team := provider.CrewConfig{ID: "fence-it-001", Slug: "fence-it", NetworkMode: "restricted", MemoryMB: 256, CPUs: 0.5}
	cid, err := p.EnsureCrewRuntime(ctx, team)
	if err != nil {
		t.Fatalf("EnsureCrewRuntime: %v", err)
	}
	defer func() { _ = p.RemoveCrewRuntime(context.Background(), cid) }()

	probe := []string{"nc", "-w", "2", peerIP, "7000"}
	dns := []string{"nslookup", "example.com"}
	assertExit := func(stage, user string, cmd []string, wantZero bool) {
		t.Helper()
		code := fenceTestExec(ctx, t, p, cid, user, cmd)
		if (code == 0) != wantZero {
			t.Fatalf("%s: %v as uid %s exited %d, want success=%v", stage, cmd, user, code, wantZero)
		}
	}

	// The agent cannot become the allowed UID: no capabilities and
	// no_new_privs, so no setuid route to 1002 or 0.
	assertExit("fenced", "1001", []string{"sh", "-c", `grep -q "^NoNewPrivs:[[:space:]]*1" /proc/self/status && grep -q "^CapEff:[[:space:]]*0000000000000000" /proc/self/status`}, true)
	assertExit("fenced", "1001", probe, false)
	assertExit("fenced", "0", probe, false)
	assertExit("fenced", "1001", dns, false)
	assertExit("fenced", "1002", probe, true)

	// Restart behind the provider's back: the namespace is recreated and the
	// fence is gone until the provider notices.
	if _, err := p.client.ContainerRestart(ctx, cid, client.ContainerRestartOptions{}); err != nil {
		t.Fatalf("restart: %v", err)
	}
	assertExit("restarted, before ensure", "1001", probe, true)

	// Every provider exec path refuses the unfenced start (the raw client
	// above is the operator's docker socket, which is root-equivalent anyway).
	if _, err := p.Exec(ctx, provider.ExecConfig{ContainerID: cid, Cmd: []string{"true"}, User: "1001:1001"}); !errors.Is(err, errFenceNotInPlace) {
		t.Fatalf("Exec into an unfenced start must be refused, got %v", err)
	}

	// A connection the agent opens while unfenced must not keep flowing once
	// the fence is in: established is accepted only in the reply direction.
	// Measured at the receiver, because TCP retransmits a rejected segment for
	// minutes instead of failing the sender's write.
	longConn := []string{"sh", "-c", "(while :; do echo x; sleep 0.5; done) | nc " + sinkIP + " 7001"}
	ex, err := p.client.ExecCreate(ctx, cid, client.ExecCreateOptions{Cmd: longConn, User: "1001"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.client.ExecStart(ctx, ex.ID, client.ExecStartOptions{Detach: true}); err != nil {
		t.Fatal(err)
	}
	sinkStalled := []string{"sh", "-c", `a=$(cat /tmp/last 2>/dev/null); sleep 3; b=$(cat /tmp/last 2>/dev/null); [ -n "$a" ] && [ "$a" = "$b" ]`}
	time.Sleep(2 * time.Second)
	if fenceTestExec(ctx, t, p, sink.ID, "0", sinkStalled) == 0 {
		t.Fatal("setup: the unfenced long connection should be delivering data to the sink")
	}

	if _, err := p.EnsureCrewRuntime(ctx, team); err != nil {
		t.Fatalf("EnsureCrewRuntime after restart: %v", err)
	}
	if fenceTestExec(ctx, t, p, sink.ID, "0", sinkStalled) != 0 {
		t.Fatal("a connection opened before the fence kept delivering data after it")
	}
	if _, err := p.Exec(ctx, provider.ExecConfig{ContainerID: cid, Cmd: []string{"true"}, User: "1001:1001"}); err != nil {
		t.Fatalf("Exec after re-fence: %v", err)
	}
	assertExit("re-fenced", "1001", probe, false)
	assertExit("re-fenced", "1002", probe, true)
}

func fenceTestExec(ctx context.Context, t *testing.T, p *Provider, cid, user string, cmd []string) int {
	t.Helper()
	ex, err := p.client.ExecCreate(ctx, cid, client.ExecCreateOptions{Cmd: cmd, User: user})
	if err != nil {
		t.Fatalf("exec create %v: %v", cmd, err)
	}
	if _, err := p.client.ExecStart(ctx, ex.ID, client.ExecStartOptions{}); err != nil {
		t.Fatalf("exec start %v: %v", cmd, err)
	}
	code, running, err := p.waitExecExit(ctx, ex.ID, 100)
	if err != nil || running {
		t.Fatalf("exec %v did not finish: running=%v err=%v", cmd, running, err)
	}
	return code
}
