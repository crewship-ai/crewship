package api

// Direct-call coverage for the metrics "filler" functions in
// metrics_fillers_runs_missions.go and metrics_fillers_issues_cost.go.
//
// These tests exercise the fillRunsCount / fillActiveMissions /
// fillIssuesClosed / fillCostUSD methods by seeding rows and invoking the
// fillers directly (no HTTP round-trip), covering the empty, populated, and
// group-by aggregation branches plus the DB-error path.

import (
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// covMetParams builds a validated timeseriesParams for the given metric /
// group_by over a 24h window with 1h buckets ending "now".
func covMetParams(wsID, metric, groupBy string) timeseriesParams {
	return timeseriesParams{
		Metric:      metric,
		Window:      "24h",
		Bucket:      "1h",
		GroupBy:     groupBy,
		WindowDur:   24 * time.Hour,
		BucketDur:   time.Hour,
		WorkspaceID: wsID,
		Now:         time.Now().UTC(),
	}
}

// covMetSetup mirrors MetricsHandler.Timeseries' pre-fill setup: it computes
// bucket starts, the zero-filled response, and the key->index lookup map.
func covMetSetup(p timeseriesParams) ([]time.Time, map[string]int, *metricsResponse) {
	starts := bucketStartsFor(p)
	resp := &metricsResponse{
		Metric:       p.Metric,
		Window:       p.Window,
		Bucket:       p.Bucket,
		GroupBy:      p.GroupBy,
		Buckets:      make([]metricsBucket, len(starts)),
		SeriesLabels: map[string]string{},
	}
	idxByKey := make(map[string]int, len(starts))
	for i, s := range starts {
		k := bucketKey(s)
		resp.Buckets[i] = metricsBucket{TS: k, Series: map[string]float64{}}
		idxByKey[k] = i
	}
	return starts, idxByKey, resp
}

// covMetReq returns an *http.Request whose context carries a workspace, so the
// fillers' r.Context() reads work even though they don't consult workspace ctx
// directly (they take WorkspaceID from params).
func covMetReq() *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/metrics/timeseries", nil)
	return req.WithContext(context.Background())
}

// covMetSumSeries totals one series key across every bucket.
func covMetSumSeries(resp *metricsResponse, key string) float64 {
	var total float64
	for _, b := range resp.Buckets {
		total += b.Series[key]
	}
	return total
}

// covMetSeedMission inserts a mission row with explicit type/cost/timestamps so
// the cost and issues-closed fillers can aggregate non-zero values.
func covMetSeedMission(t *testing.T, db *sql.DB, id, wsID, crewID, leadID, status, missionType string, cost float64, updatedAt, completedAt string) {
	t.Helper()
	var completed interface{}
	if completedAt == "" {
		completed = nil
	} else {
		completed = completedAt
	}
	_, err := db.Exec(`INSERT INTO missions
		(id, workspace_id, crew_id, lead_agent_id, trace_id, title, status, mission_type,
		 total_estimated_cost, created_at, updated_at, completed_at)
		VALUES (?, ?, ?, ?, ?, 'M', ?, ?, ?, ?, ?, ?)`,
		id, wsID, crewID, leadID, "trace-"+id, status, missionType, cost, updatedAt, updatedAt, completed)
	if err != nil {
		t.Fatalf("covMetSeedMission %s: %v", id, err)
	}
}

// --------------------------------------------------------------------------
// fillRunsCount
// --------------------------------------------------------------------------

func TestCovMetFillRunsCount_Empty(t *testing.T) {
	h, _, wsID := newMetricsHandlerForTest(t)
	p := covMetParams(wsID, "runs_count", "none")
	_, idxByKey, resp := covMetSetup(p)

	if err := h.fillRunsCount(covMetReq(), p, idxByKey, resp); err != nil {
		t.Fatalf("fillRunsCount empty: %v", err)
	}
	if got := covMetSumSeries(resp, "total"); got != 0 {
		t.Errorf("empty total = %v, want 0", got)
	}
}

func TestCovMetFillRunsCount_None(t *testing.T) {
	h, db, wsID := newMetricsHandlerForTest(t)
	seedCrewRow(t, db, "crew-r", wsID, "RunsCrew", "runs-crew")
	seedAgentRow(t, db, "agent-r", wsID, "crew-r", "Runner", "runner", "AGENT")

	seedRunFixture(t, db, "run-1", "agent-r", wsID, "COMPLETED", "USER", "")
	seedRunFixture(t, db, "run-2", "agent-r", wsID, "RUNNING", "USER", "")

	p := covMetParams(wsID, "runs_count", "none")
	_, idxByKey, resp := covMetSetup(p)
	if err := h.fillRunsCount(covMetReq(), p, idxByKey, resp); err != nil {
		t.Fatalf("fillRunsCount none: %v", err)
	}
	if got := covMetSumSeries(resp, "total"); got != 2 {
		t.Errorf("total runs = %v, want 2", got)
	}
	if resp.SeriesLabels["total"] != "Total" {
		t.Errorf("label total = %q, want Total", resp.SeriesLabels["total"])
	}
}

func TestCovMetFillRunsCount_GroupByCrew(t *testing.T) {
	h, db, wsID := newMetricsHandlerForTest(t)
	seedCrewRow(t, db, "crew-a", wsID, "Alpha", "alpha")
	seedCrewRow(t, db, "crew-b", wsID, "Bravo", "bravo")
	seedAgentRow(t, db, "agent-a", wsID, "crew-a", "AgA", "ag-a", "AGENT")
	seedAgentRow(t, db, "agent-b", wsID, "crew-b", "AgB", "ag-b", "AGENT")
	// Unassigned agent (no crew) to exercise the COALESCE 'unassigned' branch.
	seedAgentRow(t, db, "agent-u", wsID, "", "AgU", "ag-u", "AGENT")

	seedRunFixture(t, db, "run-a1", "agent-a", wsID, "COMPLETED", "USER", "")
	seedRunFixture(t, db, "run-a2", "agent-a", wsID, "COMPLETED", "USER", "")
	seedRunFixture(t, db, "run-b1", "agent-b", wsID, "COMPLETED", "USER", "")
	seedRunFixture(t, db, "run-u1", "agent-u", wsID, "COMPLETED", "USER", "")

	p := covMetParams(wsID, "runs_count", "crew")
	_, idxByKey, resp := covMetSetup(p)
	if err := h.fillRunsCount(covMetReq(), p, idxByKey, resp); err != nil {
		t.Fatalf("fillRunsCount crew: %v", err)
	}
	if got := covMetSumSeries(resp, "crew-a"); got != 2 {
		t.Errorf("crew-a runs = %v, want 2", got)
	}
	if got := covMetSumSeries(resp, "crew-b"); got != 1 {
		t.Errorf("crew-b runs = %v, want 1", got)
	}
	if got := covMetSumSeries(resp, "unassigned"); got != 1 {
		t.Errorf("unassigned runs = %v, want 1", got)
	}
	if resp.SeriesLabels["crew-a"] != "Alpha" {
		t.Errorf("label crew-a = %q, want Alpha", resp.SeriesLabels["crew-a"])
	}
	if resp.SeriesLabels["unassigned"] != "Unassigned" {
		t.Errorf("label unassigned = %q, want Unassigned", resp.SeriesLabels["unassigned"])
	}
}

func TestCovMetFillRunsCount_DBError(t *testing.T) {
	h, db, wsID := newMetricsHandlerForTest(t)
	db.Close()
	p := covMetParams(wsID, "runs_count", "none")
	_, idxByKey, resp := covMetSetup(p)
	if err := h.fillRunsCount(covMetReq(), p, idxByKey, resp); err == nil {
		t.Fatal("fillRunsCount none on closed DB: want error, got nil")
	}

	pc := covMetParams(wsID, "runs_count", "crew")
	_, idxByKey2, resp2 := covMetSetup(pc)
	if err := h.fillRunsCount(covMetReq(), pc, idxByKey2, resp2); err == nil {
		t.Fatal("fillRunsCount crew on closed DB: want error, got nil")
	}
}

// --------------------------------------------------------------------------
// fillActiveMissions
// --------------------------------------------------------------------------

func TestCovMetFillActiveMissions_Empty(t *testing.T) {
	h, _, wsID := newMetricsHandlerForTest(t)
	p := covMetParams(wsID, "active_missions", "none")
	starts, idxByKey, resp := covMetSetup(p)
	if err := h.fillActiveMissions(covMetReq(), p, starts, idxByKey, resp); err != nil {
		t.Fatalf("fillActiveMissions empty: %v", err)
	}
	// No missions => every bucket total stays zero.
	if got := covMetSumSeries(resp, "total"); got != 0 {
		t.Errorf("empty active total = %v, want 0", got)
	}
}

func TestCovMetFillActiveMissions_NoStarts(t *testing.T) {
	h, _, wsID := newMetricsHandlerForTest(t)
	p := covMetParams(wsID, "active_missions", "none")
	_, idxByKey, resp := covMetSetup(p)
	// Empty starts slice hits the early return.
	if err := h.fillActiveMissions(covMetReq(), p, nil, idxByKey, resp); err != nil {
		t.Fatalf("fillActiveMissions nil starts: %v", err)
	}
}

func TestCovMetFillActiveMissions_None(t *testing.T) {
	h, db, wsID := newMetricsHandlerForTest(t)
	seedCrewRow(t, db, "crew-m", wsID, "MCrew", "m-crew")
	seedAgentRow(t, db, "lead-m", wsID, "crew-m", "Lead", "lead-m", "LEAD")

	now := time.Now().UTC()
	created := now.Add(-3 * time.Hour).Format(time.RFC3339)
	// In-progress mission created 3h ago, not yet completed => alive across
	// recent buckets.
	covMetSeedMission(t, db, "m-active", wsID, "crew-m", "lead-m", "IN_PROGRESS", "orchestration", 0, created, "")

	p := covMetParams(wsID, "active_missions", "none")
	starts, idxByKey, resp := covMetSetup(p)
	if err := h.fillActiveMissions(covMetReq(), p, starts, idxByKey, resp); err != nil {
		t.Fatalf("fillActiveMissions none: %v", err)
	}
	if got := covMetSumSeries(resp, "total"); got == 0 {
		t.Errorf("active total = 0, want > 0 (mission should be alive in recent buckets)")
	}
	if resp.SeriesLabels["total"] != "Total" {
		t.Errorf("label total = %q, want Total", resp.SeriesLabels["total"])
	}
}

func TestCovMetFillActiveMissions_GroupByStatus(t *testing.T) {
	h, db, wsID := newMetricsHandlerForTest(t)
	seedCrewRow(t, db, "crew-s", wsID, "SCrew", "s-crew")
	seedAgentRow(t, db, "lead-s", wsID, "crew-s", "Lead", "lead-s", "LEAD")

	now := time.Now().UTC()
	created := now.Add(-2 * time.Hour).Format(time.RFC3339)
	covMetSeedMission(t, db, "m-ip", wsID, "crew-s", "lead-s", "IN_PROGRESS", "orchestration", 0, created, "")
	covMetSeedMission(t, db, "m-rev", wsID, "crew-s", "lead-s", "REVIEW", "orchestration", 0, created, "")

	p := covMetParams(wsID, "active_missions", "status")
	starts, idxByKey, resp := covMetSetup(p)
	if err := h.fillActiveMissions(covMetReq(), p, starts, idxByKey, resp); err != nil {
		t.Fatalf("fillActiveMissions status: %v", err)
	}
	if got := covMetSumSeries(resp, "IN_PROGRESS"); got == 0 {
		t.Errorf("IN_PROGRESS active = 0, want > 0")
	}
	if got := covMetSumSeries(resp, "REVIEW"); got == 0 {
		t.Errorf("REVIEW active = 0, want > 0")
	}
	if resp.SeriesLabels["IN_PROGRESS"] != "IN_PROGRESS" {
		t.Errorf("label IN_PROGRESS = %q, want IN_PROGRESS", resp.SeriesLabels["IN_PROGRESS"])
	}
}

func TestCovMetFillActiveMissions_CompletedInWindow(t *testing.T) {
	h, db, wsID := newMetricsHandlerForTest(t)
	seedCrewRow(t, db, "crew-c", wsID, "CCrew", "c-crew")
	seedAgentRow(t, db, "lead-c", wsID, "crew-c", "Lead", "lead-c", "LEAD")

	now := time.Now().UTC()
	created := now.Add(-6 * time.Hour).Format(time.RFC3339)
	completed := now.Add(-3 * time.Hour).Format(time.RFC3339)
	// Mission with a completed_at exercises the hasEnd branch (alive for the
	// buckets between created and completed).
	covMetSeedMission(t, db, "m-done", wsID, "crew-c", "lead-c", "COMPLETED", "orchestration", 0, completed, completed)
	// Use a non-nil created_at distinct from updated_at via direct update.
	if _, err := db.Exec(`UPDATE missions SET created_at = ? WHERE id = 'm-done'`, created); err != nil {
		t.Fatalf("update created_at: %v", err)
	}

	p := covMetParams(wsID, "active_missions", "none")
	starts, idxByKey, resp := covMetSetup(p)
	if err := h.fillActiveMissions(covMetReq(), p, starts, idxByKey, resp); err != nil {
		t.Fatalf("fillActiveMissions completed: %v", err)
	}
	if got := covMetSumSeries(resp, "total"); got == 0 {
		t.Errorf("completed-in-window active total = 0, want > 0")
	}
}

func TestCovMetFillActiveMissions_DBError(t *testing.T) {
	h, db, wsID := newMetricsHandlerForTest(t)
	db.Close()
	p := covMetParams(wsID, "active_missions", "none")
	starts, idxByKey, resp := covMetSetup(p)
	if err := h.fillActiveMissions(covMetReq(), p, starts, idxByKey, resp); err == nil {
		t.Fatal("fillActiveMissions on closed DB: want error, got nil")
	}
}

// --------------------------------------------------------------------------
// fillIssuesClosed
// --------------------------------------------------------------------------

func TestCovMetFillIssuesClosed_Empty(t *testing.T) {
	h, _, wsID := newMetricsHandlerForTest(t)
	p := covMetParams(wsID, "issues_closed", "none")
	_, idxByKey, resp := covMetSetup(p)
	if err := h.fillIssuesClosed(covMetReq(), p, idxByKey, resp); err != nil {
		t.Fatalf("fillIssuesClosed empty: %v", err)
	}
	if got := covMetSumSeries(resp, "total"); got != 0 {
		t.Errorf("empty issues total = %v, want 0", got)
	}
}

func TestCovMetFillIssuesClosed_None(t *testing.T) {
	h, db, wsID := newMetricsHandlerForTest(t)
	seedCrewRow(t, db, "crew-i", wsID, "ICrew", "i-crew")
	seedAgentRow(t, db, "lead-i", wsID, "crew-i", "Lead", "lead-i", "LEAD")

	completed := time.Now().UTC().Add(-30 * time.Minute).Format(time.RFC3339)
	covMetSeedMission(t, db, "iss-1", wsID, "crew-i", "lead-i", "DONE", "issue", 0, completed, completed)
	covMetSeedMission(t, db, "iss-2", wsID, "crew-i", "lead-i", "COMPLETED", "issue", 0, completed, completed)
	// An orchestration mission must be excluded by the mission_type filter.
	covMetSeedMission(t, db, "orch-1", wsID, "crew-i", "lead-i", "DONE", "orchestration", 0, completed, completed)

	p := covMetParams(wsID, "issues_closed", "none")
	_, idxByKey, resp := covMetSetup(p)
	if err := h.fillIssuesClosed(covMetReq(), p, idxByKey, resp); err != nil {
		t.Fatalf("fillIssuesClosed none: %v", err)
	}
	if got := covMetSumSeries(resp, "total"); got != 2 {
		t.Errorf("issues total = %v, want 2 (orchestration excluded)", got)
	}
}

func TestCovMetFillIssuesClosed_GroupByCrew(t *testing.T) {
	h, db, wsID := newMetricsHandlerForTest(t)
	seedCrewRow(t, db, "crew-x", wsID, "XCrew", "x-crew")
	seedCrewRow(t, db, "crew-y", wsID, "YCrew", "y-crew")
	seedAgentRow(t, db, "lead-x", wsID, "crew-x", "LeadX", "lead-x", "LEAD")
	seedAgentRow(t, db, "lead-y", wsID, "crew-y", "LeadY", "lead-y", "LEAD")

	completed := time.Now().UTC().Add(-20 * time.Minute).Format(time.RFC3339)
	covMetSeedMission(t, db, "iss-x1", wsID, "crew-x", "lead-x", "DONE", "issue", 0, completed, completed)
	covMetSeedMission(t, db, "iss-x2", wsID, "crew-x", "lead-x", "REVIEW", "issue", 0, completed, completed)
	covMetSeedMission(t, db, "iss-y1", wsID, "crew-y", "lead-y", "DONE", "issue", 0, completed, completed)

	p := covMetParams(wsID, "issues_closed", "crew")
	_, idxByKey, resp := covMetSetup(p)
	if err := h.fillIssuesClosed(covMetReq(), p, idxByKey, resp); err != nil {
		t.Fatalf("fillIssuesClosed crew: %v", err)
	}
	if got := covMetSumSeries(resp, "crew-x"); got != 2 {
		t.Errorf("crew-x issues = %v, want 2", got)
	}
	if got := covMetSumSeries(resp, "crew-y"); got != 1 {
		t.Errorf("crew-y issues = %v, want 1", got)
	}
	if resp.SeriesLabels["crew-x"] != "XCrew" {
		t.Errorf("label crew-x = %q, want XCrew", resp.SeriesLabels["crew-x"])
	}
}

func TestCovMetFillIssuesClosed_GroupByStatus(t *testing.T) {
	h, db, wsID := newMetricsHandlerForTest(t)
	seedCrewRow(t, db, "crew-z", wsID, "ZCrew", "z-crew")
	seedAgentRow(t, db, "lead-z", wsID, "crew-z", "LeadZ", "lead-z", "LEAD")

	completed := time.Now().UTC().Add(-15 * time.Minute).Format(time.RFC3339)
	covMetSeedMission(t, db, "iss-z1", wsID, "crew-z", "lead-z", "DONE", "issue", 0, completed, completed)
	covMetSeedMission(t, db, "iss-z2", wsID, "crew-z", "lead-z", "DONE", "issue", 0, completed, completed)
	covMetSeedMission(t, db, "iss-z3", wsID, "crew-z", "lead-z", "REVIEW", "issue", 0, completed, completed)

	p := covMetParams(wsID, "issues_closed", "status")
	_, idxByKey, resp := covMetSetup(p)
	if err := h.fillIssuesClosed(covMetReq(), p, idxByKey, resp); err != nil {
		t.Fatalf("fillIssuesClosed status: %v", err)
	}
	if got := covMetSumSeries(resp, "DONE"); got != 2 {
		t.Errorf("DONE issues = %v, want 2", got)
	}
	if got := covMetSumSeries(resp, "REVIEW"); got != 1 {
		t.Errorf("REVIEW issues = %v, want 1", got)
	}
	if resp.SeriesLabels["DONE"] != "DONE" {
		t.Errorf("label DONE = %q, want DONE", resp.SeriesLabels["DONE"])
	}
}

func TestCovMetFillIssuesClosed_DBError(t *testing.T) {
	h, db, wsID := newMetricsHandlerForTest(t)
	db.Close()
	for _, gb := range []string{"none", "crew", "status"} {
		p := covMetParams(wsID, "issues_closed", gb)
		_, idxByKey, resp := covMetSetup(p)
		if err := h.fillIssuesClosed(covMetReq(), p, idxByKey, resp); err == nil {
			t.Fatalf("fillIssuesClosed group_by=%s on closed DB: want error, got nil", gb)
		}
	}
}

// --------------------------------------------------------------------------
// fillCostUSD
// --------------------------------------------------------------------------

func TestCovMetFillCostUSD_Empty(t *testing.T) {
	h, _, wsID := newMetricsHandlerForTest(t)
	p := covMetParams(wsID, "cost_usd", "none")
	_, idxByKey, resp := covMetSetup(p)
	if err := h.fillCostUSD(covMetReq(), p, idxByKey, resp); err != nil {
		t.Fatalf("fillCostUSD empty: %v", err)
	}
	if got := covMetSumSeries(resp, "total"); got != 0 {
		t.Errorf("empty cost total = %v, want 0", got)
	}
}

func TestCovMetFillCostUSD_None(t *testing.T) {
	h, db, wsID := newMetricsHandlerForTest(t)
	seedCrewRow(t, db, "crew-cost", wsID, "CostCrew", "cost-crew")
	seedAgentRow(t, db, "lead-cost", wsID, "crew-cost", "Lead", "lead-cost", "LEAD")

	updated := time.Now().UTC().Add(-25 * time.Minute).Format(time.RFC3339)
	covMetSeedMission(t, db, "cm-1", wsID, "crew-cost", "lead-cost", "DONE", "orchestration", 1.50, updated, updated)
	covMetSeedMission(t, db, "cm-2", wsID, "crew-cost", "lead-cost", "DONE", "orchestration", 2.25, updated, updated)

	p := covMetParams(wsID, "cost_usd", "none")
	_, idxByKey, resp := covMetSetup(p)
	if err := h.fillCostUSD(covMetReq(), p, idxByKey, resp); err != nil {
		t.Fatalf("fillCostUSD none: %v", err)
	}
	if got := covMetSumSeries(resp, "total"); got != 3.75 {
		t.Errorf("cost total = %v, want 3.75", got)
	}
	if resp.SeriesLabels["total"] != "Total" {
		t.Errorf("label total = %q, want Total", resp.SeriesLabels["total"])
	}
}

func TestCovMetFillCostUSD_GroupByModel(t *testing.T) {
	h, db, wsID := newMetricsHandlerForTest(t)
	seedCrewRow(t, db, "crew-mdl", wsID, "ModelCrew", "model-crew")
	// Three agents with distinct llm_model values (one NULL → COALESCE
	// 'unknown' branch), each the lead_agent_id of a mission below.
	// fillCostUSD joins missions.lead_agent_id → agents and groups by
	// llm_model (no agent_role filter), and the DB enforces one LEAD per
	// crew, so these are plain AGENTs sharing the crew.
	seedAgentRow(t, db, "lead-opus", wsID, "crew-mdl", "Opus", "opus", "AGENT")
	seedAgentRow(t, db, "lead-sonnet", wsID, "crew-mdl", "Sonnet", "sonnet", "AGENT")
	seedAgentRow(t, db, "lead-none", wsID, "crew-mdl", "NoneModel", "none-model", "AGENT")
	if _, err := db.Exec(`UPDATE agents SET llm_model = 'opus-x' WHERE id = 'lead-opus'`); err != nil {
		t.Fatalf("set opus model: %v", err)
	}
	if _, err := db.Exec(`UPDATE agents SET llm_model = 'sonnet-x' WHERE id = 'lead-sonnet'`); err != nil {
		t.Fatalf("set sonnet model: %v", err)
	}
	if _, err := db.Exec(`UPDATE agents SET llm_model = NULL WHERE id = 'lead-none'`); err != nil {
		t.Fatalf("clear none model: %v", err)
	}

	updated := time.Now().UTC().Add(-10 * time.Minute).Format(time.RFC3339)
	covMetSeedMission(t, db, "cm-o1", wsID, "crew-mdl", "lead-opus", "DONE", "orchestration", 4.0, updated, updated)
	covMetSeedMission(t, db, "cm-o2", wsID, "crew-mdl", "lead-opus", "DONE", "orchestration", 1.0, updated, updated)
	covMetSeedMission(t, db, "cm-s1", wsID, "crew-mdl", "lead-sonnet", "DONE", "orchestration", 2.5, updated, updated)
	covMetSeedMission(t, db, "cm-n1", wsID, "crew-mdl", "lead-none", "DONE", "orchestration", 0.75, updated, updated)

	p := covMetParams(wsID, "cost_usd", "model")
	_, idxByKey, resp := covMetSetup(p)
	if err := h.fillCostUSD(covMetReq(), p, idxByKey, resp); err != nil {
		t.Fatalf("fillCostUSD model: %v", err)
	}
	if got := covMetSumSeries(resp, "opus-x"); got != 5.0 {
		t.Errorf("opus-x cost = %v, want 5.0", got)
	}
	if got := covMetSumSeries(resp, "sonnet-x"); got != 2.5 {
		t.Errorf("sonnet-x cost = %v, want 2.5", got)
	}
	if got := covMetSumSeries(resp, "unknown"); got != 0.75 {
		t.Errorf("unknown cost = %v, want 0.75", got)
	}
	if resp.SeriesLabels["opus-x"] != "opus-x" {
		t.Errorf("label opus-x = %q, want opus-x", resp.SeriesLabels["opus-x"])
	}
}

func TestCovMetFillCostUSD_DBError(t *testing.T) {
	h, db, wsID := newMetricsHandlerForTest(t)
	db.Close()
	for _, gb := range []string{"none", "model"} {
		p := covMetParams(wsID, "cost_usd", gb)
		_, idxByKey, resp := covMetSetup(p)
		if err := h.fillCostUSD(covMetReq(), p, idxByKey, resp); err == nil {
			t.Fatalf("fillCostUSD group_by=%s on closed DB: want error, got nil", gb)
		}
	}
}
