//go:build integration

package docker

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"log/slog"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	"github.com/crewship-ai/crewship/internal/devcontainer"
	"github.com/crewship-ai/crewship/internal/provider"
)

func TestImageChange_RealHeartbeatSurvivesNewImageAdmission(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cli, err := client.New(client.FromEnv)
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	image, err := cli.ImageInspect(ctx, "alpine:3")
	if err != nil {
		t.Fatalf("integration requires local alpine:3: %v", err)
	}
	prefix := "crewship-image-safety-" + strings.ToLower(rand.Text())
	p := &Provider{client: cli, cfg: Config{ContainerPrefix: prefix, Network: "bridge", OutputBasePath: t.TempDir()}, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	crew := provider.CrewConfig{ID: "fixture", Slug: "fixture", CachedImage: "sha256:" + strings.Repeat("b", 64)}
	name := p.CrewContainerName(crew.ID, crew.Slug)
	created, err := cli.ContainerCreate(ctx, client.ContainerCreateOptions{Name: name,
		Config:     &container.Config{Image: image.ID, User: "1001:1001", Entrypoint: []string{"/bin/sh", "-c"}, Cmd: []string{`n=0; while true; do n=$((n+1)); echo "$n" > /tmp/heartbeat; sleep 0.1; done`}},
		HostConfig: &container.HostConfig{NetworkMode: "none", ReadonlyRootfs: true, CapDrop: []string{"ALL"}, SecurityOpt: []string{"no-new-privileges"}, Tmpfs: map[string]string{"/tmp": "rw,nosuid,nodev,size=1048576,uid=1001,gid=1001"}, Resources: container.Resources{Memory: 32 << 20, NanoCPUs: 500_000_000}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		clean, stop := context.WithTimeout(context.Background(), 20*time.Second)
		defer stop()
		_, _ = cli.ContainerRemove(clean, created.ID, client.ContainerRemoveOptions{Force: true})
	})
	if _, err := cli.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatal(err)
	}
	heartbeat := func() int {
		t.Helper()
		out, err := exec.CommandContext(ctx, "docker", "exec", created.ID, "cat", "/tmp/heartbeat").CombinedOutput()
		if err != nil {
			t.Fatalf("heartbeat %v %s", err, out)
		}
		n, err := strconv.Atoi(strings.TrimSpace(string(out)))
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	before, err := cli.ContainerInspect(ctx, created.ID, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	first := heartbeat()
	use, err := p.RetainCrewRuntimeUse(ctx, crew.ID, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer use.Release()
	for i := 0; i < 3; i++ {
		if _, err := p.EnsureCrewRuntime(ctx, crew); !errors.Is(err, provider.ErrRuntimeImageUpdatePending) {
			t.Fatalf("new-image admission %v", err)
		}
	}
	waitCtx, stopWaiting := context.WithTimeout(ctx, 200*time.Millisecond)
	defer stopWaiting()
	if next, err := p.AcquireCrewRuntimeUse(waitCtx, crew); !errors.Is(err, context.DeadlineExceeded) {
		next.Release()
		t.Fatalf("activation bypassed a live reservation: %v", err)
	}
	time.Sleep(300 * time.Millisecond)
	after, err := cli.ContainerInspect(ctx, created.ID, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !after.Container.State.Running || after.Container.State.Pid != before.Container.State.Pid || heartbeat() <= first {
		t.Fatal("image selection interrupted existing work")
	}
	use.Release()
	// Explicitly stop this owned fixture. Only then may reconciliation remove it.
	timeout := 0
	if _, err := cli.ContainerStop(ctx, created.ID, client.ContainerStopOptions{Timeout: &timeout}); err != nil {
		t.Fatal(err)
	}
	_, done, err := p.reconcileExistingContainer(ctx, crew, name, crew.CachedImage, true, func(devcontainer.ProvisionEvent) {})
	if err != nil || done {
		t.Fatalf("stopped container not released for replacement: done=%v err=%v", done, err)
	}
	if _, err := cli.ContainerInspect(ctx, created.ID, client.ContainerInspectOptions{}); err == nil {
		t.Fatal("stopped old container still present")
	}
}
