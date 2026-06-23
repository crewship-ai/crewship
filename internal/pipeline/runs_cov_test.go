package pipeline

import (
	"context"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// runs.go — RunStore validation branches, list filters, interrupted
// promotion, idempotency lookup, scanRun timestamp fallbacks, and the
// nullable helpers.
// ---------------------------------------------------------------------------

func seedRunRow(t *testing.T, store *RunStore, id, pipelineID string, status RunStatus) *RunRecord {
	t.Helper()
	ver := 2
	ended := time.Now().UTC()
	r := &RunRecord{
		ID:              id,
		WorkspaceID:     "ws_runs",
		PipelineID:      pipelineID,
		PipelineSlug:    "demo",
		PipelineVersion: &ver,
		Status:          status,
		EndedAt:         &ended,
		IdempotencyKey:  "key-" + id,
	}
	if err := store.Insert(context.Background(), r); err != nil {
		t.Fatalf("seed %s: %v", id, err)
	}
	return r
}

func TestRunStore_Insert_Validation(t *testing.T) {
	store, db := openRunsTestDB(t)
	defer db.Close()
	ctx := context.Background()

	if err := store.Insert(ctx, &RunRecord{}); err == nil || !strings.Contains(err.Error(), "id required") {
		t.Errorf("missing id: %v", err)
	}
	if err := store.Insert(ctx, &RunRecord{ID: "r"}); err == nil || !strings.Contains(err.Error(), "workspace_id + pipeline_id required") {
		t.Errorf("missing workspace/pipeline: %v", err)
	}
	// FK violation surfaces as wrapped insert error.
	bad := &RunRecord{ID: "r-fk", WorkspaceID: "ws_runs", PipelineID: "pln_a"}
	if err := store.Insert(ctx, bad); err != nil {
		t.Fatalf("valid insert: %v", err)
	}
	if err := store.Insert(ctx, bad); err == nil || !strings.Contains(err.Error(), "pipeline_runs: insert") {
		t.Errorf("duplicate insert: %v", err)
	}
}

func TestRunStore_MarkTerminal_RejectsNonTerminal(t *testing.T) {
	store, db := openRunsTestDB(t)
	defer db.Close()
	err := store.MarkTerminal(context.Background(), MarkTerminalInput{RunID: "x", Status: RunStatusRunning})
	if err == nil || !strings.Contains(err.Error(), "not a terminal status") {
		t.Errorf("non-terminal status: %v", err)
	}
}

func TestRunStore_ListByPipeline_StatusFilterAndLimit(t *testing.T) {
	store, db := openRunsTestDB(t)
	defer db.Close()
	ctx := context.Background()

	seedRunRow(t, store, "r1", "pln_a", RunStatusCompleted)
	seedRunRow(t, store, "r2", "pln_a", RunStatusFailed)
	seedRunRow(t, store, "r3", "pln_b", RunStatusCompleted)

	// Status filter.
	got, err := store.ListByPipeline(ctx, "pln_a", RunStatusFailed, 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 || got[0].ID != "r2" {
		t.Errorf("status filter: %v", got)
	}

	// Out-of-range limit normalises and no filter returns both pln_a rows.
	got, err = store.ListByPipeline(ctx, "pln_a", "", 0)
	if err != nil {
		t.Fatalf("list all: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("expected 2 rows for pln_a, got %d", len(got))
	}
	// PipelineVersion round-trips through the nullable column.
	if got[0].PipelineVersion == nil || *got[0].PipelineVersion != 2 {
		t.Errorf("pipeline_version lost: %v", got[0].PipelineVersion)
	}
	// EndedAt round-trips.
	if got[0].EndedAt == nil {
		t.Error("ended_at lost in round-trip")
	}
}

func TestRunStore_ListActiveAndInFlight(t *testing.T) {
	store, db := openRunsTestDB(t)
	defer db.Close()
	ctx := context.Background()

	seedRunRow(t, store, "done", "pln_a", RunStatusCompleted)
	seedRunRow(t, store, "live", "pln_a", RunStatusRunning)
	seedRunRow(t, store, "waiting", "pln_b", RunStatusQueued)

	active, err := store.ListActive(ctx, "ws_runs")
	if err != nil {
		t.Fatalf("active: %v", err)
	}
	if len(active) != 2 {
		t.Errorf("active rows: %d", len(active))
	}

	inflight, err := store.ListInFlight(ctx)
	if err != nil {
		t.Fatalf("in-flight: %v", err)
	}
	if len(inflight) != 2 {
		t.Errorf("in-flight rows: %d", len(inflight))
	}
}

func TestRunStore_MarkInterrupted_DefaultReasonAndStatusGuard(t *testing.T) {
	store, db := openRunsTestDB(t)
	defer db.Close()
	ctx := context.Background()

	seedRunRow(t, store, "live", "pln_a", RunStatusRunning)
	seedRunRow(t, store, "done", "pln_a", RunStatusCompleted)

	// Empty reason falls back to the default wording.
	if err := store.MarkInterrupted(ctx, "live", ""); err != nil {
		t.Fatalf("mark: %v", err)
	}
	got, err := store.Get(ctx, "live")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != RunStatusInterrupted {
		t.Errorf("status: %q", got.Status)
	}
	if got.ErrorMessage != "process restarted with run in flight" {
		t.Errorf("default reason: %q", got.ErrorMessage)
	}

	// Completed rows must never be clobbered back to interrupted.
	if err := store.MarkInterrupted(ctx, "done", "should not apply"); err != nil {
		t.Fatalf("mark done: %v", err)
	}
	got, _ = store.Get(ctx, "done")
	if got.Status != RunStatusCompleted {
		t.Errorf("completed run clobbered: %q", got.Status)
	}
}

func TestRunStore_RecoverInterruptedAtBoot_Cov(t *testing.T) {
	store, db := openRunsTestDB(t)
	defer db.Close()
	ctx := context.Background()

	seedRunRow(t, store, "live", "pln_a", RunStatusRunning)
	seedRunRow(t, store, "waiting", "pln_a", RunStatusQueued)
	seedRunRow(t, store, "done", "pln_b", RunStatusCompleted)

	n, err := store.RecoverInterruptedAtBoot(ctx)
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if n != 2 {
		t.Errorf("promoted %d rows, want 2", n)
	}
	got, _ := store.Get(ctx, "waiting")
	if got.Status != RunStatusInterrupted {
		t.Errorf("queued row not promoted: %q", got.Status)
	}
}

func TestRunStore_ResolveByIdempotencyKey_Cov(t *testing.T) {
	store, db := openRunsTestDB(t)
	defer db.Close()
	ctx := context.Background()

	seedRunRow(t, store, "r1", "pln_a", RunStatusCompleted)

	id, err := store.ResolveByIdempotencyKey(ctx, "ws_runs", "key-r1")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if id != "r1" {
		t.Errorf("resolved %q", id)
	}

	// Miss returns ("", nil) — no error.
	id, err = store.ResolveByIdempotencyKey(ctx, "ws_runs", "ghost")
	if err != nil || id != "" {
		t.Errorf("miss: (%q, %v)", id, err)
	}
}

func TestRunStore_ClosedDB_ErrorPaths(t *testing.T) {
	store, db := openRunsTestDB(t)
	ctx := context.Background()
	_ = db.Close()

	if _, err := store.ListByPipeline(ctx, "p", "", 10); err == nil || !strings.Contains(err.Error(), "pipeline_runs: list") {
		t.Errorf("ListByPipeline: %v", err)
	}
	if _, err := store.ListActive(ctx, "ws"); err == nil || !strings.Contains(err.Error(), "list active") {
		t.Errorf("ListActive: %v", err)
	}
	if _, err := store.ListInFlight(ctx); err == nil || !strings.Contains(err.Error(), "list in-flight") {
		t.Errorf("ListInFlight: %v", err)
	}
	if err := store.MarkInterrupted(ctx, "x", "r"); err == nil || !strings.Contains(err.Error(), "mark interrupted") {
		t.Errorf("MarkInterrupted: %v", err)
	}
	if _, err := store.RecoverInterruptedAtBoot(ctx); err == nil || !strings.Contains(err.Error(), "recover") {
		t.Errorf("Recover: %v", err)
	}
	if err := store.AppendStepOutput(ctx, "x", map[string]string{"a": "b"}, 0, 0); err == nil {
		t.Error("AppendStepOutput should error on closed DB")
	}
}

// TestScanRun_CorruptStartedAt pins the "warn + zero time" behaviour
// for an unparseable started_at — the row must still scan so boot
// recovery can see (and interrupt) it.
func TestScanRun_CorruptStartedAt(t *testing.T) {
	store, db := openRunsTestDB(t)
	defer db.Close()
	ctx := context.Background()

	if _, err := db.Exec(`
INSERT INTO pipeline_runs (id, workspace_id, pipeline_id, pipeline_slug, status, started_at)
VALUES ('r-corrupt', 'ws_runs', 'pln_a', 'demo', 'running', 'not-a-timestamp')`); err != nil {
		t.Fatalf("seed corrupt: %v", err)
	}
	got, err := store.Get(ctx, "r-corrupt")
	if err != nil {
		t.Fatalf("get corrupt: %v", err)
	}
	if !got.StartedAt.IsZero() {
		t.Errorf("corrupt started_at should map to zero time, got %v", got.StartedAt)
	}
}

func TestRunsNullableHelpers(t *testing.T) {
	t.Parallel()

	if nullableIntPtr(nil) != nil {
		t.Error("nil *int should map to nil")
	}
	v := 5
	if nullableIntPtr(&v) != 5 {
		t.Error("non-nil *int should deref")
	}

	if nullableTime(nil) != nil {
		t.Error("nil *time should map to nil")
	}
	var zero time.Time
	if nullableTime(&zero) != nil {
		t.Error("zero time should map to nil")
	}
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if got := nullableTime(&now); got != "2026-01-02T03:04:05Z" {
		t.Errorf("non-zero time: %v", got)
	}

	// parseRFC3339Opt branches.
	if ts, err := parseRFC3339Opt(""); err != nil || !ts.IsZero() {
		t.Errorf("empty: (%v, %v)", ts, err)
	}
	if _, err := parseRFC3339Opt("2026-01-02T03:04:05Z"); err != nil {
		t.Errorf("plain RFC3339: %v", err)
	}
	if _, err := parseRFC3339Opt("junk"); err == nil {
		t.Error("junk must error")
	}
}
