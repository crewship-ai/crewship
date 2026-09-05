package providertest

// Coverage for the FakeProvider primitives B7 (PRD-ISSUES-AND-ROUTINES-2026,
// #2356) added and B7b (#2365) fixed:
//
//   - HoldCmd / HoldSessionCmd — a "process" that ignores context
//     cancellation and only stops when signalled.
//   - ExecPID reports a pid in a HOST-namespace range (hostPID) that is
//     NEVER resolvable by an in-container kill — modelling the real bug
//     #2365 found: dockerd's ExecInspect pid is a host pid, and a `kill`
//     run as a new exec INSIDE the container cannot see it.
//   - The corrected mechanism: kill-session by tmux session NAME
//     (TmuxKillSessionCmd), and process-group kill by a containerPID
//     obtained from INSIDE the container (TmuxListPanePIDsCmd /
//     KillProcessGroupCmd) — both resolve, and both touch only the one
//     exec they name, never a sibling's.
//
// internal/api's hard-stop tests drive these through the real production
// code path; these pin the primitives themselves in isolation.

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
)

func TestFakeProvider_ExecPID_ReportsPidWhileRunningZeroWhenDone(t *testing.T) {
	f := NewFakeProvider()
	res, err := f.Exec(context.Background(), provider.ExecConfig{ContainerID: "c1", Cmd: HoldCmd})
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	pid, err := f.ExecPID(context.Background(), res.ExecID)
	if err != nil {
		t.Fatalf("ExecPID: %v", err)
	}
	if pid <= 0 {
		t.Fatalf("ExecPID = %d, want a positive pid for a running exec", pid)
	}

	// End it the legitimate way (never by ExecPID's own pid — see
	// TestFakeProvider_Kill_HostPIDFromExecPIDNeverResolvesInsideContainer)
	// so ExecPID's "0 once finished" half of its contract can still be
	// pinned here.
	f.Unblock()
	waitForExecDone(t, f, res.ExecID)

	if pid2, err := f.ExecPID(context.Background(), res.ExecID); err != nil || pid2 != 0 {
		t.Errorf("ExecPID after exit = (%d, %v), want (0, nil)", pid2, err)
	}
}

// TestFakeProvider_Kill_HostPIDFromExecPIDNeverResolvesInsideContainer pins
// #2365 itself at the fake-provider level: a pid resolved via ExecPID (the
// HOST pid namespace, matching dockerd's real ExecInspect behaviour) is NOT
// a valid target for a `kill` exec'd into the SAME container the process
// lives in. This is exactly why Tier 2 hard termination no longer signals
// by ExecPID's pid (internal/api/issue_handler_hard_stop.go) — it signals
// by tmux session name instead (see TestFakeProvider_TmuxKillSession_*
// below).
func TestFakeProvider_Kill_HostPIDFromExecPIDNeverResolvesInsideContainer(t *testing.T) {
	f := NewFakeProvider()
	target, err := f.Exec(context.Background(), provider.ExecConfig{ContainerID: "c1", Cmd: HoldCmd})
	if err != nil {
		t.Fatalf("Exec(target): %v", err)
	}
	t.Cleanup(f.Unblock)

	hostPID, err := f.ExecPID(context.Background(), target.ExecID)
	if err != nil || hostPID <= 0 {
		t.Fatalf("ExecPID = (%d, %v)", hostPID, err)
	}

	killRes, err := f.Exec(context.Background(), provider.ExecConfig{ContainerID: "c1", Cmd: provider.KillSignalCmd("TERM", hostPID)})
	if err != nil {
		t.Fatalf("Exec(kill): %v", err)
	}
	code := waitForExecDone(t, f, killRes.ExecID)
	if code == 0 {
		t.Errorf("kill -TERM <ExecPID's hostPID> exited 0 (found the process), want a \"no such process\" style failure — a host-namespace pid must not resolve inside the container")
	}

	// The target itself must still be running: the signal never reached it.
	running, _, err := f.ExecInspect(context.Background(), target.ExecID)
	if err != nil {
		t.Fatalf("ExecInspect(target): %v", err)
	}
	if !running {
		t.Errorf("target exec stopped despite the signal never resolving to it — the fake's namespace split is not modelled correctly")
	}
}

func TestFakeProvider_Kill_NeverSignalsAnotherExecsPid(t *testing.T) {
	f := NewFakeProvider()
	const session = "agent-target"
	target, err := f.Exec(context.Background(), provider.ExecConfig{ContainerID: "c1", Cmd: HoldSessionCmd(session)})
	if err != nil {
		t.Fatalf("Exec(target): %v", err)
	}
	sibling, err := f.Exec(context.Background(), provider.ExecConfig{ContainerID: "c1", Cmd: HoldCmd})
	if err != nil {
		t.Fatalf("Exec(sibling): %v", err)
	}

	// The container-local pid a real `tmux list-panes` would report from
	// INSIDE the container — the legitimate way Tier 2's escalation
	// resolves a pid, never via ExecPID.
	panesRes, err := f.Exec(context.Background(), provider.ExecConfig{ContainerID: "c1", Cmd: provider.TmuxListPanePIDsCmd(session)})
	if err != nil {
		t.Fatalf("Exec(list-panes): %v", err)
	}
	// Drain to EOF (io.Pipe's Write rendezvous means the command's goroutine
	// blocks on this reader before it can finish and close entry.done) —
	// see the production runShortExec/tmuxPanePIDs pattern this mirrors.
	pid := readPanePID(t, panesRes)
	if pid <= 0 {
		t.Fatalf("list-panes reported pid %d, want > 0", pid)
	}
	waitForExecDone(t, f, panesRes.ExecID)

	killRes, err := f.Exec(context.Background(), provider.ExecConfig{ContainerID: "c1", Cmd: provider.KillProcessGroupCmd("TERM", []int{pid})})
	if err != nil {
		t.Fatalf("Exec(kill): %v", err)
	}
	waitForExecDone(t, f, killRes.ExecID)
	waitForExecDone(t, f, target.ExecID)

	running, _, err := f.ExecInspect(context.Background(), sibling.ExecID)
	if err != nil {
		t.Fatalf("ExecInspect(sibling): %v", err)
	}
	if !running {
		t.Errorf("sibling exec was affected by killing the target's pane pid — it must run until its OWN pid/session is signalled")
	}
	f.Unblock() // release the sibling so the test doesn't leak a goroutine
}

func TestFakeProvider_Kill_UnknownPidReportsFailureExit(t *testing.T) {
	f := NewFakeProvider()
	res, err := f.Exec(context.Background(), provider.ExecConfig{ContainerID: "c1", Cmd: provider.KillSignalCmd("TERM", 999999)})
	if err != nil {
		t.Fatalf("Exec(kill): %v", err)
	}
	code := waitForExecDone(t, f, res.ExecID)
	if code == 0 {
		t.Errorf("kill of an unknown pid exited 0, want a non-zero \"no such process\" style exit")
	}
}

// TestFakeProvider_TmuxKillSession_EndsOnlyItsOwnSession pins the corrected
// Tier 2 mechanism itself: a session name is a container-visible identity
// (unlike ExecPID's hostPID), and killing one session never reaches a
// differently-named session in the same (fake) container.
func TestFakeProvider_TmuxKillSession_EndsOnlyItsOwnSession(t *testing.T) {
	f := NewFakeProvider()
	target, err := f.Exec(context.Background(), provider.ExecConfig{ContainerID: "c1", Cmd: HoldSessionCmd("agent-a")})
	if err != nil {
		t.Fatalf("Exec(target): %v", err)
	}
	sibling, err := f.Exec(context.Background(), provider.ExecConfig{ContainerID: "c1", Cmd: HoldSessionCmd("agent-b")})
	if err != nil {
		t.Fatalf("Exec(sibling): %v", err)
	}

	killRes, err := f.Exec(context.Background(), provider.ExecConfig{ContainerID: "c1", Cmd: provider.TmuxKillSessionCmd("agent-a")})
	if err != nil {
		t.Fatalf("Exec(kill-session): %v", err)
	}
	waitForExecDone(t, f, killRes.ExecID)
	waitForExecDone(t, f, target.ExecID)

	running, _, err := f.ExecInspect(context.Background(), sibling.ExecID)
	if err != nil {
		t.Fatalf("ExecInspect(sibling): %v", err)
	}
	if !running {
		t.Errorf("sibling session was affected by kill-session on a different session name")
	}
	f.Unblock()
}

func TestFakeProvider_TmuxKillSession_UnknownSessionReportsFailureExit(t *testing.T) {
	f := NewFakeProvider()
	res, err := f.Exec(context.Background(), provider.ExecConfig{ContainerID: "c1", Cmd: provider.TmuxKillSessionCmd("agent-does-not-exist")})
	if err != nil {
		t.Fatalf("Exec(kill-session): %v", err)
	}
	code := waitForExecDone(t, f, res.ExecID)
	if code == 0 {
		t.Errorf("kill-session of an unknown session exited 0, want a non-zero \"session not found\" style exit")
	}
}

// readPanePID drains a TmuxListPanePIDsCmd exec's output to EOF and parses
// the single pid it reports.
func readPanePID(t *testing.T, res *provider.ExecResult) int {
	t.Helper()
	out, err := io.ReadAll(res.Reader)
	if err != nil {
		t.Fatalf("read pane pid output: %v", err)
	}
	_ = res.Reader.Close()
	var pid int
	for _, c := range out {
		if c < '0' || c > '9' {
			break
		}
		pid = pid*10 + int(c-'0')
	}
	return pid
}

// waitForExecDone polls ExecInspect until the exec has finished (the fake's
// commands all complete well under a second) and returns its exit code.
func waitForExecDone(t *testing.T, f *FakeProvider, execID string) int {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		running, code, err := f.ExecInspect(context.Background(), execID)
		if err != nil {
			t.Fatalf("ExecInspect(%s): %v", execID, err)
		}
		if !running {
			return code
		}
		if time.Now().After(deadline) {
			t.Fatalf("exec %s did not finish within the test deadline", execID)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
