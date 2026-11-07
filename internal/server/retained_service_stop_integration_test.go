//go:build integration && linux

package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"

	"github.com/crewship-ai/crewship/internal/config"
	"github.com/crewship-ai/crewship/internal/logging"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/provider/bbolt"
	"github.com/crewship-ai/crewship/internal/runoutput"
	"github.com/crewship-ai/crewship/internal/work"
)

// The whole restart path of #5081, end to end on real processes:
//
//  1. A dispatched background item is claimed and started, and its host-captured
//     turn runs through the production orchestrator in a controller process
//     that systemd owns (transient user scope, KillMode=control-group).
//  2. systemctl stops that scope. systemd itself delivers the stop signal to
//     the controller and to the capture helper it started.
//  3. The helper turns the signal into its own cancellation: it stops exactly
//     its execution (whole tree, real Docker, real sidecar stop process),
//     writes the stop receipt and the terminal result.
//  4. The item is parked with the dispatcher's shutdown reason.
//  5. A freshly constructed Server recovers it from the capture alone.
//
// The background item keeps reconciliation and its slot (possible external
// effects need a decision; #5108 owns capacity), but now carries the captured
// failure. Nothing is started again.
func TestRetainedDockerServiceStopSettlesParkedWork(t *testing.T) {
	sidecar := os.Getenv("CREWSHIP_HOST_CAPTURE_SIDECAR")
	if !filepath.IsAbs(sidecar) {
		t.Fatal("integration requires an absolute, statically linked CREWSHIP_HOST_CAPTURE_SIDECAR")
	}
	if _, err := exec.LookPath("systemd-run"); err != nil {
		t.Fatal("integration requires systemd-run for a real service stop")
	}
	userBus(t)
	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
	defer cancel()
	cli, err := client.New(client.FromEnv)
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	image, err := cli.ImageInspect(ctx, "alpine:3")
	if err != nil {
		t.Fatal(err)
	}
	root, err := os.MkdirTemp("/tmp", "crewship-service-stop-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	script := filepath.Join(root, "fixture-claude")
	// Starts once, leaves a background child and a detached session behind,
	// then runs until something stops it.
	if err = os.WriteFile(script, []byte(`#!/bin/sh
test "$(id -u)" = 1001 || exit 90
case "$PWD" in /output/*) ;; *) exit 92 ;; esac
printf x >> /tmp/starts
sleep 300 &
setsid sleep 301 &
printf 'started\n'
printf '%0200d\n' 0
while :; do sleep 0.05; done
`), 0755); err != nil {
		t.Fatal(err)
	}
	// WriteFile honours the umask; the container user must be able to run it.
	if err = os.Chmod(script, 0755); err != nil {
		t.Fatal(err)
	}
	// Staged like the provider's runtime helper: readable and executable by
	// the container user whatever the caller's umask or file mode.
	staged := filepath.Join(root, "crewship-sidecar")
	helper, err := os.ReadFile(sidecar)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(staged, helper, 0755); err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(staged, 0755); err != nil {
		t.Fatal(err)
	}
	created, err := cli.ContainerCreate(ctx, client.ContainerCreateOptions{Name: "crewship-service-stop-" + strings.ToLower(rand.Text()), Config: &container.Config{Image: image.ID, User: "1001:1001", Cmd: []string{"sleep", "180"}, Labels: map[string]string{"ai.crewship.retained-test": "owned"}}, HostConfig: &container.HostConfig{NetworkMode: "none", ReadonlyRootfs: true, CapDrop: []string{"ALL"}, SecurityOpt: []string{"no-new-privileges"}, Mounts: []mount.Mount{
		{Type: mount.TypeBind, Source: script, Target: "/usr/local/bin/claude", ReadOnly: true},
		{Type: mount.TypeBind, Source: staged, Target: "/usr/local/bin/crewship-sidecar", ReadOnly: true},
	}, Tmpfs: map[string]string{"/tmp": "rw,nosuid,nodev,size=1048576,mode=1777", "/crew": "rw,nosuid,nodev,size=1048576,mode=1777", "/output": "rw,nosuid,nodev,size=1048576,mode=1777"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if _, err := cli.ContainerRemove(cleanup, created.ID, client.ContainerRemoveOptions{Force: true}); err != nil {
			t.Error(err)
		}
	}()
	if _, err = cli.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatal(err)
	}
	inspected, err := cli.ContainerInspect(ctx, created.ID, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}

	// The dispatched item, claimed and started as the dispatcher does.
	db := openTestDB(t)
	mustExec(t, db, `INSERT INTO workspaces(id,name,slug) VALUES('retained-ws','Recovery','retained-ws')`)
	mustExec(t, db, `INSERT INTO agents(id,workspace_id,name,slug,status) VALUES('retained-agent','retained-ws','Agent','retained','RUNNING')`)
	store := work.NewStore(db)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := store.AcceptTx(ctx, tx, work.AcceptRequest{WorkspaceID: "retained-ws", Source: work.SourceWebhook, Class: work.ClassBackground, AgentID: "retained-agent"})
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	claimed, err := store.Claim(ctx, work.ClaimOptions{LeaseOwner: "stopped-server"})
	if err != nil || claimed == nil {
		t.Fatalf("claim: %+v %v", claimed, err)
	}
	runID := claimed.RunID
	locator := "agent-run:" + runID
	if err = store.MarkStarting(ctx, receipt.WorkID, runID, claimed.Generation, locator); err != nil {
		t.Fatal(err)
	}

	outputRoot := filepath.Join(root, "capture")
	if err = os.Mkdir(outputRoot, 0700); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(outputRoot, runID)
	authority, err := json.Marshal(map[string]any{"version": 1, "container_id": created.ID, "image_id": inspected.Container.Image, "started_at": inspected.Container.State.StartedAt, "directory": directory, "fence": false, "crew_id": "", "crew_slug": "", "stop_args": []string{"crewship-sidecar", "run-stop-process"}})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(90 * time.Second)
	input := runoutput.HostInput{Endpoint: cli.DaemonHost(), Authority: authority, Directory: directory, Deadline: deadline, MaxBytes: runoutput.DefaultLimit, Command: runoutput.HostCommand{ContainerID: created.ID, User: "1001:1001", Args: []string{"/usr/local/bin/claude"}}}
	run := orchestrator.RunState{ID: runID, AgentID: "retained-agent", AgentSlug: "retained", WorkspaceID: "retained-ws", ContainerID: created.ID, Status: "running", StartedAt: time.Now().UTC(), Output: &orchestrator.RunOutputState{Version: 2, Directory: filepath.Join(directory, "output"), Deadline: deadline, Adapter: "CLAUDE_CODE", Endpoint: input.Endpoint, Authority: authority}}
	statePath := filepath.Join(root, "state.db")
	spec, err := json.Marshal(retainedDockerControllerInput{Input: input, Run: run, StatePath: statePath})
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	unit := "crewship-service-stop-" + strings.ToLower(rand.Text())
	controller := exec.Command("systemd-run", "--user", "--scope", "--quiet", "--collect", "--unit="+unit,
		"-p", "KillMode=control-group", "-p", "TimeoutStopSec=45",
		executable, "-test.run=^TestRetainedDockerControllerChild$")
	controller.Env = append(os.Environ(), "CREWSHIP_RETAINED_DOCKER_CHILD=1")
	controller.Stdin = bytes.NewReader(spec)
	var diagnostic bytes.Buffer
	controller.Stderr = &diagnostic
	if err = controller.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = exec.Command("systemctl", "--user", "kill", "--signal=SIGKILL", unit+".scope").Run()
		_ = controller.Wait()
	}()
	wait := func(what string, check func() bool) {
		t.Helper()
		for !check() {
			select {
			case <-ctx.Done():
				t.Fatalf("timed out waiting for %s: %v controller=%s", what, ctx.Err(), diagnostic.String())
			case <-time.After(20 * time.Millisecond):
			}
		}
	}
	wait("controller", func() bool { _, err := os.Stat(filepath.Join(root, "controller-ready")); return err == nil })
	wait("output", func() bool {
		data, _ := os.ReadFile(filepath.Join(directory, "output", "events.jsonl"))
		return bytes.Contains(data, []byte(`"kind":"output"`))
	})
	identity, err := runoutput.ReadHostIdentity(directory)
	if err != nil {
		t.Fatal(err)
	}
	// The dispatcher confirms the started runtime (dispatcher.go StartRunning).
	if err = store.StartRunning(ctx, receipt.WorkID, runID, claimed.Generation, locator); err != nil {
		t.Fatal(err)
	}
	// 1: the scan below must be able to see the descendants it later expects gone.
	wait("descendants", func() bool {
		return hostProcessWithCmdline(created.ID, "sleep 300") != "" && hostProcessWithCmdline(created.ID, "sleep 301") != ""
	})

	// The service stop, delivered by systemd to the whole control group.
	began := time.Now()
	if out, err := exec.CommandContext(ctx, "systemctl", "--user", "stop", unit+".scope").CombinedOutput(); err != nil {
		t.Fatalf("systemctl stop: %v %s", err, out)
	}
	if took := time.Since(began); took > 30*time.Second {
		t.Fatalf("service stop took %s: the helper did not end its capture on the signal", took)
	}
	_ = controller.Wait()

	snapshot, err := runoutput.Inspect(ctx, filepath.Join(directory, "output"))
	if err != nil || !snapshot.Complete || snapshot.Result == nil || snapshot.Result.Reason != "execution_cancelled" {
		t.Fatalf("capture has no terminal result after the service stop: %+v %v controller=%s", snapshot, err, diagnostic.String())
	}
	if confirmed, err := runoutput.HostStopConfirmed(directory, identity.ExecID); err != nil || !confirmed {
		t.Fatalf("no whole-tree stop receipt: %v %v\nresult: %+v\n%s", confirmed, err, *snapshot.Result, captureListing(directory))
	}
	if state, err := cli.ExecInspect(ctx, identity.ExecID, client.ExecInspectOptions{}); err != nil || state.Running {
		t.Fatalf("execution still running after the service stop: %+v %v", state, err)
	}
	for _, survivor := range []string{"sleep 300", "sleep 301"} {
		if pid := hostProcessWithCmdline(created.ID, survivor); pid != "" {
			t.Fatalf("descendant %q survived the service stop as pid %s", survivor, pid)
		}
	}

	// The dispatcher's shutdown park for an unconfirmed stop.
	if err = store.Transition(ctx, work.TransitionRequest{WorkID: receipt.WorkID, RunID: runID, Generation: claimed.Generation, To: work.StateNeedsReconciliation, Reason: "server shutdown: runtime stop could not be confirmed at " + locator}); err != nil {
		t.Fatal(err)
	}

	// The restart: a new Server over the persisted controller state.
	state, err := bbolt.New(statePath)
	if err != nil {
		t.Fatal(err)
	}
	defer state.Close()
	cfg := config.Default()
	cfg.Auth.JWTSecret = "test-secret-for-server-routes-test-32"
	cfg.Storage.RunOutputPath = outputRoot
	s := New(cfg, logging.New("error", "json", nil), &Deps{Container: &mockContainer{}, State: state, DB: db})
	defer s.StopBackground()
	for range 2 { // the second sweep must be a no-op
		if err = s.flushRetainedOutput(ctx); err != nil {
			t.Fatal(err)
		}
	}

	item, err := store.Get(ctx, receipt.WorkID)
	if err != nil {
		t.Fatal(err)
	}
	if item.State != work.StateNeedsReconciliation || !item.State.HoldsExecutionSlot() {
		t.Fatalf("background item left reconciliation or its slot: %s", item.State)
	}
	projection, owned, err := store.RunProjection(ctx, runID)
	if err != nil || !owned || !projection.Ready || projection.Status != "FAILED" || projection.Result.ExitCode == nil {
		t.Fatalf("captured outcome not recorded: %+v owned=%v %v", projection, owned, err)
	}
	var evidence string
	var attempts, completed int
	if err = db.QueryRow(`SELECT exit_evidence FROM work_attempts WHERE run_id=?`, runID).Scan(&evidence); err != nil || evidence != "protected host capture" {
		t.Fatalf("exit evidence=%q %v", evidence, err)
	}
	if err = db.QueryRow(`SELECT COUNT(*) FROM work_attempts WHERE work_id=?`, receipt.WorkID).Scan(&attempts); err != nil || attempts != 1 {
		t.Fatalf("attempts=%d %v: recovery must not start the work again", attempts, err)
	}
	if err = db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE trace_id=? AND entry_type='run.completed'`, runID).Scan(&completed); err != nil || completed != 0 {
		t.Fatalf("a stopped run was journaled as completed: %d %v", completed, err)
	}
	// A restarted dispatcher cannot claim it again: reconciliation is not queued work.
	if again, err := store.Claim(ctx, work.ClaimOptions{LeaseOwner: "restarted-server"}); !errors.Is(err, work.ErrNoWork) {
		t.Fatalf("parked work was claimable after the restart: %+v %v", again, err)
	}
	if starts := containerFile(ctx, t, cli, created.ID, "/tmp/starts"); starts != "x" {
		t.Fatalf("the turn started %q times, want exactly once", starts)
	}
}

// userBus points systemctl/systemd-run at the user manager when the caller's
// environment (a batch queue, CI) does not carry it.
func userBus(t *testing.T) {
	t.Helper()
	if os.Getenv("XDG_RUNTIME_DIR") == "" {
		t.Setenv("XDG_RUNTIME_DIR", "/run/user/"+strconv.Itoa(os.Getuid()))
	}
	if os.Getenv("DBUS_SESSION_BUS_ADDRESS") == "" {
		t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path="+os.Getenv("XDG_RUNTIME_DIR")+"/bus")
	}
}

// hostProcessWithCmdline scans the host's /proc (container processes are
// visible there) for a process of this container (by its cgroup) whose full
// command line equals cmdline. Other workloads on the host are ignored.
func hostProcessWithCmdline(containerID, cmdline string) string {
	entries, _ := os.ReadDir("/proc")
	for _, entry := range entries {
		if _, err := strconv.Atoi(entry.Name()); err != nil {
			continue
		}
		raw, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "cmdline"))
		if err != nil {
			continue
		}
		group, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "cgroup"))
		if err != nil || !strings.Contains(string(group), containerID) {
			continue
		}
		if strings.TrimRight(strings.ReplaceAll(string(raw), "\x00", " "), " ") == cmdline {
			return entry.Name()
		}
	}
	return ""
}

func containerFile(ctx context.Context, t *testing.T, cli *client.Client, id, path string) string {
	t.Helper()
	made, err := cli.ExecCreate(ctx, id, client.ExecCreateOptions{User: "1001:1001", Cmd: []string{"cat", path}, AttachStdout: true, AttachStderr: true})
	if err != nil {
		t.Fatal(err)
	}
	attached, err := cli.ExecAttach(ctx, made.ID, client.ExecAttachOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer attached.Close()
	var out bytes.Buffer
	if _, err := io.Copy(&out, attached.Reader); err != nil {
		t.Fatal(err)
	}
	// Docker multiplexes stdout/stderr frames; the payload is the last bytes.
	data := out.Bytes()
	if len(data) > 8 {
		data = data[8:]
	}
	return string(data)
}

// captureListing names the capture's files and shows the small control
// records, so a missing receipt can be diagnosed without the output itself.
func captureListing(directory string) string {
	var out strings.Builder
	_ = filepath.WalkDir(directory, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return nil
		}
		info, _ := entry.Info()
		rel, _ := filepath.Rel(directory, path)
		out.WriteString(rel + " " + strconv.FormatInt(info.Size(), 10) + "\n")
		if rel == filepath.Join("output", "events.jsonl") {
			data, _ := os.ReadFile(path)
			for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
				var event struct {
					Kind, Stream string
					Data         []byte
				}
				if json.Unmarshal([]byte(line), &event) == nil && len(event.Data) > 0 && len(event.Data) < 300 {
					out.WriteString("  " + event.Stream + ": " + strings.TrimSpace(string(event.Data)) + "\n")
				}
			}
		}
		if !strings.HasPrefix(rel, "output") && info.Size() < 512 {
			data, _ := os.ReadFile(path)
			out.WriteString("  " + strings.TrimSpace(string(data)) + "\n")
		}
		return nil
	})
	return out.String()
}
