package server

// Tests for the B16 collectors (metrics_domain_b16.go, #2396): the two
// §19.3 rows B12 left without a series. Same shape as
// metrics_domain_b12_test.go — real INSERTs through the migrated schema,
// explicit timestamps where a latency needs a known gap, then the rendered
// Prometheus text.

import (
	"context"
	"strings"
	"testing"
)

// b16Fixture is b12Fixture plus the pipelines row pipeline_runs' FK needs.
func b16Fixture(t *testing.T) *Server {
	t.Helper()
	s := b12Fixture(t)
	mustExec(t, s.db, `INSERT INTO pipelines (id, workspace_id, slug, name, definition_json, definition_hash)
		VALUES ('pipe_b16','ws_b12','pipe-b16','P','{}','h')`)
	return s
}

// seedPipelineRun inserts one pipeline_runs row. dueAt "" leaves due_at
// NULL — the shape every non-scheduled trigger, and every scheduled run
// from before v20260905172327, has. outcome nil leaves outcome NULL.
func seedPipelineRun(t *testing.T, s *Server, id, triggeredVia, startedAt, dueAt string, outcome *string) {
	t.Helper()
	var dueVal, outcomeVal any
	if dueAt != "" {
		dueVal = dueAt
	}
	if outcome != nil {
		outcomeVal = *outcome
	}
	mustExec(t, s.db, `INSERT INTO pipeline_runs
		(id, workspace_id, pipeline_id, pipeline_slug, status, triggered_via, triggered_by_id, started_at, due_at, outcome)
		VALUES (?, 'ws_b12', 'pipe_b16', 'pipe-b16', 'completed', ?, 'sched_b16', ?, ?, ?)`,
		id, triggeredVia, startedAt, dueVal, outcomeVal)
}

func seedInboxItem(t *testing.T, s *Server, id, kind, sourceID string) {
	t.Helper()
	mustExec(t, s.db, `INSERT INTO inbox_items (id, workspace_id, kind, source_id, title) VALUES (?, 'ws_b12', ?, ?, 'x')`,
		id, kind, sourceID)
}

// ── scheduled fire punctuality ─────────────────────────────────────────────

func TestCollectScheduleFirePunctualityMetrics_RealWritePath(t *testing.T) {
	s := b16Fixture(t)
	seedPipelineRun(t, s, "run_1", "schedule", "2026-01-01T08:00:12Z", "2026-01-01T08:00:00Z", nil) // 12s late
	seedPipelineRun(t, s, "run_2", "schedule", "2026-01-01T09:00:45Z", "2026-01-01T09:00:00Z", nil) // 45s late
	// A manual run has no due time and must not be a sample even if it
	// somehow carried one; a scheduled run from before the column has no
	// due_at and is not a sample either — never a fabricated 0.
	seedPipelineRun(t, s, "run_manual", "manual", "2026-01-01T10:00:00Z", "2026-01-01T09:00:00Z", nil)
	seedPipelineRun(t, s, "run_legacy", "schedule", "2026-01-01T11:00:00Z", "", nil)

	var b strings.Builder
	s.collectScheduleFirePunctualityMetrics(context.Background(), &b, "h")
	out := b.String()

	if !strings.Contains(out, `crewshipd_schedule_fire_punctuality_seconds{hostname="h",quantile="0.5"} 12`) {
		t.Errorf("missing p50=12; output:\n%s", out)
	}
	if !strings.Contains(out, `crewshipd_schedule_fire_punctuality_seconds{hostname="h",quantile="0.95"} 45`) {
		t.Errorf("missing p95=45; output:\n%s", out)
	}
	if !strings.Contains(out, `crewshipd_schedule_fire_punctuality_sample_count{hostname="h"} 2`) {
		t.Errorf("missing sample_count=2 (manual and legacy rows excluded); output:\n%s", out)
	}
}

// TestCollectScheduleFirePunctualityMetrics_NoDataIsAbsentNotZero pins the
// F39 rule for this family: with no stamped scheduled fires the quantile
// series is absent, the sample count is a real 0, and the family header
// is still declared.
func TestCollectScheduleFirePunctualityMetrics_NoDataIsAbsentNotZero(t *testing.T) {
	s := b16Fixture(t)
	// Scheduled runs that predate due_at: rows exist, samples do not.
	seedPipelineRun(t, s, "run_legacy", "schedule", "2026-01-01T11:00:00Z", "", nil)

	var b strings.Builder
	s.collectScheduleFirePunctualityMetrics(context.Background(), &b, "h")
	out := b.String()

	if strings.Contains(out, `crewshipd_schedule_fire_punctuality_seconds{`) {
		t.Errorf("quantile series must be absent with zero samples; output:\n%s", out)
	}
	if !strings.Contains(out, `crewshipd_schedule_fire_punctuality_sample_count{hostname="h"} 0`) {
		t.Errorf("sample_count must be a real, explicit 0; output:\n%s", out)
	}
	if !strings.Contains(out, "# TYPE crewshipd_schedule_fire_punctuality_seconds gauge") {
		t.Error("family header must still be declared so absent() alerts have a stable series name")
	}
}

// ── inbox items per successful run ─────────────────────────────────────────

func TestCollectInboxItemsPerSuccessfulRunMetrics_JoinsBothRunTables(t *testing.T) {
	s := b16Fixture(t)
	succeeded, noChange, needsHuman, failed := "SUCCEEDED", "NO_CHANGE", "NEEDS_HUMAN", "FAILED"

	// Three successful agent runs (assignments) and one successful routine
	// run (pipeline_runs): denominator 4.
	seedAssignment(t, s, "as_ok_1", "COMPLETED", "2026-01-01T00:00:00Z", "", &succeeded, nil, nil)
	seedAssignment(t, s, "as_ok_2", "COMPLETED", "2026-01-01T00:00:01Z", "", &noChange, nil, nil)
	seedAssignment(t, s, "as_ok_3", "COMPLETED", "2026-01-01T00:00:02Z", "", &succeeded, nil, nil)
	seedPipelineRun(t, s, "run_ok", "schedule", "2026-01-01T00:00:03Z", "", &succeeded)
	// Runs that are ALLOWED to raise items: not in the denominator, and
	// their items are not in the numerator.
	seedAssignment(t, s, "as_human", "COMPLETED", "2026-01-01T00:00:04Z", "", &needsHuman, nil, nil)
	seedPipelineRun(t, s, "run_failed", "schedule", "2026-01-01T00:00:05Z", "", &failed)
	seedInboxItem(t, s, "ibx_human_ok", "run_needs_human", "as_human")
	seedInboxItem(t, s, "ibx_failed_ok", "failed_run", "run_failed")
	// A run with no outcome yet: neither side.
	seedAssignment(t, s, "as_running", "RUNNING", "2026-01-01T00:00:06Z", "", nil, nil, nil)

	// The violations §12 says must never happen: an item pointing at a
	// NO_CHANGE assignment, and one pointing at a SUCCEEDED routine run.
	seedInboxItem(t, s, "ibx_bad_1", "run_needs_human", "as_ok_2")
	seedInboxItem(t, s, "ibx_bad_2", "failed_run", "run_ok")

	var b strings.Builder
	s.collectInboxItemsPerSuccessfulRunMetrics(context.Background(), &b, "h")
	out := b.String()

	if !strings.Contains(out, `crewshipd_successful_runs_total{hostname="h"} 4`) {
		t.Errorf("expected 4 successful runs across both tables; output:\n%s", out)
	}
	if !strings.Contains(out, `crewshipd_inbox_items_on_successful_runs{hostname="h"} 2`) {
		t.Errorf("expected exactly 2 items on successful runs; output:\n%s", out)
	}
	if !strings.Contains(out, `crewshipd_inbox_items_per_successful_run{hostname="h"} 0.5`) {
		t.Errorf("expected ratio 2/4 = 0.5; output:\n%s", out)
	}
}

func TestCollectInboxItemsPerSuccessfulRunMetrics_CleanIsARealZero(t *testing.T) {
	s := b16Fixture(t)
	succeeded, needsHuman := "SUCCEEDED", "NEEDS_HUMAN"
	seedAssignment(t, s, "as_ok", "COMPLETED", "2026-01-01T00:00:00Z", "", &succeeded, nil, nil)
	seedAssignment(t, s, "as_human", "COMPLETED", "2026-01-01T00:00:01Z", "", &needsHuman, nil, nil)
	seedInboxItem(t, s, "ibx_human_ok", "run_needs_human", "as_human")

	var b strings.Builder
	s.collectInboxItemsPerSuccessfulRunMetrics(context.Background(), &b, "h")
	out := b.String()

	if !strings.Contains(out, `crewshipd_successful_runs_total{hostname="h"} 1`) {
		t.Errorf("expected 1 successful run; output:\n%s", out)
	}
	if !strings.Contains(out, `crewshipd_inbox_items_on_successful_runs{hostname="h"} 0`) {
		t.Errorf("expected 0 items on successful runs; output:\n%s", out)
	}
	if !strings.Contains(out, `crewshipd_inbox_items_per_successful_run{hostname="h"} 0`) {
		t.Errorf("expected ratio 0 (a real, computed zero: one successful run, no items); output:\n%s", out)
	}
}

// TestCollectInboxItemsPerSuccessfulRunMetrics_NoSuccessfulRunsIsAbsentNotZero
// pins the denominator rule: with no successful runs the ratio is absent
// (0/0 has no honest value), while both raw counts are real zeros.
func TestCollectInboxItemsPerSuccessfulRunMetrics_NoSuccessfulRunsIsAbsentNotZero(t *testing.T) {
	s := b16Fixture(t)
	needsHuman := "NEEDS_HUMAN"
	seedAssignment(t, s, "as_human", "COMPLETED", "2026-01-01T00:00:00Z", "", &needsHuman, nil, nil)
	seedInboxItem(t, s, "ibx_human_ok", "run_needs_human", "as_human")

	var b strings.Builder
	s.collectInboxItemsPerSuccessfulRunMetrics(context.Background(), &b, "h")
	out := b.String()

	if strings.Contains(out, `crewshipd_inbox_items_per_successful_run{`) {
		t.Errorf("ratio must be absent with zero successful runs; output:\n%s", out)
	}
	if !strings.Contains(out, "# TYPE crewshipd_inbox_items_per_successful_run gauge") {
		t.Error("family header must still be declared so absent() alerts have a stable series name")
	}
	if !strings.Contains(out, `crewshipd_successful_runs_total{hostname="h"} 0`) {
		t.Errorf("successful_runs_total must be a real, explicit 0; output:\n%s", out)
	}
	if !strings.Contains(out, `crewshipd_inbox_items_on_successful_runs{hostname="h"} 0`) {
		t.Errorf("items_on_successful_runs must be a real, explicit 0; output:\n%s", out)
	}
}
