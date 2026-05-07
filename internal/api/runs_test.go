package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// runsTestFixture wires a RunHandler over the migrated test DB plus a
// helper that emits journal entries directly so the runs aggregation
// has something to roll up. Returns a thin closure that emits one
// (run.started, optional terminal) pair per call so the table-driven
// tests below stay compact.
type runsTestFixture struct {
	h     *RunHandler
	wsID  string
	user  string
	agent string
}

func newRunsTestFixture(t *testing.T) *runsTestFixture {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	// Seed one crew + agent so the enrichment join has rows. The runs
	// list response includes `agent_name`/`crew_name`; without the seed
	// those would all be nil and the enrichment branch wouldn't be
	// covered.
	crewID := seedCrewRow(t, db, "c-runs", wsID, "Engineering", "engineering")
	agentID := seedAgentRow(t, db, "a-runs", wsID, crewID, "Tomáš", "tomas", "AGENT")
	return &runsTestFixture{
		h:     NewRunHandler(db, newTestLogger()),
		wsID:  wsID,
		user:  userID,
		agent: agentID,
	}
}

// emitRunRow inserts a run.started + (optional) terminal journal entry
// for traceID directly. Skips the journal Writer so the tests don't
// have to wait on the batched flush.
func (f *runsTestFixture) emitRunRow(t *testing.T, traceID, status, trigger string, when time.Time) {
	t.Helper()
	ctx := context.Background()

	insertJournal := func(id, kind string, ts time.Time, payload string) {
		_, err := f.h.db.ExecContext(ctx, `
			INSERT INTO journal_entries
				(id, workspace_id, agent_id, ts, entry_type, severity, priority, actor_type, actor_id, summary, payload, refs, trace_id)
			VALUES (?, ?, ?, ?, ?, 'info', 'normal', 'sidecar', ?, 'r', ?, '{}', ?)`,
			id, f.wsID, f.agent, ts.UTC().Format("2006-01-02T15:04:05.000Z"),
			kind, f.agent, payload, traceID)
		if err != nil {
			t.Fatalf("insert %s/%s: %v", kind, traceID, err)
		}
	}

	startedPayload := `{"trigger_type":"` + trigger + `"}`
	insertJournal(traceID+"_s", "run.started", when, startedPayload)
	if status == "" {
		return
	}
	terminalKind := map[string]string{
		"COMPLETED": "run.completed",
		"FAILED":    "run.failed",
		"CANCELLED": "run.cancelled",
		"TIMEOUT":   "run.timeout",
	}[status]
	insertJournal(traceID+"_t", terminalKind, when.Add(time.Minute), `{"exit_code":0}`)
}

func TestRunHandler_List_RequiresWorkspace(t *testing.T) {
	f := newRunsTestFixture(t)
	req := httptest.NewRequest("GET", "/api/v1/runs", nil)
	rr := httptest.NewRecorder()
	f.h.List(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status=%d want 401; body=%s", rr.Code, rr.Body.String())
	}
}

func TestRunHandler_List_HappyPath(t *testing.T) {
	f := newRunsTestFixture(t)
	now := time.Now().UTC()
	f.emitRunRow(t, "run_a", "COMPLETED", "USER", now.Add(-3*time.Minute))
	f.emitRunRow(t, "run_b", "FAILED", "WEBHOOK", now.Add(-2*time.Minute))
	f.emitRunRow(t, "run_c", "", "USER", now.Add(-1*time.Minute)) // running

	req := httptest.NewRequest("GET", "/api/v1/runs", nil)
	req = withWorkspaceUser(req, f.user, f.wsID, "OWNER")
	rr := httptest.NewRecorder()
	f.h.List(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var resp runListResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Data) != 3 {
		t.Fatalf("data len=%d want 3", len(resp.Data))
	}
	// Ordered started_at DESC → run_c (running) first.
	if resp.Data[0].ID != "run_c" || resp.Data[0].Status != "RUNNING" {
		t.Errorf("first row: %+v", resp.Data[0])
	}
	if resp.Stats.Running != 1 || resp.Stats.Today != 3 || resp.Stats.Failed != 1 {
		t.Errorf("stats: %+v want running=1 today=3 failed=1", resp.Stats)
	}
	if resp.Pagination.Total != 3 {
		t.Errorf("pagination.total=%d want 3", resp.Pagination.Total)
	}
	// Enrichment populated for the seeded agent.
	if resp.Data[0].AgentName == nil || *resp.Data[0].AgentName != "Tomáš" {
		t.Errorf("agent_name not enriched: %v", resp.Data[0].AgentName)
	}
	if resp.Data[0].CrewName == nil || *resp.Data[0].CrewName != "Engineering" {
		t.Errorf("crew_name not enriched: %v", resp.Data[0].CrewName)
	}
}

func TestRunHandler_List_StatusFilter(t *testing.T) {
	f := newRunsTestFixture(t)
	now := time.Now().UTC()
	f.emitRunRow(t, "run_c", "COMPLETED", "USER", now.Add(-3*time.Minute))
	f.emitRunRow(t, "run_f", "FAILED", "USER", now.Add(-2*time.Minute))
	f.emitRunRow(t, "run_r", "", "USER", now.Add(-1*time.Minute))

	req := httptest.NewRequest("GET", "/api/v1/runs?status=FAILED", nil)
	req = withWorkspaceUser(req, f.user, f.wsID, "OWNER")
	rr := httptest.NewRecorder()
	f.h.List(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var resp runListResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if len(resp.Data) != 1 || resp.Data[0].ID != "run_f" {
		t.Errorf("status filter: got %+v want run_f", runIDs(resp.Data))
	}
}

func TestRunHandler_List_RejectsUnknownStatus(t *testing.T) {
	f := newRunsTestFixture(t)
	req := httptest.NewRequest("GET", "/api/v1/runs?status=running", nil) // lowercase, not allowed
	req = withWorkspaceUser(req, f.user, f.wsID, "OWNER")
	rr := httptest.NewRecorder()
	f.h.List(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status=%d body=%s want 400", rr.Code, rr.Body.String())
	}
}

func TestRunHandler_List_PageBounds(t *testing.T) {
	f := newRunsTestFixture(t)
	now := time.Now().UTC()
	for i := 0; i < 5; i++ {
		traceID := "run_" + string(rune('a'+i))
		f.emitRunRow(t, traceID, "COMPLETED", "USER", now.Add(-time.Duration(5-i)*time.Minute))
	}

	cases := []struct {
		name     string
		qs       string
		wantData int
		wantPage int
	}{
		{"default page=1 limit=50", "", 5, 1},
		{"page=1 limit=2", "?page=1&limit=2", 2, 1},
		{"page=2 limit=2", "?page=2&limit=2", 2, 2},
		{"page=3 limit=2 partial", "?page=3&limit=2", 1, 3},
		{"page=0 clamped to 1", "?page=0", 5, 1},
		{"limit=0 falls to default", "?limit=0", 5, 1},
		{"limit=200 clamped to default", "?limit=200", 5, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/api/v1/runs"+c.qs, nil)
			req = withWorkspaceUser(req, f.user, f.wsID, "OWNER")
			rr := httptest.NewRecorder()
			f.h.List(rr, req)
			if rr.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
			}
			var resp runListResponse
			_ = json.Unmarshal(rr.Body.Bytes(), &resp)
			if len(resp.Data) != c.wantData {
				t.Errorf("data len=%d want %d", len(resp.Data), c.wantData)
			}
			if resp.Pagination.Page != c.wantPage {
				t.Errorf("page=%d want %d", resp.Pagination.Page, c.wantPage)
			}
		})
	}
}

func TestRunHandler_List_TriggerFilter(t *testing.T) {
	f := newRunsTestFixture(t)
	now := time.Now().UTC()
	f.emitRunRow(t, "run_u", "COMPLETED", "USER", now.Add(-3*time.Minute))
	f.emitRunRow(t, "run_w", "COMPLETED", "WEBHOOK", now.Add(-2*time.Minute))
	f.emitRunRow(t, "run_c", "COMPLETED", "CRON", now.Add(-1*time.Minute))

	req := httptest.NewRequest("GET", "/api/v1/runs?trigger=WEBHOOK", nil)
	req = withWorkspaceUser(req, f.user, f.wsID, "OWNER")
	rr := httptest.NewRecorder()
	f.h.List(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d", rr.Code)
	}
	var resp runListResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if len(resp.Data) != 1 || resp.Data[0].ID != "run_w" {
		t.Errorf("trigger filter: %+v", runIDs(resp.Data))
	}
}

func TestRunHandler_List_CrossTenantHidden(t *testing.T) {
	f := newRunsTestFixture(t)
	// Bring up a second workspace and seed a run there.  The first
	// workspace's call must not see it — pre-existing test for the
	// journal layer covers the store; this one ensures the handler
	// doesn't accidentally widen the scope.
	wsOther := "ws-other"
	if _, err := f.h.db.Exec(
		`INSERT INTO workspaces (id, name, slug) VALUES (?, 'O', 'o')`, wsOther); err != nil {
		t.Fatalf("seed other ws: %v", err)
	}
	if _, err := f.h.db.Exec(
		`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('m-other', ?, ?, 'OWNER')`,
		wsOther, f.user); err != nil {
		t.Fatalf("seed member: %v", err)
	}
	now := time.Now().UTC()
	if _, err := f.h.db.Exec(`
		INSERT INTO journal_entries
			(id, workspace_id, agent_id, ts, entry_type, severity, priority, actor_type, actor_id, summary, payload, refs, trace_id)
		VALUES ('j_x', ?, ?, ?, 'run.started', 'info', 'normal', 'sidecar', ?, 'r', '{"trigger_type":"USER"}', '{}', 'cross_tenant_run')`,
		wsOther, f.agent, now.UTC().Format("2006-01-02T15:04:05.000Z"), f.agent); err != nil {
		t.Fatalf("seed cross-tenant row: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/v1/runs", nil)
	req = withWorkspaceUser(req, f.user, f.wsID, "OWNER")
	rr := httptest.NewRecorder()
	f.h.List(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d", rr.Code)
	}
	var resp runListResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	for _, r := range resp.Data {
		if r.ID == "cross_tenant_run" {
			t.Errorf("cross-tenant leak: saw %s", r.ID)
		}
	}
}

func TestValidRunStatus(t *testing.T) {
	for _, ok := range []string{"RUNNING", "COMPLETED", "FAILED", "CANCELLED", "TIMEOUT"} {
		if !validRunStatus(ok) {
			t.Errorf("validRunStatus(%q) = false; want true", ok)
		}
	}
	for _, bad := range []string{"", "running", "Completed", "DONE", "failed"} {
		if validRunStatus(bad) {
			t.Errorf("validRunStatus(%q) = true; want false", bad)
		}
	}
}

// runIDs is a small readability helper for failure messages. Returns
// just the IDs from a list response so a wrong filter shows as a
// clean diff instead of a many-field struct dump.
func runIDs(data []runResponse) []string {
	out := make([]string, len(data))
	for i, r := range data {
		out[i] = r.ID
	}
	return out
}
