package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// covProjHandler constructs a ProjectHandler wired to a fresh test DB plus a
// seeded user + workspace, returning everything callers need.
func covProjHandler(t *testing.T) (*ProjectHandler, string, string) {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewProjectHandler(db, nil, newTestLogger())
	return h, userID, wsID
}

// covProjLinkIssueToProject inserts an issue mission already linked to a
// project, with the supplied status, so List/Get/Stats counting paths run.
func covProjLinkIssueToProject(t *testing.T, h *ProjectHandler, wsID, crewID, projectID, identifier, status string) string {
	t.Helper()
	id := generateCUID()
	_, err := h.db.ExecContext(context.Background(), `
		INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, project_id, trace_id, title,
		    status, number, identifier, priority, sort_order, mission_type,
		    created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, 'Issue', ?, 1, ?, 'medium', 0, 'issue',
		    datetime('now'), datetime('now'))`,
		id, wsID, crewID, covProjLeadID(t, h, wsID, crewID), projectID, "trace-"+id, status, identifier)
	if err != nil {
		t.Fatalf("link issue: %v", err)
	}
	return id
}

// covProjCrew seeds a crew and returns its ID.
func covProjCrew(t *testing.T, h *ProjectHandler, wsID string) string {
	t.Helper()
	crewID := generateCUID()
	_, err := h.db.ExecContext(context.Background(),
		`INSERT INTO crews (id, workspace_id, name, slug, issue_prefix) VALUES (?, ?, 'Eng', 'eng', 'ENG')`,
		crewID, wsID)
	if err != nil {
		t.Fatalf("seed crew: %v", err)
	}
	return crewID
}

// covProjLeadID lazily seeds (once per crew) a LEAD agent and returns its ID,
// satisfying the missions.lead_agent_id NOT NULL constraint.
func covProjLeadID(t *testing.T, h *ProjectHandler, wsID, crewID string) string {
	t.Helper()
	leadID := "covproj-lead-" + crewID
	var exists int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM agents WHERE id = ?`, leadID).Scan(&exists); err != nil {
		t.Fatalf("scan agent count: %v", err)
	}
	if exists == 0 {
		if _, err := h.db.ExecContext(context.Background(),
			`INSERT INTO agents (id, workspace_id, crew_id, name, slug, agent_role, status, cli_adapter, temperature, timeout_seconds, tool_profile, memory_enabled)
			 VALUES (?, ?, ?, 'Lead', 'lead', 'LEAD', 'IDLE', 'CLAUDE_CODE', 0.7, 1800, 'CODING', 0)`,
			leadID, wsID, crewID); err != nil {
			t.Fatalf("seed lead: %v", err)
		}
	}
	return leadID
}

// ── List ─────────────────────────────────────────────────────────────────

func TestCovProjList_EmptyReturnsArray(t *testing.T) {
	h, userID, wsID := covProjHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects", nil)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rec := httptest.NewRecorder()
	h.List(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatalf("body = %q, want []", rec.Body.String())
	}
}

func TestCovProjList_HappyWithProgressAndFilters(t *testing.T) {
	h, userID, wsID := covProjHandler(t)
	crewID := covProjCrew(t, h, wsID)
	p1 := seedProject(t, h.db, wsID, "Alpha")
	seedProject(t, h.db, wsID, "Beta")
	// Two issues on p1, one done → progress 50%.
	covProjLinkIssueToProject(t, h, wsID, crewID, p1, "ENG-1", "DONE")
	covProjLinkIssueToProject(t, h, wsID, crewID, p1, "ENG-2", "BACKLOG")

	// status filter + sort by created_at exercises those branches.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects?status=planned&sort=created_at", nil)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rec := httptest.NewRecorder()
	h.List(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got []projectResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	for _, p := range got {
		if p.ID == p1 {
			if p.IssueCount != 2 || p.DoneCount != 1 || p.Progress != 50 {
				t.Fatalf("p1 stats = issue %d done %d progress %d", p.IssueCount, p.DoneCount, p.Progress)
			}
		}
	}
}

func TestCovProjList_SortUpdatedAt(t *testing.T) {
	h, userID, wsID := covProjHandler(t)
	seedProject(t, h.db, wsID, "Gamma")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects?sort=updated_at", nil)
	req = withWorkspaceUser(req, userID, wsID, "VIEWER")
	rec := httptest.NewRecorder()
	h.List(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

// ── Create ───────────────────────────────────────────────────────────────

func TestCovProjCreate_Forbidden(t *testing.T) {
	h, userID, wsID := covProjHandler(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects", jsonBody(map[string]any{"name": "X"}))
	req = withWorkspaceUser(req, userID, wsID, "VIEWER")
	rec := httptest.NewRecorder()
	h.Create(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

func TestCovProjCreate_InvalidJSON(t *testing.T) {
	h, userID, wsID := covProjHandler(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects", strings.NewReader("{not json"))
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rec := httptest.NewRecorder()
	h.Create(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestCovProjCreate_MissingName(t *testing.T) {
	h, userID, wsID := covProjHandler(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects", jsonBody(map[string]any{"name": ""}))
	req = withWorkspaceUser(req, userID, wsID, "ADMIN")
	rec := httptest.NewRecorder()
	h.Create(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestCovProjCreate_HappyDefaultsAndDBState(t *testing.T) {
	h, userID, wsID := covProjHandler(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects", jsonBody(map[string]any{"name": "My Project"}))
	req = withWorkspaceUser(req, userID, wsID, "MANAGER")
	rec := httptest.NewRecorder()
	h.Create(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201", rec.Code)
	}
	var resp projectResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Color != "blue" || resp.Status != "backlog" || resp.Priority != "none" || resp.Health != "on_track" {
		t.Fatalf("defaults wrong: %+v", resp)
	}
	if resp.Slug == "" || resp.ID == "" {
		t.Fatalf("missing slug/id: %+v", resp)
	}
	// Assert DB row exists.
	var name, color, status string
	if err := h.db.QueryRow(`SELECT name, color, status FROM projects WHERE id = ?`, resp.ID).
		Scan(&name, &color, &status); err != nil {
		t.Fatalf("db read: %v", err)
	}
	if name != "My Project" || color != "blue" || status != "backlog" {
		t.Fatalf("db state wrong: %s %s %s", name, color, status)
	}
}

func TestCovProjCreate_ExplicitFields(t *testing.T) {
	h, userID, wsID := covProjHandler(t)
	desc := "a desc"
	req := httptest.NewRequest(http.MethodPost, "/api/v1/projects", jsonBody(map[string]any{
		"name":     "Custom",
		"color":    "red",
		"status":   "in_progress",
		"priority": "high",
	}))
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	_ = desc
	rec := httptest.NewRecorder()
	h.Create(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201", rec.Code)
	}
	var resp projectResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.Color != "red" || resp.Status != "in_progress" || resp.Priority != "high" {
		t.Fatalf("explicit fields not honored: %+v", resp)
	}
}

// ── Get ──────────────────────────────────────────────────────────────────

func TestCovProjGet_NotFound(t *testing.T) {
	h, userID, wsID := covProjHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects/nope", nil)
	req.SetPathValue("projectId", "nope")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rec := httptest.NewRecorder()
	h.Get(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestCovProjGet_CrossTenantMasked(t *testing.T) {
	h, userID, wsID := covProjHandler(t)
	// Project in a *different* workspace must read as not-found.
	otherWS := "other-ws"
	if _, err := h.db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'O', 'o')`, otherWS); err != nil {
		t.Fatalf("insert ws: %v", err)
	}
	pid := seedProject(t, h.db, otherWS, "Hidden")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects/"+pid, nil)
	req.SetPathValue("projectId", pid)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rec := httptest.NewRecorder()
	h.Get(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (cross-tenant masked)", rec.Code)
	}
}

func TestCovProjGet_HappyWithProgress(t *testing.T) {
	h, userID, wsID := covProjHandler(t)
	crewID := covProjCrew(t, h, wsID)
	pid := seedProject(t, h.db, wsID, "Visible")
	covProjLinkIssueToProject(t, h, wsID, crewID, pid, "ENG-10", "DONE")
	covProjLinkIssueToProject(t, h, wsID, crewID, pid, "ENG-11", "DONE")
	covProjLinkIssueToProject(t, h, wsID, crewID, pid, "ENG-12", "BACKLOG")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects/"+pid, nil)
	req.SetPathValue("projectId", pid)
	req = withWorkspaceUser(req, userID, wsID, "VIEWER")
	rec := httptest.NewRecorder()
	h.Get(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var p projectResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if p.IssueCount != 3 || p.DoneCount != 2 || p.Progress != 66 {
		t.Fatalf("stats = issue %d done %d progress %d", p.IssueCount, p.DoneCount, p.Progress)
	}
}

// ── Update ───────────────────────────────────────────────────────────────

func TestCovProjUpdate_Forbidden(t *testing.T) {
	h, userID, wsID := covProjHandler(t)
	pid := seedProject(t, h.db, wsID, "P")
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/projects/"+pid, jsonBody(map[string]any{"name": "X"}))
	req.SetPathValue("projectId", pid)
	req = withWorkspaceUser(req, userID, wsID, "VIEWER")
	rec := httptest.NewRecorder()
	h.Update(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

func TestCovProjUpdate_NotFound(t *testing.T) {
	h, userID, wsID := covProjHandler(t)
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/projects/missing", jsonBody(map[string]any{"name": "X"}))
	req.SetPathValue("projectId", "missing")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rec := httptest.NewRecorder()
	h.Update(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestCovProjUpdate_InvalidJSON(t *testing.T) {
	h, userID, wsID := covProjHandler(t)
	pid := seedProject(t, h.db, wsID, "P")
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/projects/"+pid, strings.NewReader("{bad"))
	req.SetPathValue("projectId", pid)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rec := httptest.NewRecorder()
	h.Update(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestCovProjUpdate_NoFields(t *testing.T) {
	h, userID, wsID := covProjHandler(t)
	pid := seedProject(t, h.db, wsID, "P")
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/projects/"+pid, jsonBody(map[string]any{}))
	req.SetPathValue("projectId", pid)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rec := httptest.NewRecorder()
	h.Update(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (no fields)", rec.Code)
	}
}

func TestCovProjUpdate_HappyAllFieldsAndDBState(t *testing.T) {
	h, userID, wsID := covProjHandler(t)
	pid := seedProject(t, h.db, wsID, "Old Name")
	body := map[string]any{
		"name":        "New Name",
		"description": "d",
		"icon":        "rocket",
		"color":       "green",
		"status":      "in_progress",
		"priority":    "urgent",
		"health":      "at_risk",
		"lead_type":   "user",
		"lead_id":     userID,
		"start_date":  "2026-01-01",
		"target_date": "2026-12-31",
	}
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/projects/"+pid, jsonBody(body))
	req.SetPathValue("projectId", pid)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rec := httptest.NewRecorder()
	h.Update(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var p projectResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if p.Name != "New Name" || p.Color != "green" || p.Status != "in_progress" || p.Health != "at_risk" {
		t.Fatalf("response fields not updated: %+v", p)
	}
	if p.LeadName == nil || *p.LeadName != "Test User" {
		t.Fatalf("lead_name not resolved: %+v", p.LeadName)
	}
	// DB state: slug regenerated from new name.
	var name, slug, priority string
	if err := h.db.QueryRow(`SELECT name, slug, priority FROM projects WHERE id = ?`, pid).
		Scan(&name, &slug, &priority); err != nil {
		t.Fatalf("db read: %v", err)
	}
	if name != "New Name" || slug != slugify("New Name") || priority != "urgent" {
		t.Fatalf("db state = %s / %s / %s", name, slug, priority)
	}
}

// ── Delete ───────────────────────────────────────────────────────────────

func TestCovProjDelete_Forbidden(t *testing.T) {
	h, userID, wsID := covProjHandler(t)
	pid := seedProject(t, h.db, wsID, "P")
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/projects/"+pid, nil)
	req.SetPathValue("projectId", pid)
	// MANAGER can create but NOT manage/delete.
	req = withWorkspaceUser(req, userID, wsID, "MANAGER")
	rec := httptest.NewRecorder()
	h.Delete(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

func TestCovProjDelete_NotFound(t *testing.T) {
	h, userID, wsID := covProjHandler(t)
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/projects/ghost", nil)
	req.SetPathValue("projectId", "ghost")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rec := httptest.NewRecorder()
	h.Delete(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestCovProjDelete_HappyUnlinksAndRemoves(t *testing.T) {
	h, userID, wsID := covProjHandler(t)
	crewID := covProjCrew(t, h, wsID)
	pid := seedProject(t, h.db, wsID, "Doomed")
	issueID := covProjLinkIssueToProject(t, h, wsID, crewID, pid, "ENG-99", "BACKLOG")

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/projects/"+pid, nil)
	req.SetPathValue("projectId", pid)
	req = withWorkspaceUser(req, userID, wsID, "ADMIN")
	rec := httptest.NewRecorder()
	h.Delete(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%s", rec.Code, rec.Body.String())
	}
	// Project gone.
	var cnt int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM projects WHERE id = ?`, pid).Scan(&cnt); err != nil {
		t.Fatalf("count projects: %v", err)
	}
	if cnt != 0 {
		t.Fatalf("project still present: %d", cnt)
	}
	// Issue unlinked, not deleted.
	var projectID *string
	if err := h.db.QueryRow(`SELECT project_id FROM missions WHERE id = ?`, issueID).Scan(&projectID); err != nil {
		t.Fatalf("read mission: %v", err)
	}
	if projectID != nil {
		t.Fatalf("mission still linked: %v", *projectID)
	}
}

// ── Stats ────────────────────────────────────────────────────────────────

func TestCovProjStats_NotFound(t *testing.T) {
	h, userID, wsID := covProjHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects/nope/stats", nil)
	req.SetPathValue("projectId", "nope")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rec := httptest.NewRecorder()
	h.Stats(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestCovProjStats_HappyAllBreakdowns(t *testing.T) {
	h, userID, wsID := covProjHandler(t)
	crewID := covProjCrew(t, h, wsID)
	pid := seedProject(t, h.db, wsID, "Stats")

	// An assignee agent so the by_assignee join resolves a name.
	agentID := generateCUID()
	if _, err := h.db.Exec(
		`INSERT INTO agents (id, workspace_id, crew_id, name, slug, agent_role, status, cli_adapter, temperature, timeout_seconds, tool_profile, memory_enabled)
		 VALUES (?, ?, ?, 'Worker', 'worker', 'AGENT', 'IDLE', 'CLAUDE_CODE', 0.7, 1800, 'CODING', 0)`,
		agentID, wsID, crewID); err != nil {
		t.Fatalf("insert agent: %v", err)
	}

	// Two issues, one DONE, both assigned + crewed.
	i1 := generateCUID()
	i2 := generateCUID()
	for n, row := range []struct{ id, status string }{{i1, "DONE"}, {i2, "BACKLOG"}} {
		if _, err := h.db.Exec(`
			INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, project_id, assignee_id, trace_id, title,
			    status, number, identifier, priority, sort_order, mission_type, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, 'I', ?, ?, ?, 'medium', 0, 'issue', datetime('now'), datetime('now'))`,
			row.id, wsID, crewID, covProjLeadID(t, h, wsID, crewID), pid, agentID, "trace-"+row.id, row.status, n+1, "ENG-S"+row.id); err != nil {
			t.Fatalf("insert mission: %v", err)
		}
	}

	// A label on i1 for the by_label breakdown.
	labelID := seedLabel(t, h.db, wsID, "bug")
	if _, err := h.db.Exec(`INSERT INTO mission_labels (mission_id, label_id) VALUES (?, ?)`, i1, labelID); err != nil {
		t.Fatalf("link label: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects/"+pid+"/stats", nil)
	req.SetPathValue("projectId", pid)
	req = withWorkspaceUser(req, userID, wsID, "VIEWER")
	rec := httptest.NewRecorder()
	h.Stats(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var resp struct {
		TotalIssues     int            `json:"total_issues"`
		CompletedIssues int            `json:"completed_issues"`
		ByStatus        map[string]int `json:"by_status"`
		ByAssignee      []struct {
			AgentName string `json:"agent_name"`
			Total     int    `json:"total"`
		} `json:"by_assignee"`
		ByLabel []struct {
			LabelName string `json:"label_name"`
			Count     int    `json:"count"`
		} `json:"by_label"`
		Crews []string `json:"crews"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.TotalIssues != 2 || resp.CompletedIssues != 1 {
		t.Fatalf("totals = %d / %d", resp.TotalIssues, resp.CompletedIssues)
	}
	if resp.ByStatus["DONE"] != 1 || resp.ByStatus["BACKLOG"] != 1 {
		t.Fatalf("by_status = %+v", resp.ByStatus)
	}
	if len(resp.ByAssignee) != 1 || resp.ByAssignee[0].AgentName != "Worker" || resp.ByAssignee[0].Total != 2 {
		t.Fatalf("by_assignee = %+v", resp.ByAssignee)
	}
	if len(resp.ByLabel) != 1 || resp.ByLabel[0].LabelName != "bug" || resp.ByLabel[0].Count != 1 {
		t.Fatalf("by_label = %+v", resp.ByLabel)
	}
	if len(resp.Crews) != 1 {
		t.Fatalf("crews = %+v", resp.Crews)
	}
}

func TestCovProjStats_EmptyProject(t *testing.T) {
	h, userID, wsID := covProjHandler(t)
	pid := seedProject(t, h.db, wsID, "Empty")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/projects/"+pid+"/stats", nil)
	req.SetPathValue("projectId", pid)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rec := httptest.NewRecorder()
	h.Stats(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var resp struct {
		TotalIssues int `json:"total_issues"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.TotalIssues != 0 {
		t.Fatalf("total = %d, want 0", resp.TotalIssues)
	}
}
