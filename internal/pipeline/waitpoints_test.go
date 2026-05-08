package pipeline

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func openWaitpointsTestDB(t *testing.T) (*SQLWaitpointStore, func()) {
	t.Helper()
	db := openStoreTestDB(t) // existing helper opens :memory: with single conn
	if _, err := db.ExecContext(context.Background(), `
CREATE TABLE IF NOT EXISTS pipeline_waitpoints (
    token              TEXT PRIMARY KEY,
    workspace_id       TEXT NOT NULL,
    pipeline_run_id    TEXT NOT NULL,
    step_id            TEXT NOT NULL,
    kind               TEXT NOT NULL,
    prompt             TEXT,
    invoking_crew_id   TEXT,
    status             TEXT NOT NULL DEFAULT 'pending',
    decision_payload   TEXT,
    decided_by_user_id TEXT,
    timeout_at         TEXT NOT NULL,
    created_at         TEXT NOT NULL DEFAULT (datetime('now','subsec')),
    decided_at         TEXT
);`); err != nil {
		t.Fatalf("waitpoints schema: %v", err)
	}
	store := NewSQLWaitpointStore(db)
	return store, func() {
		store.Close()
		_ = db.Close()
	}
}

func TestWaitpointStore_ApproveDuringWait(t *testing.T) {
	store, cleanup := openWaitpointsTestDB(t)
	defer cleanup()

	ctx := context.Background()
	token, err := store.CreateApproval(ctx, WaitpointApprovalRequest{
		WorkspaceID:   "ws_test",
		PipelineRunID: "run_1",
		StepID:        "approve_summary",
		Prompt:        "Look good?",
		TimeoutSec:    30,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Approve in 50ms
	go func() {
		time.Sleep(50 * time.Millisecond)
		if err := store.CompleteApproval(ctx, token, true, "user_42", `{"comment":"yes"}`); err != nil {
			t.Errorf("complete: %v", err)
		}
	}()

	approved, err := store.WaitFor(ctx, token)
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	if !approved {
		t.Errorf("expected approved=true")
	}
}

func TestWaitpointStore_DenyDuringWait(t *testing.T) {
	store, cleanup := openWaitpointsTestDB(t)
	defer cleanup()
	ctx := context.Background()
	token, _ := store.CreateApproval(ctx, WaitpointApprovalRequest{
		WorkspaceID: "ws_test", PipelineRunID: "run_1", StepID: "step_1", TimeoutSec: 30,
	})
	go func() {
		time.Sleep(20 * time.Millisecond)
		_ = store.CompleteApproval(ctx, token, false, "user_42", "")
	}()
	approved, err := store.WaitFor(ctx, token)
	if err != nil {
		t.Fatalf("wait: %v", err)
	}
	if approved {
		t.Error("expected approved=false on deny")
	}
}

func TestWaitpointStore_AlreadyDecidedRejectsSecondCall(t *testing.T) {
	store, cleanup := openWaitpointsTestDB(t)
	defer cleanup()
	ctx := context.Background()
	token, _ := store.CreateApproval(ctx, WaitpointApprovalRequest{
		WorkspaceID: "ws_test", PipelineRunID: "run_1", StepID: "step_1", TimeoutSec: 30,
	})
	if err := store.CompleteApproval(ctx, token, true, "u1", ""); err != nil {
		t.Fatalf("first complete: %v", err)
	}
	err := store.CompleteApproval(ctx, token, false, "u2", "")
	if !errors.Is(err, ErrAlreadyDecided) {
		t.Errorf("expected ErrAlreadyDecided on double-complete, got %v", err)
	}
}

func TestWaitpointStore_ContextCancelExitsWait(t *testing.T) {
	store, cleanup := openWaitpointsTestDB(t)
	defer cleanup()
	ctx, cancel := context.WithCancel(context.Background())
	token, _ := store.CreateApproval(ctx, WaitpointApprovalRequest{
		WorkspaceID: "ws_test", PipelineRunID: "run_1", StepID: "step_1", TimeoutSec: 3600,
	})
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	_, err := store.WaitFor(ctx, token)
	if err == nil || !strings.Contains(err.Error(), "context canceled") {
		t.Errorf("expected context-canceled error, got %v", err)
	}
}

func TestWaitpointStore_RecoveryAfterRestart(t *testing.T) {
	// Simulate restart: create approval, decide it, then forget the
	// in-memory listener. WaitFor should recover the decision from DB.
	store, cleanup := openWaitpointsTestDB(t)
	defer cleanup()
	ctx := context.Background()
	token, _ := store.CreateApproval(ctx, WaitpointApprovalRequest{
		WorkspaceID: "ws_test", PipelineRunID: "run_1", StepID: "step_1", TimeoutSec: 30,
	})
	if err := store.CompleteApproval(ctx, token, true, "u1", ""); err != nil {
		t.Fatalf("complete: %v", err)
	}
	// Drop the in-memory listener (simulates a restart)
	store.mu.Lock()
	delete(store.listeners, token)
	store.mu.Unlock()

	// WaitFor should still return approved=true via DB lookup
	approved, err := store.WaitFor(ctx, token)
	if err != nil {
		t.Fatalf("wait after recovery: %v", err)
	}
	if !approved {
		t.Errorf("expected recovered approval=true")
	}
}
