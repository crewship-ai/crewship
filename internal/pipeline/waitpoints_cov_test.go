package pipeline

import (
	"context"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// waitpoints.go — Close idempotence, CreateApproval defaults + prompt
// truncation, FindApprovalForStep / WaitpointStatus / checkDecided
// misses, closed-DB error paths, WaitFor's decided-state error path.
// ---------------------------------------------------------------------------

func TestWaitpointStore_Close_IsIdempotent(t *testing.T) {
	store, cleanup := openWaitpointsTestDB(t)
	defer cleanup()
	store.Close()
	store.Close() // second call hits the already-closed stopCh branch
}

func TestWaitpointStore_CreateApproval_DefaultTimeoutAndTruncation(t *testing.T) {
	store, cleanup := openWaitpointsTestDB(t)
	defer cleanup()
	ctx := context.Background()

	// TimeoutSec=0 → 24h default; long prompt → inbox title truncation
	// path executes (the row's own prompt stays full-length).
	longPrompt := strings.Repeat("approve this very important deploy ", 5)
	token, err := store.CreateApproval(ctx, WaitpointApprovalRequest{
		WorkspaceID:   "ws_test",
		PipelineRunID: "run_1",
		StepID:        "gate",
		Prompt:        longPrompt,
		TimeoutSec:    0,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if len(token) != 32 {
		t.Errorf("token length: %d", len(token))
	}

	var timeoutAt string
	if err := store.db.QueryRow(`SELECT timeout_at FROM pipeline_waitpoints WHERE token = ?`, token).Scan(&timeoutAt); err != nil {
		t.Fatalf("read timeout: %v", err)
	}
	ts, err := time.Parse(time.RFC3339Nano, timeoutAt)
	if err != nil {
		t.Fatalf("parse timeout: %v", err)
	}
	if d := time.Until(ts); d < 23*time.Hour || d > 25*time.Hour {
		t.Errorf("default timeout should be ~24h out, got %v", d)
	}

	status, err := store.WaitpointStatus(ctx, token)
	if err != nil || status != "pending" {
		t.Errorf("status: (%q, %v)", status, err)
	}
}

func TestWaitpointStore_FindApprovalForStep(t *testing.T) {
	store, cleanup := openWaitpointsTestDB(t)
	defer cleanup()
	ctx := context.Background()

	// Miss → ("", nil).
	tok, err := store.FindApprovalForStep(ctx, "run_x", "step_x")
	if err != nil || tok != "" {
		t.Errorf("miss: (%q, %v)", tok, err)
	}

	created, err := store.CreateApproval(ctx, WaitpointApprovalRequest{
		WorkspaceID: "ws_test", PipelineRunID: "run_x", StepID: "step_x", Prompt: "ok?",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	tok, err = store.FindApprovalForStep(ctx, "run_x", "step_x")
	if err != nil || tok != created {
		t.Errorf("hit: (%q, %v), want %q", tok, err, created)
	}
}

func TestWaitpointStore_StatusAndCheckDecided_Misses(t *testing.T) {
	store, cleanup := openWaitpointsTestDB(t)
	defer cleanup()
	ctx := context.Background()

	if _, err := store.WaitpointStatus(ctx, "ghost"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("status miss: %v", err)
	}
	if _, _, err := store.checkDecided(ctx, "ghost"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("checkDecided miss: %v", err)
	}

	// Unknown status value → explicit error (defensive branch).
	if _, err := store.db.Exec(`
INSERT INTO pipeline_waitpoints (token, workspace_id, pipeline_run_id, step_id, kind, status, timeout_at)
VALUES ('weird', 'ws_test', 'r', 's', 'approval', 'limbo', '2030-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, _, err := store.checkDecided(ctx, "weird"); err == nil || !strings.Contains(err.Error(), "unknown status") {
		t.Errorf("unknown status: %v", err)
	}

	// WaitFor surfaces the checkDecided error for unknown tokens
	// instead of parking forever.
	if _, err := store.WaitFor(ctx, "ghost-2"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("WaitFor unknown token: %v", err)
	}
}

func TestWaitpointStore_WaitFor_ResolvesDecidedStates(t *testing.T) {
	store, cleanup := openWaitpointsTestDB(t)
	defer cleanup()
	ctx := context.Background()

	mk := func(status string) string {
		tok := generateWaitpointToken()
		if _, err := store.db.Exec(`
INSERT INTO pipeline_waitpoints (token, workspace_id, pipeline_run_id, step_id, kind, status, timeout_at)
VALUES (?, 'ws_test', 'r', 's', 'approval', ?, '2030-01-01T00:00:00Z')`, tok, status); err != nil {
			t.Fatalf("seed %s: %v", status, err)
		}
		return tok
	}

	// approved → true, immediately (DB state path, no listener signal).
	if ok, err := store.WaitFor(ctx, mk("approved")); err != nil || !ok {
		t.Errorf("approved: (%v, %v)", ok, err)
	}
	// denied / timed_out / cancelled → false.
	for _, st := range []string{"denied", "timed_out", "cancelled"} {
		if ok, err := store.WaitFor(ctx, mk(st)); err != nil || ok {
			t.Errorf("%s: (%v, %v)", st, ok, err)
		}
	}
}

func TestWaitpointStore_ClosedDB_ErrorPaths(t *testing.T) {
	store, cleanup := openWaitpointsTestDB(t)
	cleanup() // closes DB (and sweeper) up front

	ctx := context.Background()
	if _, _, err := store.RecoverPending(ctx); err == nil || !strings.Contains(err.Error(), "recover sweep") {
		t.Errorf("RecoverPending: %v", err)
	}
	if _, err := store.CreateApproval(ctx, WaitpointApprovalRequest{WorkspaceID: "ws"}); err == nil || !strings.Contains(err.Error(), "waitpoints: insert") {
		t.Errorf("CreateApproval: %v", err)
	}
	if _, err := store.FindApprovalForStep(ctx, "r", "s"); err == nil || !strings.Contains(err.Error(), "find for step") {
		t.Errorf("FindApprovalForStep: %v", err)
	}
	if _, err := store.WaitpointStatus(ctx, "t"); err == nil {
		t.Error("WaitpointStatus should error on closed DB")
	}
	if err := store.CompleteApproval(ctx, "t", true, "", ""); err == nil || !strings.Contains(err.Error(), "waitpoints: update") {
		t.Errorf("CompleteApproval: %v", err)
	}
	if _, _, err := store.checkDecided(ctx, "t"); err == nil {
		t.Error("checkDecided should error on closed DB")
	}
	// sweepOnce must swallow the query error without panicking.
	store.sweepOnce()
}

func TestGenerateWaitpointToken_Shape(t *testing.T) {
	t.Parallel()
	t1 := generateWaitpointToken()
	t2 := generateWaitpointToken()
	if len(t1) != 32 || t1 == t2 {
		t.Errorf("tokens: %q %q", t1, t2)
	}
}
