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

// issues_internal_verbs_test.go — the fields PATCH /api/v1/internal/issues/
// {identifier} grew so an agent can do more than flip a status, plus the text
// search GET /api/v1/internal/issues grew so it can find the issue in the
// first place.
//
// The interesting half is the workspace fence on the new fields. assignee_id,
// label ids and parent-ish foreign keys are all caller-supplied ids that name
// rows in OTHER tables; the public handler validates each one against the
// session's workspace, and this handler has to do the same against the token's
// workspace or the internal surface becomes the soft way in.

func internalPatch(ident, body string, ctx context.Context) *http.Request {
	req := httptest.NewRequest("PATCH", "/", bytes.NewBufferString(body))
	req = req.WithContext(ctx)
	req.SetPathValue("identifier", ident)
	return req
}

func internalList(query string, ctx context.Context) *http.Request {
	req := httptest.NewRequest("GET", "/"+query, nil)
	return req.WithContext(ctx)
}

// --- PATCH: the new fields --------------------------------------------------

func TestInternalIssueUpdate_AssigneeDueDateEstimate(t *testing.T) {
	h, wsID, crewID, leadID, _ := newInternalIssueHandler(t)
	issueID := seedIssue(t, h.db, wsID, crewID, leadID, "ENG-1", "BACKLOG")

	rr := httptest.NewRecorder()
	h.UpdateStatus(rr, internalPatch("ENG-1",
		`{"workspace_id":"`+wsID+`","agent_id":"agent-worker","assignee_id":"agent-worker",`+
			`"due_date":"2026-09-01","estimate":5}`,
		crewBoundCtx1186(wsID, crewID)))

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var assigneeID, assigneeType, dueDate sql.NullString
	var estimate sql.NullInt64
	if err := h.db.QueryRow(
		`SELECT assignee_id, assignee_type, due_date, estimate FROM missions WHERE id = ?`, issueID).
		Scan(&assigneeID, &assigneeType, &dueDate, &estimate); err != nil {
		t.Fatalf("read issue: %v", err)
	}
	if assigneeID.String != "agent-worker" {
		t.Errorf("assignee_id = %q, want agent-worker", assigneeID.String)
	}
	// assignee_type is RESOLVED, never trusted from the row's stale value —
	// same rule the public handler learned the hard way.
	if assigneeType.String != "agent" {
		t.Errorf("assignee_type = %q, want agent", assigneeType.String)
	}
	if dueDate.String != "2026-09-01" {
		t.Errorf("due_date = %q, want 2026-09-01", dueDate.String)
	}
	if estimate.Int64 != 5 {
		t.Errorf("estimate = %d, want 5", estimate.Int64)
	}
}

func TestInternalIssueUpdate_LabelsReplaced(t *testing.T) {
	h, wsID, crewID, leadID, _ := newInternalIssueHandler(t)
	issueID := seedIssue(t, h.db, wsID, crewID, leadID, "ENG-1", "BACKLOG")
	bug := seedLabel(t, h.db, wsID, "bug")
	urgent := seedLabel(t, h.db, wsID, "urgent")

	set := func(ids string) {
		rr := httptest.NewRecorder()
		h.UpdateStatus(rr, internalPatch("ENG-1",
			`{"workspace_id":"`+wsID+`","agent_id":"agent-worker","labels":`+ids+`}`,
			crewBoundCtx1186(wsID, crewID)))
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
		}
	}
	set(`["` + bug + `","` + urgent + `"]`)
	var n int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM mission_labels WHERE mission_id = ?`, issueID).Scan(&n); err != nil {
		t.Fatalf("count labels: %v", err)
	}
	if n != 2 {
		t.Fatalf("labels = %d, want 2", n)
	}

	// labels is a full replacement, not a merge — an empty array clears.
	set(`[]`)
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM mission_labels WHERE mission_id = ?`, issueID).Scan(&n); err != nil {
		t.Fatalf("count labels: %v", err)
	}
	if n != 0 {
		t.Errorf("labels after clear = %d, want 0", n)
	}
}

// --- PATCH security: cross-workspace foreign ids ----------------------------

// An assignee_id naming a user in ANOTHER tenant. Pre-fence the internal PATCH
// had no assignee field at all; the moment it does, it inherits the disclosure
// bug issue_assignee_workspace_isolation_test.go pins for the public handler —
// the read path resolves a display name for whatever id is stored, so a
// foreign id turns into a foreign person's name rendered inside this tenant.
func TestSecInternalIssueUpdate_CrossWorkspaceAssigneeRejected(t *testing.T) {
	h, wsID, crewID, leadID, _ := newInternalIssueHandler(t)
	issueID := seedIssue(t, h.db, wsID, crewID, leadID, "ENG-1", "BACKLOG")
	foreignWS, _ := seedForeignWorkspaceIssue(t, h.db, "assignee")

	var foreignUser string
	if err := h.db.QueryRow(`SELECT user_id FROM workspace_members WHERE workspace_id = ?`, foreignWS).
		Scan(&foreignUser); err != nil {
		t.Fatalf("read foreign user: %v", err)
	}

	rr := httptest.NewRecorder()
	h.UpdateStatus(rr, internalPatch("ENG-1",
		`{"workspace_id":"`+wsID+`","agent_id":"agent-worker","assignee_type":"user","assignee_id":"`+foreignUser+`"}`,
		crewBoundCtx1186(wsID, crewID)))

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a foreign-tenant assignee; body=%s", rr.Code, rr.Body.String())
	}
	var assignee sql.NullString
	if err := h.db.QueryRow(`SELECT assignee_id FROM missions WHERE id = ?`, issueID).Scan(&assignee); err != nil {
		t.Fatalf("read assignee: %v", err)
	}
	if assignee.Valid && assignee.String == foreignUser {
		t.Errorf("foreign assignee_id was persisted: %q", assignee.String)
	}
}

// Labels are workspace-scoped rows. A label id from another tenant must not
// attach — mission_labels has no workspace column of its own, so the join in
// the read path would happily render the foreign label's name and colour.
func TestSecInternalIssueUpdate_CrossWorkspaceLabelIgnored(t *testing.T) {
	h, wsID, crewID, leadID, _ := newInternalIssueHandler(t)
	issueID := seedIssue(t, h.db, wsID, crewID, leadID, "ENG-1", "BACKLOG")
	foreignWS, _ := seedForeignWorkspaceIssue(t, h.db, "label")
	foreignLabel := seedLabel(t, h.db, foreignWS, "their-secret-label")

	rr := httptest.NewRecorder()
	h.UpdateStatus(rr, internalPatch("ENG-1",
		`{"workspace_id":"`+wsID+`","agent_id":"agent-worker","labels":["`+foreignLabel+`"]}`,
		crewBoundCtx1186(wsID, crewID)))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}

	var n int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM mission_labels WHERE mission_id = ? AND label_id = ?`,
		issueID, foreignLabel).Scan(&n); err != nil {
		t.Fatalf("count labels: %v", err)
	}
	if n != 0 {
		t.Errorf("a foreign-tenant label must not attach, found %d row(s)", n)
	}
}

// The crew fence already proven for status/comment must extend to every new
// field — otherwise the PATCH grew a way around the boundary #1365 installed.
func TestSecInternalIssueUpdate_CrossCrewAssigneeRejected(t *testing.T) {
	h, wsID, crewA, _, _ := newInternalIssueHandler(t)
	_, _, siblingIdent := seedSiblingCrewBIssue(t, h.db, wsID, "BACKLOG")

	rr := httptest.NewRecorder()
	h.UpdateStatus(rr, internalPatch(siblingIdent,
		`{"workspace_id":"`+wsID+`","agent_id":"agent-worker","assignee_id":"agent-worker","estimate":13}`,
		crewBoundCtx1186(wsID, crewA)))

	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rr.Code, rr.Body.String())
	}
	var estimate sql.NullInt64
	if err := h.db.QueryRow(`SELECT estimate FROM missions WHERE identifier = ?`, siblingIdent).Scan(&estimate); err != nil {
		t.Fatalf("read estimate: %v", err)
	}
	if estimate.Valid {
		t.Errorf("crew B's issue must be untouched, estimate = %d", estimate.Int64)
	}
}

// --- GET: text search -------------------------------------------------------

func TestInternalIssueList_TextQuery(t *testing.T) {
	h, wsID, crewID, leadID, _ := newInternalIssueHandler(t)
	seedIssue(t, h.db, wsID, crewID, leadID, "ENG-1", "BACKLOG")
	seedIssue(t, h.db, wsID, crewID, leadID, "ENG-2", "BACKLOG")
	if _, err := h.db.Exec(`UPDATE missions SET title = 'Flaky login redirect' WHERE identifier = 'ENG-1'`); err != nil {
		t.Fatalf("set title: %v", err)
	}
	if _, err := h.db.Exec(`UPDATE missions SET description = 'the OAuth callback loops' WHERE identifier = 'ENG-2'`); err != nil {
		t.Fatalf("set description: %v", err)
	}

	get := func(q string) []map[string]any {
		rr := httptest.NewRecorder()
		h.List(rr, internalList(q, crewBoundCtx1186(wsID, crewID)))
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
		}
		var out []map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return out
	}

	if got := get("?workspace_id=" + wsID + "&q=redirect"); len(got) != 1 || got[0]["identifier"] != "ENG-1" {
		t.Errorf("q=redirect matched %d issue(s): %v", len(got), got)
	}
	// The query hits the description too — an agent searching for a symptom
	// finds the issue whose BODY describes it, not only titles.
	if got := get("?workspace_id=" + wsID + "&q=OAuth"); len(got) != 1 || got[0]["identifier"] != "ENG-2" {
		t.Errorf("q=OAuth matched %d issue(s): %v", len(got), got)
	}
	if got := get("?workspace_id=" + wsID + "&q=nothing-matches-this"); len(got) != 0 {
		t.Errorf("expected no matches, got %d", len(got))
	}
}

// A bare `%` in the query is a LIKE wildcard, not a literal. Unescaped it
// matches every issue in the workspace — a search box that turns into
// "dump the board" on one character.
func TestSecInternalIssueList_WildcardIsLiteral(t *testing.T) {
	h, wsID, crewID, leadID, _ := newInternalIssueHandler(t)
	seedIssue(t, h.db, wsID, crewID, leadID, "ENG-1", "BACKLOG")
	seedIssue(t, h.db, wsID, crewID, leadID, "ENG-2", "BACKLOG")

	rr := httptest.NewRecorder()
	h.List(rr, internalList("?workspace_id="+wsID+"&q=%25", crewBoundCtx1186(wsID, crewID)))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var out []map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("a literal %% must match nothing here, got %d issue(s)", len(out))
	}
}

// The crew binding constrains the LISTING too (#1186 / effectiveCrewFilter):
// a crew-A sidecar searching the board must not see crew B's issues.
func TestSecInternalIssueList_CrewBindingConstrains(t *testing.T) {
	h, wsID, crewA, leadA, _ := newInternalIssueHandler(t)
	seedIssue(t, h.db, wsID, crewA, leadA, "ENG-1", "BACKLOG")
	_, _, siblingIdent := seedSiblingCrewBIssue(t, h.db, wsID, "BACKLOG")

	rr := httptest.NewRecorder()
	h.List(rr, internalList("?workspace_id="+wsID, crewBoundCtx1186(wsID, crewA)))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var out []map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, it := range out {
		if it["identifier"] == siblingIdent {
			t.Fatalf("crew B's issue %q leaked into a crew-A listing", siblingIdent)
		}
	}
}
