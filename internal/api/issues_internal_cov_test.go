package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// covII2NewIssueHandler builds an InternalIssueHandler backed by a freshly
// seeded DB (workspace + crew "ENG" + lead/worker agents) and returns the
// handler plus the IDs the tests need. Distinct from the existing
// newInternalIssueHandler so the two never collide.
func covII2NewIssueHandler(t *testing.T) (*InternalIssueHandler, *sql.DB, string, string, string) {
	t.Helper()
	db := setupTestDB(t)
	userID, wsID, crewID, leadID, _ := seedIssueFixtures(t, db)
	_ = userID
	return NewInternalIssueHandler(db, nil, newTestLogger()), db, wsID, crewID, leadID
}

// covII2CommentCount returns how many mission_comments rows exist for a mission.
func covII2CommentCount(t *testing.T, db *sql.DB, missionID string) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM mission_comments WHERE mission_id = ?`, missionID).Scan(&n); err != nil {
		t.Fatalf("count comments: %v", err)
	}
	return n
}

// ============================================================================
// InternalIssueHandler.List — filter combinations, default mission_type, 500
// ============================================================================

// TestCovII2List_DefaultMissionTypeFilter exercises the else-branch where no
// mission_type query param is supplied: the handler appends a COALESCE filter
// pinning results to mission_type='issue'. A non-issue mission must be excluded.
func TestCovII2List_DefaultMissionTypeFilter(t *testing.T) {
	h, db, wsID, crewID, leadID := covII2NewIssueHandler(t)
	seedIssue(t, db, wsID, crewID, leadID, "ENG-1", "BACKLOG")
	// A non-issue mission in the same workspace must NOT appear.
	if _, err := db.ExecContext(context.Background(), `
		INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title,
		    status, number, identifier, mission_type, created_at, updated_at)
		VALUES ('m-orch', ?, ?, ?, 'tr-orch', 'Orchestration', 'PLANNING', 2, 'ENG-2', 'mission',
		    datetime('now'), datetime('now'))`, wsID, crewID, leadID); err != nil {
		t.Fatalf("seed orchestration mission: %v", err)
	}

	req := httptest.NewRequest("GET", "/?workspace_id="+wsID, nil)
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var got []issueResponse
	json.Unmarshal(rr.Body.Bytes(), &got)
	if len(got) != 1 {
		t.Fatalf("got %d issues, want 1 (orchestration mission must be filtered out)", len(got))
	}
	if got[0].Identifier == nil || *got[0].Identifier != "ENG-1" {
		t.Errorf("identifier = %v want ENG-1", got[0].Identifier)
	}
}

// TestCovII2List_AssigneeFilter exercises the assignee_id filter branch.
func TestCovII2List_AssigneeFilter(t *testing.T) {
	h, db, wsID, crewID, leadID := covII2NewIssueHandler(t)
	id := seedIssue(t, db, wsID, crewID, leadID, "ENG-1", "BACKLOG")
	if _, err := db.ExecContext(context.Background(),
		`UPDATE missions SET assignee_id = 'agent-worker', assignee_type = 'agent' WHERE id = ?`, id); err != nil {
		t.Fatalf("set assignee: %v", err)
	}

	// Matching assignee returns the issue.
	req := httptest.NewRequest("GET", "/?workspace_id="+wsID+"&assignee_id=agent-worker", nil)
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	var got []issueResponse
	json.Unmarshal(rr.Body.Bytes(), &got)
	if len(got) != 1 {
		t.Errorf("matching assignee: got %d want 1", len(got))
	}

	// Non-matching assignee returns empty (and the result==nil → [] path).
	req2 := httptest.NewRequest("GET", "/?workspace_id="+wsID+"&assignee_id=nobody", nil)
	rr2 := httptest.NewRecorder()
	h.List(rr2, req2)
	if rr2.Code != http.StatusOK {
		t.Fatalf("status = %d", rr2.Code)
	}
	if body := rr2.Body.String(); body != "[]" && body != "[]\n" {
		t.Errorf("empty result body = %q, want []", body)
	}
}

// TestCovII2List_DBError closes the DB before the query to drive the
// QueryContext error → 500 branch.
func TestCovII2List_DBError(t *testing.T) {
	h, db, wsID, _, _ := covII2NewIssueHandler(t)
	db.Close()
	req := httptest.NewRequest("GET", "/?workspace_id="+wsID, nil)
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rr.Code)
	}
}

// ============================================================================
// InternalIssueHandler.Get — labels + comment count + 500
// ============================================================================

// TestCovII2Get_WithLabelsAndComments asserts that the Get happy path loads
// labels and the comment count alongside the issue body.
func TestCovII2Get_WithLabelsAndComments(t *testing.T) {
	h, db, wsID, crewID, leadID := covII2NewIssueHandler(t)
	issueID := seedIssue(t, db, wsID, crewID, leadID, "ENG-1", "BACKLOG")

	labelID := seedLabel(t, db, wsID, "bug")
	if _, err := db.ExecContext(context.Background(),
		`INSERT INTO mission_labels (mission_id, label_id) VALUES (?, ?)`, issueID, labelID); err != nil {
		t.Fatalf("link label: %v", err)
	}
	if _, err := db.ExecContext(context.Background(), `
		INSERT INTO mission_comments (id, mission_id, author_type, author_id, body, created_at, updated_at)
		VALUES ('cmt-1', ?, 'agent', 'agent-worker', 'first', datetime('now'), datetime('now'))`, issueID); err != nil {
		t.Fatalf("seed comment: %v", err)
	}

	req := httptest.NewRequest("GET", "/?workspace_id="+wsID, nil)
	req.SetPathValue("identifier", "ENG-1")
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var got issueResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got.Labels) != 1 || got.Labels[0].Name != "bug" {
		t.Errorf("labels = %+v, want one label 'bug'", got.Labels)
	}
	if got.CommentCount != 1 {
		t.Errorf("comment_count = %d, want 1", got.CommentCount)
	}
}

// TestCovII2Get_DBError closes the DB to drive the main QueryRow error → 500.
func TestCovII2Get_DBError(t *testing.T) {
	h, db, wsID, _, _ := covII2NewIssueHandler(t)
	db.Close()
	req := httptest.NewRequest("GET", "/?workspace_id="+wsID, nil)
	req.SetPathValue("identifier", "ENG-1")
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rr.Code)
	}
}

// ============================================================================
// InternalIssueHandler.Create — labels, default priority, no-LEAD, 500
// ============================================================================

// TestCovII2Create_WithLabelsAndDefaultPriority covers the labels-insert loop
// and the empty-priority → "none" default branch, then asserts persisted state.
func TestCovII2Create_WithLabelsAndDefaultPriority(t *testing.T) {
	h, db, wsID, crewID, _ := covII2NewIssueHandler(t)
	labelID := seedLabel(t, db, wsID, "feature")

	body := bytes.NewBufferString(`{"workspace_id":"` + wsID + `","crew_id":"` + crewID +
		`","title":"Has labels","labels":["` + labelID + `"]}`)
	req := httptest.NewRequest("POST", "/", body)
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var resp map[string]string
	json.Unmarshal(rr.Body.Bytes(), &resp)
	newID := resp["id"]
	if newID == "" {
		t.Fatal("missing id in response")
	}

	// Default priority applied.
	var priority string
	if err := db.QueryRowContext(context.Background(),
		`SELECT priority FROM missions WHERE id = ?`, newID).Scan(&priority); err != nil {
		t.Fatalf("read priority: %v", err)
	}
	if priority != "none" {
		t.Errorf("priority = %q, want none (default)", priority)
	}

	// Label linked.
	var linked int
	if err := db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM mission_labels WHERE mission_id = ? AND label_id = ?`, newID, labelID).Scan(&linked); err != nil {
		t.Fatalf("count labels: %v", err)
	}
	if linked != 1 {
		t.Errorf("label links = %d, want 1", linked)
	}
}

// TestCovII2Create_NoLeadAgent drives the "Crew has no LEAD agent" 400 by
// pointing at a crew that exists but has no LEAD.
func TestCovII2Create_NoLeadAgent(t *testing.T) {
	h, db, wsID, _, _ := covII2NewIssueHandler(t)
	// Crew with no agents at all.
	if _, err := db.ExecContext(context.Background(),
		`INSERT INTO crews (id, workspace_id, name, slug, issue_prefix) VALUES ('crew-noload', ?, 'NoLead', 'nl', 'NL')`, wsID); err != nil {
		t.Fatalf("seed crew: %v", err)
	}
	body := bytes.NewBufferString(`{"workspace_id":"` + wsID + `","crew_id":"crew-noload","title":"x"}`)
	req := httptest.NewRequest("POST", "/", body)
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (no LEAD)", rr.Code)
	}
}

// TestCovII2Create_EmptyPrefixDerivedFromSlug covers the issue_prefix-empty
// branch where the identifier prefix is derived from the (uppercased, ≤3 char)
// crew slug.
func TestCovII2Create_EmptyPrefixDerivedFromSlug(t *testing.T) {
	h, db, wsID, _, _ := covII2NewIssueHandler(t)
	// Crew with empty issue_prefix and a long slug → prefix = "PLA".
	if _, err := db.ExecContext(context.Background(),
		`INSERT INTO crews (id, workspace_id, name, slug, issue_prefix) VALUES ('crew-pf', ?, 'Platform', 'platform', '')`, wsID); err != nil {
		t.Fatalf("seed crew: %v", err)
	}
	if _, err := db.ExecContext(context.Background(),
		`INSERT INTO agents (id, workspace_id, crew_id, name, slug, agent_role, status, cli_adapter, temperature, timeout_seconds, tool_profile, memory_enabled)
		 VALUES ('lead-pf', ?, 'crew-pf', 'L', 'lpf', 'LEAD', 'IDLE', 'CLAUDE_CODE', 0.7, 1800, 'CODING', 0)`, wsID); err != nil {
		t.Fatalf("seed lead: %v", err)
	}

	body := bytes.NewBufferString(`{"workspace_id":"` + wsID + `","crew_id":"crew-pf","title":"derived"}`)
	req := httptest.NewRequest("POST", "/", body)
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var resp map[string]string
	json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp["identifier"] != "PLA-1" {
		t.Errorf("identifier = %q, want PLA-1 (slug-derived prefix)", resp["identifier"])
	}
}

// TestCovII2Create_DBError closes the DB to drive the BeginTx error → 500.
func TestCovII2Create_DBError(t *testing.T) {
	h, db, wsID, crewID, _ := covII2NewIssueHandler(t)
	db.Close()
	body := bytes.NewBufferString(`{"workspace_id":"` + wsID + `","crew_id":"` + crewID + `","title":"x"}`)
	req := httptest.NewRequest("POST", "/", body)
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rr.Code)
	}
}

// ============================================================================
// InternalIssueHandler.UpdateStatus — priority-only, comment, no-op, 500
// ============================================================================

// TestCovII2UpdateStatus_PriorityOnly sets only the priority (no status
// change) and asserts the update persisted.
func TestCovII2UpdateStatus_PriorityOnly(t *testing.T) {
	h, db, wsID, crewID, leadID := covII2NewIssueHandler(t)
	issueID := seedIssue(t, db, wsID, crewID, leadID, "ENG-1", "BACKLOG")

	body := bytes.NewBufferString(`{"workspace_id":"` + wsID + `","priority":"urgent"}`)
	req := httptest.NewRequest("PATCH", "/", body)
	req.SetPathValue("identifier", "ENG-1")
	rr := httptest.NewRecorder()
	h.UpdateStatus(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var priority, status string
	if err := db.QueryRowContext(context.Background(),
		`SELECT priority, status FROM missions WHERE id = ?`, issueID).Scan(&priority, &status); err != nil {
		t.Fatalf("read: %v", err)
	}
	if priority != "urgent" {
		t.Errorf("priority = %q, want urgent", priority)
	}
	if status != "BACKLOG" {
		t.Errorf("status = %q, want unchanged BACKLOG", status)
	}
}

// TestCovII2UpdateStatus_NoOp sends a status equal to the current one and no
// priority — the update builder stays empty, so no UPDATE runs but the call
// still succeeds (200).
func TestCovII2UpdateStatus_NoOp(t *testing.T) {
	h, db, wsID, crewID, leadID := covII2NewIssueHandler(t)
	seedIssue(t, db, wsID, crewID, leadID, "ENG-1", "BACKLOG")

	body := bytes.NewBufferString(`{"workspace_id":"` + wsID + `","status":"BACKLOG"}`)
	req := httptest.NewRequest("PATCH", "/", body)
	req.SetPathValue("identifier", "ENG-1")
	rr := httptest.NewRecorder()
	h.UpdateStatus(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
}

// TestCovII2UpdateStatus_DoneSetsCompletedAt transitions to DONE and asserts
// completed_at is populated (the DONE/CANCELLED branch).
func TestCovII2UpdateStatus_DoneSetsCompletedAt(t *testing.T) {
	h, db, wsID, crewID, leadID := covII2NewIssueHandler(t)
	issueID := seedIssue(t, db, wsID, crewID, leadID, "ENG-1", "IN_PROGRESS")

	body := bytes.NewBufferString(`{"workspace_id":"` + wsID + `","status":"DONE"}`)
	req := httptest.NewRequest("PATCH", "/", body)
	req.SetPathValue("identifier", "ENG-1")
	rr := httptest.NewRecorder()
	h.UpdateStatus(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var completedAt sql.NullString
	if err := db.QueryRowContext(context.Background(),
		`SELECT completed_at FROM missions WHERE id = ?`, issueID).Scan(&completedAt); err != nil {
		t.Fatalf("read completed_at: %v", err)
	}
	if !completedAt.Valid || completedAt.String == "" {
		t.Error("completed_at should be set after DONE transition")
	}
}

// TestCovII2UpdateStatus_WithComment_DefaultAuthor adds a comment with no
// agent_id so author_id falls back to "system".
func TestCovII2UpdateStatus_WithComment_DefaultAuthor(t *testing.T) {
	h, db, wsID, crewID, leadID := covII2NewIssueHandler(t)
	issueID := seedIssue(t, db, wsID, crewID, leadID, "ENG-1", "BACKLOG")

	body := bytes.NewBufferString(`{"workspace_id":"` + wsID + `","status":"TODO","comment":"moving along"}`)
	req := httptest.NewRequest("PATCH", "/", body)
	req.SetPathValue("identifier", "ENG-1")
	rr := httptest.NewRecorder()
	h.UpdateStatus(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	if covII2CommentCount(t, db, issueID) != 1 {
		t.Errorf("expected 1 comment after update-with-comment")
	}
	var authorID string
	if err := db.QueryRowContext(context.Background(),
		`SELECT author_id FROM mission_comments WHERE mission_id = ?`, issueID).Scan(&authorID); err != nil {
		t.Fatalf("read author: %v", err)
	}
	if authorID != "system" {
		t.Errorf("author_id = %q, want system (default when agent_id empty)", authorID)
	}
}

// TestCovII2UpdateStatus_DBError closes the DB before the find-issue query.
func TestCovII2UpdateStatus_DBError(t *testing.T) {
	h, db, wsID, _, _ := covII2NewIssueHandler(t)
	db.Close()
	body := bytes.NewBufferString(`{"workspace_id":"` + wsID + `","status":"TODO"}`)
	req := httptest.NewRequest("PATCH", "/", body)
	req.SetPathValue("identifier", "ENG-1")
	rr := httptest.NewRecorder()
	h.UpdateStatus(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rr.Code)
	}
}

// ============================================================================
// InternalIssueHandler.CreateComment — happy persistence, default author, 500
// ============================================================================

// TestCovII2CreateComment_DefaultAuthor adds a comment without agent_id and
// asserts the persisted row + default author "system" and the echoed body.
func TestCovII2CreateComment_DefaultAuthor(t *testing.T) {
	h, db, wsID, crewID, leadID := covII2NewIssueHandler(t)
	issueID := seedIssue(t, db, wsID, crewID, leadID, "ENG-1", "BACKLOG")

	body := bytes.NewBufferString(`{"workspace_id":"` + wsID + `","body":"a note"}`)
	req := httptest.NewRequest("POST", "/", body)
	req.SetPathValue("identifier", "ENG-1")
	rr := httptest.NewRecorder()
	h.CreateComment(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var resp commentResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.AuthorID != "system" {
		t.Errorf("author_id = %q, want system", resp.AuthorID)
	}
	if resp.Body != "a note" {
		t.Errorf("body = %q, want 'a note'", resp.Body)
	}
	if covII2CommentCount(t, db, issueID) != 1 {
		t.Errorf("expected 1 persisted comment")
	}
}

// TestCovII2CreateComment_DBError closes the DB before the find-issue query.
func TestCovII2CreateComment_DBError(t *testing.T) {
	h, db, wsID, _, _ := covII2NewIssueHandler(t)
	db.Close()
	body := bytes.NewBufferString(`{"workspace_id":"` + wsID + `","body":"x"}`)
	req := httptest.NewRequest("POST", "/", body)
	req.SetPathValue("identifier", "ENG-1")
	rr := httptest.NewRecorder()
	h.CreateComment(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rr.Code)
	}
}

// ============================================================================
// missions_internal.go — remaining uncovered branches
// ============================================================================

// covII2NewMissionHandler builds an InternalMissionHandler with a seeded crew
// + LEAD agent and returns the handler, db and useful IDs.
func covII2NewMissionHandler(t *testing.T) (*InternalMissionHandler, *sql.DB, string, string, string) {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewID := seedCrewRow(t, db, "cr-mi2", wsID, "Crew", "crewmi2")
	leadID := seedAgentRow(t, db, "lead-mi2", wsID, crewID, "Lead", "leadmi2", "LEAD")
	return NewInternalMissionHandler(db, nil, nil, newTestLogger()), db, wsID, crewID, leadID
}

// TestCovII2Mission_Create_DBError closes the DB before BeginTx → 500.
func TestCovII2Mission_Create_DBError(t *testing.T) {
	h, db, wsID, crewID, leadID := covII2NewMissionHandler(t)
	db.Close()
	body := `{"title":"M","lead_agent_id":"` + leadID + `","crew_id":"` + crewID + `","workspace_id":"` + wsID + `"}`
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(body))
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rr.Code)
	}
}

// TestCovII2Mission_Create_FullFields covers description/plan/workflow_template
// + a task carrying assigned_agent_id and max_iterations, then asserts the
// mission and task rows persisted.
func TestCovII2Mission_Create_FullFields(t *testing.T) {
	h, db, wsID, crewID, leadID := covII2NewMissionHandler(t)
	body := `{
		"title":"Full",
		"description":"desc",
		"lead_agent_id":"` + leadID + `",
		"crew_id":"` + crewID + `",
		"workspace_id":"` + wsID + `",
		"plan":"the plan",
		"workflow_template":"default",
		"tasks":[{"title":"t1","description":"d1","assigned_agent_id":"` + leadID + `","task_order":0,"max_iterations":5}]
	}`
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(body))
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var resp struct {
		ID    string         `json:"id"`
		Tasks map[string]int `json:"tasks"`
	}
	json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.ID == "" {
		t.Fatal("missing mission id")
	}
	var taskCount int
	if err := db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM mission_tasks WHERE mission_id = ?`, resp.ID).Scan(&taskCount); err != nil {
		t.Fatalf("count tasks: %v", err)
	}
	if taskCount != 1 {
		t.Errorf("task count = %d, want 1", taskCount)
	}
}

// TestCovII2Mission_Start_PathFallback omits the {missionId} path value so the
// handler's URL-parsing fallback ("missions/<id>" split) is exercised.
func TestCovII2Mission_Start_PathFallback(t *testing.T) {
	h, db, wsID, crewID, leadID := covII2NewMissionHandler(t)
	if _, err := db.ExecContext(context.Background(),
		`INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, status)
		 VALUES ('mi2-1', ?, ?, ?, 'tr-mi2-1', 'Test', 'PLANNING')`, wsID, crewID, leadID); err != nil {
		t.Fatalf("seed mission: %v", err)
	}
	// No SetPathValue → forces the strings.Split fallback on the URL path.
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/missions/mi2-1/start", nil)
	rr := httptest.NewRecorder()
	h.Start(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var status string
	if err := db.QueryRowContext(context.Background(),
		`SELECT status FROM missions WHERE id = 'mi2-1'`).Scan(&status); err != nil {
		t.Fatalf("read status: %v", err)
	}
	if status != "IN_PROGRESS" {
		t.Errorf("status = %q, want IN_PROGRESS", status)
	}
}

// TestCovII2Mission_Start_DBError closes the DB before the status lookup → 500.
func TestCovII2Mission_Start_DBError(t *testing.T) {
	h, db, _, _, _ := covII2NewMissionHandler(t)
	db.Close()
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.SetPathValue("missionId", "anything")
	rr := httptest.NewRecorder()
	h.Start(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rr.Code)
	}
}

// TestCovII2Mission_Get_PathFallbackAndDBError covers the Get URL-parse
// fallback (happy) and the DB-error 500.
func TestCovII2Mission_Get_PathFallbackAndDBError(t *testing.T) {
	h, db, wsID, crewID, leadID := covII2NewMissionHandler(t)
	if _, err := db.ExecContext(context.Background(),
		`INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, status)
		 VALUES ('mi2-g', ?, ?, ?, 'tr-mi2-g', 'Test', 'PLANNING')`, wsID, crewID, leadID); err != nil {
		t.Fatalf("seed mission: %v", err)
	}

	// Path fallback (no SetPathValue).
	req := httptest.NewRequest(http.MethodGet, "/api/v1/internal/missions/mi2-g", nil)
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("path-fallback status = %d body=%s", rr.Code, rr.Body.String())
	}

	// DB error.
	db.Close()
	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	req2.SetPathValue("missionId", "mi2-g")
	rr2 := httptest.NewRecorder()
	h.Get(rr2, req2)
	if rr2.Code != http.StatusInternalServerError {
		t.Errorf("db-error status = %d, want 500", rr2.Code)
	}
}
