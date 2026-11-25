//go:build integration && linux

// Readiness of a containerized server against a real daemon (#5418).
//
// The official Compose file puts the server in a container whose runtime files
// (sidecar, entrypoint) live in that container's own filesystem, while crew
// containers are created by the host's daemon. This test reproduces that split
// without the whole Compose stack: a named volume is written by a container
// that sees the files at a path the host does not have, and the provider is
// pointed at that server-local path. It is not a run of the production Compose
// file, the socket proxy or the server image.
//
// Needs a local engine and a local alpine:3 (CI prepares it); it pulls nothing.
// Run: go test -tags integration -run TestRuntimeReadyContainerizedServerBindSource ./internal/provider/docker/

package docker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
)

func TestRuntimeReadyContainerizedServerBindSource(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	cli, err := client.New(client.FromEnv)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cli.Close() })
	if _, err := cli.Ping(ctx, client.PingOptions{}); err != nil {
		t.Fatalf("no container engine reachable: %v", err)
	}
	image := os.Getenv("CREWSHIP_DATA_MOUNT_TEST_IMAGE")
	if image == "" {
		image = "alpine:3"
	}

	// Runtime files the daemon can see: a host directory.
	visible := t.TempDir()
	hostSidecar := filepath.Join(visible, "crewship-sidecar")
	hostEntrypoint := filepath.Join(visible, "entrypoint.sh")
	if err := os.WriteFile(hostSidecar, []byte{0x7f, 'E', 'L', 'F', 2, 1, 1, 0}, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hostEntrypoint, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	p := &Provider{
		client:        cli,
		detected:      DetectResult{Runtime: "docker"},
		logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
		cfg:           Config{ReportRuntimeReadiness: true, OutputBasePath: visible, SidecarBinaryPath: hostSidecar, EntrypointPath: hostEntrypoint},
		inContainerFn: func() bool { return true },
	}
	if err := p.RuntimeReady(ctx); err != nil {
		t.Fatalf("mandatory binds visible to the daemon reported %v", err)
	}

	// The same files exist only inside a "server" container's volume mount.
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	volume := "crewship-ready-" + suffix
	root := "/var/lib/crewship-ready-" + suffix
	if _, err := os.Stat(root); err == nil {
		t.Fatalf("%s exists on the host; the test needs a path only the container has", root)
	}
	if _, err := cli.VolumeCreate(ctx, client.VolumeCreateOptions{Name: volume}); err != nil {
		t.Fatalf("create volume: %v", err)
	}
	script := fmt.Sprintf(`printf '\177ELF' > %[1]s/crewship-sidecar && printf '#!/bin/sh\n' > %[1]s/entrypoint.sh && chmod 755 %[1]s/crewship-sidecar %[1]s/entrypoint.sh && test -x %[1]s/crewship-sidecar`, root)
	helper, err := cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config:     &container.Config{Image: image, User: "0:0", Cmd: []string{"sh", "-ec", script}},
		HostConfig: &container.HostConfig{Mounts: []mount.Mount{{Type: mount.TypeVolume, Source: volume, Target: root}}},
	})
	if err != nil {
		_, _ = cli.VolumeRemove(context.WithoutCancel(ctx), volume, client.VolumeRemoveOptions{Force: true})
		t.Fatalf("create server stand-in: %v", err)
	}
	t.Cleanup(func() {
		bg, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		_, _ = cli.ContainerRemove(bg, helper.ID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
		_, _ = cli.VolumeRemove(bg, volume, client.VolumeRemoveOptions{Force: true})
	})
	if _, err := cli.ContainerStart(ctx, helper.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatalf("start server stand-in: %v", err)
	}
	wait := cli.ContainerWait(ctx, helper.ID, client.ContainerWaitOptions{Condition: container.WaitConditionNotRunning})
	select {
	case status, ok := <-wait.Result:
		if !ok || status.StatusCode != 0 {
			t.Fatalf("server stand-in did not write its files (ok=%v status=%d)", ok, status.StatusCode)
		}
	case err := <-wait.Error:
		t.Fatalf("wait for server stand-in: %v", err)
	}

	// Now the provider's runtime files are server-local: the daemon must refuse
	// them and readiness must say so, without naming the path.
	p.cfg.SidecarBinaryPath = root + "/crewship-sidecar"
	p.cfg.EntrypointPath = root + "/entrypoint.sh"
	age(p, false, bindVerifiedTTL+time.Second)
	if err := p.RuntimeReady(ctx); !errors.Is(err, ErrMandatoryBindsUnreachable) {
		t.Fatalf("a bind source that exists only inside the server container reported %v", err)
	}

	// The operator fixes it (files visible to the daemon again): ready by itself.
	p.cfg.SidecarBinaryPath = hostSidecar
	p.cfg.EntrypointPath = hostEntrypoint
	age(p, true, bindRejectedTTL+time.Second)
	if err := p.RuntimeReady(ctx); err != nil {
		t.Fatalf("readiness stayed red after the daemon could see the files: %v", err)
	}
}
