package provider_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
)

// Callers used to read ExecInspect's second return as an exit code without
// looking at the first. That was survivable while a running exec reported 0 —
// wrong, but wrong in the direction of "success". Once it reports a fail-closed
// sentinel instead, the same callers turn a race into a hard failure: the tmux
// probe (orchestrator_exec_env.go) caches "tmux missing" for the container's
// whole life after one inspect/EOF race, and file writes report "exited -1".
//
// The exit code is only meaningful once the process has exited, so waiting for
// that is the fix — not choosing a friendlier lie for the in-flight case
// (#1779).
type scriptedInspector struct {
	provider.ContainerProvider
	runningFor int // number of inspects that report "still running"
	calls      int
	code       int
	err        error
}

func (s *scriptedInspector) ExecInspect(_ context.Context, _ string) (bool, int, error) {
	s.calls++
	if s.err != nil {
		return false, -1, s.err
	}
	if s.calls <= s.runningFor {
		return true, -1, nil
	}
	return false, s.code, nil
}

func TestWaitExecExit_WaitsForTheProcessToFinish(t *testing.T) {
	insp := &scriptedInspector{runningFor: 3, code: 0}

	code, err := provider.WaitExecExit(context.Background(), insp, "e1", time.Second)
	if err != nil {
		t.Fatalf("WaitExecExit: %v", err)
	}
	if code != 0 {
		t.Errorf("code = %d, want the real 0 rather than the in-flight sentinel", code)
	}
	if insp.calls < 4 {
		t.Errorf("expected polling until the exec finished, got %d inspects", insp.calls)
	}
}

func TestWaitExecExit_ReportsTheRealNonZeroCode(t *testing.T) {
	insp := &scriptedInspector{runningFor: 1, code: 7}

	code, err := provider.WaitExecExit(context.Background(), insp, "e1", time.Second)
	if err != nil {
		t.Fatalf("WaitExecExit: %v", err)
	}
	if code != 7 {
		t.Errorf("code = %d, want 7", code)
	}
}

// An exec that never finishes must not hang the caller — that is the failure
// this whole area keeps producing.
func TestWaitExecExit_IsBounded(t *testing.T) {
	insp := &scriptedInspector{runningFor: 1 << 30, code: 0}

	start := time.Now()
	_, err := provider.WaitExecExit(context.Background(), insp, "e1", 150*time.Millisecond)
	if err == nil {
		t.Fatal("an exec that never exits must surface as an error, not a code")
	}
	if time.Since(start) > 5*time.Second {
		t.Errorf("waited %s — the bound did not apply", time.Since(start))
	}
}

func TestWaitExecExit_PropagatesInspectErrors(t *testing.T) {
	want := errors.New("daemon gone")
	insp := &scriptedInspector{err: want}

	if _, err := provider.WaitExecExit(context.Background(), insp, "e1", time.Second); !errors.Is(err, want) {
		t.Errorf("err = %v, want it to wrap %v", err, want)
	}
}
