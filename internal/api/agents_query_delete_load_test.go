package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// agents_query.go — Delete + Load (the two zero-coverage handlers).
//
// Delete is a manage-tier soft-delete that doubles as the audit-trail
// trigger; the test surface is RBAC + 404 semantics + the soft-delete
// versus hard-delete invariant.
//
// Load aggregates per-agent workload from mission_tasks for the toolbar
// load widget. The query joins on a 24h window so the test seeds tasks
// straddling that boundary to pin the windowing behavior.
// ---------------------------------------------------------------------------

func newAgentHandlerForQueryTest(t *testing.T) (*AgentHandler, string, string) {
	t.Helper()
	h := NewAgentHandler(setupTestDB(t), newTestLogger())
	userID := seedTestUser(t, h.db)
	wsID := seedTestWorkspace(t, h.db, userID)
	return h, userID, wsID
}

// ---- Delete ----

func TestAgentDelete_VIEWER_Forbidden(t *testing.T) {
	h, userID, wsID := newAgentHandlerForQueryTest(t)
	seedAgentForStatus(t, h, "ag-1", wsID, "", "IDLE", false)

	req := httptest.NewRequest("DELETE", "/api/v1/agents/ag-1", nil)
	req.SetPathValue("agentId", "ag-1")
	req = withWorkspaceUser(req, userID, wsID, "VIEWER")
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("VIEWER delete = %d, want 403", rr.Code)
	}
	// Verify the row was NOT marked deleted as a side effect.
	var deleted int
	if err := h.db.QueryRow(`SELECT deleted_at IS NOT NULL FROM agents WHERE id = 'ag-1'`).Scan(&deleted); err != nil {
		t.Fatalf("readback: %v", err)
	}
	if deleted != 0 {
		t.Error("VIEWER 403 still soft-deleted the row")
	}
}

func TestAgentDelete_MANAGER_AlsoForbidden(t *testing.T) {
	// Delete is in the 'manage' tier — only OWNER/ADMIN pass. MANAGER
	// can create+update but not destructive ops; pin that boundary.
	h, userID, wsID := newAgentHandlerForQueryTest(t)
	seedAgentForStatus(t, h, "ag-mgr", wsID, "", "IDLE", false)

	req := httptest.NewRequest("DELETE", "/api/v1/agents/ag-mgr", nil)
	req.SetPathValue("agentId", "ag-mgr")
	req = withWorkspaceUser(req, userID, wsID, "MANAGER")
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("MANAGER delete = %d, want 403 (manage tier is OWNER/ADMIN only)", rr.Code)
	}
}

func TestAgentDelete_NotFound_UnknownID(t *testing.T) {
	h, userID, wsID := newAgentHandlerForQueryTest(t)
	req := httptest.NewRequest("DELETE", "/api/v1/agents/missing", nil)
	req.SetPathValue("agentId", "missing")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("unknown ID = %d, want 404", rr.Code)
	}
}

func TestAgentDelete_NotFound_CrossWorkspace(t *testing.T) {
	h, userID, wsA := newAgentHandlerForQueryTest(t)
	// Seed agent in a different workspace.
	wsB := "ws-foreign-del"
	if _, err := h.db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'F', 'f-del')`, wsB); err != nil {
		t.Fatalf("seed other ws: %v", err)
	}
	seedAgentForStatus(t, h, "ag-foreign", wsB, "", "IDLE", false)

	req := httptest.NewRequest("DELETE", "/api/v1/agents/ag-foreign", nil)
	req.SetPathValue("agentId", "ag-foreign")
	req = withWorkspaceUser(req, userID, wsA, "OWNER")
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("cross-workspace = %d, want 404 (no existence leak)", rr.Code)
	}
	// Foreign row must still exist + remain undeleted.
	var deleted int
	if err := h.db.QueryRow(`SELECT deleted_at IS NOT NULL FROM agents WHERE id = 'ag-foreign'`).Scan(&deleted); err != nil {
		t.Fatalf("readback: %v", err)
	}
	if deleted != 0 {
		t.Error("cross-workspace 404 still soft-deleted the foreign row")
	}
}

func TestAgentDelete_NotFound_AlreadySoftDeleted(t *testing.T) {
	// The UPDATE filter is `deleted_at IS NULL`; re-deleting an already
	// soft-deleted agent matches zero rows and must 404 — the idempotent
	// outcome from a UI perspective, with a clear signal for callers.
	h, userID, wsID := newAgentHandlerForQueryTest(t)
	seedAgentForStatus(t, h, "ag-gone", wsID, "", "IDLE", true)

	req := httptest.NewRequest("DELETE", "/api/v1/agents/ag-gone", nil)
	req.SetPathValue("agentId", "ag-gone")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("already-deleted = %d, want 404", rr.Code)
	}
}

func TestAgentDelete_HappyPath_SoftDeletesAndReturnsSuccess(t *testing.T) {
	h, userID, wsID := newAgentHandlerForQueryTest(t)
	seedAgentForStatus(t, h, "ag-bye", wsID, "", "IDLE", false)

	before := time.Now().UTC()
	req := httptest.NewRequest("DELETE", "/api/v1/agents/ag-bye", nil)
	req.SetPathValue("agentId", "ag-bye")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	after := time.Now().UTC()

	if rr.Code != http.StatusOK {
		t.Fatalf("delete = %d body=%s, want 200", rr.Code, rr.Body.String())
	}
	var body map[string]bool
	json.Unmarshal(rr.Body.Bytes(), &body)
	if !body["success"] {
		t.Errorf("response = %v, want success:true", body)
	}

	// Row must still exist (soft-delete invariant) with deleted_at set
	// inside the call window.
	var deletedAt string
	if err := h.db.QueryRow(`SELECT deleted_at FROM agents WHERE id = 'ag-bye'`).Scan(&deletedAt); err != nil {
		t.Fatalf("row vanished — Delete must SOFT delete, not hard delete: %v", err)
	}
	parsed, err := time.Parse(time.RFC3339, deletedAt)
	if err != nil {
		t.Fatalf("deleted_at parse: %v (raw=%q)", err, deletedAt)
	}
	// Allow a 1s tolerance window on either side for clock skew between
	// the Go runtime and the SQLite datetime serialization.
	if parsed.Before(before.Add(-time.Second)) || parsed.After(after.Add(time.Second)) {
		t.Errorf("deleted_at = %v, want in [%v, %v]", parsed, before, after)
	}
}

// ---- Load ----

// seedMissionForAgent seeds a minimal mission + crew + lead agent so we
// can drop mission_tasks rows against an arbitrary assigned_agent_id.
// Returns the mission ID — callers attach tasks to it.
func seedMissionForAgent(t *testing.T, h *AgentHandler, missionID, wsID, crewID, leadID string) {
	t.Helper()
	if _, err := h.db.Exec(`INSERT INTO missions
		(id, workspace_id, crew_id, lead_agent_id, trace_id, title, status, created_at)
		VALUES (?, ?, ?, ?, ?, 'Load Test', 'IN_PROGRESS', datetime('now'))`,
		missionID, wsID, crewID, leadID, "trace-"+missionID); err != nil {
		t.Fatalf("seed mission: %v", err)
	}
}

func seedTaskForLoad(t *testing.T, h *AgentHandler, taskID, missionID, agentID, status string,
	tokensUsed, tokenBudget int, completedAt *time.Time) {
	t.Helper()
	var done interface{}
	if completedAt != nil {
		done = completedAt.UTC().Format(time.RFC3339)
	}
	if _, err := h.db.Exec(`INSERT INTO mission_tasks
		(id, mission_id, assigned_agent_id, title, status, task_order, depends_on,
		 tokens_used, token_budget, completed_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, 0, '[]', ?, ?, ?, datetime('now'), datetime('now'))`,
		taskID, missionID, agentID, "T-"+taskID, status, tokensUsed, tokenBudget, done); err != nil {
		t.Fatalf("seed task %s: %v", taskID, err)
	}
}

func TestAgentLoad_EmptyWorkspace_ReturnsEmptyArray(t *testing.T) {
	h, userID, wsID := newAgentHandlerForQueryTest(t)
	req := httptest.NewRequest("GET", "/api/v1/agent-load", nil)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Load(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	body := strings.TrimSpace(rr.Body.String())
	if body != "[]" {
		t.Errorf("empty workspace body = %q, want \"[]\" (UI iterates; never null)", body)
	}
}

func TestAgentLoad_AggregatesPerAgent_With24hWindow(t *testing.T) {
	h, userID, wsID := newAgentHandlerForQueryTest(t)
	seedCrewRow(t, h.db, "crew-load", wsID, "C", "c-load")
	// Agent we'll attach tasks to.
	seedAgentForStatus(t, h, "ag-busy", wsID, "crew-load", "RUNNING", false)
	// Agent with zero workload — must appear in result with all zeros.
	seedAgentForStatus(t, h, "ag-idle", wsID, "crew-load", "IDLE", false)
	// Soft-deleted agent — excluded entirely.
	seedAgentForStatus(t, h, "ag-gone", wsID, "crew-load", "IDLE", true)
	// Foreign workspace agent — excluded.
	wsB := "ws-load-foreign"
	if _, err := h.db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'F', 'f-load')`, wsB); err != nil {
		t.Fatalf("seed wsB: %v", err)
	}
	seedCrewRow(t, h.db, "crew-foreign-load", wsB, "F", "f-load")
	seedAgentForStatus(t, h, "ag-foreign", wsB, "crew-foreign-load", "RUNNING", false)

	// Lead for mission FK chain (mission requires a lead agent).
	seedAgentForStatus(t, h, "ag-lead", wsID, "crew-load", "IDLE", false)
	seedMissionForAgent(t, h, "mission-load", wsID, "crew-load", "ag-lead")

	now := time.Now().UTC()
	insideWindow := now.Add(-3 * time.Hour) // within 24h
	outsideWindow := now.Add(-30 * time.Hour)

	// ag-busy: 2 IN_PROGRESS (active, contributes to active count + budget),
	//          1 PENDING + 1 BLOCKED (pending count + budget),
	//          1 COMPLETED inside window (completed_today, tokens_used),
	//          1 COMPLETED outside window (excluded from completed_today;
	//                                       also excluded from join clause)
	seedTaskForLoad(t, h, "t-active-1", "mission-load", "ag-busy", "IN_PROGRESS", 100, 500, nil)
	seedTaskForLoad(t, h, "t-active-2", "mission-load", "ag-busy", "IN_PROGRESS", 200, 600, nil)
	seedTaskForLoad(t, h, "t-pending-1", "mission-load", "ag-busy", "PENDING", 0, 1000, nil)
	seedTaskForLoad(t, h, "t-blocked-1", "mission-load", "ag-busy", "BLOCKED", 0, 700, nil)
	seedTaskForLoad(t, h, "t-done-recent", "mission-load", "ag-busy", "COMPLETED", 1500, 1500, &insideWindow)
	seedTaskForLoad(t, h, "t-done-old", "mission-load", "ag-busy", "COMPLETED", 99999, 99999, &outsideWindow)

	// Foreign workspace task — must NOT bleed in.
	seedMissionForAgent(t, h, "mission-foreign-load", wsB, "crew-foreign-load", "ag-foreign")
	seedTaskForLoad(t, h, "t-foreign", "mission-foreign-load", "ag-foreign", "IN_PROGRESS", 99999, 99999, nil)

	req := httptest.NewRequest("GET", "/api/v1/agent-load", nil)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Load(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}

	var got []struct {
		AgentID         string `json:"agent_id"`
		AgentName       string `json:"agent_name"`
		AgentSlug       string `json:"agent_slug"`
		AgentStatus     string `json:"agent_status"`
		ActiveTasks     int    `json:"active_tasks"`
		PendingTasks    int    `json:"pending_tasks"`
		CompletedToday  int    `json:"completed_today"`
		TokensUsedToday int    `json:"tokens_used_today"`
		TokenBudget     int    `json:"token_budget"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}

	byID := map[string]int{}
	for i, e := range got {
		byID[e.AgentID] = i
	}
	// Soft-deleted + foreign excluded; ag-busy + ag-idle + ag-lead remain.
	if len(got) != 3 {
		t.Fatalf("got %d agents, want 3 (busy + idle + lead; deleted + foreign excluded). got=%+v", len(got), got)
	}
	if _, ok := byID["ag-gone"]; ok {
		t.Error("soft-deleted agent leaked into result")
	}
	if _, ok := byID["ag-foreign"]; ok {
		t.Error("foreign-workspace agent leaked into result")
	}

	busyIdx, ok := byID["ag-busy"]
	if !ok {
		t.Fatal("ag-busy missing from result")
	}
	busy := got[busyIdx]
	if busy.ActiveTasks != 2 {
		t.Errorf("ag-busy ActiveTasks = %d, want 2", busy.ActiveTasks)
	}
	if busy.PendingTasks != 2 {
		t.Errorf("ag-busy PendingTasks = %d, want 2 (PENDING + BLOCKED)", busy.PendingTasks)
	}
	if busy.CompletedToday != 1 {
		t.Errorf("ag-busy CompletedToday = %d, want 1 (the 30h-old completion is excluded)", busy.CompletedToday)
	}
	// TokensUsedToday sums tokens_used across the JOIN's filtered rows:
	// active (100+200) + recent completion (1500) + the old completion
	// is EXCLUDED by the join's AND clause. Pending/blocked rows have
	// tokens_used=0 so they don't contribute.
	if busy.TokensUsedToday != 1800 {
		t.Errorf("ag-busy TokensUsedToday = %d, want 1800 (100+200+1500; old completion excluded)", busy.TokensUsedToday)
	}
	// TokenBudget sums token_budget for IN_PROGRESS+PENDING+BLOCKED only.
	if busy.TokenBudget != 500+600+1000+700 {
		t.Errorf("ag-busy TokenBudget = %d, want %d (sum of active+pending+blocked budgets)",
			busy.TokenBudget, 500+600+1000+700)
	}

	// ag-idle: no tasks at all. The LEFT JOIN keeps the agent row; all
	// aggregates are zero.
	idleIdx, ok := byID["ag-idle"]
	if !ok {
		t.Fatal("ag-idle missing from result")
	}
	idle := got[idleIdx]
	if idle.ActiveTasks != 0 || idle.PendingTasks != 0 || idle.CompletedToday != 0 ||
		idle.TokensUsedToday != 0 || idle.TokenBudget != 0 {
		t.Errorf("ag-idle aggregates non-zero: %+v", idle)
	}
}

func TestAgentLoad_TokensUsedCoalescesToTokenCount(t *testing.T) {
	// The COALESCE(tokens_used, token_count, 0) lets pre-v26 rows (which
	// only have token_count) still surface a meaningful number. Pin the
	// fallback so a refactor that drops the COALESCE doesn't silently
	// zero-out historical data.
	h, userID, wsID := newAgentHandlerForQueryTest(t)
	seedCrewRow(t, h.db, "crew-coa", wsID, "C", "c-coa")
	seedAgentForStatus(t, h, "ag-coa", wsID, "crew-coa", "RUNNING", false)
	seedAgentForStatus(t, h, "ag-lead-coa", wsID, "crew-coa", "IDLE", false)
	seedMissionForAgent(t, h, "m-coa", wsID, "crew-coa", "ag-lead-coa")

	now := time.Now().UTC().Add(-time.Hour)
	nowStr := now.Format(time.RFC3339)
	// Insert directly with NULL tokens_used and a non-NULL token_count
	// (the pre-v26 shape). seedTaskForLoad always sets tokens_used, so
	// we need the bare INSERT here.
	if _, err := h.db.Exec(`INSERT INTO mission_tasks
		(id, mission_id, assigned_agent_id, title, status, task_order, depends_on,
		 tokens_used, token_count, token_budget, completed_at, created_at, updated_at)
		VALUES ('t-legacy', 'm-coa', 'ag-coa', 'legacy', 'COMPLETED', 0, '[]',
		        NULL, 4242, 5000, ?, datetime('now'), datetime('now'))`, nowStr); err != nil {
		t.Fatalf("seed legacy task: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/v1/agent-load", nil)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Load(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var got []struct {
		AgentID         string `json:"agent_id"`
		TokensUsedToday int    `json:"tokens_used_today"`
	}
	json.Unmarshal(rr.Body.Bytes(), &got)
	var found bool
	for _, e := range got {
		if e.AgentID == "ag-coa" {
			found = true
			if e.TokensUsedToday != 4242 {
				t.Errorf("ag-coa TokensUsedToday = %d, want 4242 (COALESCE fallback to token_count)", e.TokensUsedToday)
			}
		}
	}
	if !found {
		t.Error("ag-coa missing from result")
	}
}
