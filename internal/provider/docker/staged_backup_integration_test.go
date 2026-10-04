//go:build integration && linux

package docker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/client"

	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/stagedstart"
	"github.com/crewship-ai/crewship/internal/stagedstart/testfixture"
)

// No self-skip: the separate adapter must cross actual Docker create/start.
func TestStagedBackupRealDocker(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	f := testfixture.New(t, ctx, []byte("#!/bin/sh\nset -eu\ntest \"$1\" = --bootstrap-only\nid -u >> /workspace/bootstrap-runs\n"))
	cli := f.Client
	p, e := New(ctx, Config{ContainerPrefix: f.Prefix, InstanceID: f.Prefix, Network: f.Prefix, RuntimeImage: f.ImageID, OutputBasePath: f.Root, SidecarBinaryPath: f.Binary, EntrypointPath: f.Bootstrap, EgressFenceCrews: []string{"pilot"}, DefaultRuntime: "runc"}, quietLogger())
	if e != nil {
		t.Fatal(e)
	}
	defer p.Close()
	t.Setenv("CREWSHIP_RUNTIME", "runc")
	crew := provider.CrewConfig{ID: "pilot", Slug: "pilot", NetworkMode: "restricted", MemoryMB: 256, CPUs: 0.5}
	memoryDir := filepath.Join(f.Root, "crews", crew.ID, "shared", ".memory")
	if e := os.MkdirAll(memoryDir, 0755); e != nil {
		t.Fatal(e)
	}
	f.WriteCanaries(t, memoryDir)
	workspace := filepath.Join(f.Root, "workspaces", crew.ID)
	cid, hostname, preSealArrivals := "", "", 0
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 20*time.Second)
		defer stop()
		if cid != "" {
			if e := p.RemoveCrewRuntime(cleanup, cid); e != nil {
				t.Errorf("backup runtime cleanup: %v", e)
			}
		}
		for _, volume := range []string{p.homeVolumeName(crew.ID, crew.Slug), p.toolsVolumeName(crew.ID, crew.Slug)} {
			if _, e := cli.VolumeRemove(cleanup, volume, client.VolumeRemoveOptions{}); e != nil {
				t.Errorf("backup volume cleanup: %v", e)
			}
		}
	}()
	calibrated := false
	p.stagedTestHook = func(phase, id string) error {
		if phase == "before-fence" {
			candidate, e := cli.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
			if e != nil {
				return e
			}
			hostname = candidate.Container.Config.Hostname
			f.Calibrate(t, candidate.Container, memoryDir)
			calibrated = true
			preSealArrivals = f.Arrivals(t)
		}
		return nil
	}
	cid, e = p.EnsureCrewRuntime(ctx, crew)
	p.stagedTestHook = nil
	if e != nil {
		t.Fatal(e)
	}
	if !calibrated {
		t.Fatal("backup acceptance lacks malicious image positive calibration")
	}
	arrivals := func() int { return f.Arrivals(t) }
	count := func() int {
		raw, e := os.ReadFile(filepath.Join(workspace, "bootstrap-runs"))
		if e != nil {
			t.Fatal(e)
		}
		return strings.Count(string(raw), "1001\n")
	}
	assertNoImage := func(t *testing.T, before int) {
		t.Helper()
		if count() != before || arrivals() != preSealArrivals {
			t.Fatal("backup raced workload or packet into unsealed generation")
		}
		for _, marker := range testfixture.Canaries {
			if _, e := os.Stat(filepath.Join(memoryDir, marker+"-"+hostname)); !os.IsNotExist(e) {
				t.Fatalf("backup race image canary: %s %v", marker, e)
			}
		}
	}
	probe := func(t *testing.T, user string, cmd []string) string {
		t.Helper()
		command, env, e := p.WrapExternalExec(ctx, cid, cmd, nil)
		if e != nil {
			t.Fatal(e)
		}
		out, e := p.stagedRawExec(ctx, cid, user, command, env)
		if e != nil {
			t.Fatal(e)
		}
		return strings.TrimSpace(string(out))
	}
	networkCheck := func(t *testing.T) { assertStagedNetwork(t, ctx, p, cid, f, probe) }
	t.Run("external_backup_ready_and_restart_race", func(t *testing.T) {
		ops := &backup.MobyDockerOps{Client: cli, Prepare: p.PrepareExternalExec}
		if code, out, e := ops.ExecAs(ctx, cid, "1001:1001", []string{"id", "-u"}); e != nil || code != 0 || string(out) != "1001\n" {
			t.Fatalf("ready backup exec: code=%d out=%q err=%v", code, out, e)
		}
		if code, _, e := ops.ExecAs(ctx, cid, "1001:1001", []string{"/bin/sh", "-c", "echo ready > /workspace/backup-ready"}); e != nil || code != 0 {
			t.Fatalf("ready backup write control failed: code=%d err=%v", code, e)
		}
		if b, e := os.ReadFile(filepath.Join(workspace, "backup-ready")); e != nil || string(b) != "ready\n" {
			t.Fatalf("backup write control did not fire: %v", e)
		}
		if code, _, e := ops.Exec(ctx, cid, []string{"/bin/sh", "-c", "echo denied > /workspace/backup-root"}); e != nil || code != 126 {
			t.Fatalf("root backup command lacked peer-UID denial: code=%d err=%v", code, e)
		}
		if _, e := os.Stat(filepath.Join(workspace, "backup-root")); !os.IsNotExist(e) {
			t.Fatal("denied root backup command executed")
		}
		before := count()
		preSealArrivals = arrivals()
		old, e := cli.ContainerInspect(ctx, cid, client.ContainerInspectOptions{})
		if e != nil {
			t.Fatal(e)
		}
		var oldCommand, oldEnv []string
		wrapped, restarted := 0, 0
		ops.Prepare = func(ctx context.Context, id string, cmd, env []string) ([]string, []string, func(context.Context) error, func(context.Context) error, error) {
			command, values, check, after, e := p.PrepareExternalExec(ctx, id, cmd, env)
			if e != nil {
				return nil, nil, nil, nil, e
			}
			wrapped++
			oldCommand, oldEnv = append([]string(nil), command...), append([]string(nil), values...)
			return command, values, func(ctx context.Context) error {
				if e := check(ctx); e != nil {
					return e
				}
				restarted++
				_, e := cli.ContainerRestart(ctx, id, client.ContainerRestartOptions{})
				return e
			}, after, nil
		}
		_, _, e = ops.ExecAs(ctx, cid, "1001:1001", []string{"/bin/sh", "-c", "echo raced > /workspace/backup-raced"})
		missingExec := e != nil && e.Error() == fmt.Sprintf("backup: exec attach %s: unable to upgrade to tcp, received 404", cid)
		if !errors.Is(e, errFenceNotInPlace) && !missingExec {
			t.Fatalf("external restart lacked generation denial or exact invalidated-exec outcome: %v", e)
		}
		if wrapped != 1 || restarted != 1 || len(oldCommand) < 4 || oldCommand[0] != stagedstart.Binary || oldCommand[1] != "--staged-exec" {
			t.Fatal("backup race did not cross the actual wrapped/create/start boundaries once")
		}
		current, inspectErr := cli.ContainerInspect(ctx, cid, client.ContainerInspectOptions{})
		if inspectErr != nil || current.Container.State.StartedAt == old.Container.State.StartedAt {
			t.Fatalf("backup race lacked changed generation: %v", inspectErr)
		}
		if !current.Container.State.Running {
			if !errors.Is(e, errFenceNotInPlace) || current.Container.State.ExitCode != 0 {
				t.Fatalf("unexpected backup race stop: %+v %v", current.Container.State, e)
			}
			if _, e := cli.ContainerStart(ctx, cid, client.ContainerStartOptions{}); e != nil {
				t.Fatal(e)
			}
		}
		state, stateErr := p.stagedControl(ctx, cid, "status", "")
		if stateErr != nil || state.Phase != "staging" || state.Nonce == oldCommand[2] {
			t.Fatalf("backup restart lacked unsealed fresh nonce: %+v %v", state, stateErr)
		}
		// Some daemons invalidate the old exec before attach. Submit its exact
		// captured static gate into the new namespace too: it must reject 126.
		if _, e := p.stagedRawExec(ctx, cid, "1001:1001", oldCommand, oldEnv); e == nil || !strings.Contains(e.Error(), "exited 126") {
			t.Fatalf("captured stale backup gate did not deny inside new namespace: %v", e)
		}
		if _, e := os.Stat(filepath.Join(workspace, "backup-raced")); !os.IsNotExist(e) {
			t.Fatal("external image command ran in unsealed new boot")
		}
		assertNoImage(t, before)
		if _, e := p.EnsureCrewRuntime(ctx, crew); e != nil {
			t.Fatal(e)
		}
		networkCheck(t)
	})
}
