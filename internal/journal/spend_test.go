package journal

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

// pipelineRunsSchemaSQL is a minimal pipeline_runs shape (migration
// v83) — just the columns Spend's routine-side queries touch. Layered
// onto openTestDB(t)'s DB (journal_test.go) so spend_test.go stays
// self-contained without pulling in the full pipeline package schema.
const pipelineRunsSchemaSQL = `
CREATE TABLE pipeline_runs (
    id            TEXT PRIMARY KEY,
    workspace_id  TEXT NOT NULL,
    pipeline_id   TEXT NOT NULL,
    pipeline_slug TEXT NOT NULL,
    started_at    TEXT NOT NULL,
    cost_usd      REAL NOT NULL DEFAULT 0
);
`

func openSpendTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db := openTestDB(t)
	if _, err := db.ExecContext(context.Background(), pipelineRunsSchemaSQL); err != nil {
		t.Fatalf("pipeline_runs schema: %v", err)
	}
	return db
}

func insertPipelineRun(t *testing.T, db *sql.DB, id, workspaceID, pipelineID, pipelineSlug string, startedAt time.Time, costUSD float64) {
	t.Helper()
	if _, err := db.Exec(
		`INSERT INTO pipeline_runs (id, workspace_id, pipeline_id, pipeline_slug, started_at, cost_usd) VALUES (?, ?, ?, ?, ?, ?)`,
		id, workspaceID, pipelineID, pipelineSlug, startedAt.Format("2006-01-02T15:04:05.000000000Z07:00"), costUSD,
	); err != nil {
		t.Fatalf("insert pipeline_run: %v", err)
	}
}

func emitCost(t *testing.T, w *Writer, ws, crewID, agentID string, costUSD float64, ts time.Time) {
	t.Helper()
	if _, err := w.Emit(context.Background(), Entry{
		WorkspaceID: ws,
		CrewID:      crewID,
		AgentID:     agentID,
		Type:        EntryCostIncurred,
		ActorType:   ActorSystem,
		Summary:     "spend",
		Payload:     map[string]any{"cost_usd": costUSD, "provider": "anthropic", "model": "claude-haiku-4-5"},
		TS:          ts,
	}); err != nil {
		t.Fatalf("emit cost.incurred: %v", err)
	}
}

func TestSpend_RequiresWorkspace(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	if _, err := Spend(context.Background(), db, "", RunWindow24h, 5); err == nil {
		t.Fatal("expected error when workspace_id is empty")
	}
}

func TestSpend_EmptyWorkspace_ZeroResult(t *testing.T) {
	db := openSpendTestDB(t)
	defer db.Close()
	res, err := Spend(context.Background(), db, "ws_empty", RunWindow7d, 5)
	if err != nil {
		t.Fatalf("spend: %v", err)
	}
	if res.TotalCostUSD != 0 || len(res.ByAgent) != 0 || len(res.ByRoutine) != 0 {
		t.Errorf("empty workspace should be all-zero, got %+v", res)
	}
	if res.Window != "7d" {
		t.Errorf("Window = %q, want 7d", res.Window)
	}
}

func TestSpend_ByAgent_GroupsAndSums(t *testing.T) {
	db := openSpendTestDB(t)
	defer db.Close()
	w := NewWriter(db, quietLogger(), WriterOptions{FlushSize: 1})
	defer w.Close()

	now := time.Now().UTC()
	emitCost(t, w, "ws_s", "crew_a", "agent_a", 1.50, now.Add(-2*time.Hour))
	emitCost(t, w, "ws_s", "crew_a", "agent_a", 0.50, now.Add(-1*time.Hour))
	emitCost(t, w, "ws_s", "crew_b", "agent_b", 3.00, now.Add(-30*time.Minute))
	// Outside the 24h window — must not count.
	emitCost(t, w, "ws_s", "crew_a", "agent_a", 100.0, now.Add(-48*time.Hour))
	if err := w.Flush(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}
	time.Sleep(50 * time.Millisecond)

	res, err := Spend(context.Background(), db, "ws_s", RunWindow24h, 5)
	if err != nil {
		t.Fatalf("spend: %v", err)
	}
	if got, want := res.TotalCostUSD, 5.0; got < want-0.0001 || got > want+0.0001 {
		t.Errorf("TotalCostUSD = %v, want %v (100.0 outside window must be excluded)", got, want)
	}

	var agentABucket, agentBBucket *SpendByAgentBucket
	for i := range res.ByAgent {
		b := &res.ByAgent[i]
		if b.AgentID == "agent_a" {
			agentABucket = b
		}
		if b.AgentID == "agent_b" {
			agentBBucket = b
		}
	}
	if agentABucket == nil {
		t.Fatal("missing agent_a bucket")
	}
	if agentABucket.CostUSD < 1.9999 || agentABucket.CostUSD > 2.0001 {
		t.Errorf("agent_a cost = %v, want 2.0 (1.50+0.50 same day)", agentABucket.CostUSD)
	}
	if agentABucket.CallCount != 2 {
		t.Errorf("agent_a call count = %d, want 2", agentABucket.CallCount)
	}
	if agentBBucket == nil || agentBBucket.CostUSD < 2.9999 || agentBBucket.CostUSD > 3.0001 {
		t.Errorf("agent_b bucket = %+v, want cost 3.0", agentBBucket)
	}
}

func TestSpend_ByRoutine_And_TopRoutinesRuns(t *testing.T) {
	db := openSpendTestDB(t)
	defer db.Close()

	now := time.Now().UTC()
	insertPipelineRun(t, db, "run_1", "ws_s", "pln_1", "expensive-routine", now.Add(-1*time.Hour), 10.0)
	insertPipelineRun(t, db, "run_2", "ws_s", "pln_1", "expensive-routine", now.Add(-2*time.Hour), 5.0)
	insertPipelineRun(t, db, "run_3", "ws_s", "pln_2", "cheap-routine", now.Add(-30*time.Minute), 0.10)
	// Outside window.
	insertPipelineRun(t, db, "run_old", "ws_s", "pln_1", "expensive-routine", now.Add(-48*time.Hour), 999.0)

	res, err := Spend(context.Background(), db, "ws_s", RunWindow24h, 5)
	if err != nil {
		t.Fatalf("spend: %v", err)
	}

	var pln1Bucket *SpendByRoutineBucket
	for i := range res.ByRoutine {
		if res.ByRoutine[i].PipelineID == "pln_1" {
			pln1Bucket = &res.ByRoutine[i]
		}
	}
	if pln1Bucket == nil {
		t.Fatal("missing pln_1 bucket")
	}
	if pln1Bucket.CostUSD < 14.9999 || pln1Bucket.CostUSD > 15.0001 {
		t.Errorf("pln_1 cost = %v, want 15.0 (10+5, excludes the 999 outside window)", pln1Bucket.CostUSD)
	}
	if pln1Bucket.RunCount != 2 {
		t.Errorf("pln_1 run count = %d, want 2", pln1Bucket.RunCount)
	}
	if pln1Bucket.PipelineSlug != "expensive-routine" {
		t.Errorf("pln_1 slug = %q, want expensive-routine", pln1Bucket.PipelineSlug)
	}

	if len(res.TopRoutines) == 0 || res.TopRoutines[0].ID != "pln_1" {
		t.Fatalf("TopRoutines[0] = %+v, want pln_1 first (highest total spend)", res.TopRoutines)
	}

	if len(res.TopRuns) == 0 || res.TopRuns[0].ID != "run_1" {
		t.Fatalf("TopRuns[0] = %+v, want run_1 first (single most expensive run)", res.TopRuns)
	}
}

// TestSpend_DayBuckets_AreUTC asserts the day×crew×agent buckets are
// keyed on the UTC calendar day, not the server's local day. It forces
// process-local time to a zone whose local date differs from UTC for the
// inserted instant (so a regression to date(ts,'localtime') would bucket
// under the wrong day) and asserts the returned bucket Date equals the
// UTC date of the entry.
func TestSpend_DayBuckets_AreUTC(t *testing.T) {
	inst := time.Now().UTC().Add(-2 * time.Hour) // in the 24h window

	// Pick a fixed offset that pushes inst across a day boundary, so the
	// local calendar date is guaranteed to differ from the UTC one. This
	// makes a 'localtime' regression observable on every run regardless of
	// the wall-clock hour.
	var offsetHours int
	if h := inst.Hour(); h < 12 {
		offsetHours = -(h + 1) // roll into the previous day
	} else {
		offsetHours = 24 - h // roll into the next day
	}
	saved := time.Local
	time.Local = time.FixedZone("straddle", offsetHours*3600)
	defer func() { time.Local = saved }()

	if got, want := inst.In(time.Local).Format("2006-01-02"), inst.Format("2006-01-02"); got == want {
		t.Fatalf("test setup: local date %s == UTC date %s, straddle not achieved", got, want)
	}

	db := openSpendTestDB(t)
	defer db.Close()
	w := NewWriter(db, quietLogger(), WriterOptions{FlushSize: 1})
	defer w.Close()

	emitCost(t, w, "ws_utc", "crew_x", "agent_x", 4.00, inst)
	if err := w.Flush(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}
	time.Sleep(50 * time.Millisecond)

	res, err := Spend(context.Background(), db, "ws_utc", RunWindow24h, 5)
	if err != nil {
		t.Fatalf("spend: %v", err)
	}
	if len(res.ByAgent) != 1 {
		t.Fatalf("ByAgent = %+v, want exactly 1 bucket", res.ByAgent)
	}
	wantDay := inst.Format("2006-01-02") // UTC (inst is already UTC)
	if res.ByAgent[0].Date != wantDay {
		t.Errorf("bucket Date = %q, want UTC day %q (buckets must be UTC, not local %q)",
			res.ByAgent[0].Date, wantDay, inst.In(time.Local).Format("2006-01-02"))
	}
}

// TestSpend_TotalExact_WhenByAgentTruncates proves TotalCostUSD stays
// exact even when the per-bucket ByAgent breakdown is clipped at
// maxSpendRows: the total is a dedicated unbounded SUM, not an
// accumulation of the (truncated) buckets.
func TestSpend_TotalExact_WhenByAgentTruncates(t *testing.T) {
	saved := maxSpendRows
	maxSpendRows = 2
	defer func() { maxSpendRows = saved }()

	db := openSpendTestDB(t)
	defer db.Close()
	w := NewWriter(db, quietLogger(), WriterOptions{FlushSize: 1})
	defer w.Close()

	now := time.Now().UTC()
	// Three distinct (crew,agent) buckets, same day → three groups. With
	// the cap at 2 the ByAgent breakdown truncates, but all three costs
	// must still be reflected in the total.
	emitCost(t, w, "ws_t", "crew_1", "agent_1", 1.00, now.Add(-1*time.Hour))
	emitCost(t, w, "ws_t", "crew_2", "agent_2", 2.00, now.Add(-1*time.Hour))
	emitCost(t, w, "ws_t", "crew_3", "agent_3", 4.00, now.Add(-1*time.Hour))
	if err := w.Flush(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}
	time.Sleep(50 * time.Millisecond)

	res, err := Spend(context.Background(), db, "ws_t", RunWindow24h, 5)
	if err != nil {
		t.Fatalf("spend: %v", err)
	}
	if !res.Truncated {
		t.Fatalf("expected Truncated=true with cap=2 and 3 buckets")
	}
	if len(res.ByAgent) != 2 {
		t.Fatalf("ByAgent len = %d, want 2 (clipped at cap)", len(res.ByAgent))
	}
	if got, want := res.TotalCostUSD, 7.0; got < want-0.0001 || got > want+0.0001 {
		t.Errorf("TotalCostUSD = %v, want %v (must sum ALL spend, not just the un-truncated buckets)", got, want)
	}
}

func TestSpend_TopN_Respected(t *testing.T) {
	db := openSpendTestDB(t)
	defer db.Close()

	now := time.Now().UTC()
	labels := []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j"}
	for i, l := range labels {
		insertPipelineRun(t, db, "run_"+l, "ws_s", "pln_"+l, "r"+l, now.Add(-time.Duration(i)*time.Minute), float64(10-i))
	}

	res, err := Spend(context.Background(), db, "ws_s", RunWindow24h, 3)
	if err != nil {
		t.Fatalf("spend: %v", err)
	}
	if len(res.TopRoutines) != 3 {
		t.Errorf("len(TopRoutines) = %d, want 3", len(res.TopRoutines))
	}
	if len(res.TopRuns) != 3 {
		t.Errorf("len(TopRuns) = %d, want 3", len(res.TopRuns))
	}
}
