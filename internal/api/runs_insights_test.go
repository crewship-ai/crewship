package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// emitRunRowFull inserts a run.started + optional terminal entry for a run,
// with a controllable agent, trigger, resolved model and duration so the
// insights aggregation + enrichment can be asserted. status="" leaves it
// RUNNING (model then goes on run.started metadata).
func (f *runsTestFixture) emitRunRowFull(t *testing.T, traceID, agentID, status, trigger, model string, when time.Time, dur time.Duration) {
	t.Helper()
	ctx := context.Background()

	insertJournal := func(id, kind string, ts time.Time, payload string) {
		_, err := f.h.db.ExecContext(ctx, `
			INSERT INTO journal_entries
				(id, workspace_id, agent_id, ts, entry_type, severity, priority, actor_type, actor_id, summary, payload, refs, trace_id)
			VALUES (?, ?, ?, ?, ?, 'info', 'normal', 'sidecar', ?, 'r', ?, '{}', ?)`,
			id, f.wsID, agentID, ts.UTC().Format("2006-01-02T15:04:05.000Z"),
			kind, agentID, payload, traceID)
		if err != nil {
			t.Fatalf("insert %s/%s: %v", kind, traceID, err)
		}
	}

	startedPayload := `{"trigger_type":"` + trigger + `"}`
	if status == "" && model != "" {
		startedPayload = `{"trigger_type":"` + trigger + `","metadata":{"model":"` + model + `"}}`
	}
	insertJournal(traceID+"_s", "run.started", when, startedPayload)
	if status == "" {
		return
	}
	terminalKind, ok := map[string]string{
		"COMPLETED": "run.completed",
		"FAILED":    "run.failed",
		"CANCELLED": "run.cancelled",
		"TIMEOUT":   "run.timeout",
	}[status]
	if !ok {
		t.Fatalf("unknown status %q", status)
	}
	terminalPayload := `{"exit_code":0}`
	if model != "" {
		terminalPayload = `{"exit_code":0,"metadata":{"model":"` + model + `"}}`
	}
	insertJournal(traceID+"_t", terminalKind, when.Add(dur), terminalPayload)
}

func TestRunHandler_Insights_RequiresWorkspace(t *testing.T) {
	f := newRunsTestFixture(t)
	req := httptest.NewRequest("GET", "/api/v1/runs/insights", nil)
	rr := httptest.NewRecorder()
	f.h.Insights(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status=%d want 401; body=%s", rr.Code, rr.Body.String())
	}
}

func TestRunHandler_Insights_RejectsBadWindow(t *testing.T) {
	f := newRunsTestFixture(t)
	req := httptest.NewRequest("GET", "/api/v1/runs/insights?window=1y", nil)
	req = withWorkspaceUser(req, f.user, f.wsID, "OWNER")
	rr := httptest.NewRecorder()
	f.h.Insights(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status=%d want 400 for unknown window; body=%s", rr.Code, rr.Body.String())
	}
}

func TestRunHandler_Insights_HappyPath(t *testing.T) {
	f := newRunsTestFixture(t)
	// Second crew + agent so the crew rollup has more than one bucket.
	crew2 := seedCrewRow(t, f.h.db, "c-runs2", f.wsID, "Growth", "growth")
	agent2 := seedAgentRow(t, f.h.db, "a-runs2", f.wsID, crew2, "Nadia", "nadia", "AGENT")

	now := time.Now().UTC()
	// agent f.agent (crew Engineering): 2 completed USER opus, 1 running AGENT opus
	f.emitRunRowFull(t, "i_a", f.agent, "COMPLETED", "USER", "claude-opus", now.Add(-5*time.Hour), 10*time.Second)
	f.emitRunRowFull(t, "i_b", f.agent, "COMPLETED", "USER", "claude-opus", now.Add(-4*time.Hour), 20*time.Second)
	f.emitRunRowFull(t, "i_e", f.agent, "", "AGENT", "claude-opus", now.Add(-1*time.Hour), 0)
	// agent2 (crew Growth): 1 completed CRON sonnet, 1 failed WEBHOOK sonnet
	f.emitRunRowFull(t, "i_c", agent2, "COMPLETED", "CRON", "claude-sonnet", now.Add(-3*time.Hour), 30*time.Second)
	f.emitRunRowFull(t, "i_d", agent2, "FAILED", "WEBHOOK", "claude-sonnet", now.Add(-2*time.Hour), 40*time.Second)
	// out of the 24h window — excluded
	f.emitRunRowFull(t, "i_old", f.agent, "COMPLETED", "USER", "claude-opus", now.Add(-48*time.Hour), 10*time.Second)

	req := httptest.NewRequest("GET", "/api/v1/runs/insights?window=24h", nil)
	req = withWorkspaceUser(req, f.user, f.wsID, "OWNER")
	rr := httptest.NewRecorder()
	f.h.Insights(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}

	var resp runInsightsResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v\nbody: %s", err, rr.Body.String())
	}

	if resp.Window != "24h" {
		t.Errorf("window=%q want 24h", resp.Window)
	}
	if resp.Totals.Total != 5 || resp.Totals.Succeeded != 3 || resp.Totals.Failed != 1 || resp.Totals.Running != 1 {
		t.Errorf("totals=%+v want total5 ok3 failed1 running1", resp.Totals)
	}
	if resp.Duration.P50Ms != 20000 || resp.Duration.P95Ms != 40000 {
		t.Errorf("duration=%+v want p50=20000 p95=40000", resp.Duration)
	}

	// crew rollup: Engineering total3 fail0, Growth total2 fail1
	eng := findCrew(resp.ByCrew, "Engineering")
	if eng == nil || eng.Total != 3 || eng.Failed != 0 {
		t.Errorf("ByCrew[Engineering]=%+v want total3 fail0", eng)
	}
	growth := findCrew(resp.ByCrew, "Growth")
	if growth == nil || growth.Total != 2 || growth.Failed != 1 {
		t.Errorf("ByCrew[Growth]=%+v want total2 fail1", growth)
	}

	// top agents carry display names
	tom := findAgent(resp.TopAgents, "Thomas")
	if tom == nil || tom.Total != 3 || tom.CrewName != "Engineering" {
		t.Errorf("TopAgents[Thomas]=%+v want total3 crew Engineering", tom)
	}

	// by_model surfaced from journal aggregate
	if tot, fail, ok := insightCat(resp.ByModel, "claude-opus"); !ok || tot != 3 || fail != 0 {
		t.Errorf("ByModel[opus]=(%d,%d,%v) want (3,0,true)", tot, fail, ok)
	}
	if tot, _, ok := insightCat(resp.ByTrigger, "USER"); !ok || tot != 2 {
		t.Errorf("ByTrigger[USER] total=%d want 2", tot)
	}
}

func findCrew(rows []crewCount, name string) *crewCount {
	for i := range rows {
		if rows[i].Name == name {
			return &rows[i]
		}
	}
	return nil
}

func findAgent(rows []insightAgentCount, name string) *insightAgentCount {
	for i := range rows {
		if rows[i].Name == name {
			return &rows[i]
		}
	}
	return nil
}

func insightCat(rows []journalCategory, key string) (total, failed int, ok bool) {
	for _, c := range rows {
		if c.Key == key {
			return c.Total, c.Failed, true
		}
	}
	return 0, 0, false
}
