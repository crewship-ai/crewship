//go:build integration && linux

package docker

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/stagedstart"
	"github.com/crewship-ai/crewship/internal/stagedstart/testfixture"
	"github.com/moby/moby/client"
)

// Docker absence, missing fixture execution and any denial are failures.
func TestStagedQualificationRealDocker(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	productionBootstrap, e := os.ReadFile("../../../scripts/entrypoint.sh")
	if e != nil {
		t.Fatal(e)
	}
	f := testfixture.New(t, ctx, append([]byte("#!/bin/sh\nset -eu\ntest \"$1\" = --bootstrap-only\nid -u >> /workspace/bootstrap-runs\ntest \"$STAGED_FIXTURE\" = retained\n"), productionBootstrap...))
	p := &Provider{client: f.Client, cfg: Config{ContainerPrefix: f.Prefix, InstanceID: f.Prefix, Network: f.Prefix, RuntimeImage: f.ImageID, OutputBasePath: f.Root, SidecarBinaryPath: f.Binary, EntrypointPath: f.Bootstrap, EgressFenceCrews: []string{"pilot"}, DefaultRuntime: "runc"}, logger: quietLogger()}
	t.Setenv("CREWSHIP_RUNTIME", "runc")
	crew := provider.CrewConfig{ID: "pilot", Slug: "pilot", NetworkMode: "restricted", MemoryMB: 256, CPUs: 0.5, ContainerEnv: map[string]string{"STAGED_FIXTURE": "retained", "LD_PRELOAD": "fixture-unavailable-allocator"}}
	dirs, e := p.prepareCrewDirs(crew)
	if e != nil {
		t.Fatal(e)
	}
	memoryDir := filepath.Join(dirs.crew, "shared", ".memory")
	if e := os.MkdirAll(memoryDir, 0755); e != nil {
		t.Fatal(e)
	}
	f.WriteCanaries(t, memoryDir)
	volumes := []string{p.homeVolumeName(crew.ID, crew.Slug), p.toolsVolumeName(crew.ID, crew.Slug)}
	for _, volume := range volumes {
		if e := p.ensureVolume(ctx, volume); e != nil {
			t.Fatal(e)
		}
	}
	id := ""
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 20*time.Second)
		defer stop()
		if id != "" {
			if e := p.removeCrewContainer(cleanup, id, client.ContainerRemoveOptions{Force: true}); e != nil {
				t.Errorf("qualified runtime cleanup: %v", e)
			}
		}
		for _, volume := range volumes {
			if _, e := f.Client.VolumeRemove(cleanup, volume, client.VolumeRemoveOptions{}); e != nil {
				t.Errorf("owned volume cleanup: %v", e)
			}
		}
	}()
	if e := p.stagedOwnership(ctx, crew, f.ImageID, dirs, volumes); e != nil {
		t.Fatal(e)
	}
	c, h, env, e := p.buildStagedCrewContainerConfig(ctx, crew, p.CrewContainerName(crew.ID, crew.Slug), f.ImageID, "runc", 256, 0.5, dirs)
	if e != nil {
		t.Fatal(e)
	}
	created, e := f.Client.ContainerCreate(ctx, client.ContainerCreateOptions{Config: c, HostConfig: h})
	if e != nil {
		t.Fatal(e)
	}
	id = created.ID
	if e := p.prepareCreatedStaged(ctx, id, c, env, crew); e != nil {
		t.Fatal(e)
	}
	if _, e := f.Client.ContainerStart(ctx, id, client.ContainerStartOptions{}); e != nil {
		t.Fatal(e)
	}
	candidate, e := f.Client.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if e != nil {
		t.Fatal(e)
	}
	baseline, calibrated, checks := 0, false, 0
	assertNoImage := func() {
		t.Helper()
		if f.Arrivals(t) != baseline {
			t.Fatal("candidate packet arrived before seal")
		}
		for _, marker := range testfixture.Canaries {
			if _, e := os.Stat(filepath.Join(memoryDir, marker+"-"+candidate.Container.Config.Hostname)); !os.IsNotExist(e) {
				t.Fatalf("candidate image startup executed: %s %v", marker, e)
			}
		}
	}
	p.stagedTestHook = func(phase, target string) error {
		if target != id {
			t.Fatal("qualification hook targeted foreign runtime")
		}
		if phase == "before-fence" {
			f.Calibrate(t, candidate.Container, memoryDir)
			calibrated = true
			baseline = f.Arrivals(t)
			time.Sleep(1100 * time.Millisecond)
		}
		if phase == "before-fence" || phase == "before-check" || phase == "after-check" {
			checks++
			assertNoImage()
		}
		return nil
	}
	if e := p.ensureStagedStart(ctx, crew, id); e != nil {
		t.Fatal(e)
	}
	p.stagedTestHook = nil
	if !calibrated || checks != 3 {
		t.Fatalf("missing actual admission boundaries: calibrated=%v checks=%d", calibrated, checks)
	}
	state, e := p.stagedControl(ctx, id, "status", "")
	if e != nil || state.Phase != "ready" {
		t.Fatalf("qualified keeper not ready: %+v %v", state, e)
	}
	cmd, env, before, after, e := p.PrepareExternalExec(ctx, id, []string{"/bin/sh", "-c", "test $(id -u) = 1001; test \"$STAGED_FIXTURE\" = retained; test -z \"${LD_PRELOAD+x}\"; test -z \"${ENV+x}\"; cat /workspace/bootstrap-runs"}, nil)
	if e != nil {
		t.Fatal(e)
	}
	if e := before(ctx); e != nil {
		t.Fatal(e)
	}
	out, e := p.stagedRawExec(ctx, id, "1001:1001", cmd, env)
	if e != nil {
		t.Fatal(e)
	}
	if e := after(ctx); e != nil {
		t.Fatal(e)
	}
	if strings.TrimSpace(string(out)) != "1001" {
		t.Fatalf("bootstrap did not run once: %s", out)
	}
	assertStagedNetwork(t, ctx, p, id, f, func(t *testing.T, user string, command []string) string {
		t.Helper()
		cmd, env, e := p.WrapExternalExec(ctx, id, command, nil)
		if e != nil {
			t.Fatal(e)
		}
		out, e := p.stagedRawExec(ctx, id, user, cmd, env)
		if e != nil {
			t.Fatal(e)
		}
		return strings.TrimSpace(string(out))
	})
	if _, e := p.stagedRawExec(ctx, id, "1001:1001", []string{stagedstart.Binary, "--staged-bootstrap", state.Nonce}, nil); e == nil {
		t.Fatal("bootstrap replay admitted")
	}
	if _, e := f.Client.ContainerRestart(ctx, id, client.ContainerRestartOptions{}); e != nil {
		t.Fatal(e)
	}
	if _, e := p.stagedRawExec(ctx, id, "1001:1001", append([]string{stagedstart.Binary, "--staged-exec", state.Nonce}, []string{"/bin/sh", "-c", "touch /workspace/forbidden-replay"}...), nil); e == nil {
		t.Fatal("old generation workload admitted")
	}
	if _, e := os.Stat(filepath.Join(dirs.workspace, "forbidden-replay")); !os.IsNotExist(e) {
		t.Fatal("stale generation executed image command")
	}
	// Host-only evidence is deliberately read without exporting workload values.
	if _, e := p.loadStagedEnv(candidate.Container); e != nil {
		t.Fatal(e)
	}
}
