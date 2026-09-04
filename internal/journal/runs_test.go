package journal

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// emitRun is a tiny helper that writes a run.started + a single
// terminal entry for trace_id=runID. status="" means "leave running"
// (only emit run.started).
func emitRun(t *testing.T, w *Writer, ws, agentID, runID, status, trigger string, when time.Time) {
	t.Helper()
	ctx := context.Background()
	_, err := w.Emit(ctx, Entry{
		WorkspaceID: ws,
		AgentID:     agentID,
		Type:        EntryRunStarted,
		ActorType:   ActorSidecar,
		Summary:     "started",
		Payload:     map[string]any{"trigger_type": trigger},
		TraceID:     runID,
		TS:          when,
	})
	if err != nil {
		t.Fatalf("emit started: %v", err)
	}
	if status == "" {
		return
	}
	var et EntryType
	switch status {
	case "COMPLETED":
		et = EntryRunCompleted
	case "FAILED":
		et = EntryRunFailed
	case "CANCELLED":
		et = EntryRunCancelled
	case "TIMEOUT":
		et = EntryRunTimeout
	default:
		t.Fatalf("unknown status %q", status)
	}
	_, err = w.Emit(ctx, Entry{
		WorkspaceID: ws,
		AgentID:     agentID,
		Type:        et,
		ActorType:   ActorSidecar,
		Summary:     status,
		Payload:     map[string]any{"exit_code": float64(0)},
		TraceID:     runID,
		TS:          when.Add(2 * time.Minute),
	})
	if err != nil {
		t.Fatalf("emit terminal: %v", err)
	}
}

// emitPipelineRun writes a pipeline.run.started + terminal entry pair the way
// internal/pipeline/journal.go's pipelineEmitContext actually stamps them:
// ActorID = runID and TraceID left empty — routine runs never set trace_id
// (#2284). status="" leaves the run RUNNING (only pipeline.run.started is
// written). status="CANCELLED" writes pipeline.run.failed with
// payload.status="CANCELLED", which is how emitRunFailed records a run
// cancelled mid-flight — there is no dedicated pipeline.run.cancelled entry
// type, see internal/pipeline/journal.go emitRunFailed.
func emitPipelineRun(t *testing.T, w *Writer, ws, agentID, runID, status string, when time.Time) {
	t.Helper()
	ctx := context.Background()
	_, err := w.Emit(ctx, Entry{
		WorkspaceID: ws,
		AgentID:     agentID,
		Type:        EntryPipelineRunStarted,
		ActorType:   ActorOrchestrator,
		ActorID:     runID,
		Summary:     "Pipeline test-routine started",
		Payload: map[string]any{
			"mode":              "run",
			"invoking_agent_id": agentID,
			"pipeline_id":       "pl_test",
			"pipeline_slug":     "test-routine",
			"run_id":            runID,
		},
		TS: when,
	})
	if err != nil {
		t.Fatalf("emit pipeline started: %v", err)
	}
	if status == "" {
		return
	}
	switch status {
	case "COMPLETED":
		_, err = w.Emit(ctx, Entry{
			WorkspaceID: ws,
			AgentID:     agentID,
			Type:        EntryPipelineRunCompleted,
			ActorType:   ActorOrchestrator,
			ActorID:     runID,
			Summary:     "Pipeline test-routine completed",
			Payload: map[string]any{
				"total_duration_ms": float64(1200),
				"total_cost_usd":    0.05,
				"pipeline_id":       "pl_test",
				"pipeline_slug":     "test-routine",
				"run_id":            runID,
			},
			TS: when.Add(2 * time.Minute),
		})
	case "FAILED", "CANCELLED":
		_, err = w.Emit(ctx, Entry{
			WorkspaceID: ws,
			AgentID:     agentID,
			Type:        EntryPipelineRunFailed,
			ActorType:   ActorOrchestrator,
			ActorID:     runID,
			Summary:     "Pipeline test-routine failed",
			Payload: map[string]any{
				"failed_at_step": "step_1",
				"error_message":  "boom",
				"status":         status,
				"pipeline_id":    "pl_test",
				"pipeline_slug":  "test-routine",
				"run_id":         runID,
			},
			TS: when.Add(2 * time.Minute),
		})
	default:
		t.Fatalf("unknown status %q", status)
	}
	if err != nil {
		t.Fatalf("emit pipeline terminal: %v", err)
	}
}

func TestListRuns_GroupsByTrace(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	w := NewWriter(db, quietLogger(), WriterOptions{FlushSize: 1})
	defer w.Close()

	now := time.Now().UTC()
	emitRun(t, w, "ws_test", "agent_a", "run_1", "COMPLETED", "USER", now.Add(-10*time.Minute))
	emitRun(t, w, "ws_test", "agent_b", "run_2", "FAILED", "WEBHOOK", now.Add(-5*time.Minute))
	emitRun(t, w, "ws_test", "agent_a", "run_3", "", "USER", now.Add(-1*time.Minute)) // still running
	_ = w.Flush(context.Background())
	time.Sleep(50 * time.Millisecond)

	runs, total, err := ListRuns(context.Background(), db, RunsQuery{WorkspaceID: "ws_test"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 3 {
		t.Errorf("total: got %d want 3", total)
	}
	if len(runs) != 3 {
		t.Fatalf("rows: got %d want 3", len(runs))
	}
	// ORDER BY started_at DESC → run_3 first
	if runs[0].ID != "run_3" || runs[0].Status != RunStatusRunning {
		t.Errorf("first row: %+v", runs[0])
	}
	if runs[1].ID != "run_2" || runs[1].Status != RunStatusFailed {
		t.Errorf("second row: %+v", runs[1])
	}
	if runs[2].ID != "run_1" || runs[2].Status != RunStatusCompleted {
		t.Errorf("third row: %+v", runs[2])
	}
}

func TestListRuns_StatusFilter(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	w := NewWriter(db, quietLogger(), WriterOptions{FlushSize: 1})
	defer w.Close()

	now := time.Now().UTC()
	emitRun(t, w, "ws_test", "agent_a", "run_c", "COMPLETED", "USER", now.Add(-3*time.Minute))
	emitRun(t, w, "ws_test", "agent_a", "run_f", "FAILED", "USER", now.Add(-2*time.Minute))
	emitRun(t, w, "ws_test", "agent_a", "run_r", "", "USER", now.Add(-1*time.Minute))
	_ = w.Flush(context.Background())
	time.Sleep(50 * time.Millisecond)

	for _, tc := range []struct {
		status RunStatus
		want   string
	}{
		{RunStatusRunning, "run_r"},
		{RunStatusCompleted, "run_c"},
		{RunStatusFailed, "run_f"},
	} {
		runs, _, err := ListRuns(context.Background(), db, RunsQuery{WorkspaceID: "ws_test", Status: tc.status})
		if err != nil {
			t.Fatalf("list %s: %v", tc.status, err)
		}
		if len(runs) != 1 || runs[0].ID != tc.want {
			t.Errorf("status=%s: got %d rows (first=%v) want exactly run id %s",
				tc.status, len(runs), runFirstID(runs), tc.want)
		}
	}
}

func TestListRuns_AgentFilter(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	w := NewWriter(db, quietLogger(), WriterOptions{FlushSize: 1})
	defer w.Close()

	now := time.Now().UTC()
	emitRun(t, w, "ws_test", "agent_a", "run_a1", "COMPLETED", "USER", now.Add(-3*time.Minute))
	emitRun(t, w, "ws_test", "agent_b", "run_b1", "COMPLETED", "USER", now.Add(-2*time.Minute))
	emitRun(t, w, "ws_test", "agent_a", "run_a2", "FAILED", "USER", now.Add(-1*time.Minute))
	_ = w.Flush(context.Background())
	time.Sleep(50 * time.Millisecond)

	runs, total, err := ListRuns(context.Background(), db, RunsQuery{WorkspaceID: "ws_test", AgentID: "agent_a"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 2 || len(runs) != 2 {
		t.Errorf("rows: got %d / total %d, want 2/2", len(runs), total)
	}
}

func TestListRuns_TriggerFilter(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	w := NewWriter(db, quietLogger(), WriterOptions{FlushSize: 1})
	defer w.Close()

	now := time.Now().UTC()
	emitRun(t, w, "ws_test", "agent_a", "run_u", "COMPLETED", "USER", now.Add(-3*time.Minute))
	emitRun(t, w, "ws_test", "agent_a", "run_w", "COMPLETED", "WEBHOOK", now.Add(-2*time.Minute))
	emitRun(t, w, "ws_test", "agent_a", "run_c", "COMPLETED", "CRON", now.Add(-1*time.Minute))
	_ = w.Flush(context.Background())
	time.Sleep(50 * time.Millisecond)

	runs, _, err := ListRuns(context.Background(), db, RunsQuery{WorkspaceID: "ws_test", TriggerType: "WEBHOOK"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(runs) != 1 || runs[0].ID != "run_w" {
		t.Errorf("trigger filter: %+v", runs)
	}
}

func TestRunStats(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	w := NewWriter(db, quietLogger(), WriterOptions{FlushSize: 1})
	defer w.Close()

	// All four entries land at the SAME instant (`now`) instead of the
	// previous `now - 30 min / 10 min / 5 min / 1 min` fan-out. The
	// negative offsets used to cross midnight when CI happened to run
	// during the first half-hour of UTC day, dropping r1 onto the
	// previous day and making this test fail with "today: got 3 want 4".
	// The assertions only care about per-day bucket counts, not the
	// ordering of the four entries, so anchoring them to the same
	// timestamp is the minimal fix that preserves the test's intent
	// while making it midnight-boundary safe.
	now := time.Now().UTC()
	emitRun(t, w, "ws_test", "agent_a", "r1", "COMPLETED", "USER", now)
	emitRun(t, w, "ws_test", "agent_a", "r2", "FAILED", "USER", now)
	emitRun(t, w, "ws_test", "agent_a", "r3", "TIMEOUT", "USER", now) // counts as failed too
	emitRun(t, w, "ws_test", "agent_a", "r4", "", "USER", now)        // running (no terminal entry emitted)
	_ = w.Flush(context.Background())
	time.Sleep(50 * time.Millisecond)

	stats, err := RunStats(context.Background(), db, "ws_test")
	if err != nil {
		t.Fatalf("stats: %v", err)
	}
	if stats.Running != 1 {
		t.Errorf("running: got %d want 1", stats.Running)
	}
	if stats.Today != 4 {
		t.Errorf("today: got %d want 4", stats.Today)
	}
	if stats.FailedToday != 2 {
		t.Errorf("failed today: got %d want 2 (FAILED + TIMEOUT)", stats.FailedToday)
	}
}

func TestListRuns_WorkspaceIsolation(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	if _, err := db.ExecContext(context.Background(), `INSERT INTO workspaces (id) VALUES ('ws_other')`); err != nil {
		t.Fatal(err)
	}
	w := NewWriter(db, quietLogger(), WriterOptions{FlushSize: 1})
	defer w.Close()

	now := time.Now().UTC()
	emitRun(t, w, "ws_test", "agent_a", "mine_1", "COMPLETED", "USER", now.Add(-2*time.Minute))
	emitRun(t, w, "ws_other", "agent_a", "theirs_1", "COMPLETED", "USER", now.Add(-1*time.Minute))
	_ = w.Flush(context.Background())
	time.Sleep(50 * time.Millisecond)

	runs, _, err := ListRuns(context.Background(), db, RunsQuery{WorkspaceID: "ws_test"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(runs) != 1 || runs[0].ID != "mine_1" {
		t.Errorf("workspace leak: %+v", runs)
	}
}

// TestGetRunByID pins the indexed single-run lookup (#1411/#1408): it
// must find a run by trace_id directly rather than the caller paging
// through ListRuns, and it must stay workspace-scoped like every other
// journal read.
func TestGetRunByID(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	if _, err := db.ExecContext(context.Background(), `INSERT INTO workspaces (id) VALUES ('ws_other')`); err != nil {
		t.Fatal(err)
	}
	w := NewWriter(db, quietLogger(), WriterOptions{FlushSize: 1})
	defer w.Close()

	now := time.Now().UTC()
	emitRun(t, w, "ws_test", "agent_a", "run_1", "COMPLETED", "USER", now.Add(-10*time.Minute))
	emitRun(t, w, "ws_other", "agent_a", "run_1_other_tenant", "COMPLETED", "USER", now.Add(-9*time.Minute))
	_ = w.Flush(context.Background())
	time.Sleep(50 * time.Millisecond)

	run, err := GetRunByID(context.Background(), db, "ws_test", "run_1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if run == nil {
		t.Fatal("expected a run, got nil")
	}
	if run.ID != "run_1" || run.Status != RunStatusCompleted {
		t.Errorf("got %+v", run)
	}

	// Unknown trace_id → nil, not an error.
	missing, err := GetRunByID(context.Background(), db, "ws_test", "run_does_not_exist")
	if err != nil {
		t.Fatalf("get missing: %v", err)
	}
	if missing != nil {
		t.Errorf("expected nil for missing run, got %+v", missing)
	}

	// Cross-tenant lookup must not leak — same trace_id shape, different
	// workspace than the caller's.
	leaked, err := GetRunByID(context.Background(), db, "ws_test", "run_1_other_tenant")
	if err != nil {
		t.Fatalf("get cross-tenant: %v", err)
	}
	if leaked != nil {
		t.Errorf("cross-tenant leak: %+v", leaked)
	}
}

func runFirstID(runs []RunAggregated) string {
	if len(runs) == 0 {
		return ""
	}
	return runs[0].ID
}

// TestListRuns_LimitClampAllocation pins the two properties that make an
// attacker-supplied RunsQuery.Limit harmless:
//
//  1. the effective page size is clamped to 100 no matter how large the
//     caller asks for (functional clamp — already true), and
//  2. the returned slice's capacity is bounded by the maxRunsPage constant
//     and never by the requested limit. Property (2) is what the
//     go/uncontrolled-allocation-size alert was about: a
//     `make([]RunAggregated, 0, limit)` sink pre-allocates from a value the
//     caller influences. Pre-sizing to the constant instead removes the
//     tainted value entirely.
func TestListRuns_LimitClampAllocation(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	w := NewWriter(db, quietLogger(), WriterOptions{FlushSize: 1})

	now := time.Now().UTC()
	// 120 runs in ws_big (> the 100 cap), 3 runs in ws_small.
	for i := 0; i < 120; i++ {
		emitRun(t, w, "ws_big", "agent_a", fmt.Sprintf("big_%03d", i), "COMPLETED", "USER",
			now.Add(-time.Duration(i+1)*time.Minute))
	}
	for i := 0; i < 3; i++ {
		emitRun(t, w, "ws_small", "agent_a", fmt.Sprintf("small_%d", i), "COMPLETED", "USER",
			now.Add(-time.Duration(i+1)*time.Minute))
	}
	if err := w.Flush(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}
	w.Close()

	// A limit far beyond anything a page could hold. Must neither panic nor
	// try to reserve 2^40 elements.
	huge := 1 << 40

	runs, total, err := ListRuns(context.Background(), db, RunsQuery{WorkspaceID: "ws_big", Limit: huge})
	if err != nil {
		t.Fatalf("list ws_big: %v", err)
	}
	if total != 120 {
		t.Errorf("total: got %d want 120", total)
	}
	if len(runs) != 100 {
		t.Errorf("effective limit not clamped to 100: got %d rows", len(runs))
	}

	// Small workspace, same hostile limit: capacity must track the result
	// size, not the requested limit.
	small, _, err := ListRuns(context.Background(), db, RunsQuery{WorkspaceID: "ws_small", Limit: huge})
	if err != nil {
		t.Fatalf("list ws_small: %v", err)
	}
	if len(small) != 3 {
		t.Fatalf("ws_small rows: got %d want 3", len(small))
	}
	// Capacity is deliberately NOT asserted to track the row count. The
	// result slice is pre-sized to the maxRunsPage constant, which is
	// bounded and caller-independent — that is what takes the tainted value
	// out of the make() and satisfies go/uncontrolled-allocation-size. What
	// must hold is that capacity can never be influenced by q.Limit.
	if cap(small) > maxRunsPage {
		t.Errorf("result capacity exceeds the page cap — allocation is not bounded by a constant: cap=%d", cap(small))
	}

	// Negative / zero limits fall back to the 50 default rather than
	// producing a negative make() argument.
	for _, lim := range []int{0, -1, -(1 << 40)} {
		got, _, err := ListRuns(context.Background(), db, RunsQuery{WorkspaceID: "ws_big", Limit: lim})
		if err != nil {
			t.Fatalf("list limit=%d: %v", lim, err)
		}
		if len(got) != 50 {
			t.Errorf("limit=%d: got %d rows want default 50", lim, len(got))
		}
	}
}

// TestListRuns_IncludesPipelineRuns is the red→green pin for #2284: a
// routine (pipeline) run writes pipeline.run.* entries keyed by ActorID, not
// trace_id (internal/pipeline/journal.go), so the pre-fix filter
// (`trace_id IS NOT NULL AND entry_type LIKE 'run.%'`) structurally could
// never surface it — GET /api/v1/runs, and therefore /journal?tab=runs, was
// blind to an entire class of execution. Seeds one ad-hoc run.* trace and one
// pipeline.run.* run side by side and asserts both come back with the
// correct kind and status.
func TestListRuns_IncludesPipelineRuns(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	w := NewWriter(db, quietLogger(), WriterOptions{FlushSize: 1})
	defer w.Close()

	now := time.Now().UTC()
	emitRun(t, w, "ws_test", "agent_a", "adhoc_1", "COMPLETED", "USER", now.Add(-5*time.Minute))
	emitPipelineRun(t, w, "ws_test", "agent_b", "routine_1", "COMPLETED", now.Add(-3*time.Minute))
	_ = w.Flush(context.Background())
	time.Sleep(50 * time.Millisecond)

	runs, total, err := ListRuns(context.Background(), db, RunsQuery{WorkspaceID: "ws_test"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 2 {
		t.Fatalf("total: got %d want 2 (ad-hoc + routine)", total)
	}
	if len(runs) != 2 {
		t.Fatalf("rows: got %d want 2: %+v", len(runs), runs)
	}

	byID := map[string]RunAggregated{}
	for _, r := range runs {
		byID[r.ID] = r
	}

	adhoc, ok := byID["adhoc_1"]
	if !ok {
		t.Fatalf("ad-hoc run missing from results: %+v", runs)
	}
	if adhoc.Kind != RunKindAgent {
		t.Errorf("ad-hoc run kind: got %q want %q", adhoc.Kind, RunKindAgent)
	}
	if adhoc.Status != RunStatusCompleted {
		t.Errorf("ad-hoc run status: got %q want COMPLETED", adhoc.Status)
	}

	routine, ok := byID["routine_1"]
	if !ok {
		t.Fatalf("pipeline/routine run missing from results — the read-side filter is still blind to pipeline.run.*: %+v", runs)
	}
	if routine.Kind != RunKindPipeline {
		t.Errorf("routine run kind: got %q want %q", routine.Kind, RunKindPipeline)
	}
	if routine.Status != RunStatusCompleted {
		t.Errorf("routine run status: got %q want COMPLETED", routine.Status)
	}
	if routine.AgentID != "agent_b" {
		t.Errorf("routine run agent_id: got %q want agent_b", routine.AgentID)
	}
}

// TestListRuns_PipelineRunCancelledStatus pins that a routine run cancelled
// mid-flight reports CANCELLED, not FAILED — internal/pipeline/journal.go's
// emitRunFailed reuses EntryPipelineRunFailed for both outcomes (there is no
// dedicated pipeline.run.cancelled entry type) and rides the real status on
// payload.status instead.
func TestListRuns_PipelineRunCancelledStatus(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	w := NewWriter(db, quietLogger(), WriterOptions{FlushSize: 1})
	defer w.Close()

	now := time.Now().UTC()
	emitPipelineRun(t, w, "ws_test", "agent_a", "routine_cancel", "CANCELLED", now)
	_ = w.Flush(context.Background())
	time.Sleep(50 * time.Millisecond)

	runs, _, err := ListRuns(context.Background(), db, RunsQuery{WorkspaceID: "ws_test"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(runs) != 1 || runs[0].ID != "routine_cancel" {
		t.Fatalf("got %+v", runs)
	}
	if runs[0].Status != RunStatusCancelled {
		t.Errorf("status: got %q want CANCELLED", runs[0].Status)
	}
}

// TestGetRunByID_PipelineRun mirrors TestGetRunByID for the pipeline/routine
// path: the single-run lookup must find a run keyed by ActorID (no
// trace_id), scoped to the caller's workspace like every other journal read.
func TestGetRunByID_PipelineRun(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	if _, err := db.ExecContext(context.Background(), `INSERT INTO workspaces (id) VALUES ('ws_other')`); err != nil {
		t.Fatal(err)
	}
	w := NewWriter(db, quietLogger(), WriterOptions{FlushSize: 1})
	defer w.Close()

	now := time.Now().UTC()
	emitPipelineRun(t, w, "ws_test", "agent_a", "routine_1", "COMPLETED", now.Add(-10*time.Minute))
	emitPipelineRun(t, w, "ws_other", "agent_a", "routine_1_other_tenant", "COMPLETED", now.Add(-9*time.Minute))
	_ = w.Flush(context.Background())
	time.Sleep(50 * time.Millisecond)

	run, err := GetRunByID(context.Background(), db, "ws_test", "routine_1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if run == nil {
		t.Fatal("expected a run, got nil — GetRunByID is still blind to pipeline.run.*")
	}
	if run.ID != "routine_1" || run.Status != RunStatusCompleted || run.Kind != RunKindPipeline {
		t.Errorf("got %+v", run)
	}

	// Cross-tenant lookup must not leak.
	leaked, err := GetRunByID(context.Background(), db, "ws_test", "routine_1_other_tenant")
	if err != nil {
		t.Fatalf("get cross-tenant: %v", err)
	}
	if leaked != nil {
		t.Errorf("cross-tenant leak: %+v", leaked)
	}
}

// TestListRuns_PipelineResumeDoesNotOverwriteStartedAt pins a defect the
// #2284 widening itself introduced (caught in self-review before merge): a
// pipeline/routine run that parks on a waitpoint and later resumes re-emits
// pipeline.run.started via pipelineEmitContext.emitRunResumed
// (internal/pipeline/journal.go) — same actor_id, a LATER ts, and
// payload.resumed=true. Naively widening started_at's projection to
// MAX(ts) over 'run.started' OR 'pipeline.run.started' (with no filter for
// the resume marker) would make MAX(ts) pick the resume time instead of the
// run's true original start — a routine approved three hours after it
// parked would report itself as having started seconds ago. Ad-hoc runs
// never hit this (internal/api/assignments_run.go has exactly one
// run.started call site per trace_id), so this is pipeline-only.
func TestListRuns_PipelineResumeDoesNotOverwriteStartedAt(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	w := NewWriter(db, quietLogger(), WriterOptions{FlushSize: 1})
	defer w.Close()

	ctx := context.Background()
	originalStart := time.Now().UTC().Add(-3 * time.Hour)
	resumedAt := time.Now().UTC().Add(-5 * time.Minute)
	completedAt := time.Now().UTC()

	// Original start.
	if _, err := w.Emit(ctx, Entry{
		WorkspaceID: "ws_test",
		AgentID:     "agent_a",
		Type:        EntryPipelineRunStarted,
		ActorType:   ActorOrchestrator,
		ActorID:     "routine_resumed",
		Summary:     "started",
		Payload: map[string]any{
			"mode":              "run",
			"invoking_agent_id": "agent_a",
			"pipeline_id":       "pl_test",
			"pipeline_slug":     "test-routine",
			"run_id":            "routine_resumed",
		},
		TS: originalStart,
	}); err != nil {
		t.Fatalf("emit started: %v", err)
	}
	// Resume — same run, same entry type, later ts, resumed marker. Mirrors
	// pipelineEmitContext.emitRunResumed's payload shape.
	if _, err := w.Emit(ctx, Entry{
		WorkspaceID: "ws_test",
		AgentID:     "agent_a",
		Type:        EntryPipelineRunStarted,
		ActorType:   ActorOrchestrator,
		ActorID:     "routine_resumed",
		Summary:     "resumed after approval",
		Payload: map[string]any{
			"mode":              "run",
			"invoking_agent_id": "agent_a",
			"pipeline_id":       "pl_test",
			"pipeline_slug":     "test-routine",
			"run_id":            "routine_resumed",
			"resumed":           true,
			"resume_reason":     "approval",
			"restored_steps":    2,
		},
		TS: resumedAt,
	}); err != nil {
		t.Fatalf("emit resumed: %v", err)
	}
	// Terminal.
	if _, err := w.Emit(ctx, Entry{
		WorkspaceID: "ws_test",
		AgentID:     "agent_a",
		Type:        EntryPipelineRunCompleted,
		ActorType:   ActorOrchestrator,
		ActorID:     "routine_resumed",
		Summary:     "completed",
		Payload: map[string]any{
			"total_duration_ms": float64(1000),
			"pipeline_id":       "pl_test",
			"pipeline_slug":     "test-routine",
			"run_id":            "routine_resumed",
		},
		TS: completedAt,
	}); err != nil {
		t.Fatalf("emit completed: %v", err)
	}
	_ = w.Flush(ctx)
	time.Sleep(50 * time.Millisecond)

	run, err := GetRunByID(ctx, db, "ws_test", "routine_resumed")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if run == nil {
		t.Fatal("expected a run, got nil")
	}
	gotStart := run.StartedAt.UTC()
	wantStart := originalStart.Truncate(time.Millisecond)
	if !gotStart.Equal(wantStart) {
		t.Errorf("started_at: got %v want %v (the original start, not the %v resume) — diff from resume: %v",
			gotStart, wantStart, resumedAt, gotStart.Sub(wantStart))
	}
	// started_payload must correlate with the SAME (original) row too — a
	// resumed=true payload leaking into the fields callers decode from
	// started_payload (mode, metadata, chat_id) would be the same class of
	// defect one level worse: MAX() over the payload TEXT column has no
	// reason to agree with MAX() over ts.
	if run.Metadata != nil {
		if _, ok := run.Metadata["resumed"]; ok {
			t.Errorf("started_payload leaked the resumed marker into decoded fields: %+v", run.Metadata)
		}
	}

	// ListRuns must agree with GetRunByID.
	runs, _, err := ListRuns(ctx, db, RunsQuery{WorkspaceID: "ws_test"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(runs) != 1 || !runs[0].StartedAt.UTC().Equal(wantStart) {
		t.Errorf("ListRuns started_at: got %+v want %v", runs, wantStart)
	}
}

// TestListRuns_MultiplePipelineRunsStayDistinct pins the sharpest defect
// CodeRabbit's review caught before merge: runAggregatesCTE's SELECT
// computes COALESCE(trace_id, actor_id) AS trace_id, but the GROUP BY
// clause used to say the bareword `trace_id`. SQLite resolves a GROUP BY
// bareword against a same-named FROM-clause column when one exists —
// journal_entries.trace_id, here — NOT the SELECT list's alias. trace_id is
// NULL for every pipeline/routine row by construction (#2284), so `GROUP BY
// trace_id` grouped ALL of a workspace's routine runs into a single NULL
// bucket instead of one row per run — the read side would have silently
// merged an arbitrary subset of a workspace's routine runs into one
// aggregate row (mixed timestamps, one payload winning over the others via
// whatever MAX() picked) and lost the rest entirely, the opposite of what
// #2284 set out to fix. Every other test in this file happens to seed at
// most one pipeline run per workspace, so none of them could have caught
// this — this test seeds three, all in the same workspace.
func TestListRuns_MultiplePipelineRunsStayDistinct(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	w := NewWriter(db, quietLogger(), WriterOptions{FlushSize: 1})
	defer w.Close()

	now := time.Now().UTC()
	emitPipelineRun(t, w, "ws_test", "agent_a", "routine_1", "COMPLETED", now.Add(-30*time.Minute))
	emitPipelineRun(t, w, "ws_test", "agent_b", "routine_2", "FAILED", now.Add(-20*time.Minute))
	emitPipelineRun(t, w, "ws_test", "agent_a", "routine_3", "", now.Add(-10*time.Minute)) // still running
	_ = w.Flush(context.Background())
	time.Sleep(50 * time.Millisecond)

	runs, total, err := ListRuns(context.Background(), db, RunsQuery{WorkspaceID: "ws_test"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if total != 3 {
		t.Fatalf("total: got %d want 3 (three distinct routine runs) — a merged/collapsed count means the GROUP BY key regressed", total)
	}
	if len(runs) != 3 {
		t.Fatalf("rows: got %d want 3: %+v", len(runs), runs)
	}
	byID := map[string]RunAggregated{}
	for _, r := range runs {
		byID[r.ID] = r
	}
	for _, tc := range []struct {
		id     string
		status RunStatus
	}{
		{"routine_1", RunStatusCompleted},
		{"routine_2", RunStatusFailed},
		{"routine_3", RunStatusRunning},
	} {
		r, ok := byID[tc.id]
		if !ok {
			t.Errorf("%s missing from results — routine runs collapsed into fewer rows than seeded: %+v", tc.id, runs)
			continue
		}
		if r.Kind != RunKindPipeline {
			t.Errorf("%s kind: got %q want %q", tc.id, r.Kind, RunKindPipeline)
		}
		if r.Status != tc.status {
			t.Errorf("%s status: got %q want %q", tc.id, r.Status, tc.status)
		}
	}

	// GetRunByID must resolve each one individually too, not return
	// whichever row a merged aggregate happened to keep.
	for _, id := range []string{"routine_1", "routine_2", "routine_3"} {
		got, err := GetRunByID(context.Background(), db, "ws_test", id)
		if err != nil {
			t.Fatalf("get %s: %v", id, err)
		}
		if got == nil || got.ID != id {
			t.Errorf("GetRunByID(%s): got %+v", id, got)
		}
	}
}
