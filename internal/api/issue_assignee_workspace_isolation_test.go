package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// This file pins the fix for a cross-tenant information disclosure: the
// issue write paths (Create/Update) accepted an arbitrary assignee_id
// without checking it belonged to the caller's workspace, and the read
// path (issueSelectQuery's assignee-name subqueries) resolved
// full_name/name for *any* assignee_id regardless of workspace. Together
// a workspace-A member could assign an issue to a guessed/enumerated
// workspace-B user or agent ID, and every subsequent read of that issue
// would return the foreign identity's display name to anyone in
// workspace A.
//
// Do not edit the other issue_handler*_test.go files for this — this
// file is intentionally standalone so it doesn't collide with parallel
// work on the existing suites.

func TestIssueCreate_RejectsCrossWorkspaceAssignee(t *testing.T) {
	h, userID, wsID, crewID, _, _ := newTestIssueHandler(t)

	// Foreign workspace B with its own user, unrelated to wsID (workspace A).
	foreignWsID := "foreign-ws-b"
	foreignUserID := "foreign-user-b"
	if _, err := h.db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Foreign', 'foreign-b')`, foreignWsID); err != nil {
		t.Fatalf("seed foreign workspace: %v", err)
	}
	if _, err := h.db.Exec(`INSERT INTO users (id, email, full_name) VALUES (?, 'foreign-b@example.com', 'Foreign B Person')`, foreignUserID); err != nil {
		t.Fatalf("seed foreign user: %v", err)
	}
	if _, err := h.db.Exec(`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('fm-b', ?, ?, 'OWNER')`,
		foreignWsID, foreignUserID); err != nil {
		t.Fatalf("seed foreign membership: %v", err)
	}

	body := bytes.NewBufferString(`{"title":"Cross-tenant assign attempt","assignee_type":"user","assignee_id":"` + foreignUserID + `"}`)
	req := httptest.NewRequest("POST", "/", body)
	req.SetPathValue("crewId", crewID)
	ctx := withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.Create(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (cross-workspace assignee_id must be rejected); body=%s", rr.Code, rr.Body.String())
	}

	// Confirm nothing was persisted despite the 400.
	var count int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM missions WHERE assignee_id = ?`, foreignUserID).Scan(&count); err != nil {
		t.Fatalf("count check: %v", err)
	}
	if count != 0 {
		t.Errorf("expected no issue to be created with the foreign assignee_id, found %d", count)
	}
}

func TestIssueCreate_RejectsCrossWorkspaceAssignee_AgentType(t *testing.T) {
	h, userID, wsID, crewID, _, _ := newTestIssueHandler(t)

	foreignWsID := "foreign-ws-c"
	foreignAgentID := "foreign-agent-c"
	if _, err := h.db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Foreign', 'foreign-c')`, foreignWsID); err != nil {
		t.Fatalf("seed foreign workspace: %v", err)
	}
	if _, err := h.db.Exec(`INSERT INTO agents (id, workspace_id, crew_id, name, slug, agent_role, status, cli_adapter, temperature, timeout_seconds, tool_profile, memory_enabled)
		 VALUES (?, ?, NULL, 'Foreign Agent', 'foreign-agent', 'AGENT', 'IDLE', 'CLAUDE_CODE', 0.7, 1800, 'CODING', 0)`,
		foreignAgentID, foreignWsID); err != nil {
		t.Fatalf("seed foreign agent: %v", err)
	}

	body := bytes.NewBufferString(`{"title":"Cross-tenant assign attempt","assignee_type":"agent","assignee_id":"` + foreignAgentID + `"}`)
	req := httptest.NewRequest("POST", "/", body)
	req.SetPathValue("crewId", crewID)
	ctx := withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.Create(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (cross-workspace agent assignee_id must be rejected); body=%s", rr.Code, rr.Body.String())
	}

	var count int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM missions WHERE assignee_id = ?`, foreignAgentID).Scan(&count); err != nil {
		t.Fatalf("count check: %v", err)
	}
	if count != 0 {
		t.Errorf("expected no issue to be created with the foreign agent assignee_id, found %d", count)
	}
}

func TestIssueUpdate_RejectsCrossWorkspaceAssignee(t *testing.T) {
	h, userID, wsID, crewID, leadID, _ := newTestIssueHandler(t)
	seedIssue(t, h.db, wsID, crewID, leadID, "ENG-1", "BACKLOG")

	foreignWsID := "foreign-ws-d"
	foreignUserID := "foreign-user-d"
	if _, err := h.db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Foreign', 'foreign-d')`, foreignWsID); err != nil {
		t.Fatalf("seed foreign workspace: %v", err)
	}
	if _, err := h.db.Exec(`INSERT INTO users (id, email, full_name) VALUES (?, 'foreign-d@example.com', 'Foreign D Person')`, foreignUserID); err != nil {
		t.Fatalf("seed foreign user: %v", err)
	}
	if _, err := h.db.Exec(`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('fm-d', ?, ?, 'OWNER')`,
		foreignWsID, foreignUserID); err != nil {
		t.Fatalf("seed foreign membership: %v", err)
	}

	body := bytes.NewBufferString(`{"assignee_type":"user","assignee_id":"` + foreignUserID + `"}`)
	req := httptest.NewRequest("PATCH", "/", body)
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("identifier", "ENG-1")
	ctx := withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.Update(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (cross-workspace assignee_id must be rejected on update); body=%s", rr.Code, rr.Body.String())
	}

	var count int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM missions WHERE identifier = ? AND assignee_id = ?`, "ENG-1", foreignUserID).Scan(&count); err != nil {
		t.Fatalf("count check: %v", err)
	}
	if count != 0 {
		t.Errorf("expected assignee_id to remain unset, found %d rows with the foreign assignee_id", count)
	}
}

// TestIssueGet_NeverLeaksForeignAssigneeName is the defense-in-depth pin:
// even if a cross-workspace assignee_id somehow ends up on a row (manual
// SQL, imported backup, a future regression in the write-path check), the
// read path must never resolve and return that foreign user's/agent's
// display name. We insert the foreign assignee_id directly with raw SQL,
// deliberately bypassing the handler validation, then assert the GET
// response does not contain the foreign person's name.
func TestIssueGet_NeverLeaksForeignAssigneeName(t *testing.T) {
	h, userID, wsID, crewID, leadID, _ := newTestIssueHandler(t)
	issueID := seedIssue(t, h.db, wsID, crewID, leadID, "ENG-1", "BACKLOG")

	foreignWsID := "foreign-ws-e"
	foreignUserID := "foreign-user-e"
	const foreignName = "TopSecret Foreign Person"
	if _, err := h.db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Foreign', 'foreign-e')`, foreignWsID); err != nil {
		t.Fatalf("seed foreign workspace: %v", err)
	}
	if _, err := h.db.Exec(`INSERT INTO users (id, email, full_name) VALUES (?, 'foreign-e@example.com', ?)`, foreignUserID, foreignName); err != nil {
		t.Fatalf("seed foreign user: %v", err)
	}
	if _, err := h.db.Exec(`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('fm-e', ?, ?, 'OWNER')`,
		foreignWsID, foreignUserID); err != nil {
		t.Fatalf("seed foreign membership: %v", err)
	}

	// Bypass the (fixed) write-path validation on purpose: raw SQL, direct
	// UPDATE of the mission row's assignee columns.
	if _, err := h.db.Exec(`UPDATE missions SET assignee_type = 'user', assignee_id = ? WHERE id = ?`,
		foreignUserID, issueID); err != nil {
		t.Fatalf("raw-SQL assignee injection: %v", err)
	}

	req := httptest.NewRequest("GET", "/", nil)
	req.SetPathValue("crewId", crewID)
	req.SetPathValue("identifier", "ENG-1")
	ctx := withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.Get(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}

	if strings.Contains(rr.Body.String(), foreignName) {
		t.Fatalf("read path leaked foreign workspace's assignee full_name into the response: %s", rr.Body.String())
	}

	var resp issueResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.AssigneeName != nil && *resp.AssigneeName == foreignName {
		t.Fatalf("issue.assignee_name resolved to the foreign workspace's user: %q", *resp.AssigneeName)
	}
}
