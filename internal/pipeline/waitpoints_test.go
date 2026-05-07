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

// TestWaitpointStore_LostWakeupRace targets the specific bug fixed in
// the routines stabilization commit: WaitFor used to drop the
// in-memory listener registration BEFORE the decided-state DB check,
// so a CompleteApproval that fired between the two had nowhere to
// deliver — its `default` branch silently consumed the signal and
// the goroutine parked forever.
//
// The fix pre-registers the listener channel and re-checks the DB
// state with the listener already in place. This test forces the
// race window (slow checkDecided via mock would be ideal but the
// pure-time approach is good enough given how tight the window is on
// real hardware).
func TestWaitpointStore_LostWakeupRace(t *testing.T) {
	store, cleanup := openWaitpointsTestDB(t)
	defer cleanup()
	ctx := context.Background()

	// Run the race scenario several times — the bug was timing-
	// dependent; with the fix, every iteration must converge.
	const iterations = 50
	for i := 0; i < iterations; i++ {
		token, err := store.CreateApproval(ctx, WaitpointApprovalRequest{
			WorkspaceID:   "ws_race",
			PipelineRunID: "run_race",
			StepID:        "race_step",
			TimeoutSec:    10,
		})
		if err != nil {
			t.Fatalf("[%d] create: %v", i, err)
		}
		// Drop the listener so WaitFor must take the "no listener"
		// branch. CompleteApproval fires concurrently; the race window
		// is between WaitFor's listener-pre-register and the DB
		// re-check. Without the fix this hangs.
		store.mu.Lock()
		delete(store.listeners, token)
		store.mu.Unlock()

		ready := make(chan struct{})
		done := make(chan bool, 1)
		go func() {
			close(ready)
			approved, _ := store.WaitFor(ctx, token)
			done <- approved
		}()
		<-ready
		// Tiny stagger so the WaitFor goroutine reaches the
		// post-register checkDecided path before CompleteApproval
		// flips the row. Without the fix, the value 0 here would
		// make the test hang reliably.
		time.Sleep(time.Microsecond)
		if err := store.CompleteApproval(ctx, token, true, "u1", ""); err != nil {
			t.Fatalf("[%d] complete: %v", i, err)
		}

		select {
		case got := <-done:
			if !got {
				t.Errorf("[%d] expected approved=true", i)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("[%d] LOST WAKEUP — WaitFor did not return within 2s", i)
		}
	}
}

// TestWaitpointStore_RecoverPending exercises the boot-time recovery
// scan added so stranded waitpoints from a previous process lifetime
// don't accumulate forever pending. Sweeps elapsed-timeout entries
// and reports how many remain pending so abnormal accumulation is
// observable in the boot log.
func TestWaitpointStore_RecoverPending(t *testing.T) {
	store, cleanup := openWaitpointsTestDB(t)
	defer cleanup()
	ctx := context.Background()

	// Insert a mix: one expired-pending, one fresh-pending,
	// one already-decided. The recovery scan should mark the
	// expired one timed_out and report 1 fresh-pending.
	expiredAt := time.Now().Add(-1 * time.Minute).UTC().Format(time.RFC3339Nano)
	freshAt := time.Now().Add(1 * time.Hour).UTC().Format(time.RFC3339Nano)
	if _, err := store.db.ExecContext(ctx, `
INSERT INTO pipeline_waitpoints
  (token, workspace_id, pipeline_run_id, step_id, kind, status, timeout_at)
VALUES
  ('expired_tok',  'ws',  'run_old',  'step_x', 'approval', 'pending',   ?),
  ('fresh_tok',    'ws',  'run_new',  'step_y', 'approval', 'pending',   ?),
  ('decided_tok',  'ws',  'run_done', 'step_z', 'approval', 'approved',  ?)`,
		expiredAt, freshAt, freshAt); err != nil {
		t.Fatalf("seed waitpoints: %v", err)
	}

	timedOut, pending, err := store.RecoverPending(ctx)
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if timedOut != 1 {
		t.Errorf("expected 1 timed_out, got %d", timedOut)
	}
	if pending != 1 {
		t.Errorf("expected 1 stranded-pending (fresh), got %d", pending)
	}

	// Verify the expired one is now actually status=timed_out
	var status string
	if err := store.db.QueryRowContext(ctx,
		`SELECT status FROM pipeline_waitpoints WHERE token = 'expired_tok'`).Scan(&status); err != nil {
		t.Fatalf("verify status: %v", err)
	}
	if status != "timed_out" {
		t.Errorf("expected timed_out, got %q", status)
	}
}
