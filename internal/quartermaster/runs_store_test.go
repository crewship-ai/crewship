package quartermaster

import (
	"context"
	"strings"
	"testing"
	"time"
)

// runsSchemaSQL extends the journal schema with the eval_runs table.
// We append it to the in-memory DB created by openDB so the runs_store
// helpers can read/write a real row.
const runsSchemaSQL = `
CREATE TABLE eval_runs (
    id TEXT PRIMARY KEY,
    workspace_id TEXT NOT NULL,
    kind TEXT NOT NULL,
    mission_id TEXT,
    baseline_mission_id TEXT,
    candidate_mission_id TEXT,
    status TEXT NOT NULL DEFAULT 'queued',
    result TEXT,
    seed INTEGER NOT NULL DEFAULT 0,
    signature TEXT,
    total_tokens INTEGER NOT NULL DEFAULT 0,
    total_cost_usd REAL NOT NULL DEFAULT 0,
    regressed INTEGER NOT NULL DEFAULT 0,
    created_by TEXT,
    created_at TEXT NOT NULL,
    completed_at TEXT
);
`

// TestInsertReplayRun_RequiresFields covers the validation guards.
func TestInsertReplayRun_RequiresFields(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	if _, err := db.Exec(runsSchemaSQL); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		r    RunRecord
	}{
		{"missing id", RunRecord{WorkspaceID: "ws", MissionID: "m"}},
		{"missing workspace", RunRecord{ID: "r1", MissionID: "m"}},
		{"missing mission", RunRecord{ID: "r1", WorkspaceID: "ws"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := InsertReplayRun(context.Background(), db, tt.r)
			if err == nil {
				t.Errorf("want error, got nil")
			}
		})
	}
}

// TestInsertReplayRun_HappyPath inserts a row and verifies the columns
// land via direct SELECT.
func TestInsertReplayRun_HappyPath(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	if _, err := db.Exec(runsSchemaSQL); err != nil {
		t.Fatal(err)
	}

	err := InsertReplayRun(context.Background(), db, RunRecord{
		ID: "r1", WorkspaceID: "ws_test", MissionID: "m1",
		Seed: 42, CreatedBy: "tester",
	})
	if err != nil {
		t.Fatalf("InsertReplayRun: %v", err)
	}

	var (
		kind, status string
		seed         int64
		mission      string
		createdBy    string
	)
	err = db.QueryRow(`SELECT kind, status, mission_id, seed, COALESCE(created_by, '')
	                   FROM eval_runs WHERE id = ?`, "r1").
		Scan(&kind, &status, &mission, &seed, &createdBy)
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if kind != "replay" || status != "queued" || mission != "m1" || seed != 42 || createdBy != "tester" {
		t.Errorf("got kind=%s status=%s mission=%s seed=%d createdBy=%s",
			kind, status, mission, seed, createdBy)
	}
}

// TestInsertRegressionRun_RequiresFields covers regression-specific guards.
func TestInsertRegressionRun_RequiresFields(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	if _, err := db.Exec(runsSchemaSQL); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		r    RunRecord
	}{
		{"missing id", RunRecord{WorkspaceID: "ws", BaselineMissionID: "b", CandidateMissionID: "c"}},
		{"missing workspace", RunRecord{ID: "r1", BaselineMissionID: "b", CandidateMissionID: "c"}},
		{"missing baseline", RunRecord{ID: "r1", WorkspaceID: "ws", CandidateMissionID: "c"}},
		{"missing candidate", RunRecord{ID: "r1", WorkspaceID: "ws", BaselineMissionID: "b"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := InsertRegressionRun(context.Background(), db, tt.r)
			if err == nil {
				t.Errorf("want error, got nil")
			}
		})
	}
}

// TestInsertRegressionRun_HappyPath verifies the regression row writes
// both baseline and candidate IDs.
func TestInsertRegressionRun_HappyPath(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	if _, err := db.Exec(runsSchemaSQL); err != nil {
		t.Fatal(err)
	}

	err := InsertRegressionRun(context.Background(), db, RunRecord{
		ID: "rg1", WorkspaceID: "ws_test",
		BaselineMissionID: "m_baseline", CandidateMissionID: "m_candidate",
	})
	if err != nil {
		t.Fatalf("InsertRegressionRun: %v", err)
	}

	var kind, baseline, candidate string
	err = db.QueryRow(`SELECT kind, baseline_mission_id, candidate_mission_id
	                   FROM eval_runs WHERE id = ?`, "rg1").
		Scan(&kind, &baseline, &candidate)
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if kind != "regression" || baseline != "m_baseline" || candidate != "m_candidate" {
		t.Errorf("got %s/%s/%s", kind, baseline, candidate)
	}
}

// TestUpdateRunStatus_StampsCompletedAt verifies the completed_at column
// is filled only for terminal statuses (completed/failed) — running stays
// NULL.
func TestUpdateRunStatus_StampsCompletedAt(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	if _, err := db.Exec(runsSchemaSQL); err != nil {
		t.Fatal(err)
	}
	if err := InsertReplayRun(context.Background(), db,
		RunRecord{ID: "r1", WorkspaceID: "ws_test", MissionID: "m1"}); err != nil {
		t.Fatal(err)
	}

	// running → completed_at remains NULL
	if err := UpdateRunStatus(context.Background(), db, "r1",
		"running", "", "", 0, 0, false); err != nil {
		t.Fatal(err)
	}
	var completedAt *string
	_ = db.QueryRow(`SELECT completed_at FROM eval_runs WHERE id = 'r1'`).Scan(&completedAt)
	if completedAt != nil {
		t.Errorf("running status should not stamp completed_at, got %v", *completedAt)
	}

	// completed → completed_at populated
	if err := UpdateRunStatus(context.Background(), db, "r1",
		"completed", "ok", "sig", 100, 0.05, false); err != nil {
		t.Fatal(err)
	}
	_ = db.QueryRow(`SELECT completed_at FROM eval_runs WHERE id = 'r1'`).Scan(&completedAt)
	if completedAt == nil {
		t.Errorf("completed status should stamp completed_at")
	}
}

// TestUpdateRunStatus_FailedAlsoStamps confirms 'failed' also closes the
// row (mirrors 'completed').
func TestUpdateRunStatus_FailedAlsoStamps(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	if _, err := db.Exec(runsSchemaSQL); err != nil {
		t.Fatal(err)
	}
	if err := InsertReplayRun(context.Background(), db,
		RunRecord{ID: "r1", WorkspaceID: "ws_test", MissionID: "m1"}); err != nil {
		t.Fatal(err)
	}
	if err := UpdateRunStatus(context.Background(), db, "r1",
		"failed", "boom", "", 0, 0, false); err != nil {
		t.Fatal(err)
	}
	var completedAt *string
	_ = db.QueryRow(`SELECT completed_at FROM eval_runs WHERE id = 'r1'`).Scan(&completedAt)
	if completedAt == nil {
		t.Errorf("failed should stamp completed_at")
	}
}

// TestUpdateRunStatus_RegressedFlag round-trips the bool ↔ int conversion.
func TestUpdateRunStatus_RegressedFlag(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	if _, err := db.Exec(runsSchemaSQL); err != nil {
		t.Fatal(err)
	}
	if err := InsertReplayRun(context.Background(), db,
		RunRecord{ID: "r1", WorkspaceID: "ws_test", MissionID: "m1"}); err != nil {
		t.Fatal(err)
	}
	if err := UpdateRunStatus(context.Background(), db, "r1",
		"completed", "ok", "", 0, 0, true); err != nil {
		t.Fatal(err)
	}
	var regressed int
	_ = db.QueryRow(`SELECT regressed FROM eval_runs WHERE id = 'r1'`).Scan(&regressed)
	if regressed != 1 {
		t.Errorf("regressed=true should write 1, got %d", regressed)
	}
}

// TestListRuns_RequiresWorkspaceID — cross-tenant guard.
func TestListRuns_RequiresWorkspaceID(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	if _, err := db.Exec(runsSchemaSQL); err != nil {
		t.Fatal(err)
	}

	_, err := ListRuns(context.Background(), db, "", 10)
	if err == nil {
		t.Fatal("want workspace_id error")
	}
	if !strings.Contains(err.Error(), "workspace_id") {
		t.Errorf("err: %v", err)
	}
}

// TestListRuns_LimitClamping verifies 0/-1 → 50 and >200 → 50.
func TestListRuns_LimitClamping(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	if _, err := db.Exec(runsSchemaSQL); err != nil {
		t.Fatal(err)
	}

	// Seed 60 runs.
	for i := 0; i < 60; i++ {
		id := "r" + itoaQM(i)
		if err := InsertReplayRun(context.Background(), db,
			RunRecord{ID: id, WorkspaceID: "ws_test", MissionID: "m1"}); err != nil {
			t.Fatal(err)
		}
	}

	tests := []struct {
		name        string
		limit       int
		wantMaxRows int
	}{
		{"zero clamps to 50", 0, 50},
		{"negative clamps to 50", -10, 50},
		{"under cap respected", 30, 30},
		{"over cap clamps to 50 (200 is the upper bound)", 500, 50},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ListRuns(context.Background(), db, "ws_test", tt.limit)
			if err != nil {
				t.Fatalf("ListRuns: %v", err)
			}
			if len(got) != tt.wantMaxRows {
				t.Errorf("got %d rows, want %d", len(got), tt.wantMaxRows)
			}
		})
	}
}

// TestListRuns_OrderedDescending — newest first contract.
func TestListRuns_OrderedDescending(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	if _, err := db.Exec(runsSchemaSQL); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 5; i++ {
		id := "r" + itoaQM(i)
		if err := InsertReplayRun(context.Background(), db,
			RunRecord{ID: id, WorkspaceID: "ws_test", MissionID: "m1"}); err != nil {
			t.Fatal(err)
		}
		// Tiny sleep so created_at has measurable ordering.
		time.Sleep(time.Millisecond)
	}

	got, err := ListRuns(context.Background(), db, "ws_test", 10)
	if err != nil {
		t.Fatalf("ListRuns: %v", err)
	}
	if len(got) != 5 {
		t.Fatalf("got %d, want 5", len(got))
	}
	// Last inserted (r4) should be first.
	if got[0].ID != "r4" {
		t.Errorf("expected newest-first; got %s as first", got[0].ID)
	}
}

// TestParseRunTS covers all three accepted timestamp formats.
func TestParseRunTS(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		wantErr bool
	}{
		{"RFC3339Nano", "2026-04-30T12:30:45.123456789Z", false},
		{"RFC3339", "2026-04-30T12:30:45Z", false},
		{"sqlite default", "2026-04-30 12:30:45", false},
		{"empty", "", true},
		{"garbage", "not-a-time", true},
		{"date only", "2026-04-30", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseRunTS(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Errorf("want err, got %v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if got.Location() != time.UTC {
				t.Errorf("not UTC: %v", got.Location())
			}
		})
	}
}

// TestNullStr / TestBoolInt — small private helpers, but keep them
// covered so a regression that flipped boolInt's polarity (1↔0) would
// fail loudly.
func TestNullStr(t *testing.T) {
	if got := nullStr(""); got != nil {
		t.Errorf("nullStr(\"\") = %v want nil", got)
	}
	if got := nullStr("x"); got != "x" {
		t.Errorf("nullStr(\"x\") = %v", got)
	}
}

func TestBoolInt(t *testing.T) {
	if got := boolInt(true); got != 1 {
		t.Errorf("boolInt(true) = %d want 1", got)
	}
	if got := boolInt(false); got != 0 {
		t.Errorf("boolInt(false) = %d want 0", got)
	}
}

// TestDetectRegression_RequiresEmitter exercises the early validation.
func TestDetectRegression_RequiresEmitter(t *testing.T) {
	db := openDB(t)
	defer db.Close()
	_, err := DetectRegression(context.Background(), db, nil, "ws", "m_b", "m_c")
	if err == nil {
		t.Fatal("want emitter-required error")
	}
}

// TestRegressedNames pins the structured-payload helper used in the
// eval.regression_detected entry.
func TestRegressedNames(t *testing.T) {
	report := RegressionReport{Deltas: []MetricDelta{
		{Name: "tool_success_rate", Regressed: true},
		{Name: "steps_to_goal", Regressed: false},
		{Name: "total_cost_usd", Regressed: true},
		{Name: "hallucinations", Regressed: false},
	}}
	got := regressedNames(report)
	if len(got) != 2 || got[0] != "tool_success_rate" || got[1] != "total_cost_usd" {
		t.Errorf("regressedNames = %v", got)
	}
}

// TestSummarize_NoRegression returns the canonical "no regression" string.
func TestSummarize_NoRegression(t *testing.T) {
	if got := summarize(RegressionReport{}); got != "no regression" {
		t.Errorf("summarize(empty) = %q", got)
	}
}

// TestSummarize_JoinsReasons concatenates regressed deltas with semicolons.
func TestSummarize_JoinsReasons(t *testing.T) {
	r := RegressionReport{
		Regressed: true,
		Deltas: []MetricDelta{
			{Name: "tool_success_rate", Regressed: true, Reason: "drop A"},
			{Name: "total_cost_usd", Regressed: true, Reason: "rise B"},
		},
	}
	got := summarize(r)
	if !strings.Contains(got, "drop A") || !strings.Contains(got, "rise B") {
		t.Errorf("summarize = %q", got)
	}
	if !strings.Contains(got, ";") {
		t.Errorf("expected semicolon separator, got %q", got)
	}
}

// itoaQM is a small int-to-string for tests; avoids importing strconv.
func itoaQM(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	if n < 0 {
		b = append(b, '-')
		n = -n
	}
	start := len(b)
	for n > 0 {
		b = append(b, byte('0'+n%10))
		n /= 10
	}
	for i, j := start, len(b)-1; i < j; i, j = i+1, j-1 {
		b[i], b[j] = b[j], b[i]
	}
	return string(b)
}
