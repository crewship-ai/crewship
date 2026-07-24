package pipeline

// DigestStats tests (#1422 item 4) — the deterministic aggregate the
// workspace-digest routine template's `query` step reads.

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/tsformat"
)

func seedDigestRun(t *testing.T, db *sql.DB, id, workspaceID, pipelineSlug, status string, costUSD float64, startedAt time.Time) {
	t.Helper()
	_, err := db.ExecContext(context.Background(), `
INSERT INTO pipeline_runs (id, workspace_id, pipeline_id, pipeline_slug, status, started_at, cost_usd, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, workspaceID, "pln_"+pipelineSlug, pipelineSlug, status,
		tsformat.Format(startedAt), costUSD, tsformat.Format(startedAt), tsformat.Format(startedAt))
	if err != nil {
		t.Fatalf("seed digest run %s: %v", id, err)
	}
}

func TestRunStore_DigestStats(t *testing.T) {
	db := openResumeTestDB(t)
	defer db.Close()
	runs := NewRunStore(db)
	now := time.Now().UTC()

	// In-window rows for ws_digest.
	seedDigestRun(t, db, "run_1", "ws_digest", "summarize-text", "completed", 0.01, now.Add(-1*time.Hour))
	seedDigestRun(t, db, "run_2", "ws_digest", "summarize-text", "completed", 0.02, now.Add(-2*time.Hour))
	seedDigestRun(t, db, "run_3", "ws_digest", "cost-report", "failed", 0.0, now.Add(-3*time.Hour))
	seedDigestRun(t, db, "run_4", "ws_digest", "cost-report", "failed", 0.0, now.Add(-4*time.Hour))
	seedDigestRun(t, db, "run_5", "ws_digest", "nightly-cleanup", "waiting", 0.0, now.Add(-5*time.Hour))
	// Out-of-window row — must not be counted.
	seedDigestRun(t, db, "run_old", "ws_digest", "summarize-text", "completed", 99.0, now.Add(-48*time.Hour))
	// Different workspace — must not leak in (tenant isolation).
	seedDigestRun(t, db, "run_other_ws", "ws_other", "summarize-text", "completed", 5.0, now.Add(-1*time.Hour))

	stats, err := runs.DigestStats(context.Background(), "ws_digest", 24)
	if err != nil {
		t.Fatalf("DigestStats: %v", err)
	}
	if stats.TotalRuns != 5 {
		t.Errorf("TotalRuns = %d, want 5", stats.TotalRuns)
	}
	if stats.Completed != 2 {
		t.Errorf("Completed = %d, want 2", stats.Completed)
	}
	if stats.Failed != 2 {
		t.Errorf("Failed = %d, want 2", stats.Failed)
	}
	if stats.Waiting != 1 {
		t.Errorf("Waiting = %d, want 1", stats.Waiting)
	}
	if got, want := stats.TotalCostUSD, 0.03; got < want-1e-9 || got > want+1e-9 {
		t.Errorf("TotalCostUSD = %v, want %v", got, want)
	}
	if len(stats.TopFailures) != 1 || stats.TopFailures[0].PipelineSlug != "cost-report" || stats.TopFailures[0].Count != 2 {
		t.Errorf("TopFailures = %+v, want [{cost-report 2}]", stats.TopFailures)
	}
	if !strings.Contains(stats.SummaryMD, "5") || !strings.Contains(stats.SummaryMD, "cost-report") {
		t.Errorf("SummaryMD missing expected content:\n%s", stats.SummaryMD)
	}
}

func TestRunStore_DigestStats_EmptyWindow(t *testing.T) {
	db := openResumeTestDB(t)
	defer db.Close()
	runs := NewRunStore(db)

	stats, err := runs.DigestStats(context.Background(), "ws_empty", 24)
	if err != nil {
		t.Fatalf("DigestStats: %v", err)
	}
	if stats.TotalRuns != 0 {
		t.Errorf("TotalRuns = %d, want 0", stats.TotalRuns)
	}
	if !strings.Contains(stats.SummaryMD, "No routine runs") {
		t.Errorf("SummaryMD = %q, want a no-runs message", stats.SummaryMD)
	}
}

func TestRunStore_DigestStats_DefaultsWindowHours(t *testing.T) {
	db := openResumeTestDB(t)
	defer db.Close()
	runs := NewRunStore(db)
	stats, err := runs.DigestStats(context.Background(), "ws_x", 0)
	if err != nil {
		t.Fatalf("DigestStats: %v", err)
	}
	if stats.WindowHours != 24 {
		t.Errorf("WindowHours = %d, want default 24", stats.WindowHours)
	}
}
