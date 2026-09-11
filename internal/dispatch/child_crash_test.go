package dispatch

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/testutil"
	"github.com/crewship-ai/crewship/internal/work"
)

// A real process, a real runtime, a real SIGKILL, and a real restart.
//
// The mock crash test in integration_test.go simulates the same window inside
// one process: it is faster, it can crash on demand at instructions a signal
// cannot target, and it stays. What it cannot show is that the evidence a crash
// leaves in the DATABASE is the evidence recovery reads — because in a single
// process, nothing was ever really lost. Here the owner of the attempt is
// killed outright, its runtime outlives it as an orphan, and a second process
// starts over the same file with no memory of the first.
//
// What this still does NOT prove, and must not be described as proving:
// durability against an OS crash or a power cut. SIGKILL ends a process; the
// kernel keeps and eventually writes its page cache. That is a storage-fault
// harness, and the difference is why the handoff lists the layers separately.

const (
	crashChildEnv  = "CREWSHIP_DISPATCH_CRASH_CHILD"
	crashOrphanEnv = "CREWSHIP_DISPATCH_ORPHAN_RUNTIME"
	crashChildMark = "RUNTIME-STARTED "
	// orphanLifetime outlives the test by a wide margin. The orphan is killed
	// by the test that made it; this is only a backstop so a harness that dies
	// before its cleanup cannot leave a process behind forever.
	orphanLifetime = 10 * time.Minute
)

// TestMain lets this binary re-exec itself as two other things: the dispatcher
// that dies, and the runtime process that outlives it.
//
// The runtime is this same binary rather than /bin/sleep so the test depends on
// nothing outside the repository. An external tool would have to be probed for
// and skipped around, and a skip reports the same "ok" as a pass — which is
// exactly the wrong property for the one test that proves a restart does not
// start a second runtime.
func TestMain(m *testing.M) {
	if os.Getenv(crashOrphanEnv) != "" {
		time.Sleep(orphanLifetime)
		os.Exit(0)
	}
	if spec := os.Getenv(crashChildEnv); spec != "" {
		runCrashingDispatcher(spec)
		return // unreachable: the child always dies
	}
	os.Exit(m.Run())
}

// ---------------------------------------------------------------------------
// A runtime that survives its dispatcher.
// ---------------------------------------------------------------------------

// pidfileRuntime creates a real operating-system process and records it at a
// path derived from the run id alone.
//
// The pidfile IS the point. A locator has to be computable before the process
// exists and resolvable afterwards by something that did not create it — that
// is what lets a restarted server ask "is there still a runtime for this
// attempt?" instead of inferring the answer from silence. tmux session names
// play this role in production; a pidfile plays it here for the same reason and
// with the same property: it outlives the process that wrote it.
type pidfileRuntime struct {
	dir string
	// starts counts runtimes CREATED. In the restart half of this test the
	// number that matters is zero.
	starts atomic.Int64
}

func (p *pidfileRuntime) path(runID string) string { return filepath.Join(p.dir, runID+".pid") }

func (p *pidfileRuntime) Locator(a Assignment) string { return "proc:" + a.RunID }

func (p *pidfileRuntime) Classify(_ Assignment, err error) Outcome {
	if err == nil {
		return OutcomeSucceeded
	}
	return OutcomeUnclear
}

func (p *pidfileRuntime) Run(ctx context.Context, a Assignment, started func()) error {
	p.starts.Add(1)
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), crashOrphanEnv+"=1")
	// The crash-child variable must not be inherited, or the orphan would build
	// a dispatcher of its own against the same database.
	cmd.Env = append(cmd.Env, crashChildEnv+"=")
	// No inherited pipes: this process outlives its parent on purpose, and a
	// grandchild holding the parent's stdout would keep the harness's
	// CombinedOutput blocked long after the crash it is waiting for.
	cmd.Stdout, cmd.Stderr, cmd.Stdin = nil, nil, nil
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start the runtime process: %w", err)
	}

	// Written immediately, so the identity is discoverable the instant the
	// process exists rather than once it has said something.
	tmp := p.path(a.RunID) + ".tmp"
	if err := os.WriteFile(tmp, []byte(strconv.Itoa(cmd.Process.Pid)), 0o600); err != nil {
		return fmt.Errorf("write the pidfile: %w", err)
	}
	if err := os.Rename(tmp, p.path(a.RunID)); err != nil {
		return fmt.Errorf("publish the pidfile: %w", err)
	}

	// started() is deliberately NOT called, and the child's confirm poll is set
	// beyond the life of the test. This is the window the crash has to land in:
	// the runtime exists, and nothing has recorded that it does.
	_ = started
	<-ctx.Done()
	return ctx.Err()
}

func (p *pidfileRuntime) Stop(_ context.Context, locator string) (bool, error) {
	pid, err := p.pid(locator)
	if err != nil || pid == 0 {
		return false, err
	}
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return false, err
	}
	_ = os.Remove(p.path(strings.TrimPrefix(locator, "proc:")))
	return true, nil
}

func (p *pidfileRuntime) Alive(_ context.Context, locator string) (bool, error) {
	pid, err := p.pid(locator)
	if err != nil {
		// Unreadable is not absent. Saying "no" here is the exact inference the
		// start protocol exists to prevent.
		return false, err
	}
	if pid == 0 {
		return false, nil
	}
	// Signal 0 asks the kernel without delivering anything.
	if err := syscall.Kill(pid, 0); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// pid returns 0 when no runtime was ever recorded for this locator, and an
// error only when the question could not be answered.
func (p *pidfileRuntime) pid(locator string) (int, error) {
	runID := strings.TrimPrefix(locator, "proc:")
	raw, err := os.ReadFile(p.path(runID))
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(raw)))
}

var _ Runtime = (*pidfileRuntime)(nil)

// ---------------------------------------------------------------------------
// The child: a dispatcher that starts a runtime and is then killed.
// ---------------------------------------------------------------------------

func runCrashingDispatcher(spec string) {
	parts := strings.SplitN(spec, "|", 2)
	if len(parts) != 2 {
		fmt.Fprintf(os.Stderr, "crash child: malformed spec %q\n", spec)
		os.Exit(2)
	}
	dbPath, runtimeDir := parts[0], parts[1]

	db, err := database.Open("file:" + dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "crash child: open: %v\n", err)
		os.Exit(2)
	}
	rt := &pidfileRuntime{dir: runtimeDir}

	d := New(work.NewStore(db.DB), rt, nil, Config{
		Owner:              "crashing-dispatcher",
		Kinds:              []work.Kind{{Source: work.SourceWebhook}},
		PollInterval:       15 * time.Millisecond,
		HeartbeatInterval:  20 * time.Millisecond,
		CancelPollInterval: time.Hour,
		// Beyond the life of this process: the crash must land BEFORE any
		// confirmation, which is the state a restart has the least to go on.
		ConfirmPollInterval: time.Hour,
		RecoveryInterval:    time.Hour,
	}, quiet())
	go func() { _ = d.Run(context.Background()) }()

	// Wait for the runtime to really exist, announce it, and die.
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		entries, _ := os.ReadDir(runtimeDir)
		for _, e := range entries {
			if !strings.HasSuffix(e.Name(), ".pid") {
				continue
			}
			raw, err := os.ReadFile(filepath.Join(runtimeDir, e.Name()))
			if err != nil {
				continue
			}
			fmt.Printf("%s%s %s\n", crashChildMark,
				strings.TrimSuffix(e.Name(), ".pid"), strings.TrimSpace(string(raw)))
			os.Stdout.Sync()
			die()
		}
		time.Sleep(5 * time.Millisecond)
	}
	fmt.Fprintln(os.Stderr, "crash child: no runtime was ever created")
	os.Exit(2)
}

// die SIGKILLs this process. Not os.Exit: an orderly exit unwinds defers,
// flushes buffers and closes the database — precisely the cooperation a crash
// does not offer.
func die() {
	_ = syscall.Kill(os.Getpid(), syscall.SIGKILL)
	os.Exit(97)
}

// ---------------------------------------------------------------------------
// The test.
// ---------------------------------------------------------------------------

func TestChildCrash_ARestartFindsTheOrphanedRuntimeAndStartsNoSecondOne(t *testing.T) {
	db := testutil.MigratedDB(t)
	path := db.Path()
	store := work.NewStore(db.DB)

	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	rec, err := store.AcceptTx(ctx, tx, work.AcceptRequest{
		WorkspaceID: "ws1", Source: work.SourceWebhook,
		Class: work.ClassBackground, AgentID: "agent-jamie",
	})
	if err != nil {
		_ = tx.Rollback()
		t.Fatalf("accept: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	// The child writes through its own handle; close ours so the two are not
	// sharing a connection pool across a fork.
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	runtimeDir := t.TempDir()
	runID, pid := runCrashingChild(t, path, runtimeDir)
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })

	// The runtime outlived its dispatcher. That is the whole premise: if it had
	// not, there would be nothing for a restart to get wrong.
	if err := syscall.Kill(pid, 0); err != nil {
		t.Fatalf("the runtime process %d did not survive its dispatcher (%v); "+
			"this test proves nothing unless it does", pid, err)
	}

	// Reopen exactly as a restarting server would.
	reopened, err := database.Open("file:" + path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer reopened.Close()
	store = work.NewStore(reopened.DB)

	// What the crash left behind: an intent, recorded before the process, naming
	// where it would be.
	var state, phase, locator string
	if err := reopened.QueryRow(`
		SELECT i.state, a.runtime_phase, COALESCE(a.runtime_locator,'')
		FROM work_attempts a JOIN work_items i ON i.id = a.work_id
		WHERE a.run_id = ?`, runID).Scan(&state, &phase, &locator); err != nil {
		t.Fatalf("read what the crash left: %v", err)
	}
	if state != string(work.StateStarting) {
		t.Errorf("work state = %q, want starting", state)
	}
	if phase != "starting" {
		t.Errorf("runtime phase = %q, want starting — the crash had to land before confirmation", phase)
	}
	if locator != "proc:"+runID {
		t.Fatalf("locator = %q, want proc:%s — with no identity written down, a restart has "+
			"nothing to look for and silence is all it can read", locator, runID)
	}

	// Sixty seconds of wall clock, without spending them.
	if _, err := reopened.Exec(
		`UPDATE work_attempts SET lease_expires_at = ?, heartbeat_at = ? WHERE run_id = ?`,
		"2000-01-01T00:00:00.000Z", "2000-01-01T00:00:00.000Z", runID); err != nil {
		t.Fatalf("expire the lease: %v", err)
	}

	// The restart. A brand-new dispatcher, in a brand-new process's position,
	// with no memory of the first — and a runtime that counts every process it
	// creates.
	rt := &pidfileRuntime{dir: runtimeDir}
	d := New(store, rt, nil, Config{
		Owner:               "restarted-dispatcher",
		Kinds:               []work.Kind{{Source: work.SourceWebhook}},
		PollInterval:        15 * time.Millisecond,
		HeartbeatInterval:   20 * time.Millisecond,
		CancelPollInterval:  10 * time.Millisecond,
		ConfirmPollInterval: 10 * time.Millisecond,
		RecoveryInterval:    50 * time.Millisecond,
		StopGrace:           time.Second,
	}, quiet())
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); _ = d.Run(runCtx) }()
	defer func() { cancel(); <-done }()

	it := waitForStateIn(t, store, rec.WorkID, 15*time.Second, work.StateNeedsReconciliation)

	// The two things that matter, and they are not the same thing.
	//
	// First: no second runtime. An abandoned attempt whose locator was written
	// down must never be retried, because a retry would put a second process
	// beside one that is demonstrably still running.
	if n := rt.starts.Load(); n != 0 {
		t.Errorf("the restart created %d more runtimes; the orphan at %s is still alive", n, locator)
	}
	// Second: the original is FINDABLE. "Nothing started a second one" is only
	// half the guarantee — an operator has to be able to reach the first.
	alive, err := rt.Alive(ctx, locator)
	if err != nil {
		t.Fatalf("the recorded locator could not be resolved after a restart: %v", err)
	}
	if !alive {
		t.Errorf("the runtime at %s cannot be found after the restart, though its process is running; "+
			"a locator nobody can resolve is not evidence", locator)
	}
	if it.StateReason == "" {
		t.Error("parked with no reason; an operator has nothing to act on")
	}

	// And it is reachable for the obvious next step: stopping it.
	stopped, err := rt.Stop(ctx, locator)
	if err != nil || !stopped {
		t.Fatalf("Stop(%s) = (%v, %v), want (true, nil)", locator, stopped, err)
	}
	// SIGKILL is delivered asynchronously and the orphan is reaped by init, so
	// poll rather than assume the kernel has caught up with us.
	gone := false
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			gone = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !gone {
		t.Errorf("the orphaned process %d is still alive 5s after a confirmed stop", pid)
	}
}

// runCrashingChild re-execs this binary as the dispatcher that dies, and
// returns the run id and pid of the runtime it left behind.
func runCrashingChild(t *testing.T, dbPath, runtimeDir string) (string, int) {
	t.Helper()
	cmd := exec.Command(os.Args[0])
	cmd.Env = append(os.Environ(), fmt.Sprintf("%s=%s|%s", crashChildEnv, dbPath, runtimeDir))
	out, err := cmd.CombinedOutput()

	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		t.Fatalf("the crashing dispatcher did not die (err=%v), output:\n%s", err, out)
	}
	status, ok := exit.Sys().(syscall.WaitStatus)
	if !ok {
		t.Fatalf("cannot read the child's wait status on this platform; output:\n%s", out)
	}
	if !status.Signaled() || status.Signal() != syscall.SIGKILL {
		t.Fatalf("the child ended with %v (exit %d) rather than SIGKILL — it failed before reaching "+
			"the crash point, so nothing here is a result. Output:\n%s", status, exit.ExitCode(), out)
	}

	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, crashChildMark) {
			continue
		}
		fields := strings.Fields(strings.TrimPrefix(line, crashChildMark))
		if len(fields) != 2 {
			continue
		}
		pid, convErr := strconv.Atoi(fields[1])
		if convErr != nil {
			continue
		}
		return fields[0], pid
	}
	t.Fatalf("the child never announced a runtime; output:\n%s", out)
	return "", 0
}

func waitForStateIn(t *testing.T, store *work.Store, workID string, within time.Duration, want work.State) *work.Item {
	t.Helper()
	deadline := time.Now().Add(within)
	var last work.State
	for time.Now().Before(deadline) {
		it, err := store.Get(context.Background(), workID)
		if err == nil {
			last = it.State
			if it.State == want {
				return it
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("work %s is %q after %s, want %q", workID, last, within, want)
	return nil
}
