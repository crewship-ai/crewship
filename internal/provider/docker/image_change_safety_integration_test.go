//go:build integration

package docker

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"

	"github.com/crewship-ai/crewship/internal/devcontainer"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/resourcelifecycle"
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

// This is the positive half of the heartbeat test: real controller occupancy,
// rather than an always-true test callback, permits replacement only once the
// detached process has actually exited.
func TestImageChange_RealIdleVerifierProtectsDetachedWork(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	cli, err := client.New(client.FromEnv)
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	image, err := cli.ImageInspect(ctx, "alpine:3")
	if err != nil {
		t.Fatalf("requires local alpine:3: %v", err)
	}
	prefix := "crewship-idle-proof-" + strings.ToLower(rand.Text())
	p := &Provider{client: cli, cfg: Config{ContainerPrefix: prefix, InstanceID: prefix, Network: "bridge", OutputBasePath: t.TempDir()}, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	crew := provider.CrewConfig{ID: "fixture", Slug: "fixture"}
	entrypoint := filepath.Join(t.TempDir(), "entrypoint.sh")
	if err := os.WriteFile(entrypoint, []byte("#!/bin/sh\nexec /bin/sleep infinity\n"), 0755); err != nil {
		t.Fatal(err)
	}
	p.cfg.EntrypointPath = entrypoint
	// The fixture never starts the credential sidecar. Supply a real Linux ELF
	// for the required read-only mount without building or downloading a CLI.
	p.cfg.SidecarBinaryPath, err = os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	var replacementID, imageID string
	created, err := cli.ContainerCreate(ctx, client.ContainerCreateOptions{Name: p.CrewContainerName(crew.ID, crew.Slug), Config: &container.Config{Image: image.ID, User: "1001:1001", Entrypoint: []string{"/bin/sleep"}, Cmd: []string{"infinity"}, Labels: map[string]string{resourcelifecycle.InstanceLabel: prefix, crewCrewIDLabel: crew.ID, crewKindLabel: crewRuntimeKind}}, HostConfig: &container.HostConfig{NetworkMode: "none", ReadonlyRootfs: true, CapDrop: []string{"ALL"}, SecurityOpt: []string{"no-new-privileges"}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 20*time.Second)
		defer stop()
		_, _ = cli.ContainerRemove(cleanup, created.ID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
		if replacementID != "" {
			_, _ = cli.ContainerRemove(cleanup, replacementID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
		}
		for _, name := range []string{p.homeVolumeName(crew.ID, crew.Slug), p.toolsVolumeName(crew.ID, crew.Slug)} {
			_, _ = cli.VolumeRemove(cleanup, name, client.VolumeRemoveOptions{})
		}
		if imageID != "" {
			_, _ = cli.ImageRemove(cleanup, imageID, client.ImageRemoveOptions{})
		}
	})
	if _, err = cli.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatal(err)
	}
	// Commit only this empty, owned fixture to produce a second immutable
	// artifact. No production container or credential data is captured.
	committed, err := cli.ContainerCommit(ctx, created.ID, client.ContainerCommitOptions{})
	if err != nil {
		t.Fatal(err)
	}
	imageID = committed.ID
	crew.CachedImage = imageID
	orch := orchestrator.New(p, idleIntegrationState{}, p.logger)
	p.SetRuntimeIdleVerifier(orch.VerifyRuntimeIdle)
	ex, err := cli.ExecCreate(ctx, created.ID, client.ExecCreateOptions{User: "1001:1001", Cmd: []string{"/bin/sleep", "30"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = cli.ExecStart(ctx, ex.ID, client.ExecStartOptions{Detach: true}); err != nil {
		t.Fatal(err)
	}
	if stopped, err := p.StopUnusedCrewRuntime(ctx, crew.ID, created.ID); stopped || !errors.Is(err, provider.ErrRuntimeImageUpdatePending) {
		t.Fatalf("detached work lost: stopped=%v err=%v", stopped, err)
	}
	state, err := cli.ExecInspect(ctx, ex.ID, client.ExecInspectOptions{})
	if err != nil || !state.Running {
		t.Fatalf("background process was lost: %v", err)
	}
	if out, err := exec.CommandContext(ctx, "docker", "exec", created.ID, "/bin/sh", "-c", `for p in /proc/[0-9]*; do [ "${p##*/}" = 1 ] && continue; [ "$(cat "$p/comm" 2>/dev/null)" = sleep ] && kill -TERM "${p##*/}"; done; true`).CombinedOutput(); err != nil {
		t.Fatalf("finish fixture: %v %s", err, out)
	}
	use, err := p.AcquireCrewRuntimeUse(ctx, crew)
	if use != nil {
		replacementID = use.ContainerID()
		defer use.Release()
	}
	if err != nil {
		t.Fatalf("idle image activation: %v", err)
	}
	if replacementID == "" || replacementID == created.ID {
		t.Fatal("old runtime was not replaced")
	}
	inspected, err := cli.ContainerInspect(ctx, replacementID, client.ContainerInspectOptions{})
	if err != nil || !inspected.Container.State.Running || inspected.Container.Image != imageID {
		t.Fatalf("new image is not running: %v", err)
	}
	if _, err := cli.ContainerInspect(ctx, created.ID, client.ContainerInspectOptions{}); err == nil {
		t.Fatal("old runtime remains after activation")
	}
}

type idleIntegrationState struct{ provider.StateProvider }

func (idleIntegrationState) List(context.Context, string) (map[string][]byte, error) {
	return map[string][]byte{}, nil
}
