//go:build linux

package docker

import (
	"context"
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

	if _, err := p.EnsureCrewRuntime(ctx, team); err != nil {
		t.Fatalf("EnsureCrewRuntime after restart: %v", err)
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
