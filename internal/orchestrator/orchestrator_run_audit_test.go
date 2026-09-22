package orchestrator

// Tests for #1207 (coverage-gap half): RunAgent must record one
// agent.run.* audit-log entry per terminal run outcome (completed /
// error / cancelled), via the AuditEmitter wired in by SetAuditLog.
// Previously agent-run activity was entirely invisible to
// `crewship audit` — these pin the fix at the orchestrator boundary,
// independent of the server-side api.WriteAuditLog wiring (covered
// separately in internal/server).

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/provider"
)

// recordingAudit is a fake AuditEmitter that captures every RecordAudit
// call so tests can assert on action name, entity, actor, and scope.
type recordingAudit struct {
	mu    sync.Mutex
	calls []auditCall
}

type auditCall struct {
	action      string
	entityType  string
	entityID    string
	userID      string
	workspaceID string
	metadata    map[string]any
}

func (a *recordingAudit) RecordAudit(_ context.Context, action, entityType, entityID, userID, workspaceID string, metadata map[string]any) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls = append(a.calls, auditCall{
		action:      action,
		entityType:  entityType,
		entityID:    entityID,
		userID:      userID,
		workspaceID: workspaceID,
		metadata:    metadata,
	})
}

func (a *recordingAudit) snapshot() []auditCall {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]auditCall, len(a.calls))
	copy(out, a.calls)
	return out
}

func TestRunAgent_RecordsAuditOnCompletion(t *testing.T) {
	t.Parallel()
	rec := &recordingAudit{}
	o := New(covNewRunContainer(covRunOpts{stream: "{}\n"}), newMemState(), covQuietLogger())
	o.SetAuditLog(rec)

	req := covRunReq()
	req.OpenedByUserID = "user-1"
	if err := o.RunAgent(context.Background(), req, nil); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}

	calls := rec.snapshot()
	if len(calls) != 1 {
		t.Fatalf("audit calls = %d, want exactly 1 (once per completed run): %+v", len(calls), calls)
	}
	got := calls[0]
	if got.action != "agent.run.completed" {
		t.Errorf("action = %q, want agent.run.completed", got.action)
	}
	// The audited entity is the RUN. It used to read "chat1" only because
	// RunState.ID was the chat id; since E0 it is the run id the dispatch
	// path minted, so one chat's successive runs no longer audit as one
	// entity.
	if got.entityID != covRunID {
		t.Errorf("entityID = %q, want %q (the run id, not the chat id)", got.entityID, covRunID)
	}
	if got.userID != "user-1" {
		t.Errorf("userID = %q, want user-1 (req.OpenedByUserID)", got.userID)
	}
	if got.workspaceID != "ws1" {
		t.Errorf("workspaceID = %q, want ws1", got.workspaceID)
	}
	if got.metadata["agent_slug"] != "cov-agent" {
		t.Errorf("metadata[agent_slug] = %v, want cov-agent", got.metadata["agent_slug"])
	}
}

func TestRunAgent_RecordsAuditOnFailure(t *testing.T) {
	t.Parallel()
	rec := &recordingAudit{}
	o := New(covNewRunContainer(covRunOpts{stream: "{}\n", agentExit: 5}), newMemState(), covQuietLogger())
	o.SetAuditLog(rec)

	req := covRunReq()
	if err := o.RunAgent(context.Background(), req, nil); err == nil {
		t.Fatal("expected error from non-zero exit code")
	}

	calls := rec.snapshot()
	if len(calls) != 1 {
		t.Fatalf("audit calls = %d, want exactly 1: %+v", len(calls), calls)
	}
	if calls[0].action != "agent.run.failed" {
		t.Errorf("action = %q, want agent.run.failed", calls[0].action)
	}
}

func TestRunAgent_RecordsAuditOnCancellation(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mc := &mockContainer{
		// Cancel synchronously inside the first Exec so ctx.Err() is
		// already non-nil by the time RunAgent reaches its cancellation
		// check — see TestRunAgentCancelledContext for the same pattern
		// and why it's race-free across schedulers.
		execFn: func(_ provider.ExecConfig) (*provider.ExecResult, error) {
			cancel()
			return &provider.ExecResult{
				ExecID: "noop",
				Reader: io.NopCloser(strings.NewReader("")),
			}, nil
		},
		inspectResult: struct {
			running  bool
			exitCode int
		}{false, 0},
	}

	rec := &recordingAudit{}
	o := New(mc, newMemState(), covQuietLogger())
	o.SetAuditLog(rec)

	req := covRunReq()
	if err := o.RunAgent(ctx, req, nil); err == nil {
		t.Fatal("expected error from cancelled context")
	}

	calls := rec.snapshot()
	if len(calls) != 1 {
		t.Fatalf("audit calls = %d, want exactly 1: %+v", len(calls), calls)
	}
	if calls[0].action != "agent.run.cancelled" {
		t.Errorf("action = %q, want agent.run.cancelled", calls[0].action)
	}
}

// TestRunAgent_NoAuditOnDetachedStillRunning pins the granularity
// guarantee from the issue: "running" (the CLI exec outlives RunAgent,
// e.g. a detached tmux session) is not a terminal outcome, so it must
// NOT produce an agent.run.* row — only the eventual completed/error/
// cancelled state does. This also guards against the 39-runs-in-24h
// volume concern: a run that stays open across the window must not
// double-log once here and again wherever it eventually terminates.
func TestRunAgent_NoAuditOnDetachedStillRunning(t *testing.T) {
	t.Parallel()
	rec := &recordingAudit{}
	o := New(covNewRunContainer(covRunOpts{stream: "{}\n", agentRunning: true}), newMemState(), covQuietLogger())
	o.SetAuditLog(rec)
	// Shrink the monitoring budget so the always-alive fake reaches the
	// detached outcome in milliseconds instead of the 30-minute default.
	o.SetDetachedExecMonitoring(50*time.Millisecond, 5*time.Millisecond)

	req := covRunReq()
	err := o.RunAgent(context.Background(), req, nil)
	if err == nil {
		t.Fatal("still-running exec must not read as success: RunAgent returned nil for a live process (#2626)")
	}
	if !errors.Is(err, ErrDetachedStillRunning) {
		t.Fatalf("still-running exec must return ErrDetachedStillRunning, got: %v", err)
	}

	if calls := rec.snapshot(); len(calls) != 0 {
		t.Errorf("audit calls = %d, want 0 for a still-running detached exec: %+v", len(calls), calls)
	}
}

// TestRunAgent_DetachedExecThatEndsResolvesItsRealOutcome pins the monitoring
// half of #2626: a stream that ends while the exec lives is followed until
// the process terminates, and the run then resolves with the exit code the
// synchronous path would have used — no caller invention, no second exec.
func TestRunAgent_DetachedExecThatEndsResolvesItsRealOutcome(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		exit     int
		wantErr  bool
		wantStat string
	}{
		{"ends clean reads as completed", 0, false, "completed"},
		{"ends non-zero reads as error", 1, true, "error"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			st := newMemState()
			o := New(covNewRunContainer(covRunOpts{
				stream:                 "{}\n",
				agentExit:              tc.exit,
				agentRunningFlipsAfter: 2, // alive for two inspects, then terminal
			}), st, covQuietLogger())
			o.SetDetachedExecMonitoring(5*time.Second, time.Millisecond)

			req := covRunReq()
			err := o.RunAgent(context.Background(), req, nil)
			if tc.wantErr && err == nil {
				t.Fatal("a detached exec that exited non-zero must surface an error")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("a detached exec that exited 0 must complete: %v", err)
			}
			if got := covRunStatus(t, st, covRunID); got != tc.wantStat {
				t.Errorf("run status = %q, want %q", got, tc.wantStat)
			}
		})
	}
}

// TestRunAgent_NoAuditEmitterConfigured_DoesNotPanic guards the noop
// default: a server built without SetAuditLog (or tests that never call
// it) must run exactly as before — no nil-pointer panic.
func TestRunAgent_NoAuditEmitterConfigured_DoesNotPanic(t *testing.T) {
	t.Parallel()
	o := New(covNewRunContainer(covRunOpts{stream: "{}\n"}), newMemState(), covQuietLogger())
	if err := o.RunAgent(context.Background(), covRunReq(), nil); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
}

// TestRunAgent_UnconfirmedStopHoldsAgentAndCapacity pins the ownership rule
// from #2626's review: a stop ATTEMPT is not a stop CONFIRMATION. When the
// bounded stop inside the detached branch cannot confirm the process is gone,
// the run slot transfers to a detached hold and the agent's admission is
// refused — a second run for the same agent must not start beside a process
// nobody has confirmed dead. When the hold's watcher later confirms the
// runtime gone, the hold lifts and a new run is admitted again.
func TestRunAgent_UnconfirmedStopHoldsAgentAndCapacity(t *testing.T) {
	t.Parallel()
	st := newMemState()
	c := covNewRunContainer(covRunOpts{
		stream:       "{}\n",
		agentRunning: true,      // the exec never terminates on its own
		tmuxAliveOut: "PRESENT", // RunIsAliveAt: alive
		tmuxStopOut:  "PRESENT", // StopRunAt: asked, still there
	})
	o := New(c, st, covQuietLogger())
	o.SetDetachedExecMonitoring(50*time.Millisecond, 5*time.Millisecond)

	req := covRunReq()
	err := o.RunAgent(context.Background(), req, nil)
	if !errors.Is(err, ErrDetachedStillRunning) {
		t.Fatalf("first run: want ErrDetachedStillRunning, got %v", err)
	}

	// The hold exists and refuses a second run for the agent.
	if _, held := o.detachedHoldRunID(req.AgentID); !held {
		t.Fatal("no detached hold registered for an unconfirmed stop")
	}
	second := covRunReq()
	second.RunID = "run-cov2"
	err2 := o.RunAgent(context.Background(), second, nil)
	if !errors.Is(err2, ErrAgentDetachedBusy) {
		t.Fatalf("second run must be refused with ErrAgentDetachedBusy while the detached exec is unconfirmed, got %v", err2)
	}

	// A later stop answering "gone" must also settle the hold on its own
	// (the watcher's periodic re-stop path), not only the Alive probe.
	c.setTmuxStop("ABSENT")

	// The runtime finally ends: the watcher must lift the hold.
	c.setTmuxAlive("ABSENT")
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, held := o.detachedHoldRunID(req.AgentID); !held {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, held := o.detachedHoldRunID(req.AgentID); held {
		t.Fatal("the detached hold did not lift after the runtime was confirmed gone")
	}

	// And admission works again (this run detaches too — the fake still
	// reports a live exec — which is fine: the assertion is that it is NOT
	// refused as busy).
	third := covRunReq()
	third.RunID = "run-cov3"
	err3 := o.RunAgent(context.Background(), third, nil)
	if errors.Is(err3, ErrAgentDetachedBusy) {
		t.Fatal("admission still refused after the hold lifted")
	}
}

// TestRunAgent_DetachedAliveEmitsNoTerminalEvents pins the journal ordering
// rule from #2626's review: while the detached runtime is alive, no
// exec.command END entry may be emitted and the agent must not be marked
// online — a running agent is not "available". The terminal entry appears
// only after the end is confirmed (here: by the hold's watcher).
func TestRunAgent_DetachedAliveEmitsNoTerminalEvents(t *testing.T) {
	t.Parallel()
	st := newMemState()
	j := &covJournal{}
	c := covNewRunContainer(covRunOpts{
		stream:       "{}\n",
		agentRunning: true,
		tmuxAliveOut: "PRESENT",
		tmuxStopOut:  "PRESENT",
	})
	o := New(c, st, covQuietLogger())
	o.SetJournal(j)
	o.SetDetachedExecMonitoring(50*time.Millisecond, 5*time.Millisecond)

	req := covRunReq()
	if err := o.RunAgent(context.Background(), req, nil); !errors.Is(err, ErrDetachedStillRunning) {
		t.Fatalf("want ErrDetachedStillRunning, got %v", err)
	}
	if n := covExecEndEntries(j); n != 0 {
		t.Fatalf("exec.command end emitted %d time(s) while the detached runtime was still alive — terminal events must wait for a confirmed end", n)
	}

	c.setTmuxAlive("ABSENT")
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && covExecEndEntries(j) == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	if n := covExecEndEntries(j); n == 0 {
		t.Fatal("no exec.command end after the runtime was confirmed gone — the watcher must emit the terminal entry")
	}
}

// covExecEndEntries counts exec.command journal entries in their end phase.
func covExecEndEntries(j *covJournal) int {
	n := 0
	for _, e := range j.byType("exec.command") {
		if phase, _ := e.Payload["phase"].(string); phase == "end" {
			n++
		}
	}
	return n
}

func TestRunAgent_ConfirmedDetachedStopIsTerminal(t *testing.T) {
	c := covNewRunContainer(covRunOpts{stream: "{}\n", agentRunning: true, tmuxStopOut: "ABSENT"})
	o := New(c, newMemState(), covQuietLogger())
	o.SetDetachedExecMonitoring(time.Millisecond, time.Millisecond)
	req := covRunReq()
	err := o.RunAgent(t.Context(), req, nil)
	if err == nil || errors.Is(err, ErrDetachedStillRunning) {
		t.Fatalf("confirmed stop must return a terminal failure, got %v", err)
	}
	if _, held := o.detachedHoldRunID(req.AgentID); held {
		t.Fatal("confirmed stop retained a detached hold")
	}
}
