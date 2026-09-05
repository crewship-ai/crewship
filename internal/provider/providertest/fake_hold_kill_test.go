package providertest

// Coverage for the FakeProvider primitives B7 (PRD-ISSUES-AND-ROUTINES-2026,
// #2356) added: HoldCmd (a "process" that ignores context cancellation) and
// ExecPID/the "kill" pseudo-command (resolving and signalling exactly one
// pid, never touching a sibling exec). internal/api's hard-stop tests drive
// these through the real production code path; these pin the primitives
// themselves in isolation.

import (
	"context"
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

	// Signal it via the exact argv hardTerminateExec issues in production.
	killRes, err := f.Exec(context.Background(), provider.ExecConfig{ContainerID: "c1", Cmd: provider.KillSignalCmd("TERM", pid)})
	if err != nil {
		t.Fatalf("Exec(kill): %v", err)
	}
	waitForExecDone(t, f, killRes.ExecID)
	waitForExecDone(t, f, res.ExecID)

	if pid2, err := f.ExecPID(context.Background(), res.ExecID); err != nil || pid2 != 0 {
		t.Errorf("ExecPID after exit = (%d, %v), want (0, nil)", pid2, err)
	}
}

func TestFakeProvider_Kill_NeverSignalsAnotherExecsPid(t *testing.T) {
	f := NewFakeProvider()
	target, err := f.Exec(context.Background(), provider.ExecConfig{ContainerID: "c1", Cmd: HoldCmd})
	if err != nil {
		t.Fatalf("Exec(target): %v", err)
	}
	sibling, err := f.Exec(context.Background(), provider.ExecConfig{ContainerID: "c1", Cmd: HoldCmd})
	if err != nil {
		t.Fatalf("Exec(sibling): %v", err)
	}
	targetPID, err := f.ExecPID(context.Background(), target.ExecID)
	if err != nil || targetPID <= 0 {
		t.Fatalf("target pid = (%d, %v)", targetPID, err)
	}

	killRes, err := f.Exec(context.Background(), provider.ExecConfig{ContainerID: "c1", Cmd: provider.KillSignalCmd("TERM", targetPID)})
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
		t.Errorf("sibling exec was affected by killing the target's pid — it must run until its OWN pid is signalled")
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
