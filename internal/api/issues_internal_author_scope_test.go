package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
)

// seedForeignAgent creates a second workspace holding one agent, and returns
// that agent's id. It is the other tenant whose data must never surface here.
func seedForeignAgent(t *testing.T, h *InternalIssueHandler) string {
	t.Helper()
	const (
		otherWS    = "ws-other-tenant"
		otherCrew  = "crew-other-tenant"
		otherAgent = "agent-other-tenant"
		secretName = "Head of Security (other tenant)"
	)
	if _, err := h.db.Exec(
		`INSERT INTO workspaces (id, name, slug, created_at, updated_at)
		 VALUES (?, 'Other', 'other', datetime('now'), datetime('now'))`, otherWS); err != nil {
		t.Fatalf("seed foreign workspace: %v", err)
	}
	if _, err := h.db.Exec(
		`INSERT INTO crews (id, workspace_id, name, slug, created_at, updated_at)
		 VALUES (?, ?, 'Other', 'other', datetime('now'), datetime('now'))`, otherCrew, otherWS); err != nil {
		t.Fatalf("seed foreign crew: %v", err)
	}
	if _, err := h.db.Exec(
		`INSERT INTO agents (id, workspace_id, crew_id, name, slug, cli_adapter, created_at, updated_at)
		 VALUES (?, ?, ?, ?, 'other-lead', 'CLAUDE_CODE', datetime('now'), datetime('now'))`,
		otherAgent, otherWS, otherCrew, secretName); err != nil {
		t.Fatalf("seed foreign agent: %v", err)
	}
	return otherAgent
}

// TestInternalIssue_CreateComment_ForeignAuthorAgentIsRefused pins the
// workspace binding on the AUTHOR of an agent-written comment.
//
// The handler already scopes the ISSUE to the caller's workspace (PR-F24 F-4),
// but it accepted any non-empty agent_id as the author and then looked its
// display name up with an unscoped `SELECT name FROM agents WHERE id = ?`.
// Two things follow, and the second is the reason this is a security test
// rather than a tidy-up:
//
//   - the comment is attributed to an agent that does not exist in this
//     tenant, so the timeline names a stranger;
//   - that stranger's display name is handed to the mention recorder as
//     AuthorName, which puts it inside the brief the woken agent is given.
//     A name is chosen by whoever created the agent, so this is a read of
//     another tenant's data through a field nobody thinks of as a read.
//
// The fix is the same one every sibling internal write door applies: prove the
// id belongs to the caller's workspace, and refuse when it does not.
func TestInternalIssue_CreateComment_ForeignAuthorAgentIsRefused(t *testing.T) {
	h, wsID, crewID, leadID, _ := newInternalIssueHandler(t)
	seedIssue(t, h.db, wsID, crewID, leadID, "ENG-1", "BACKLOG")
	foreign := seedForeignAgent(t, h)

	body := bytes.NewBufferString(`{"workspace_id":"` + wsID + `","agent_id":"` + foreign + `","body":"hello"}`)
	req := httptest.NewRequest("POST", "/", body)
	req.SetPathValue("identifier", "ENG-1")
	rr := httptest.NewRecorder()
	h.CreateComment(rr, req)

	if rr.Code == http.StatusCreated {
		t.Fatalf("an agent from another workspace was accepted as the comment author (status %d)", rr.Code)
	}
	if rr.Code != http.StatusBadRequest && rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 400 or 404; body=%s", rr.Code, rr.Body.String())
	}

	var n int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM mission_comments WHERE author_id = ?`, foreign).Scan(&n); err != nil {
		t.Fatalf("count comments: %v", err)
	}
	if n != 0 {
		t.Errorf("comment rows authored by the foreign agent = %d, want 0", n)
	}
}

// TestInternalIssue_CreateComment_OwnWorkspaceAuthorStillWorks is the guard on
// the over-correction. The refusal above must not cost the ordinary case, so
// this is the flow the fix could plausibly break, driven end to end.
func TestInternalIssue_CreateComment_OwnWorkspaceAuthorStillWorks(t *testing.T) {
	h, wsID, crewID, leadID, _ := newInternalIssueHandler(t)
	seedIssue(t, h.db, wsID, crewID, leadID, "ENG-1", "BACKLOG")

	body := bytes.NewBufferString(`{"workspace_id":"` + wsID + `","agent_id":"agent-worker","body":"hello"}`)
	req := httptest.NewRequest("POST", "/", body)
	req.SetPathValue("identifier", "ENG-1")
	rr := httptest.NewRecorder()
	h.CreateComment(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("an agent commenting in its OWN workspace must still succeed: status = %d body=%s",
			rr.Code, rr.Body.String())
	}
}

// TestInternalIssue_UpdateStatus_ForeignAuthorAgentIsRefused covers the second
// door. UpdateStatus accepts an inline `comment`, which writes the same
// mission_comments row and feeds the same mention brief, so it needs the same
// binding — a fix applied to CreateComment alone would leave this one open.
func TestInternalIssue_UpdateStatus_ForeignAuthorAgentIsRefused(t *testing.T) {
	h, wsID, crewID, leadID, _ := newInternalIssueHandler(t)
	seedIssue(t, h.db, wsID, crewID, leadID, "ENG-1", "BACKLOG")
	foreign := seedForeignAgent(t, h)

	body := bytes.NewBufferString(
		`{"workspace_id":"` + wsID + `","status":"IN_PROGRESS","agent_id":"` + foreign + `","comment":"picking this up"}`)
	req := httptest.NewRequest("PATCH", "/", body)
	req.SetPathValue("identifier", "ENG-1")
	rr := httptest.NewRecorder()
	h.UpdateStatus(rr, req)

	if rr.Code == http.StatusOK {
		t.Fatalf("an agent from another workspace was accepted as the inline-comment author (status %d)", rr.Code)
	}

	var n int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM mission_comments WHERE author_id = ?`, foreign).Scan(&n); err != nil {
		t.Fatalf("count comments: %v", err)
	}
	if n != 0 {
		t.Errorf("comment rows authored by the foreign agent = %d, want 0", n)
	}
}
