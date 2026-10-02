//go:build linux

package docker

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"

	"github.com/crewship-ai/crewship/internal/backup"
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

	network := networkTestID("egresspilot-it")
	t.Setenv("CREWSHIP_RUNTIME", "runc")
	var p *Provider
	p, err = New(ctx, Config{
		ContainerPrefix:   network,
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
	// CI runners start without alpine:3; the peer and sink below create
	// containers from it before EnsureCrewRuntime would pull it.
	if err := p.pullSidecarImage(ctx, "alpine:3"); err != nil {
		t.Fatalf("pull alpine:3: %v", err)
	}

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
	// A second listener that holds each connection open, for the
	// opened-before-the-fence case.
	sink, err := p.client.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config:     &container.Config{Image: "alpine:3", Cmd: []string{"sh", "-c", "while :; do nc -l -p 7001 | while read l; do date +%s%N > /tmp/last; done; done"}},
		HostConfig: &container.HostConfig{NetworkMode: container.NetworkMode(network)},
	})
	if err != nil {
		t.Fatalf("create sink: %v", err)
	}
	defer func() {
		_, _ = p.client.ContainerRemove(context.Background(), sink.ID, client.ContainerRemoveOptions{Force: true})
	}()
	if _, err := p.client.ContainerStart(ctx, sink.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatalf("start sink: %v", err)
	}
	si, err := p.client.ContainerInspect(ctx, sink.ID, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	sinkIP := si.Container.NetworkSettings.Networks[network].IPAddress.String()
	pi, err := p.client.ContainerInspect(ctx, peer.ID, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	peerIP := pi.Container.NetworkSettings.Networks[network].IPAddress.String()

	team := provider.CrewConfig{ID: "fence-it-001", Slug: "fence-it", NetworkMode: "restricted", MemoryMB: 256, CPUs: 0.5}
	// Ensure can replace a container. Resolve its unique fixture name at cleanup
	// time, including when Ensure fails after creating a replacement.
	cleanupCrew := func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		name := p.CrewContainerName(team.ID, team.Slug)
		if _, err := p.client.ContainerRemove(cleanupCtx, name, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true}); err != nil && !cerrdefs.IsNotFound(err) {
			t.Errorf("remove fixture container %s: %v", name, err)
		}
		// Production removal deliberately retains persistent data. This fixture
		// owns these exact volumes and must not leave them on the shared daemon.
		for _, name := range []string{p.homeVolumeName(team.ID, team.Slug), p.toolsVolumeName(team.ID, team.Slug)} {
			if _, err := p.client.VolumeRemove(cleanupCtx, name, client.VolumeRemoveOptions{}); err != nil && !cerrdefs.IsNotFound(err) {
				t.Errorf("remove fixture volume %s: %v", name, err)
			}
		}
	}
	defer cleanupCrew()
	cid, err := p.EnsureCrewRuntime(ctx, team)
	if err != nil {
		t.Fatalf("EnsureCrewRuntime: %v", err)
	}

	probe := []string{"nc", "-w", "2", peerIP, "7000"}
	dns := []string{"nslookup", "example.com"}
	assertExit := func(stage, user string, cmd []string, wantZero bool) {
		t.Helper()
		code := fenceTestExec(ctx, t, p, cid, user, cmd)
		if (code == 0) != wantZero {
			t.Fatalf("%s: %v as uid %s exited %d, want success=%v", stage, cmd, user, code, wantZero)
		}
	}

	// The agent cannot become the allowed UID: no capabilities and
	// no_new_privs, so no setuid route to 1002 or 0.
	assertExit("fenced", "1001", []string{"sh", "-c", `grep -q "^NoNewPrivs:[[:space:]]*1" /proc/self/status && grep -q "^CapEff:[[:space:]]*0000000000000000" /proc/self/status`}, true)
	assertExit("fenced", "1001", probe, false)
	assertExit("fenced", "0", probe, false)
	assertExit("fenced", "1001", dns, false)
	assertExit("fenced", "1002", probe, true)

	// Some callers exec by container NAME (the file-save path). The fence is
	// recorded by id; a name must resolve to the same confirmation, not be
	// refused as unfenced.
	byName, err := p.client.ContainerInspect(ctx, cid, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Exec(ctx, provider.ExecConfig{ContainerID: strings.TrimPrefix(byName.Container.Name, "/"), Cmd: []string{"true"}, User: "1001:1001"}); err != nil {
		t.Fatalf("Exec by container name into a fenced crew: %v", err)
	}

	// Restart behind the provider's back: the namespace is recreated and the
	// fence is gone until the provider notices.
	if _, err := p.client.ContainerRestart(ctx, cid, client.ContainerRestartOptions{}); err != nil {
		t.Fatalf("restart: %v", err)
	}
	assertExit("restarted, before ensure", "1001", probe, true)

	// A connection the agent opens while unfenced must not keep flowing once
	// the fence is in: established is accepted only in the reply direction.
	// Measured at the receiver, because TCP retransmits a rejected segment for
	// minutes instead of failing the sender's write.
	longConn := []string{"sh", "-c", "(while :; do echo x; sleep 0.5; done) | nc " + sinkIP + " 7001"}
	ex, err := p.client.ExecCreate(ctx, cid, client.ExecCreateOptions{Cmd: longConn, User: "1001"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.client.ExecStart(ctx, ex.ID, client.ExecStartOptions{Detach: true}); err != nil {
		t.Fatal(err)
	}
	sinkStalled := []string{"sh", "-c", `a=$(cat /tmp/last 2>/dev/null); sleep 3; b=$(cat /tmp/last 2>/dev/null); [ -n "$a" ] && [ "$a" = "$b" ]`}
	time.Sleep(2 * time.Second)
	if fenceTestExec(ctx, t, p, sink.ID, "0", sinkStalled) == 0 {
		t.Fatal("setup: the unfenced long connection should be delivering data to the sink")
	}

	// A provider exec into the unfenced start (the orchestrator's cached-
	// container path, the terminal) re-installs the fence BEFORE the command
	// runs — never runs it unfenced. The raw client above is the operator's
	// docker socket, which is root-equivalent anyway.
	if code := fenceTestProviderExec(ctx, t, p, cid, "1001:1001", probe); code == 0 {
		t.Fatal("a provider exec after an outside restart reached the peer: it ran unfenced")
	}
	if _, err := p.EnsureCrewRuntime(ctx, team); err != nil {
		t.Fatalf("EnsureCrewRuntime after restart: %v", err)
	}
	if fenceTestExec(ctx, t, p, sink.ID, "0", sinkStalled) != 0 {
		t.Fatal("a connection opened before the fence kept delivering data after it")
	}
	if _, err := p.Exec(ctx, provider.ExecConfig{ContainerID: cid, Cmd: []string{"true"}, User: "1001:1001"}); err != nil {
		t.Fatalf("Exec after re-fence: %v", err)
	}
	assertExit("re-fenced", "1001", probe, false)
	assertExit("re-fenced", "1002", probe, true)

	// Backup flush hooks, restore scripts and memory imports exec with the
	// backup package's own Docker client (review of #2760). Wired with
	// GuardExternalExec, such an exec after an outside restart re-installs
	// the fence before it runs — "restarted, before ensure" above shows the
	// same probe reaches the peer from an unfenced start.
	if _, err := p.client.ContainerRestart(ctx, cid, client.ContainerRestartOptions{}); err != nil {
		t.Fatalf("restart: %v", err)
	}
	ops := &backup.MobyDockerOps{Client: p.client, Guard: p.GuardExternalExec}
	if code, out, err := ops.ExecAs(ctx, cid, "1001:1001", probe); err != nil {
		t.Fatalf("backup exec after an outside restart: %v (%s)", err, out)
	} else if code == 0 {
		t.Fatal("a backup exec after an outside restart reached the peer: it ran unfenced")
	}
	assertExit("re-fenced by a backup exec", "1001", probe, false)

	// Restart racing an exec, at each point between the guard and the
	// process running. Deterministic: the hook restarts the container at
	// exactly that stage, once.
	for _, stage := range []fenceExecStage{fenceStageAfterGuard, fenceStageBeforeStart, fenceStageAfterStart} {
		t.Run(string(stage), func(t *testing.T) {
			if _, err := p.EnsureCrewRuntime(ctx, team); err != nil {
				t.Fatalf("EnsureCrewRuntime: %v", err)
			}
			fired := false
			p.fenceTestHook = func(s fenceExecStage) {
				if s != stage || fired {
					return
				}
				fired = true
				if _, err := p.client.ContainerRestart(ctx, cid, client.ContainerRestartOptions{}); err != nil {
					t.Errorf("hook restart: %v", err)
				}
			}
			defer func() { p.fenceTestHook = nil }()

			marker := "/tmp/raced-" + string(stage)
			_, err := p.Exec(ctx, provider.ExecConfig{ContainerID: cid, Cmd: []string{"sh", "-c", "sleep 1; touch " + marker}, User: "1001:1001"})
			if !fired {
				t.Fatal("hook did not fire")
			}
			if !errors.Is(err, errFenceNotInPlace) {
				t.Fatalf("an exec raced by a restart must be refused, got %v", err)
			}
			switch stage {
			case fenceStageAfterGuard, fenceStageBeforeStart:
				// Refused before it ran: nothing executed in the new start.
				if code := fenceTestExec(ctx, t, p, cid, "0", []string{"test", "-e", marker}); code == 0 {
					t.Fatal("the raced exec ran in the unfenced start")
				}
			case fenceStageAfterStart:
				// It may have started; the container must have been stopped.
				insp, err := p.client.ContainerInspect(ctx, cid, client.ContainerInspectOptions{})
				if err != nil {
					t.Fatal(err)
				}
				if insp.Container.State.Running {
					t.Fatal("a crew container whose exec may have run unfenced must be stopped")
				}
			}
		})
	}
	if _, err := p.EnsureCrewRuntime(ctx, team); err != nil {
		t.Fatalf("EnsureCrewRuntime after races: %v", err)
	}
	assertExit("after races", "1001", probe, false)

	// Review of #2760: once fenced, a container stays fenced by id even when
	// its crew no longer matches the list by label (missing or stale labels,
	// simulated here by emptying the list). An outside restart must still be
	// re-fenced before a provider exec runs.
	listed := p.cfg.EgressFenceCrews
	p.cfg.EgressFenceCrews = nil
	if _, err := p.client.ContainerRestart(ctx, cid, client.ContainerRestartOptions{}); err != nil {
		t.Fatalf("restart: %v", err)
	}
	if code := fenceTestProviderExec(ctx, t, p, cid, "1001:1001", probe); code == 0 {
		t.Fatal("a fenced container whose crew no longer matches by label ran an exec unfenced after a restart")
	}
	p.cfg.EgressFenceCrews = listed

	// Check reads rule contents, not just markers: an in-place edit that
	// keeps every marker (final reject -> accept) must read as not valid.
	tamperPath := filepath.Join(tmp, "fencetamper")
	tb := exec.CommandContext(ctx, "go", "build", "-o", tamperPath, "./testdata/fencetamper")
	tb.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux")
	if out, err := tb.CombinedOutput(); err != nil {
		t.Fatalf("build fencetamper: %v\n%s", err, out)
	}
	if code := fenceTestNetnsRun(ctx, t, p, cid, "alpine:3", sidecarPath, "--fence-check", "--fence-allow-uids", "1002"); code != 0 {
		t.Fatalf("--fence-check on an intact fence exited %d, want 0", code)
	}
	// Two helpers applying at once leave exactly one valid fence, not two
	// rule sets appended to one chain (CodeRabbit on #2760).
	var wg sync.WaitGroup
	codes := make([]int64, 2)
	for i := range codes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			codes[i] = fenceTestNetnsRun(ctx, t, p, cid, "alpine:3", sidecarPath, "--fence-apply", "--fence-allow-uids", "1002")
		}(i)
	}
	wg.Wait()
	if codes[0] != 0 || codes[1] != 0 {
		t.Fatalf("concurrent applies exited %v, want both 0 (one valid fence)", codes)
	}
	if code := fenceTestNetnsRun(ctx, t, p, cid, "alpine:3", sidecarPath, "--fence-check", "--fence-allow-uids", "1002"); code != 0 {
		t.Fatalf("--fence-check after concurrent applies exited %d, want 0", code)
	}

	// Concurrent execs after an outside restart: one install, none of them
	// runs unfenced, and the crew is not stopped.
	if _, err := p.client.ContainerRestart(ctx, cid, client.ContainerRestartOptions{}); err != nil {
		t.Fatalf("restart: %v", err)
	}
	execCodes := make([]int, 4)
	for i := range execCodes {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			execCodes[i] = fenceTestProviderExec(ctx, t, p, cid, "1001:1001", probe)
		}(i)
	}
	wg.Wait()
	for i, c := range execCodes {
		if c == 0 {
			t.Fatalf("concurrent exec %d reached the peer: ran unfenced", i)
		}
	}
	if insp, err := p.client.ContainerInspect(ctx, cid, client.ContainerInspectOptions{}); err != nil || !insp.Container.State.Running {
		t.Fatalf("concurrent re-fencing stopped the crew: err=%v", err)
	}

	if code := fenceTestNetnsRun(ctx, t, p, cid, "alpine:3", tamperPath); code != 0 {
		t.Fatalf("fencetamper exited %d", code)
	}
	assertExit("tampered (final rule now accepts)", "1001", probe, true)
	if code := fenceTestNetnsRun(ctx, t, p, cid, "alpine:3", sidecarPath, "--fence-check", "--fence-allow-uids", "1002"); code != 3 {
		t.Fatalf("--fence-check on a tampered fence exited %d, want 3 (present but not valid)", code)
	}

	// Regression for failed runs that replaced the original container: the
	// deferred cleanup must remove the successor, not just the initial ID.
	if err := p.RemoveCrewRuntime(ctx, cid); err != nil {
		t.Fatalf("remove original fixture runtime: %v", err)
	}
	replacement, err := p.EnsureCrewRuntime(ctx, team)
	if err != nil {
		t.Fatalf("create replacement fixture runtime: %v", err)
	}
	if replacement == cid {
		t.Fatal("replacement fixture reused removed container ID")
	}
	cleanupCrew()
	if _, err := p.client.ContainerInspect(ctx, replacement, client.ContainerInspectOptions{}); !cerrdefs.IsNotFound(err) {
		t.Errorf("replacement fixture survived cleanup: %v", err)
	}
	for _, name := range []string{p.homeVolumeName(team.ID, team.Slug), p.toolsVolumeName(team.ID, team.Slug)} {
		if _, err := p.client.VolumeInspect(ctx, name, client.VolumeInspectOptions{}); !cerrdefs.IsNotFound(err) {
			t.Errorf("fixture volume %s survived cleanup: %v", name, err)
		}
	}

}

// fenceTestNetnsRun runs bin (bind-mounted) in a one-shot container joined to
// cid's network namespace with NET_ADMIN, and returns its exit code.
func fenceTestNetnsRun(ctx context.Context, t *testing.T, p *Provider, cid, image, bin string, args ...string) int64 {
	t.Helper()
	created, err := p.client.ContainerCreate(ctx, client.ContainerCreateOptions{
		Config: &container.Config{Image: image, User: "0:0", Entrypoint: []string{"/x"}, Cmd: args},
		HostConfig: &container.HostConfig{
			NetworkMode: container.NetworkMode("container:" + cid),
			CapDrop:     []string{"ALL"},
			CapAdd:      []string{"NET_ADMIN"},
			Mounts:      []mount.Mount{{Type: mount.TypeBind, Source: bin, Target: "/x", ReadOnly: true}},
		},
	})
	if err != nil {
		t.Fatalf("create netns runner: %v", err)
	}
	defer func() {
		_, _ = p.client.ContainerRemove(context.Background(), created.ID, client.ContainerRemoveOptions{Force: true})
	}()
	if _, err := p.client.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatalf("start netns runner: %v", err)
	}
	wait := p.client.ContainerWait(ctx, created.ID, client.ContainerWaitOptions{Condition: container.WaitConditionNotRunning})
	select {
	case st := <-wait.Result:
		return st.StatusCode
	case err := <-wait.Error:
		t.Fatalf("wait netns runner: %v", err)
	}
	return -1
}

// fenceTestProviderExec runs cmd through Provider.Exec (the guarded path)
// and returns its exit code.
func fenceTestProviderExec(ctx context.Context, t *testing.T, p *Provider, cid, user string, cmd []string) int {
	t.Helper()
	res, err := p.Exec(ctx, provider.ExecConfig{ContainerID: cid, Cmd: cmd, User: user})
	if err != nil {
		t.Fatalf("provider exec %v: %v", cmd, err)
	}
	_, _ = io.Copy(io.Discard, res.Reader)
	_ = res.Reader.Close()
	code, running, err := p.waitExecExit(ctx, res.ExecID, 100)
	if err != nil || running {
		t.Fatalf("provider exec %v did not finish: running=%v err=%v", cmd, running, err)
	}
	return code
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
