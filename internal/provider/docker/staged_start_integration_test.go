//go:build integration && linux

package docker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/dockerutil"
	"github.com/crewship-ai/crewship/internal/provider"
	"github.com/crewship-ai/crewship/internal/resourcelifecycle"
	"github.com/crewship-ai/crewship/internal/stagedstart"
	"github.com/crewship-ai/crewship/internal/stagedstart/testfixture"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
)

// No self-skip: this fixture is mandatory in managed-environments CI.
func TestStagedStartRealDocker(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Minute)
	defer cancel()
	f := testfixture.New(t, ctx, []byte("#!/bin/sh\nset -eu\ntest \"$1\" = --bootstrap-only\nid -u >> /workspace/bootstrap-runs\ntest \"${FAIL_BOOTSTRAP:-0}\" = 0\nif test \"${HANG_BOOTSTRAP:-0}\" = 1; then while test \"$(cat /workspace/bootstrap-release)\" != 1; do sleep 0.1; done; fi\n"))
	cli, root, prefix := f.Client, f.Root, f.Prefix
	binary, script, faultWrapper := f.Binary, f.Bootstrap, f.FaultWrapper
	var e error
	p := &Provider{client: cli, cfg: Config{ContainerPrefix: prefix, InstanceID: prefix, Network: prefix, RuntimeImage: f.ImageID, OutputBasePath: root, SidecarBinaryPath: binary, EntrypointPath: script, EgressFenceCrews: []string{"pilot"}, DefaultRuntime: "runc"}, logger: slog.New(slog.NewTextHandler(io.Discard, nil)), digestResolver: dockerutil.NewDigestResolver(time.Hour, time.Second)}
	t.Setenv("CREWSHIP_RUNTIME", "runc")
	originalEnv := map[string]string{"STAGED_FIXTURE_ENV": "retained", "STAGED_EXPAND": "${PATH}", "STAGED_FIXTURE_SECRET": "fixture-confidential"}
	crew := provider.CrewConfig{ID: "pilot", Slug: "pilot", NetworkMode: "restricted", MemoryMB: 256, CPUs: 0.5, ContainerEnv: originalEnv}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 20*time.Second)
		defer stop()
		if t.Failed() {
			if got, e := cli.ContainerInspect(cleanup, p.CrewContainerName(crew.ID, crew.Slug), client.ContainerInspectOptions{}); e == nil {
				t.Logf("failed runtime state=%+v entrypoint=%v keeper_hash=%s", got.Container.State, got.Container.Config.Entrypoint, got.Container.Config.Labels["crewship.keeper-sha256"])
			}
		}
		_, _ = cli.ContainerRemove(cleanup, p.CrewContainerName(crew.ID, crew.Slug), client.ContainerRemoveOptions{Force: true})
		for _, v := range []string{p.homeVolumeName(crew.ID, crew.Slug), p.toolsVolumeName(crew.ID, crew.Slug)} {
			_, _ = cli.VolumeRemove(cleanup, v, client.VolumeRemoveOptions{})
		}
		_, _ = cli.NetworkRemove(cleanup, prefix, client.NetworkRemoveOptions{})
	}()
	memoryDir := filepath.Join(root, "crews", crew.ID, "shared", ".memory")
	if e = os.MkdirAll(memoryDir, 0755); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(memoryDir, "retained"), []byte("memory retained"), 0600); e != nil {
		t.Fatal(e)
	}
	// Owned special files must not make the offline initializer fail or block.
	memoryLink := filepath.Join(memoryDir, "retained-hardlink")
	if e = os.Link(filepath.Join(memoryDir, "retained"), memoryLink); e != nil {
		t.Fatal(e)
	}
	memoryFIFO := filepath.Join(memoryDir, "fixture-fifo")
	if e = syscall.Mkfifo(memoryFIFO, 0644); e != nil {
		t.Fatal(e)
	}
	memorySocket := filepath.Join(memoryDir, "fixture-socket")
	listener, e := net.ListenUnix("unix", &net.UnixAddr{Name: memorySocket, Net: "unix"})
	if e != nil {
		t.Fatal(e)
	}
	listener.SetUnlinkOnClose(false)
	defer listener.Close()
	outside := filepath.Join(root, "outside-init")
	if e = os.WriteFile(outside, []byte("outside"), 0600); e != nil {
		t.Fatal(e)
	}
	if e = os.Symlink(outside, filepath.Join(memoryDir, "link")); e != nil {
		t.Fatal(e)
	}
	originDir := f.OriginDir
	arrivals := func() int { return f.Arrivals(t) }
	f.WriteCanaries(t, memoryDir)
	canaries := []string{"image-entrypoint-ran", "image-healthcheck-ran", "image-env-ran"}
	candidateHostnames := map[string]bool{}
	calibrated := false
	preSealArrivals := 0
	workspace := filepath.Join(root, "workspaces", crew.ID)
	if e = os.MkdirAll(workspace, 0755); e != nil {
		t.Fatal(e)
	}
	// Only this synthetic fault-control file is host-writable after ownership
	// init; real workspace and memory directory permissions stay unchanged.
	releaseFile := filepath.Join(workspace, "bootstrap-release")
	if e = os.WriteFile(releaseFile, []byte("0"), 0666); e != nil {
		t.Fatal(e)
	}
	if e = os.Chmod(releaseFile, 0666); e != nil {
		t.Fatal(e)
	}
	count := func() int {
		b, e := os.ReadFile(filepath.Join(workspace, "bootstrap-runs"))
		if e != nil && !os.IsNotExist(e) {
			t.Fatalf("bootstrap evidence read: %v", e)
		}
		return strings.Count(string(b), "1001\n")
	}
	assertNoImage := func(t *testing.T, before int) {
		t.Helper()
		if count() != before {
			t.Fatalf("image bootstrap before seal: count=%d expected=%d", count(), before)
		}
		if !calibrated {
			t.Fatal("image execution assertions lack a positive control")
		}
		if arrivals() != preSealArrivals {
			t.Fatalf("pre-seal origin arrivals=%d expected=%d", arrivals(), preSealArrivals)
		}
		b, e := os.ReadFile(filepath.Join(originDir, "arrivals"))
		if e != nil {
			t.Fatalf("origin evidence unreadable: %v", e)
		}
		for hostname := range candidateHostnames {
			if strings.Contains(string(b), "GET /image-start/"+hostname+" ") {
				t.Fatalf("candidate %s image contacted pre-seal origin", hostname)
			}
			for _, name := range canaries {
				if _, e := os.Stat(filepath.Join(memoryDir, name+"-"+hostname)); !os.IsNotExist(e) {
					t.Fatalf("candidate %s image program executed: %s err=%v", hostname, name, e)
				}
			}
		}
	}
	p.stagedTestHook = func(phase, id string) error {
		if phase == "before-fence" {
			t.Run("same_identity_positive_control", func(t *testing.T) {
				candidate, e := cli.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
				if e != nil {
					t.Fatal(e)
				}
				candidateHostname := candidate.Container.Config.Hostname
				candidateHostnames[candidateHostname] = true
				f.Calibrate(t, candidate.Container, memoryDir)
				calibrated = true
				preSealArrivals = arrivals()
			})
			time.Sleep(1100 * time.Millisecond)
			assertNoImage(t, 0)
			s, e := p.stagedControl(ctx, id, "status", "")
			if e != nil || s.Phase != "staging" {
				t.Fatalf("keeper-only prepare: %+v %v", s, e)
			}
		}
		return nil
	}
	cid, e := p.EnsureCrewRuntime(ctx, crew)
	if e != nil {
		t.Fatal(e)
	}
	p.stagedTestHook = nil
	for _, path := range []string{memoryDir, filepath.Join(memoryDir, "retained"), memoryLink} {
		info, e := os.Stat(path)
		if e != nil {
			t.Fatal(e)
		}
		stat := info.Sys().(*syscall.Stat_t)
		if stat.Uid != 1001 || stat.Gid != 1002 || info.Mode().Perm()&0060 != 0060 {
			t.Fatalf("memory ownership contract: %s %v uid=%d gid=%d", path, info.Mode(), stat.Uid, stat.Gid)
		}
		if path == memoryDir && info.Mode()&os.ModeSetgid == 0 {
			t.Fatal("memory setgid lost")
		}
	}
	for _, path := range []string{memoryFIFO, memorySocket} {
		info, e := os.Stat(path)
		if e != nil {
			t.Fatal(e)
		}
		stat := info.Sys().(*syscall.Stat_t)
		if stat.Uid != 1001 || stat.Gid != 1002 {
			t.Fatalf("memory special-file ownership %s uid=%d gid=%d", path, stat.Uid, stat.Gid)
		}
	}
	originalInfo, e := os.Stat(filepath.Join(memoryDir, "retained"))
	if e != nil || originalInfo.Sys().(*syscall.Stat_t).Nlink != 2 {
		t.Fatalf("owned in-tree hardlink was not preserved: %v", e)
	}
	info, e := os.Stat(outside)
	if e != nil || info.Sys().(*syscall.Stat_t).Uid != uint32(os.Getuid()) {
		t.Fatal("initializer followed image-controlled symlink")
	}
	if count() != 1 {
		t.Fatalf("bootstrap count=%d", count())
	}
	got, e := cli.ContainerInspect(ctx, cid, client.ContainerInspectOptions{})
	if e != nil {
		t.Fatal(e)
	}
	t.Logf("image=%s container=%s started_at=%s arch=%s keeper_hash=%s", f.ImageID, cid, got.Container.State.StartedAt, f.Architecture, got.Container.Config.Labels["crewship.keeper-sha256"])
	if e = p.auditStaged(ctx, got.Container, crew); e != nil {
		t.Fatal(e)
	}
	probe := func(t *testing.T, user string, cmd []string) string {
		t.Helper()
		out, e := p.Exec(ctx, provider.ExecConfig{ContainerID: cid, User: user, Cmd: cmd})
		if e != nil {
			t.Fatal(e)
		}
		defer out.Reader.Close()
		b, e := io.ReadAll(out.Reader)
		if e != nil {
			t.Fatal(e)
		}
		_, code, e := p.ExecInspect(ctx, out.ExecID)
		if e != nil || code != 0 {
			t.Fatalf("probe=%v code=%d err=%v output=%s", cmd, code, e, b)
		}
		return string(b)
	}
	probe(t, "1001:1001", []string{"/bin/sh", "-c", "echo persistent > /home/agent/retained; test $(id -u) = 1001"})
	t.Run("controller_restart_environment", func(t *testing.T) {
		fresh, e := New(ctx, p.cfg, p.logger)
		if e != nil {
			t.Fatal(e)
		}
		defer fresh.Close()
		cmd, env, e := fresh.WrapExternalExec(ctx, cid, []string{"/bin/sh", "-c", "test \"$HOME\" = /home/agent; test \"$STAGED_FIXTURE_ENV\" = retained; test \"$STAGED_EXPAND\" = /usr/bin:/bin; test \"$STAGED_FIXTURE_SECRET\" = fixture-confidential"}, nil)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = fresh.stagedRawExec(ctx, cid, "1001:1001", cmd, env); e != nil {
			t.Fatal(e)
		}
		got, e := cli.ContainerInspect(ctx, cid, client.ContainerInspectOptions{})
		if e != nil {
			t.Fatal(e)
		}
		for _, v := range got.Container.Config.Labels {
			if strings.Contains(v, "fixture-confidential") {
				t.Fatal("confidential env leaked to Docker labels")
			}
		}
	})

	// ContainerEnv is an immutable creation snapshot. Fault controls explicitly
	// recreate only this owned runtime, retaining its binds and named volumes.
	recreate := func(budget context.Context, env map[string]string) error {
		if e := p.RemoveCrewRuntime(ctx, cid); e != nil {
			return e
		}
		crew.ContainerEnv = env
		_, ensureErr := p.EnsureCrewRuntime(budget, crew)
		got, e := cli.ContainerInspect(ctx, p.CrewContainerName(crew.ID, crew.Slug), client.ContainerInspectOptions{})
		if e != nil {
			return errors.Join(ensureErr, e)
		}
		cid = got.Container.ID
		candidateHostnames[got.Container.Config.Hostname] = true
		return ensureErr
	}
	networkCheck := func(t *testing.T) { assertStagedNetwork(t, ctx, p, cid, f, probe) }
	networkCheck(t)
	for _, fault := range []string{"delay", "fail"} {
		name := "delayed_fence"
		if fault == "fail" {
			name = "failing_fence_helper"
		}
		t.Run(name, func(t *testing.T) {
			defer func() { p.stagedFenceHelperTestHook, p.stagedTestHook = nil, nil }()
			for _, mode := range []string{"--fence-apply", "--fence-check"} {
				for attempt := 0; attempt < 3; attempt++ {
					before := count()
					preSealArrivals = arrivals()
					if _, e := cli.ContainerRestart(ctx, cid, client.ContainerRestartOptions{}); e != nil {
						t.Fatal(e)
					}
					observation, e := os.MkdirTemp(root, "helper-fault-")
					if e != nil {
						t.Fatal(e)
					}
					// NET_ADMIN-only root cannot bypass the host owner of this recorder.
					if e := os.Chmod(observation, 0777); e != nil {
						t.Fatal(e)
					}
					mutations, observations := 0, 0
					// Confirmed author seam: mutate only this fixture's trusted helper
					// before Docker create. No image shell or public runtime flag runs.
					p.stagedFenceHelperTestHook = func(config *container.Config, host *container.HostConfig) {
						if len(config.Cmd) == 0 || config.Cmd[0] != mode {
							return
						}
						if len(config.Entrypoint) != 1 || config.User != "0:0" || host.NetworkMode != container.NetworkMode("container:"+cid) {
							t.Fatal("fault callback targeted an unexpected helper")
						}
						mutations++
						config.Entrypoint = []string{"/fixture-fence-wrapper", config.Entrypoint[0]}
						config.Env = append(config.Env, "STAGED_FIXTURE_FAULT="+fault)
						host.Mounts = append(host.Mounts,
							mount.Mount{Type: mount.TypeBind, Source: faultWrapper, Target: "/fixture-fence-wrapper", ReadOnly: true},
							mount.Mount{Type: mount.TypeBind, Source: observation, Target: "/fixture-fence-observation"})
					}
					p.stagedTestHook = func(phase, id string) error {
						if phase != mode+"-started" {
							return nil
						}
						observations++
						if id != cid {
							return errors.New("helper observation targeted a different runtime")
						}
						deadline := time.Now().Add(4 * time.Second)
						started := filepath.Join(observation, "started")
						for {
							if _, e := os.Stat(started); e == nil {
								break
							}
							if time.Now().After(deadline) {
								return errors.New("fault helper process did not write start acknowledgement")
							}
							time.Sleep(10 * time.Millisecond)
						}
						assertNoImage(t, before)
						if fault == "fail" {
							return nil // The actual helper process exits 42; provider must deny.
						}
						finished := filepath.Join(observation, "delay-complete")
						if _, e := os.Stat(finished); !os.IsNotExist(e) {
							return errors.New("helper delay already ended before its fault window was observed")
						}
						for {
							assertNoImage(t, before)
							if _, e := os.Stat(finished); e == nil {
								break
							}
							if time.Now().After(deadline) {
								return errors.New("bounded helper process delay did not finish")
							}
							time.Sleep(100 * time.Millisecond)
						}
						readTime := func(path string) (int64, error) {
							b, e := os.ReadFile(path)
							if e != nil {
								return 0, e
							}
							return strconv.ParseInt(string(b), 10, 64)
						}
						a, e := readTime(started)
						if e != nil {
							return e
						}
						b, e := readTime(finished)
						if e != nil {
							return e
						}
						if time.Duration(b) < 2*time.Second {
							return errors.New("actual helper process did not spend the required interval in delay")
						}
						t.Logf("helper fault=%s mode=%s attempt=%d started_at=%s measured_delay=%s", fault, mode, attempt, time.Unix(0, a).UTC().Format(time.RFC3339Nano), time.Duration(b))
						return nil
					}
					_, e = p.EnsureCrewRuntime(ctx, crew)
					p.stagedFenceHelperTestHook, p.stagedTestHook = nil, nil
					if mutations != 1 || observations != 1 {
						t.Fatalf("actual helper fault not reached exactly once: mutations=%d observations=%d", mutations, observations)
					}
					if fault == "delay" {
						if e != nil {
							t.Fatal(e)
						}
					} else {
						if e == nil || !strings.Contains(e.Error(), "helper exited 42") {
							t.Fatalf("actual failing helper accepted or wrong fault: %v", e)
						}
						assertNoImage(t, before)
						if _, e := p.EnsureCrewRuntime(ctx, crew); e != nil {
							t.Fatal(e)
						}
					}
					if count() != before+1 {
						t.Fatal("helper fault recovery did not bootstrap exactly once")
					}
					networkCheck(t)
				}
			}
		})
	}
	t.Run("failed_kernel_readback", func(t *testing.T) {
		for attempt := 0; attempt < 3; attempt++ {
			before := count()
			preSealArrivals = arrivals()
			if _, e := cli.ContainerRestart(ctx, cid, client.ContainerRestartOptions{}); e != nil {
				t.Fatal(e)
			}
			p.stagedTestHook = func(phase, id string) error {
				if phase != "before-check" {
					return nil
				}
				// Replace the real kernel policy with a deliberately wrong UID allowance.
				// Only the qualified static binary executes in this trusted fault helper.
				source, _, e := p.stageArtifact(ctx, f.ImageID)
				if e != nil {
					return e
				}
				fault, e := cli.ContainerCreate(ctx, client.ContainerCreateOptions{Config: &container.Config{Image: f.ImageID, User: "0:0", Env: keeperEnv(nil, map[string]string{"LD_PRELOAD": "", "BASH_ENV": ""}), Entrypoint: []string{stagedstart.Binary}, Cmd: []string{"--fence-apply", "--fence-allow-uids", "1001"}, Healthcheck: &container.HealthConfig{Test: []string{"NONE"}}}, HostConfig: &container.HostConfig{NetworkMode: container.NetworkMode("container:" + id), ReadonlyRootfs: true, CapDrop: []string{"ALL"}, CapAdd: []string{"NET_ADMIN"}, SecurityOpt: []string{"no-new-privileges"}, Mounts: []mount.Mount{{Type: mount.TypeBind, Source: source, Target: stagedstart.Binary, ReadOnly: true}}}})
				if e != nil {
					return e
				}
				defer cli.ContainerRemove(ctx, fault.ID, client.ContainerRemoveOptions{Force: true})
				if _, e = cli.ContainerStart(ctx, fault.ID, client.ContainerStartOptions{}); e != nil {
					return e
				}
				wait := cli.ContainerWait(ctx, fault.ID, client.ContainerWaitOptions{Condition: container.WaitConditionNotRunning})
				select {
				case result := <-wait.Result:
					if result.StatusCode != 0 {
						return fmt.Errorf("fault helper exit=%d", result.StatusCode)
					}
					return nil
				case e := <-wait.Error:
					return e
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			_, e := p.EnsureCrewRuntime(ctx, crew)
			p.stagedTestHook = nil
			if e == nil || !strings.Contains(e.Error(), "fence absent") {
				t.Fatalf("bad kernel policy accepted or fault failed: %v", e)
			}
			assertNoImage(t, before)
			if _, e = p.EnsureCrewRuntime(ctx, crew); e != nil {
				t.Fatal(e)
			}
			if count() != before+1 {
				t.Fatal("kernel failure recovery did not bootstrap once")
			}
			networkCheck(t)
		}
	})
	t.Run("restart_races", func(t *testing.T) {
		for _, phase := range []string{"before-fence", "before-check", "after-check", "before-bootstrap-start"} {
			for i := 0; i < 3; i++ {
				before := count()
				preSealArrivals = arrivals()
				if _, e = cli.ContainerRestart(ctx, cid, client.ContainerRestartOptions{}); e != nil {
					t.Fatal(e)
				}
				fired := false
				p.stagedTestHook = func(at, id string) error {
					if at == phase && !fired {
						fired = true
						_, e := cli.ContainerRestart(ctx, id, client.ContainerRestartOptions{})
						return e
					}
					return nil
				}
				_, e = p.EnsureCrewRuntime(ctx, crew)
				if !errors.Is(e, errStagedDenied) && !(phase == "before-check" && e != nil && strings.Contains(e.Error(), "fence absent")) {
					t.Fatalf("restart at %s lacked stale-generation/kernel denial: %v", phase, e)
				}
				current, inspectErr := cli.ContainerInspect(ctx, cid, client.ContainerInspectOptions{})
				if inspectErr != nil {
					t.Fatal(inspectErr)
				}
				if current.Container.State.Running {
					state, stateErr := p.stagedControl(ctx, cid, "status", "")
					if stateErr != nil || state.Phase != "staging" {
						t.Fatalf("restarted generation phase=%+v err=%v", state, stateErr)
					}
				} else if phase != "before-check" {
					t.Fatal("unexpected stopped generation at restart boundary")
				}
				p.stagedTestHook = nil
				if !fired {
					t.Fatalf("restart injection at %s did not execute", phase)
				}
				assertNoImage(t, before)
				if _, e = p.EnsureCrewRuntime(ctx, crew); e != nil {
					t.Fatal(e)
				}
				if count() != before+1 {
					t.Fatal("restart bootstrap duplicated or absent")
				}
				networkCheck(t)
			}
		}
	})
	t.Run("workload_material_before_reservation", func(t *testing.T) {
		before := count()
		preSealArrivals = arrivals()
		if _, e := cli.ContainerRestart(ctx, cid, client.ContainerRestartOptions{}); e != nil {
			t.Fatal(e)
		}
		got, e := cli.ContainerInspect(ctx, cid, client.ContainerInspectOptions{})
		if e != nil {
			t.Fatal(e)
		}
		material, e := p.stagedEnvPath(got.Container)
		if e != nil {
			t.Fatal(e)
		}
		defer os.Chmod(material, 0600)
		fired := false
		p.stagedTestHook = func(phase, id string) error {
			if phase != "before-reserve" {
				return nil
			}
			fired = true
			return os.Chmod(material, 0000)
		}
		err := p.ensureStagedStart(ctx, crew, cid)
		p.stagedTestHook = nil
		if !fired || !errors.Is(err, errStagedDenied) || !strings.Contains(err.Error(), "before reservation") {
			t.Fatalf("material fault not reached before permit reservation: %v", err)
		}
		state, e := p.stagedControl(ctx, cid, "status", "")
		if e != nil || state.Phase != "sealed" {
			t.Fatalf("material failure burned bootstrap permit: %+v %v", state, e)
		}
		assertNoImage(t, before)
		if e := os.Chmod(material, 0600); e != nil {
			t.Fatal(e)
		}
		if _, e := p.EnsureCrewRuntime(ctx, crew); e != nil {
			t.Fatal(e)
		}
		if count() != before+1 {
			t.Fatal("material recovery did not use the original permit exactly once")
		}
		networkCheck(t)
	})
	t.Run("unknown_attach_reservation", func(t *testing.T) {
		before := count()
		preSealArrivals = arrivals()
		if _, e := cli.ContainerRestart(ctx, cid, client.ContainerRestartOptions{}); e != nil {
			t.Fatal(e)
		}
		attachAttempts, readbacks := 0, 0
		p.stagedTestHook = func(phase, id string) error {
			if phase == "after-check" {
				readbacks++
			}
			if phase == "before-bootstrap-start" {
				attachAttempts++
				return errors.New("synthetic unknown exec attach")
			}
			return nil
		}
		err := p.ensureStagedStart(ctx, crew, cid)
		if attachAttempts != 1 {
			t.Fatalf("unknown attach boundary not reached once: %d", attachAttempts)
		}
		if err == nil || !strings.Contains(err.Error(), "synthetic unknown exec attach") {
			t.Fatalf("unknown attach lacked injected outcome error: %v", err)
		}
		state, e := p.stagedControl(ctx, cid, "status", "")
		if e != nil || state.Phase != "reserved" {
			t.Fatalf("unknown attach state=%+v err=%v", state, e)
		}
		retry, stop := context.WithTimeout(ctx, 3*time.Second)
		_, err = p.EnsureCrewRuntime(retry, crew)
		stop()
		p.stagedTestHook = nil
		if attachAttempts != 1 || readbacks != 1 {
			t.Fatalf("reserved generation retry repeated attach/readback: attaches=%d readbacks=%d", attachAttempts, readbacks)
		}
		if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, errStagedDenied) {
			t.Fatalf("reserved generation did not reach bounded reconciliation deadline: %v", err)
		}
		state, e = p.stagedControl(ctx, cid, "status", "")
		if e != nil || state.Phase != "reserved" {
			t.Fatalf("unknown reservation changed phase: %+v %v", state, e)
		}
		assertNoImage(t, before)
		if _, e = cli.ContainerRestart(ctx, cid, client.ContainerRestartOptions{}); e != nil {
			t.Fatal(e)
		}
		if _, e = p.EnsureCrewRuntime(ctx, crew); e != nil {
			t.Fatal(e)
		}
		if count() != before+1 {
			t.Fatal("unknown attach recovery did not bootstrap once")
		}
		networkCheck(t)
	})
	t.Run("unknown_result", func(t *testing.T) {
		before := count()
		preSealArrivals = arrivals()
		if _, e = cli.ContainerRestart(ctx, cid, client.ContainerRestartOptions{}); e != nil {
			t.Fatal(e)
		}
		lostReplies := 0
		p.stagedTestHook = func(phase, id string) error {
			if phase == "bootstrap-result" {
				lostReplies++
				time.Sleep(25 * time.Millisecond)
				return errors.New("synthetic lost exec reply")
			}
			return nil
		}
		if _, e = p.EnsureCrewRuntime(ctx, crew); e != nil {
			t.Fatalf("confirmed result not reconciled: %v", e)
		}
		p.stagedTestHook = nil
		if _, e = p.EnsureCrewRuntime(ctx, crew); e != nil {
			t.Fatal(e)
		}
		if lostReplies != 1 || count() != before+1 {
			t.Fatal("unknown result retried image bootstrap")
		}
		networkCheck(t)
	})
	t.Run("concurrent_controller_reuse", func(t *testing.T) {
		before := count()
		preSealArrivals = arrivals()
		if _, e := cli.ContainerRestart(ctx, cid, client.ContainerRestartOptions{}); e != nil {
			t.Fatal(e)
		}
		other := &Provider{client: cli, cfg: p.cfg, logger: p.logger, digestResolver: p.digestResolver}
		var wg sync.WaitGroup
		errs := make(chan error, 2)
		for _, current := range []*Provider{p, other} {
			wg.Add(1)
			go func(current *Provider) { defer wg.Done(); _, e := current.EnsureCrewRuntime(ctx, crew); errs <- e }(current)
		}
		wg.Wait()
		close(errs)
		for e := range errs {
			if e != nil {
				t.Fatal(e)
			}
		}
		if count() != before+1 {
			t.Fatal("controllers did not share one bootstrap")
		}
		networkCheck(t)
	})
	t.Run("auto_restart", func(t *testing.T) {
		for i := 0; i < 3; i++ {
			// Use a fresh retry budget, independent of preceding injected restarts.
			if e := recreate(ctx, originalEnv); e != nil {
				t.Fatal(e)
			}
			got, e := cli.ContainerInspect(ctx, cid, client.ContainerInspectOptions{})
			if e != nil {
				t.Fatal(e)
			}
			started, e := time.Parse(time.RFC3339Nano, got.Container.State.StartedAt)
			if e != nil {
				t.Fatal(e)
			}
			// Docker arms/resets its restart manager after a successful ten-second start.
			if wait := time.Until(started.Add(11 * time.Second)); wait > 0 {
				timer := time.NewTimer(wait)
				select {
				case <-timer.C:
				case <-ctx.Done():
					timer.Stop()
					t.Fatal(ctx.Err())
				}
			}
			before := count()
			preSealArrivals = arrivals()
			old, e := p.stagedControl(ctx, cid, "status", "")
			if e != nil {
				t.Fatal(e)
			}
			// Docker Kill marks a manual stop. Crash Go PID1 through its installed
			// fatal signal handler after ready admission, so the daemon policy fires.
			crash, e := p.Exec(ctx, provider.ExecConfig{ContainerID: cid, User: "1002:1002", Cmd: []string{"/bin/kill", "-ABRT", "1"}})
			if e != nil {
				t.Fatal(e)
			}
			_, _ = io.Copy(io.Discard, crash.Reader)
			_ = crash.Reader.Close() // The intentional runtime crash can interrupt exec.

			for j := 0; j < 500; j++ {
				got, e := cli.ContainerInspect(ctx, cid, client.ContainerInspectOptions{})
				if e == nil && got.Container.State.Running && !got.Container.State.Restarting {
					next, e := p.stagedControl(ctx, cid, "status", "")
					if e == nil && next.Nonce != old.Nonce {
						break
					}
				}
				if j == 499 {
					t.Fatalf("auto-restart did not create keeper boot: state=%+v restart_count=%d err=%v", got.Container.State, got.Container.RestartCount, e)
				}
				time.Sleep(20 * time.Millisecond)
			}
			assertNoImage(t, before)
			if _, e := p.EnsureCrewRuntime(ctx, crew); e != nil {
				t.Fatal(e)
			}
			if count() != before+1 {
				t.Fatal("auto-restart duplicated bootstrap")
			}
			networkCheck(t)
		}
	})
	t.Run("legacy_opt_in", func(t *testing.T) {
		legacy := provider.CrewConfig{ID: "legacy", Slug: "legacy", NetworkMode: "restricted"}
		dirs, e := p.prepareCrewDirs(legacy)
		if e != nil {
			t.Fatal(e)
		}
		if e = os.Chmod(dirs.workspace, 0777); e != nil {
			t.Fatal(e)
		}
		canary := filepath.Join(dirs.workspace, "preserved")
		if e = os.WriteFile(canary, []byte("retained"), 0644); e != nil {
			t.Fatal(e)
		}
		made, e := cli.ContainerCreate(ctx, client.ContainerCreateOptions{Name: p.CrewContainerName(legacy.ID, legacy.Slug), Config: &container.Config{Image: f.BaseID, User: "1001:1001", Entrypoint: []string{"/bin/sh", "-c", "exec sleep infinity"}, Labels: resourcelifecycle.WithInstanceLabel(map[string]string{crewCrewIDLabel: legacy.ID, crewCrewLabel: legacy.Slug, crewKindLabel: crewRuntimeKind}, prefix)}, HostConfig: &container.HostConfig{NetworkMode: container.NetworkMode(prefix), Mounts: []mount.Mount{{Type: mount.TypeBind, Source: dirs.workspace, Target: "/workspace"}}}})
		if e != nil {
			t.Fatal(e)
		}
		defer func() {
			cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
			defer stop()
			_, _ = cli.ContainerRemove(cleanup, made.ID, client.ContainerRemoveOptions{Force: true})
		}()
		if _, e = cli.ContainerStart(ctx, made.ID, client.ContainerStartOptions{}); e != nil {
			t.Fatal(e)
		}
		cfg := p.cfg
		cfg.EgressFenceCrews = []string{legacy.ID}
		controller := &Provider{client: cli, cfg: cfg, logger: p.logger, digestResolver: p.digestResolver}
		for _, running := range []bool{true, false} {
			if !running {
				if _, e = cli.ContainerStop(ctx, made.ID, client.ContainerStopOptions{}); e != nil {
					t.Fatal(e)
				}
			}
			before, e := cli.ContainerInspect(ctx, made.ID, client.ContainerInspectOptions{})
			if e != nil {
				t.Fatal(e)
			}
			if _, e = controller.EnsureCrewRuntime(ctx, legacy); !errors.Is(e, errStagedDenied) {
				t.Fatalf("legacy opt-in lacked immutable-config denial: %v", e)
			}
			after, e := cli.ContainerInspect(ctx, made.ID, client.ContainerInspectOptions{})
			if e != nil || after.Container.State.Running != running || after.Container.State.StartedAt != before.Container.State.StartedAt {
				t.Fatalf("legacy runtime changed: %v", e)
			}
			if b, e := os.ReadFile(canary); e != nil || string(b) != "retained" {
				t.Fatal("legacy canary removed")
			}
			sibling, e := cli.ContainerInspect(ctx, cid, client.ContainerInspectOptions{})
			if e != nil || !sibling.Container.State.Running {
				t.Fatal("sibling changed")
			}
		}
		// The stopped legacy runtime is explicitly drained/removed, never migrated
		// by Ensure. The same workspace and sibling must survive staged recreation.
		if e = controller.RemoveCrewRuntime(ctx, made.ID); e != nil {
			t.Fatal(e)
		}
		phases := map[string]int{}
		controller.stagedTestHook = func(phase, id string) error {
			phases[phase]++
			return nil
		}
		recreated, e := controller.EnsureCrewRuntime(ctx, legacy)
		controller.stagedTestHook = nil
		if e != nil {
			t.Fatal(e)
		}
		defer func() {
			cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
			defer stop()
			if e := controller.RemoveCrewRuntime(cleanup, recreated); e != nil {
				t.Errorf("recreated legacy cleanup: %v", e)
			}
			if e := controller.RemoveCrewVolumes(cleanup, legacy.ID, legacy.Slug); e != nil {
				t.Errorf("recreated legacy volume cleanup: %v", e)
			}
		}()
		for _, phase := range []string{"before-fence", "before-check", "after-check", "before-bootstrap-start"} {
			if phases[phase] != 1 {
				t.Fatalf("legacy recreate phase %s count=%d", phase, phases[phase])
			}
		}
		if _, e = controller.EnsureCrewRuntime(ctx, legacy); e != nil {
			t.Fatal(e)
		}
		b, e := os.ReadFile(filepath.Join(dirs.workspace, "bootstrap-runs"))
		if e != nil || string(b) != "1001\n" {
			t.Fatalf("legacy recreate did not bootstrap exactly once: %q %v", b, e)
		}
		if b, e := os.ReadFile(canary); e != nil || string(b) != "retained" {
			t.Fatal("legacy recreate lost persistent canary")
		}
		sibling, e := cli.ContainerInspect(ctx, cid, client.ContainerInspectOptions{})
		if e != nil || !sibling.Container.State.Running {
			t.Fatal("legacy recreate changed sibling")
		}

	})
	t.Run("selector_removed", func(t *testing.T) {
		before := count()
		preSealArrivals = arrivals()
		cfg := p.cfg
		cfg.EgressFenceCrews = nil
		controller := &Provider{client: cli, cfg: cfg, logger: p.logger, digestResolver: p.digestResolver}
		if _, e := controller.EnsureCrewRuntime(ctx, crew); e != nil {
			t.Fatal(e)
		}
		if count() != before {
			t.Fatal("selector removal duplicated ready bootstrap")
		}
		if _, e := cli.ContainerRestart(ctx, cid, client.ContainerRestartOptions{}); e != nil {
			t.Fatal(e)
		}
		out, e := controller.Exec(ctx, provider.ExecConfig{ContainerID: cid, User: "1001:1001", Cmd: []string{"/bin/sh", "-c", "echo bypass >> /workspace/selector-bypass"}})
		if out != nil {
			defer out.Reader.Close()
		}
		if !errors.Is(e, errStagedDenied) && !errors.Is(e, errFenceNotInPlace) {
			t.Fatalf("selector removal lacked unready gate denial: %v", e)
		}
		state, stateErr := controller.stagedControl(ctx, cid, "status", "")
		if stateErr != nil || state.Phase != "staging" {
			t.Fatalf("selector-removed restart phase=%+v err=%v", state, stateErr)
		}
		assertNoImage(t, before)
		if _, e = controller.EnsureCrewRuntime(ctx, crew); e != nil {
			t.Fatal(e)
		}
		if count() != before+1 {
			t.Fatal("selector removal restart did not bootstrap exactly once")
		}
		if _, e := os.Stat(filepath.Join(workspace, "selector-bypass")); !os.IsNotExist(e) {
			t.Fatal("selector removal executed unready image command")
		}
		networkCheck(t)
	})
	t.Run("HANG_BOOTSTRAP", func(t *testing.T) {
		if e := os.WriteFile(releaseFile, []byte("0"), 0666); e != nil {
			t.Fatal(e)
		}
		before := count()
		preSealArrivals = arrivals()
		budget, stop := context.WithTimeout(ctx, 12*time.Second)
		e := recreate(budget, map[string]string{"HANG_BOOTSTRAP": "1"})
		stop()
		if !errors.Is(e, context.DeadlineExceeded) || count() != before+1 {
			t.Fatalf("nonfinishing bootstrap did not start once and reach deadline: count=%d err=%v", count(), e)
		}
		retry, stop := context.WithTimeout(ctx, 3*time.Second)
		_, e = p.EnsureCrewRuntime(retry, crew)
		stop()
		if (!errors.Is(e, context.DeadlineExceeded) && !errors.Is(e, errStagedDenied)) || count() != before+1 {
			t.Fatalf("nonfinishing bootstrap retry lacked bounded denial: count=%d err=%v", count(), e)
		}
		state, stateErr := p.stagedControl(ctx, cid, "status", "")
		if stateErr != nil || state.Phase != "bootstrapping" {
			t.Fatalf("unfinished bootstrap state=%+v err=%v", state, stateErr)
		}
		if _, e := p.stagedRawExec(ctx, cid, "1001:1001", []string{stagedstart.Binary, "--staged-bootstrap", state.Nonce}, nil); e == nil || !strings.Contains(e.Error(), "exited 126") {
			t.Fatal("unfinished consumed permit admitted another bootstrap")
		}
		assertNoImage(t, before+1) // The one old sealed bootstrap is permitted.
		if e := recreate(ctx, originalEnv); e != nil {
			t.Fatal(e)
		}
		if count() != before+2 {
			t.Fatal("nonfinishing bootstrap recovery did not release one new bootstrap")
		}
		networkCheck(t)
	})
	t.Run("mid_bootstrap_restart", func(t *testing.T) {
		for attempt := 0; attempt < 3; attempt++ {
			if e := os.WriteFile(releaseFile, []byte("0"), 0666); e != nil {
				t.Fatal(e)
			}
			before := count()
			preSealArrivals = arrivals()
			crew.ContainerEnv = map[string]string{"HANG_BOOTSTRAP": "1"}
			if e := p.RemoveCrewRuntime(ctx, cid); e != nil {
				t.Fatal(e)
			}
			budget, stop := context.WithTimeout(ctx, 15*time.Second)
			result := make(chan error, 1)
			go func() { _, e := p.EnsureCrewRuntime(budget, crew); result <- e }()
			deadline := time.Now().Add(10 * time.Second)
			for count() == before && time.Now().Before(deadline) {
				time.Sleep(25 * time.Millisecond)
			}
			if count() != before+1 {
				stop()
				<-result
				t.Fatal("mid-bootstrap injection did not observe a started bootstrap")
			}
			got, e := cli.ContainerInspect(ctx, p.CrewContainerName(crew.ID, crew.Slug), client.ContainerInspectOptions{})
			if e != nil {
				stop()
				<-result
				t.Fatal(e)
			}
			cid = got.Container.ID
			candidateHostnames[got.Container.Config.Hostname] = true
			state, e := p.stagedControl(ctx, cid, "status", "")
			if e != nil || state.Phase != "bootstrapping" {
				stop()
				<-result
				t.Fatalf("mid-bootstrap injection lacks live bootstrap: %+v %v", state, e)
			}
			_, restartErr := cli.ContainerRestart(ctx, cid, client.ContainerRestartOptions{})
			e = <-result
			stop()
			if restartErr != nil || (!errors.Is(e, errStagedDenied) && !(e != nil && strings.Contains(e.Error(), "bootstrap outcome unknown") && strings.Contains(e.Error(), "exited 137"))) {
				t.Fatalf("mid-bootstrap restart was accepted or failed injection: restart=%v ensure=%v", restartErr, e)
			}
			nextInspect, inspectErr := cli.ContainerInspect(ctx, cid, client.ContainerInspectOptions{})
			if inspectErr != nil || nextInspect.Container.State.StartedAt == got.Container.State.StartedAt {
				t.Fatalf("mid-bootstrap fault lacked a changed Docker generation: %v", inspectErr)
			}
			if nextInspect.Container.State.Running {
				next, stateErr := p.stagedControl(ctx, cid, "status", "")
				if stateErr != nil || next.Phase != "staging" || next.Nonce == state.Nonce {
					t.Fatalf("mid-bootstrap restart lacked fresh staging nonce: %+v %v", next, stateErr)
				}
			} else if !midBootstrapFailClosedStop(nextInspect.Container.State, cid, e) {
				t.Fatalf("unexpected stop without fail-closed unknown-bootstrap verdict: state=%+v err=%v", nextInspect.Container.State, e)
			}
			t.Logf("mid-bootstrap attempt=%d old_started_at=%s next_state=%+v ensure=%v", attempt+1, got.Container.State.StartedAt, nextInspect.Container.State, e)
			assertNoImage(t, before+1)
			if e := os.WriteFile(releaseFile, []byte("1"), 0666); e != nil {
				t.Fatal(e)
			}
			if _, e := p.EnsureCrewRuntime(ctx, crew); e != nil {
				t.Fatal(e)
			}
			if count() != before+2 {
				t.Fatal("mid-bootstrap restart released duplicate or absent new bootstrap")
			}
			recovered, stateErr := p.stagedControl(ctx, cid, "status", "")
			if stateErr != nil || recovered.Phase != "ready" || recovered.Nonce == state.Nonce {
				t.Fatalf("mid-bootstrap recovery reused stale boot: %+v %v", recovered, stateErr)
			}
			networkCheck(t)
		}
	})
	t.Run("FAIL_BOOTSTRAP", func(t *testing.T) {
		before := count()
		preSealArrivals = arrivals()
		observedFailure, observingFailure := false, false
		p.stagedTestHook = func(phase, id string) error {
			if phase != "bootstrap-result" || observingFailure {
				return nil
			}
			observingFailure = true
			defer func() { observingFailure = false }()
			// Observe the live failed generation before Ensure's fail-closed stop.
			deadline := time.Now().Add(time.Second)
			var state stagedstart.Status
			for {
				var e error
				state, e = p.stagedControl(ctx, id, "status", "")
				if e != nil {
					return e
				}
				if state.Phase == "failed" {
					break
				}
				if time.Now().After(deadline) {
					return errors.New("nonzero bootstrap did not enter failed phase")
				}
				time.Sleep(10 * time.Millisecond)
			}
			if e := p.ensureStagedStart(ctx, crew, id); !errors.Is(e, errStagedDenied) {
				return errors.New("failed generation reused")
			}
			if count() != before+1 {
				return errors.New("failed bootstrap blindly retried")
			}
			if _, e := p.stagedRawExec(ctx, id, "1001:1001", []string{stagedstart.Binary, "--staged-bootstrap", state.Nonce}, nil); e == nil || !strings.Contains(e.Error(), "exited 126") {
				return errors.New("consumed permit replay admitted")
			}
			observedFailure = true
			return nil
		}
		defer func() { p.stagedTestHook = nil }()
		if e := recreate(ctx, map[string]string{"FAIL_BOOTSTRAP": "1"}); e == nil {
			t.Fatal("failed bootstrap marked ready")
		}
		p.stagedTestHook = nil
		if !observedFailure || count() != before+1 {
			t.Fatal("live failed generation/replay control did not execute exactly once")
		}
		got, e := cli.ContainerInspect(ctx, cid, client.ContainerInspectOptions{})
		if e != nil || got.Container.State.Running {
			t.Fatalf("failed bootstrap not stopped: %v", e)
		}
		assertNoImage(t, before+1)
		if e := recreate(ctx, originalEnv); e != nil {
			t.Fatal(e)
		}
		if count() != before+2 {
			t.Fatal("failed bootstrap recovery did not bootstrap exactly once")
		}
	})
	probe(t, "1001:1001", []string{"/bin/sh", "-c", "test $(cat /home/agent/retained) = persistent"})
	networkCheck(t)
}

// The interrupted exec must be SIGKILLed by the injected restart, and the
// keeper status read must fail or remain incomplete. No other unknown
// outcome justifies accepting a stopped replacement generation.
func midBootstrapFailClosedStop(state *container.State, id string, err error) bool {
	if state == nil || state.Running || state.Status != "exited" || state.OOMKilled || state.Dead || state.Error != "" || err == nil {
		return false
	}
	// stopUnfenced sends Docker's default TERM to the replacement keeper.
	// Its signal.Notify handler exits zero; TERM before that registration can
	// terminate the Go process with 128+SIGTERM instead. Neither is readiness.
	bootstrap, status, joined := strings.Cut(err.Error(), "\n")
	return (state.ExitCode == 0 || state.ExitCode == 128+int(syscall.SIGTERM)) &&
		joined && strings.HasPrefix(bootstrap, "staged start: bootstrap outcome unknown: staged start: trusted exec ") &&
		strings.HasSuffix(bootstrap, " incomplete or exited 137 (bounded stderr 0 bytes withheld)") &&
		(status == "Error response from daemon: container "+id+" is not running" ||
			(strings.HasPrefix(status, "staged start: trusted exec ") &&
				strings.HasSuffix(status, " incomplete or exited 0 (bounded stderr 0 bytes withheld)")))
}

func TestMidBootstrapFailClosedStop(t *testing.T) {
	const id = "replacement-container"
	interrupted := fmt.Errorf("staged start: bootstrap outcome unknown: %w", errors.Join(
		errors.New("staged start: trusted exec interrupted-exec incomplete or exited 137 (bounded stderr 0 bytes withheld)"),
		fmt.Errorf("Error response from daemon: container %s is not running", id)))
	for _, tc := range []struct {
		name string
		code int
		err  error
		want bool
	}{
		{"graceful stop", 0, interrupted, true},
		{"TERM during keeper startup", 143, interrupted, true},
		{"incomplete status exec", 143, fmt.Errorf("%s", strings.ReplaceAll(interrupted.Error(), "Error response from daemon: container "+id+" is not running", "staged start: trusted exec status-exec incomplete or exited 0 (bounded stderr 0 bytes withheld)")), true},
		{"SIGKILL replacement", 137, interrupted, false},
		{"keeper failure", 1, interrupted, false},
		{"no verdict", 143, nil, false},
		{"arbitrary unknown outcome", 143, errors.New("bootstrap outcome unknown"), false},
		{"staged denial only", 143, errStagedDenied, false},
		{"wrong container", 143, fmt.Errorf("%s", strings.ReplaceAll(interrupted.Error(), id, "other-container")), false},
		{"wrong exec exit", 143, fmt.Errorf("%s", strings.ReplaceAll(interrupted.Error(), "exited 137", "exited 1")), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := &container.State{Status: "exited", ExitCode: tc.code}
			if got := midBootstrapFailClosedStop(state, id, tc.err); got != tc.want {
				t.Fatalf("accepted=%v want=%v state=%+v err=%v", got, tc.want, state, tc.err)
			}
		})
	}
}
