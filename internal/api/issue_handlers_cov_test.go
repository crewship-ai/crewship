package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// covIHNew builds an IssueHandler over a fresh test DB with seeded issue
// fixtures, mirroring newTestIssueHandler but kept local so this file is
// self-contained for the coverage suite.
func covIHNew(t *testing.T) (*IssueHandler, string, string, string, string, string) {
	t.Helper()
	db := setupTestDB(t)
	userID, wsID, crewID, leadID, workerID := seedIssueFixtures(t, db)
	h := NewIssueHandler(db, nil, nil, newTestLogger())
	return h, userID, wsID, crewID, leadID, workerID
}

// covIHReq builds a request with user+workspace context and the given path
// values applied (crewId / identifier), returning the request + recorder.
func covIHReq(method, body, userID, wsID, role string, pv map[string]string) (*http.Request, *httptest.ResponseRecorder) {
	var rdr *bytes.Buffer
	if body != "" {
		rdr = bytes.NewBufferString(body)
	} else {
		rdr = bytes.NewBufferString("")
	}
	req := httptest.NewRequest(method, "/", rdr)
	for k, v := range pv {
		req.SetPathValue(k, v)
	}
	req = withWorkspaceUser(req, userID, wsID, role)
	return req, httptest.NewRecorder()
}

// ── List ───────────────────────────────────────────────────────────────

func TestCovIHListDBError(t *testing.T) {
	h, userID, wsID, _, _, _ := covIHNew(t)
	h.db.Close() // fault inject: first query fails → 500
	req, rr := covIHReq("GET", "", userID, wsID, "OWNER", nil)
	h.List(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rr.Code)
	}
}

func TestCovIHListMissionTypeOverride(t *testing.T) {
	h, userID, wsID, crewID, leadID, _ := covIHNew(t)
	seedIssue(t, h.db, wsID, crewID, leadID, "ENG-1", "BACKLOG")
	// mission_type=mission should match nothing (rows are 'issue')
	req := httptest.NewRequest("GET", "/?mission_type=mission&offset=-1&limit=999", nil)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	var got []issueResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("len = %d, want 0", len(got))
	}
}

// ── Get / GetByIdentifier ─────────────────────────────────────────────

func TestCovIHGetDBError(t *testing.T) {
	h, userID, wsID, crewID, leadID, _ := covIHNew(t)
	seedIssue(t, h.db, wsID, crewID, leadID, "ENG-1", "BACKLOG")
	h.db.Close()
	req, rr := covIHReq("GET", "", userID, wsID, "OWNER", map[string]string{"crewId": crewID, "identifier": "ENG-1"})
	h.Get(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rr.Code)
	}
}

func TestCovIHGetByIdentifierDBError(t *testing.T) {
	h, userID, wsID, crewID, leadID, _ := covIHNew(t)
	seedIssue(t, h.db, wsID, crewID, leadID, "ENG-1", "BACKLOG")
	h.db.Close()
	req, rr := covIHReq("GET", "", userID, wsID, "OWNER", map[string]string{"identifier": "ENG-1"})
	h.GetByIdentifier(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rr.Code)
	}
}

// ── Create ─────────────────────────────────────────────────────────────

func TestCovIHCreateDBError(t *testing.T) {
	h, userID, wsID, crewID, _, _ := covIHNew(t)
	h.db.Close() // BeginTx fails → 500
	req, rr := covIHReq("POST", `{"title":"x"}`, userID, wsID, "OWNER", map[string]string{"crewId": crewID})
	h.Create(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rr.Code)
	}
}

func TestCovIHCreateRoutineInputsWithoutID(t *testing.T) {
	h, userID, wsID, crewID, _, _ := covIHNew(t)
	body := `{"title":"x","routine_inputs":{"a":1}}`
	req, rr := covIHReq("POST", body, userID, wsID, "OWNER", map[string]string{"crewId": crewID})
	h.Create(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (orphan inputs)", rr.Code)
	}
}

func TestCovIHCreateRoutineIDNotFound(t *testing.T) {
	h, userID, wsID, crewID, _, _ := covIHNew(t)
	body := `{"title":"x","routine_id":"no-such-pipeline"}`
	req, rr := covIHReq("POST", body, userID, wsID, "OWNER", map[string]string{"crewId": crewID})
	h.Create(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (routine_id absent)", rr.Code)
	}
}

func TestCovIHCreateParentNotFound(t *testing.T) {
	h, userID, wsID, crewID, _, _ := covIHNew(t)
	body := `{"title":"x","parent_issue_id":"ghost-parent"}`
	req, rr := covIHReq("POST", body, userID, wsID, "OWNER", map[string]string{"crewId": crewID})
	h.Create(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (parent absent)", rr.Code)
	}
}

func TestCovIHCreateWithLabelsHappy(t *testing.T) {
	h, userID, wsID, crewID, _, _ := covIHNew(t)
	lbl := seedLabel(t, h.db, wsID, "bug")
	body := `{"title":"With labels","priority":"high","labels":["` + lbl + `"]}`
	req, rr := covIHReq("POST", body, userID, wsID, "OWNER", map[string]string{"crewId": crewID})
	h.Create(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var resp issueResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var n int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM mission_labels WHERE mission_id=?`, resp.ID).Scan(&n); err != nil {
		t.Fatalf("scan label count: %v", err)
	}
	if n != 1 {
		t.Errorf("label assoc count = %d, want 1", n)
	}
}

// ── Update ─────────────────────────────────────────────────────────────

func TestCovIHUpdateDBError(t *testing.T) {
	h, userID, wsID, crewID, leadID, _ := covIHNew(t)
	seedIssue(t, h.db, wsID, crewID, leadID, "ENG-1", "BACKLOG")
	h.db.Close()
	req, rr := covIHReq("PATCH", `{"title":"x"}`, userID, wsID, "OWNER", map[string]string{"crewId": crewID, "identifier": "ENG-1"})
	h.Update(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rr.Code)
	}
}

func TestCovIHUpdateInvalidJSON(t *testing.T) {
	h, userID, wsID, crewID, _, _ := covIHNew(t)
	req, rr := covIHReq("PATCH", `{bad`, userID, wsID, "OWNER", map[string]string{"crewId": crewID, "identifier": "ENG-1"})
	h.Update(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestCovIHUpdateForbidden(t *testing.T) {
	h, userID, wsID, crewID, _, _ := covIHNew(t)
	req, rr := covIHReq("PATCH", `{"title":"x"}`, userID, wsID, "VIEWER", map[string]string{"crewId": crewID, "identifier": "ENG-1"})
	h.Update(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rr.Code)
	}
}

func TestCovIHUpdateSelfParent(t *testing.T) {
	h, userID, wsID, crewID, leadID, _ := covIHNew(t)
	id := seedIssue(t, h.db, wsID, crewID, leadID, "ENG-1", "BACKLOG")
	body := `{"parent_issue_id":"` + id + `"}`
	req, rr := covIHReq("PATCH", body, userID, wsID, "OWNER", map[string]string{"crewId": crewID, "identifier": "ENG-1"})
	h.Update(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (self-parent)", rr.Code)
	}
}

func TestCovIHUpdateParentNotFound(t *testing.T) {
	h, userID, wsID, crewID, leadID, _ := covIHNew(t)
	seedIssue(t, h.db, wsID, crewID, leadID, "ENG-1", "BACKLOG")
	body := `{"parent_issue_id":"ghost"}`
	req, rr := covIHReq("PATCH", body, userID, wsID, "OWNER", map[string]string{"crewId": crewID, "identifier": "ENG-1"})
	h.Update(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (parent absent)", rr.Code)
	}
}

func TestCovIHUpdateRoutineIDNotFound(t *testing.T) {
	h, userID, wsID, crewID, leadID, _ := covIHNew(t)
	seedIssue(t, h.db, wsID, crewID, leadID, "ENG-1", "BACKLOG")
	body := `{"routine_id":"no-such"}`
	req, rr := covIHReq("PATCH", body, userID, wsID, "OWNER", map[string]string{"crewId": crewID, "identifier": "ENG-1"})
	h.Update(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (routine absent)", rr.Code)
	}
}

func TestCovIHUpdateClearRoutineAndParent(t *testing.T) {
	h, userID, wsID, crewID, leadID, _ := covIHNew(t)
	seedIssue(t, h.db, wsID, crewID, leadID, "ENG-1", "BACKLOG")
	// Clear paths: empty strings hit SetNull branches.
	body := `{"routine_id":"","parent_issue_id":"","project_id":"","milestone_id":"","routine_inputs":{"k":"v"}}`
	req, rr := covIHReq("PATCH", body, userID, wsID, "OWNER", map[string]string{"crewId": crewID, "identifier": "ENG-1"})
	h.Update(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
}

// ── Delete ─────────────────────────────────────────────────────────────

func TestCovIHDeleteDBError(t *testing.T) {
	h, userID, wsID, crewID, leadID, _ := covIHNew(t)
	seedIssue(t, h.db, wsID, crewID, leadID, "ENG-1", "BACKLOG")
	h.db.Close()
	req, rr := covIHReq("DELETE", "", userID, wsID, "OWNER", map[string]string{"crewId": crewID, "identifier": "ENG-1"})
	h.Delete(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rr.Code)
	}
}

// ── Comments ───────────────────────────────────────────────────────────

func TestCovIHListCommentsDBError(t *testing.T) {
	h, userID, wsID, crewID, leadID, _ := covIHNew(t)
	seedIssue(t, h.db, wsID, crewID, leadID, "ENG-1", "BACKLOG")
	h.db.Close()
	req, rr := covIHReq("GET", "", userID, wsID, "OWNER", map[string]string{"crewId": crewID, "identifier": "ENG-1"})
	h.ListComments(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rr.Code)
	}
}

func TestCovIHCreateCommentDBError(t *testing.T) {
	h, userID, wsID, crewID, leadID, _ := covIHNew(t)
	seedIssue(t, h.db, wsID, crewID, leadID, "ENG-1", "BACKLOG")
	h.db.Close()
	req, rr := covIHReq("POST", `{"body":"hi"}`, userID, wsID, "OWNER", map[string]string{"crewId": crewID, "identifier": "ENG-1"})
	h.CreateComment(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rr.Code)
	}
}

// ── Labels ─────────────────────────────────────────────────────────────

func TestCovIHListLabelsDBError(t *testing.T) {
	h, userID, wsID, _, _, _ := covIHNew(t)
	h.db.Close()
	req, rr := covIHReq("GET", "", userID, wsID, "OWNER", nil)
	h.ListLabels(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rr.Code)
	}
}

func TestCovIHCreateLabelDBError(t *testing.T) {
	h, userID, wsID, _, _, _ := covIHNew(t)
	h.db.Close()
	req, rr := covIHReq("POST", `{"name":"x","color":"#fff"}`, userID, wsID, "OWNER", nil)
	h.CreateLabel(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rr.Code)
	}
}

func TestCovIHCreateLabelInvalidJSON(t *testing.T) {
	h, userID, wsID, _, _, _ := covIHNew(t)
	req, rr := covIHReq("POST", `{bad`, userID, wsID, "OWNER", nil)
	h.CreateLabel(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestCovIHUpdateLabelDBError(t *testing.T) {
	h, userID, wsID, _, _, _ := covIHNew(t)
	lbl := seedLabel(t, h.db, wsID, "x")
	h.db.Close()
	req, rr := covIHReq("PATCH", `{"name":"y"}`, userID, wsID, "OWNER", map[string]string{"labelId": lbl})
	h.UpdateLabel(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rr.Code)
	}
}

func TestCovIHDeleteLabelDBError(t *testing.T) {
	h, userID, wsID, _, _, _ := covIHNew(t)
	lbl := seedLabel(t, h.db, wsID, "x")
	h.db.Close()
	req, rr := covIHReq("DELETE", "", userID, wsID, "OWNER", map[string]string{"labelId": lbl})
	h.DeleteLabel(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rr.Code)
	}
}

// ── Relations ──────────────────────────────────────────────────────────

func TestCovIHListRelationsDBError(t *testing.T) {
	h, userID, wsID, crewID, leadID, _ := covIHNew(t)
	seedIssue(t, h.db, wsID, crewID, leadID, "ENG-1", "BACKLOG")
	h.db.Close()
	req, rr := covIHReq("GET", "", userID, wsID, "OWNER", map[string]string{"crewId": crewID, "identifier": "ENG-1"})
	h.ListRelations(rr, req)
	// resolveMissionID fails first → 404 (handler maps all resolve errors to 404).
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

func TestCovIHCreateRelationInvalidJSON(t *testing.T) {
	h, userID, wsID, crewID, _, _ := covIHNew(t)
	req, rr := covIHReq("POST", `{bad`, userID, wsID, "OWNER", map[string]string{"crewId": crewID, "identifier": "ENG-1"})
	h.CreateRelation(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestCovIHCreateRelationForbidden(t *testing.T) {
	h, userID, wsID, crewID, _, _ := covIHNew(t)
	req, rr := covIHReq("POST", `{"target_identifier":"ENG-2","relation_type":"blocks"}`, userID, wsID, "VIEWER", map[string]string{"crewId": crewID, "identifier": "ENG-1"})
	h.CreateRelation(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rr.Code)
	}
}

func TestCovIHDeleteRelationForbidden(t *testing.T) {
	h, userID, wsID, _, _, _ := covIHNew(t)
	req, rr := covIHReq("DELETE", "", userID, wsID, "VIEWER", map[string]string{"relationId": "x"})
	h.DeleteRelation(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rr.Code)
	}
}

func TestCovIHDeleteRelationDBError(t *testing.T) {
	h, userID, wsID, _, _, _ := covIHNew(t)
	h.db.Close()
	req, rr := covIHReq("DELETE", "", userID, wsID, "OWNER", map[string]string{"relationId": "x"})
	h.DeleteRelation(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rr.Code)
	}
}

// ── Workflow ───────────────────────────────────────────────────────────

func TestCovIHReviewDBError(t *testing.T) {
	h, userID, wsID, crewID, leadID, _ := covIHNew(t)
	seedIssue(t, h.db, wsID, crewID, leadID, "ENG-1", "REVIEW")
	h.db.Close()
	req, rr := covIHReq("POST", `{"action":"approve"}`, userID, wsID, "OWNER", map[string]string{"crewId": crewID, "identifier": "ENG-1"})
	h.Review(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rr.Code)
	}
}

func TestCovIHReviewInvalidJSON(t *testing.T) {
	h, userID, wsID, crewID, _, _ := covIHNew(t)
	req, rr := covIHReq("POST", `{bad`, userID, wsID, "OWNER", map[string]string{"crewId": crewID, "identifier": "ENG-1"})
	h.Review(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestCovIHReviewApproveFromInProgress(t *testing.T) {
	h, userID, wsID, crewID, leadID, _ := covIHNew(t)
	seedIssue(t, h.db, wsID, crewID, leadID, "ENG-1", "IN_PROGRESS")
	req, rr := covIHReq("POST", `{"action":"approve","comment":"ok"}`, userID, wsID, "OWNER", map[string]string{"crewId": crewID, "identifier": "ENG-1"})
	h.Review(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var st string
	if err := h.db.QueryRow(`SELECT status FROM missions WHERE crew_id=? AND identifier='ENG-1'`, crewID).Scan(&st); err != nil {
		t.Fatalf("scan status: %v", err)
	}
	if st != "DONE" {
		t.Errorf("status = %q, want DONE", st)
	}
}

func TestCovIHReviewRequestChangesReassign(t *testing.T) {
	h, userID, wsID, crewID, leadID, _ := covIHNew(t)
	seedIssue(t, h.db, wsID, crewID, leadID, "ENG-1", "REVIEW")
	// reassign_to=worker resolves to the seeded worker agent slug.
	req, rr := covIHReq("POST", `{"action":"request_changes","reassign_to":"worker","comment":"redo"}`, userID, wsID, "OWNER", map[string]string{"crewId": crewID, "identifier": "ENG-1"})
	h.Review(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var assignee string
	if err := h.db.QueryRow(`SELECT COALESCE(assignee_id,'') FROM missions WHERE crew_id=? AND identifier='ENG-1'`, crewID).Scan(&assignee); err != nil {
		t.Fatalf("scan assignee: %v", err)
	}
	if assignee != "agent-worker" {
		t.Errorf("assignee_id = %q, want agent-worker", assignee)
	}
}

func TestCovIHListActivityDBError(t *testing.T) {
	h, userID, wsID, crewID, leadID, _ := covIHNew(t)
	seedIssue(t, h.db, wsID, crewID, leadID, "ENG-1", "BACKLOG")
	h.db.Close()
	req, rr := covIHReq("GET", "", userID, wsID, "OWNER", map[string]string{"crewId": crewID, "identifier": "ENG-1"})
	h.ListActivity(rr, req)
	// resolveMissionID fails → 404.
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

func TestCovIHListActivityNotFound(t *testing.T) {
	h, userID, wsID, crewID, _, _ := covIHNew(t)
	req, rr := covIHReq("GET", "", userID, wsID, "OWNER", map[string]string{"crewId": crewID, "identifier": "GHOST"})
	h.ListActivity(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

func TestCovIHStartDBError(t *testing.T) {
	h, userID, wsID, crewID, leadID, _ := covIHNew(t)
	seedIssue(t, h.db, wsID, crewID, leadID, "ENG-1", "BACKLOG")
	h.db.Close()
	req, rr := covIHReq("POST", "", userID, wsID, "OWNER", map[string]string{"crewId": crewID, "identifier": "ENG-1"})
	h.Start(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rr.Code)
	}
}

func TestCovIHStartLeadAssignee(t *testing.T) {
	h, userID, wsID, crewID, leadID, _ := covIHNew(t)
	id := seedIssue(t, h.db, wsID, crewID, leadID, "ENG-1", "BACKLOG")
	// Assign the LEAD agent → handler skips default task creation.
	if _, err := h.db.ExecContext(context.Background(),
		`UPDATE missions SET assignee_id=?, assignee_type='agent' WHERE id=?`, leadID, id); err != nil {
		t.Fatal(err)
	}
	req, rr := covIHReq("POST", "", userID, wsID, "OWNER", map[string]string{"crewId": crewID, "identifier": "ENG-1"})
	h.Start(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var n int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM mission_tasks WHERE mission_id=?`, id).Scan(&n); err != nil {
		t.Fatalf("scan task count: %v", err)
	}
	if n != 0 {
		t.Errorf("task count = %d, want 0 (lead planning skips default task)", n)
	}
}

func TestCovIHStopDBError(t *testing.T) {
	h, userID, wsID, crewID, leadID, _ := covIHNew(t)
	seedIssue(t, h.db, wsID, crewID, leadID, "ENG-1", "IN_PROGRESS")
	h.db.Close()
	req, rr := covIHReq("POST", "", userID, wsID, "OWNER", map[string]string{"crewId": crewID, "identifier": "ENG-1"})
	h.Stop(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rr.Code)
	}
}

// ── Bulk / SubIssues ───────────────────────────────────────────────────

func TestCovIHBulkUpdateInvalidJSON(t *testing.T) {
	h, userID, wsID, _, _, _ := covIHNew(t)
	req, rr := covIHReq("POST", `{bad`, userID, wsID, "OWNER", nil)
	h.BulkUpdate(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rr.Code)
	}
}

func TestCovIHBulkUpdateWithLabels(t *testing.T) {
	h, userID, wsID, crewID, leadID, workerID := covIHNew(t)
	id1 := seedIssue(t, h.db, wsID, crewID, leadID, "ENG-1", "BACKLOG")
	id2 := seedIssue(t, h.db, wsID, crewID, leadID, "ENG-2", "BACKLOG")
	lbl := seedLabel(t, h.db, wsID, "bug")
	body := `{"ids":["` + id1 + `","` + id2 + `","ghost-skip"],"updates":{"status":"TODO","priority":"high","assignee_type":"agent","assignee_id":"` + workerID + `","project_id":"","labels":["` + lbl + `"]}}`
	req, rr := covIHReq("POST", body, userID, wsID, "OWNER", nil)
	h.BulkUpdate(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var resp map[string]int
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp["updated"] != 2 {
		t.Errorf("updated = %d, want 2", resp["updated"])
	}
	var n int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM mission_labels WHERE mission_id=?`, id1).Scan(&n); err != nil {
		t.Fatalf("scan label assoc: %v", err)
	}
	if n != 1 {
		t.Errorf("label assoc = %d, want 1", n)
	}
}

func TestCovIHBulkUpdateInvalidTransitionSkipped(t *testing.T) {
	h, userID, wsID, crewID, leadID, _ := covIHNew(t)
	id := seedIssue(t, h.db, wsID, crewID, leadID, "ENG-1", "BACKLOG")
	// BACKLOG -> DONE is invalid, so it's skipped (updated=0).
	body := `{"ids":["` + id + `"],"updates":{"status":"DONE"}}`
	req, rr := covIHReq("POST", body, userID, wsID, "OWNER", nil)
	h.BulkUpdate(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	var resp map[string]int
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp["updated"] != 0 {
		t.Errorf("updated = %d, want 0", resp["updated"])
	}
}

func TestCovIHListSubIssuesDBError(t *testing.T) {
	h, userID, wsID, crewID, leadID, _ := covIHNew(t)
	seedIssue(t, h.db, wsID, crewID, leadID, "ENG-1", "BACKLOG")
	h.db.Close()
	req, rr := covIHReq("GET", "", userID, wsID, "OWNER", map[string]string{"crewId": crewID, "identifier": "ENG-1"})
	h.ListSubIssues(rr, req)
	// resolveMissionID returns a non-ErrNoRows error on closed DB → 500.
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rr.Code)
	}
}
