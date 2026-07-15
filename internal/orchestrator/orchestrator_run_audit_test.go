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
	"io"
	"strings"
	"sync"
	"testing"

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
	if got.entityID != "chat1" {
		t.Errorf("entityID = %q, want chat1 (run id)", got.entityID)
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

	req := covRunReq()
	if err := o.RunAgent(context.Background(), req, nil); err != nil {
		t.Fatalf("still-running exec must return nil: %v", err)
	}

	if calls := rec.snapshot(); len(calls) != 0 {
		t.Errorf("audit calls = %d, want 0 for a still-running detached exec: %+v", len(calls), calls)
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
