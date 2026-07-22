package docker

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/moby/moby/client"
)

// TestResilienceNetworkRecreate verifies that EnsureCrewRuntime auto-recreates
// the Docker network if it was deleted while the server was running.
func TestResilienceNetworkRecreate(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	tmpDir, err := os.MkdirTemp("", "crewship-resilience-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	networkName := "crewship-test-resilience"

	// EnsureCrewRuntime requires sidecar+entrypoint bind-mount sources.
	// Create placeholder files so the provider passes its fail-fast validation;
	// the test doesn't exercise sidecar IPC, only network/container lifecycle.
	// The sidecar placeholder must carry the ELF magic — buildMounts rejects
	// non-ELF sidecars since #953 (it is bind-mounted into Linux containers
	// and exec'd there; this test never execs it).
	sidecarPath := filepath.Join(tmpDir, "crewship-sidecar")
	entrypointPath := filepath.Join(tmpDir, "entrypoint.sh")
	if err := os.WriteFile(sidecarPath, []byte("\x7fELF placeholder - never exec'd by this test"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(entrypointPath, []byte("#!/bin/sh\nexec sleep infinity\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	p, err := New(ctx, Config{
		RuntimeImage:      "alpine:latest",
		DefaultRuntime:    "runc",
		Network:           networkName,
		OutputBasePath:    tmpDir,
		SidecarBinaryPath: sidecarPath,
		EntrypointPath:    entrypointPath,
	}, nil)
	if err != nil {
		t.Skipf("Docker not available: %v", err)
	}
	defer p.Close()

	// Step 1: Create container (network exists from New())
	containerID, err := p.EnsureCrewRuntime(ctx, provider.CrewConfig{
		ID: "res-001", Slug: "resilience-test", MemoryMB: 256, CPUs: 0.5,
	})
	if err != nil {
		t.Fatalf("EnsureCrewRuntime #1: %v", err)
	}
	t.Logf("Container created: %s", containerID[:12])

	// Verify workspace dir on host FS (bind mount, not Docker volume)
	wsPath := filepath.Join(tmpDir, "workspaces", "res-001")
	if fi, err := os.Stat(wsPath); err != nil || !fi.IsDir() {
		t.Fatalf("workspace dir not found at %s", wsPath)
	}
	t.Logf("Workspace dir OK: %s", wsPath)

	// Cleanup container
	_ = p.RemoveCrewRuntime(ctx, containerID)

	// Step 2: Delete the network behind the server's back (via Docker API, not CLI)
	if _, err := p.client.NetworkRemove(ctx, networkName, client.NetworkRemoveOptions{}); err != nil {
		t.Fatalf("NetworkRemove: %v", err)
	}
	t.Log("Network deleted")

	// Step 3: EnsureCrewRuntime should auto-recreate network
	containerID2, err := p.EnsureCrewRuntime(ctx, provider.CrewConfig{
		ID: "res-002", Slug: "resilience-test-2", MemoryMB: 256, CPUs: 0.5,
	})
	if err != nil {
		t.Fatalf("EnsureCrewRuntime after network delete: %v", err)
	}
	t.Logf("Container recreated after network delete: %s", containerID2[:12])

	// Verify container exists (alpine exits immediately, so accept any state)
	status, err := p.ContainerStatus(ctx, containerID2)
	if err != nil {
		t.Fatalf("ContainerStatus: %v", err)
	}
	t.Logf("Container state: %s (alpine has no long-running CMD)", status.State)

	// Step 4: Verify stale container ID detection — remove and check
	_ = p.RemoveCrewRuntime(ctx, containerID2)
	_, err = p.ContainerStatus(ctx, containerID2)
	if err == nil {
		t.Error("expected error for removed container, got nil")
	} else {
		t.Logf("Stale container correctly detected as gone: %v", err)
	}

	// Final cleanup
	_, _ = p.client.NetworkRemove(ctx, networkName, client.NetworkRemoveOptions{})
}
