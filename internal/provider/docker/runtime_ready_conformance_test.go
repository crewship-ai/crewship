//go:build conformance

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
// Run:
//
//	go test -tags conformance -run TestRuntimeReadyContainerizedServerBindSource -v ./internal/provider/docker/

package docker

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
)

func TestRuntimeReadyContainerizedServerBindSource(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	p, cleanup := newConformanceProvider(ctx, t)
	defer cleanup()
	image := conformanceImageRef()
	ensureConformanceImage(ctx, t, p, image)

	p.cfg.ReportRuntimeReadiness = true
	p.inContainerFn = func() bool { return true }
	staged := p.cfg // sidecar and entrypoint under the host temp dir the daemon can see

	// The daemon sees the host paths: ready.
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
	if _, err := p.client.VolumeCreate(ctx, client.VolumeCreateOptions{Name: volume}); err != nil {
		t.Fatalf("create volume: %v", err)
	}
	t.Cleanup(func() {
		bg, stop := context.WithTimeout(context.Background(), 20*time.Second)
		defer stop()
		_, _ = p.client.VolumeRemove(bg, volume, client.VolumeRemoveOptions{Force: true})
	})
	script := fmt.Sprintf(`printf '\177ELF' > %[1]s/crewship-sidecar && printf '#!/bin/sh\n' > %[1]s/entrypoint.sh && chmod 755 %[1]s/crewship-sidecar %[1]s/entrypoint.sh && test -x %[1]s/crewship-sidecar`, root)
	helper, err := p.client.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config:     &container.Config{Image: image, Cmd: []string{"sh", "-ec", script}},
		HostConfig: &container.HostConfig{Mounts: []mount.Mount{{Type: mount.TypeVolume, Source: volume, Target: root}}},
	})
	if err != nil {
		t.Fatalf("create server stand-in: %v", err)
	}
	defer func() {
		bg, stop := context.WithTimeout(context.Background(), 20*time.Second)
		defer stop()
		_, _ = p.client.ContainerRemove(bg, helper.ID, client.ContainerRemoveOptions{Force: true})
	}()
	if _, err := p.client.ContainerStart(ctx, helper.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatalf("start server stand-in: %v", err)
	}
	wait := p.client.ContainerWait(ctx, helper.ID, client.ContainerWaitOptions{Condition: container.WaitConditionNotRunning})
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
	err = p.RuntimeReady(ctx)
	if err == nil {
		t.Fatal("a bind source that exists only inside the server container was reported ready")
	}
	if err != ErrMandatoryBindsUnreachable {
		t.Fatalf("unexpected readiness error: %v", err)
	}

	// The operator fixes it (files visible to the daemon again): ready by itself.
	p.cfg.SidecarBinaryPath = staged.SidecarBinaryPath
	p.cfg.EntrypointPath = staged.EntrypointPath
	age(p, true, bindRejectedTTL+time.Second)
	if err := p.RuntimeReady(ctx); err != nil {
		t.Fatalf("readiness stayed red after the daemon could see the files: %v", err)
	}
}
