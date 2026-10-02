//go:build linux

package docker

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/resourcelifecycle"
)

// TestCrewNetworkIsolationIntegration is #2240's acceptance against a real
// daemon: two crews on their own networks cannot reach each other or be
// reached from a crew on the shared network, and each resolves its own
// `pg` service alias. Skips when Docker is unavailable.
func TestCrewNetworkIsolationIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	tmp, err := os.MkdirTemp("", "crewship-crewnet-it-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmp)
	sidecarPath := filepath.Join(tmp, "crewship-sidecar")
	entrypointPath := filepath.Join(tmp, "entrypoint.sh")
	if err := os.WriteFile(sidecarPath, []byte("\x7fELF placeholder - never exec'd by this test"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(entrypointPath, []byte("#!/bin/sh\nexec sleep infinity\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	shared := "crewnet-it-" + time.Now().Format("150405")
	p, err := New(ctx, Config{
		RuntimeImage:      "alpine:3",
		DefaultRuntime:    "runc",
		Network:           shared,
		OutputBasePath:    tmp,
		SidecarBinaryPath: sidecarPath,
		EntrypointPath:    entrypointPath,
		CrewNetworkCrews:  []string{"net-a", "net-b"},
		CrewNetworkPool:   "10.239.248.0/24",
	}, nil)
	if err != nil {
		// SKIP-WAIVER(#2240): needs a live Docker daemon to create real
		// bridges; same guard as TestResilienceNetworkRecreate.
		t.Skipf("Docker not available: %v", err)
	}
	defer p.Close()
	// Registered first so it runs last: the shared network can only go once
	// every container on it has been removed by the deferred crew teardowns.
	defer func() { _, _ = p.client.NetworkRemove(context.Background(), shared, client.NetworkRemoveOptions{}) }()

	svc := func(tag string) []provider.CrewService {
		return []provider.CrewService{{Name: "pg", Image: "alpine:3", Command: []string{"nc", "-lk", "-p", "5432", "-e", "echo", tag}}}
	}
	crews := []provider.CrewConfig{
		{ID: "crewnet-a-001", Slug: "net-a", MemoryMB: 128, CPUs: 0.25, Services: svc("crew-a")},
		{ID: "crewnet-b-001", Slug: "net-b", MemoryMB: 128, CPUs: 0.25, Services: svc("crew-b")},
		{ID: "crewnet-c-001", Slug: "net-c", MemoryMB: 128, CPUs: 0.25},
	}
	ids := map[string]string{}
	for _, c := range crews {
		cid, err := p.EnsureCrewRuntime(ctx, c)
		if err != nil {
			t.Fatalf("EnsureCrewRuntime %s: %v", c.Slug, err)
		}
		ids[c.Slug] = cid
		defer func(c provider.CrewConfig, cid string) {
			bg := context.Background()
			_ = p.RemoveCrewServices(bg, c.ID, c.Slug)
			_ = p.RemoveCrewRuntime(bg, cid)
			if c.Slug != "net-c" {
				_, _ = p.client.NetworkRemove(bg, p.crewNetworkName(c.ID), client.NetworkRemoveOptions{})
			}
		}(c, cid)
		if len(c.Services) > 0 {
			if _, err := p.EnsureCrewServices(ctx, c); err != nil {
				t.Fatalf("EnsureCrewServices %s: %v", c.Slug, err)
			}
		}
	}
	ip := map[string]string{}
	for slug, cid := range ids {
		got, err := p.ContainerIP(ctx, cid, shared)
		if err != nil {
			t.Fatalf("ContainerIP %s via the instance network name: %v", slug, err)
		}
		ip[slug] = got
	}
	for _, slug := range []string{"net-a", "net-b"} {
		if !strings.HasPrefix(ip[slug], "10.239.248.") {
			t.Fatalf("%s got %s, want an address from the crew-network pool", slug, ip[slug])
		}
	}

	// A listener on every runtime so reachability is about the network,
	// not about a closed port.
	for _, cid := range ids {
		crewNetExec(ctx, t, p, cid, true, "sh", "-c", "nc -lk -p 7000 -e echo here")
	}
	reach := func(from, toIP string) bool {
		out, _ := crewNetExec(ctx, t, p, ids[from], false, "sh", "-c", "nc -w 2 "+toIP+" 7000 </dev/null")
		return strings.Contains(out, "here")
	}
	if !reach("net-a", ip["net-a"]) {
		t.Fatal("setup: a crew must reach its own listener")
	}
	for _, pair := range [][2]string{{"net-a", "net-b"}, {"net-b", "net-a"}, {"net-c", "net-a"}, {"net-a", "net-c"}} {
		if reach(pair[0], ip[pair[1]]) {
			t.Fatalf("%s reached %s across crew networks", pair[0], pair[1])
		}
	}
	for slug, want := range map[string]string{"net-a": "crew-a", "net-b": "crew-b"} {
		out, _ := crewNetExec(ctx, t, p, ids[slug], false, "sh", "-c", "nc -w 2 pg 5432 </dev/null")
		if strings.TrimSpace(out) != want {
			t.Fatalf("%s resolved its pg service to %q, want %q", slug, strings.TrimSpace(out), want)
		}
	}
}

// crewNetExec runs cmd in a container as root (test containers, not agents)
// and returns its stdout; detach starts it in the background.
func crewNetExec(ctx context.Context, t *testing.T, p *Provider, cid string, detach bool, cmd ...string) (string, int) {
	t.Helper()
	ex, err := p.client.ExecCreate(ctx, cid, client.ExecCreateOptions{Cmd: cmd, User: "0", AttachStdout: !detach, AttachStderr: !detach})
	if err != nil {
		t.Fatalf("exec create: %v", err)
	}
	if detach {
		if _, err := p.client.ExecStart(ctx, ex.ID, client.ExecStartOptions{Detach: true}); err != nil {
			t.Fatalf("exec start: %v", err)
		}
		time.Sleep(300 * time.Millisecond)
		return "", 0
	}
	att, err := p.client.ExecAttach(ctx, ex.ID, client.ExecAttachOptions{})
	if err != nil {
		t.Fatalf("exec attach: %v", err)
	}
	defer att.Close()
	var out, errb bytes.Buffer
	_, _ = stdcopy.StdCopy(&out, &errb, att.Reader)
	code, _, _ := p.waitExecExit(ctx, ex.ID, 50)
	return out.String(), code
}

// The cleanup runtime's side of #2240 against a real daemon: a crew network
// reports its ownership labels, refuses removal as in-use while a container
// is attached (never forced), and goes once the container is gone.
func TestCleanupRuntimeCrewNetworkIntegration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	p, err := New(ctx, Config{RuntimeImage: "alpine:3", Network: "", InstanceID: "inst-it", CrewNetworkCrews: []string{"rm-crew"}, CrewNetworkPool: "10.239.249.0/24"}, nil)
	if err != nil {
		// SKIP-WAIVER(#2240): needs a live Docker daemon to create and remove
		// real bridges; same guard as TestResilienceNetworkRecreate.
		t.Skipf("Docker not available: %v", err)
	}
	defer p.Close()
	name, err := p.ensureCrewNetwork(ctx, "rm-crew-001", "rm-crew")
	if err != nil {
		t.Fatalf("ensureCrewNetwork: %v", err)
	}
	defer func() { _, _ = p.client.NetworkRemove(context.Background(), name, client.NetworkRemoveOptions{}) }()
	created, err := p.client.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config:     &container.Config{Image: "alpine:3", Cmd: []string{"sleep", "60"}},
		HostConfig: &container.HostConfig{NetworkMode: container.NetworkMode(name)},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = p.client.ContainerRemove(context.Background(), created.ID, client.ContainerRemoveOptions{Force: true})
	}()
	if _, err := p.client.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatal(err)
	}

	rt := &cleanupRuntime{client: p.client}
	nets, err := rt.ListNetworks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var found resourcelifecycle.Network
	for _, n := range nets {
		if n.CrewID == "rm-crew-001" {
			found = n
		}
	}
	if found.ID == "" || found.InstanceID != "inst-it" || found.Kind != resourcelifecycle.NetworkKind {
		t.Fatalf("crew network not reported with its ownership labels: %+v", found)
	}
	if err := rt.RemoveNetwork(ctx, found.ID); !errors.Is(err, resourcelifecycle.ErrNetworkInUse) {
		t.Fatalf("removing a network with an attached container: %v, want ErrNetworkInUse", err)
	}
	if _, err := p.client.ContainerRemove(ctx, created.ID, client.ContainerRemoveOptions{Force: true}); err != nil {
		t.Fatal(err)
	}
	if err := rt.RemoveNetwork(ctx, found.ID); err != nil {
		t.Fatalf("removing the emptied network: %v", err)
	}
	if err := rt.RemoveNetwork(ctx, found.ID); !errors.Is(err, resourcelifecycle.ErrNotFound) {
		t.Fatalf("removing it again: %v, want ErrNotFound", err)
	}
}
