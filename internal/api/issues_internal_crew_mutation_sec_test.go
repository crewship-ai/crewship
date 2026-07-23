package api

import (
	"bytes"
	"context"
	"database/sql"
	"net/http"
	"net/http/httptest"
	"testing"
)

// #1365: issue MUTATION (UpdateStatus, CreateComment) must honour the same
// crew blast-radius boundary the CREATE path already enforces. A crew-bound
// (crwv1) internal token for crew A must not change the status of — nor
// comment on — an issue owned by a sibling crew B in the SAME workspace.
//
// These are red-first: on current main the mutate handlers only call
// assertInternalTokenWorkspace and locate the issue by identifier+workspace_id
// with no crew predicate, so a crew-A token drives crew-B's issue (200/201).
// The negative controls guard against over-blocking: a same-crew crwv1 token
// and a workspace-bound (wsv1) token must retain their legitimate reach.

// seedSiblingCrewBIssue inserts a second crew + its LEAD in the same workspace
// and an issue owned by that crew, returning the issue identifier.
func seedSiblingCrewBIssue(t *testing.T, db *sql.DB, wsID, status string) (crewB, leadB, identifier string) {
	t.Helper()
	crewB = "crew-b-mut"
	if _, err := db.ExecContext(context.Background(),
		`INSERT INTO crews (id, workspace_id, name, slug, issue_prefix) VALUES (?, ?, 'Bravo', 'bravomut', 'BRV')`,
		crewB, wsID); err != nil {
		t.Fatalf("insert crew B: %v", err)
	}
	leadB = "agent-lead-b-mut"
	if _, err := db.ExecContext(context.Background(),
		`INSERT INTO agents (id, workspace_id, crew_id, name, slug, agent_role, status, cli_adapter, temperature, timeout_seconds, tool_profile, memory_enabled)
		 VALUES (?, ?, ?, 'LeadB', 'leadbmut', 'LEAD', 'IDLE', 'CLAUDE_CODE', 0.7, 1800, 'CODING', 0)`,
		leadB, wsID, crewB); err != nil {
		t.Fatalf("insert lead B: %v", err)
	}
	identifier = "BRV-1"
	seedIssue(t, db, wsID, crewB, leadB, identifier, status)
	return crewB, leadB, identifier
}

func TestSecIssueMutation_UpdateStatus_CrossCrewRejected(t *testing.T) {
	h, wsID, crewA, _, _ := newInternalIssueHandler(t)
	_, _, ident := seedSiblingCrewBIssue(t, h.db, wsID, "BACKLOG")

	// crwv1 token bound to crew A tries to advance crew B's issue.
	body := bytes.NewBufferString(`{"workspace_id":"` + wsID + `","status":"TODO"}`)
	req := httptest.NewRequest("PATCH", "/", body)
	req = req.WithContext(crewBoundCtx1186(wsID, crewA))
	req.SetPathValue("identifier", ident)
	rr := httptest.NewRecorder()
	h.UpdateStatus(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for cross-crew status update, got %d body=%s", rr.Code, rr.Body.String())
	}
	var status string
	if err := h.db.QueryRow(`SELECT status FROM missions WHERE identifier = ?`, ident).Scan(&status); err != nil {
		t.Fatalf("read status: %v", err)
	}
	if status != "BACKLOG" {
		t.Errorf("crew B's issue status must be unchanged, got %q", status)
	}
}

func TestSecIssueMutation_UpdateStatus_CrossCrewDoneRejected(t *testing.T) {
	h, wsID, crewA, _, _ := newInternalIssueHandler(t)
	// IN_PROGRESS -> DONE is a valid transition, so pre-fix this crosses the
	// crew boundary AND completes a sibling's issue (the worst case in the report).
	_, _, ident := seedSiblingCrewBIssue(t, h.db, wsID, "IN_PROGRESS")

	body := bytes.NewBufferString(`{"workspace_id":"` + wsID + `","status":"DONE"}`)
	req := httptest.NewRequest("PATCH", "/", body)
	req = req.WithContext(crewBoundCtx1186(wsID, crewA))
	req.SetPathValue("identifier", ident)
	rr := httptest.NewRecorder()
	h.UpdateStatus(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for cross-crew DONE, got %d body=%s", rr.Code, rr.Body.String())
	}
	var status string
	if err := h.db.QueryRow(`SELECT status FROM missions WHERE identifier = ?`, ident).Scan(&status); err != nil {
		t.Fatalf("read status: %v", err)
	}
	if status != "IN_PROGRESS" {
		t.Errorf("crew B's issue must not be completed cross-crew, got %q", status)
	}
}

func TestSecIssueMutation_CreateComment_CrossCrewRejected(t *testing.T) {
	h, wsID, crewA, _, _ := newInternalIssueHandler(t)
	_, _, ident := seedSiblingCrewBIssue(t, h.db, wsID, "BACKLOG")

	body := bytes.NewBufferString(`{"workspace_id":"` + wsID + `","agent_id":"agent-worker","body":"sneaky cross-crew note"}`)
	req := httptest.NewRequest("POST", "/", body)
	req = req.WithContext(crewBoundCtx1186(wsID, crewA))
	req.SetPathValue("identifier", ident)
	rr := httptest.NewRecorder()
	h.CreateComment(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for cross-crew comment, got %d body=%s", rr.Code, rr.Body.String())
	}
	var count int
	if err := h.db.QueryRow(
		`SELECT COUNT(*) FROM mission_comments mc JOIN missions m ON m.id = mc.mission_id WHERE m.identifier = ?`,
		ident).Scan(&count); err != nil {
		t.Fatalf("count comments: %v", err)
	}
	if count != 0 {
		t.Errorf("expected no comment on crew B's issue, got %d", count)
	}
}

// --- Negative controls: legitimate callers must not be blocked. ---

func TestSecIssueMutation_SameCrewUpdateSucceeds(t *testing.T) {
	h, wsID, crewA, leadA, _ := newInternalIssueHandler(t)
	seedIssue(t, h.db, wsID, crewA, leadA, "ENG-1", "BACKLOG")

	body := bytes.NewBufferString(`{"workspace_id":"` + wsID + `","status":"TODO"}`)
	req := httptest.NewRequest("PATCH", "/", body)
	req = req.WithContext(crewBoundCtx1186(wsID, crewA))
	req.SetPathValue("identifier", "ENG-1")
	rr := httptest.NewRecorder()
	h.UpdateStatus(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 for same-crew update, got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestSecIssueMutation_SameCrewCommentSucceeds(t *testing.T) {
	h, wsID, crewA, leadA, _ := newInternalIssueHandler(t)
	seedIssue(t, h.db, wsID, crewA, leadA, "ENG-1", "BACKLOG")

	body := bytes.NewBufferString(`{"workspace_id":"` + wsID + `","agent_id":"agent-worker","body":"in-crew note"}`)
	req := httptest.NewRequest("POST", "/", body)
	req = req.WithContext(crewBoundCtx1186(wsID, crewA))
	req.SetPathValue("identifier", "ENG-1")
	rr := httptest.NewRecorder()
	h.CreateComment(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("expected 201 for same-crew comment, got %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestSecIssueMutation_WsBoundTokenUpdatesSibling(t *testing.T) {
	// A workspace-bound (wsv1) token carries no crew binding and keeps its
	// workspace-wide reach: it may update any issue in its workspace, including
	// one owned by crew B. The crew guard must resolve B's crew to the bound
	// workspace and allow it.
	h, wsID, _, _, _ := newInternalIssueHandler(t)
	_, _, ident := seedSiblingCrewBIssue(t, h.db, wsID, "BACKLOG")

	body := bytes.NewBufferString(`{"workspace_id":"` + wsID + `","status":"TODO"}`)
	req := httptest.NewRequest("PATCH", "/", body)
	req = req.WithContext(context.WithValue(context.Background(), ctxInternalTokenWS, wsID))
	req.SetPathValue("identifier", ident)
	rr := httptest.NewRecorder()
	h.UpdateStatus(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 for workspace-bound token on sibling issue, got %d body=%s", rr.Code, rr.Body.String())
	}
}
