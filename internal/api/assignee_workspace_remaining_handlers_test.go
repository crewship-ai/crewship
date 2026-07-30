package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// This file pins the follow-up fix to the cross-tenant assignee_id leak
// closed for issue Create/Update in issue_assignee_workspace_isolation_test.go.
// That fix added validateAssigneeWorkspace() but deliberately left three other
// write paths untouched so the original PR stayed a single defect:
//
//   - IssueHandler.BulkUpdate (issue_handler_bulk.go)
//   - RecurringIssueHandler.Create / Update (recurring_issue_handler.go)
//   - TriageHandler.CreateRule / UpdateRule (triage_handler.go)
//
// All three accept assignee_id straight from the request body and persist
// it with no workspace check. The read path (issueSelectQuery) is already
// fixed globally, so a poisoned row would no longer resolve a display name —
// but the foreign assignee_id would still land in the row, which is exactly
// the "does not exist in this workspace" case the other write paths reject.
//
// Do not edit the other *_test.go files for this — standalone on purpose.

// ── BulkUpdate ──────────────────────────────────────────────────────────────

func TestBulkUpdate_RejectsCrossWorkspaceAssignee(t *testing.T) {
	h, userID, wsID, crewID, leadID, _ := newTestIssueHandler(t)
	id := seedIssue(t, h.db, wsID, crewID, leadID, "ENG-1", "BACKLOG")

	foreignWsID := "foreign-ws-bulk"
	foreignUserID := "foreign-user-bulk"
	if _, err := h.db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Foreign', 'foreign-bulk')`, foreignWsID); err != nil {
		t.Fatalf("seed foreign workspace: %v", err)
	}
	if _, err := h.db.Exec(`INSERT INTO users (id, email, full_name) VALUES (?, 'foreign-bulk@example.com', 'Foreign Bulk Person')`, foreignUserID); err != nil {
		t.Fatalf("seed foreign user: %v", err)
	}
	if _, err := h.db.Exec(`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('fm-bulk', ?, ?, 'OWNER')`,
		foreignWsID, foreignUserID); err != nil {
		t.Fatalf("seed foreign membership: %v", err)
	}

	body := bytes.NewBufferString(`{"ids":["` + id + `"],"updates":{"assignee_type":"user","assignee_id":"` + foreignUserID + `"}}`)
	req := httptest.NewRequest("POST", "/", body)
	ctx := withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.BulkUpdate(rr, req)

	// Bulk semantics (pinned by TestCovIHBulkUpdateInvalidTransitionSkipped):
	// a per-item validation failure skips that item rather than failing the
	// whole request, so this must stay 200 with updated=0 — NOT 400.
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (bad item is skipped, not a batch failure); body=%s", rr.Code, rr.Body.String())
	}
	var resp map[string]int
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["updated"] != 0 {
		t.Errorf("updated = %d, want 0 (cross-workspace assignee_id must be skipped)", resp["updated"])
	}

	var assigneeID *string
	if err := h.db.QueryRow(`SELECT assignee_id FROM missions WHERE id = ?`, id).Scan(&assigneeID); err != nil {
		t.Fatalf("check assignee: %v", err)
	}
	if assigneeID != nil {
		t.Errorf("assignee_id = %v, want nil (foreign assignee must not persist)", *assigneeID)
	}
}

func TestBulkUpdate_PartialBatch_GoodAssigneeAppliedBadSkipped(t *testing.T) {
	h, userID, wsID, crewID, leadID, workerID := newTestIssueHandler(t)
	goodID := seedIssue(t, h.db, wsID, crewID, leadID, "ENG-1", "BACKLOG")
	badID := seedIssue(t, h.db, wsID, crewID, leadID, "ENG-2", "BACKLOG")

	foreignWsID := "foreign-ws-bulk2"
	foreignUserID := "foreign-user-bulk2"
	if _, err := h.db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Foreign', 'foreign-bulk2')`, foreignWsID); err != nil {
		t.Fatalf("seed foreign workspace: %v", err)
	}
	if _, err := h.db.Exec(`INSERT INTO users (id, email, full_name) VALUES (?, 'foreign-bulk2@example.com', 'Foreign Bulk2 Person')`, foreignUserID); err != nil {
		t.Fatalf("seed foreign user: %v", err)
	}
	if _, err := h.db.Exec(`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('fm-bulk2', ?, ?, 'OWNER')`,
		foreignWsID, foreignUserID); err != nil {
		t.Fatalf("seed foreign membership: %v", err)
	}
	// badID already carries a foreign assignee_type=user pinned in via raw SQL
	// isn't needed here — each ID in the batch gets its own updates payload in
	// this handler (same updates struct is applied to all ids), so drive two
	// separate requests instead: one legit assignee (positive control) and one
	// crew-scoped batch containing only the foreign id (must be skipped).

	// Positive control: same-workspace agent assignee must apply cleanly.
	body := bytes.NewBufferString(`{"ids":["` + goodID + `"],"updates":{"assignee_type":"agent","assignee_id":"` + workerID + `"}}`)
	req := httptest.NewRequest("POST", "/", body)
	ctx := withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.BulkUpdate(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var resp map[string]int
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp["updated"] != 1 {
		t.Fatalf("updated = %d, want 1 (legit same-workspace assignee must not be blocked)", resp["updated"])
	}
	var gotAssignee string
	if err := h.db.QueryRow(`SELECT assignee_id FROM missions WHERE id = ?`, goodID).Scan(&gotAssignee); err != nil {
		t.Fatalf("check assignee: %v", err)
	}
	if gotAssignee != workerID {
		t.Errorf("assignee_id = %q, want %q", gotAssignee, workerID)
	}

	// Now a batch mixing a good id (no assignee change, just priority) with the
	// bad id carrying a cross-workspace assignee_id: the good id's OTHER field
	// (priority) must still apply even though this same request also tries (and
	// fails) to set a foreign assignee on badID — each id is processed
	// independently by the existing per-id loop.
	body2 := bytes.NewBufferString(`{"ids":["` + goodID + `","` + badID + `"],"updates":{"priority":"high","assignee_type":"user","assignee_id":"` + foreignUserID + `"}}`)
	req2 := httptest.NewRequest("POST", "/", body2)
	req2 = req2.WithContext(withWorkspace(withUser(req2.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr2 := httptest.NewRecorder()
	h.BulkUpdate(rr2, req2)
	if rr2.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr2.Code, rr2.Body.String())
	}
	var resp2 map[string]int
	if err := json.Unmarshal(rr2.Body.Bytes(), &resp2); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp2["updated"] != 0 {
		t.Errorf("updated = %d, want 0 (both ids in this batch carry the same bad assignee_id, so both are skipped)", resp2["updated"])
	}
	// Neither row picked up the foreign assignee_id or the priority change,
	// since the whole per-id update (including priority) is skipped when the
	// assignee_id in that same updates payload doesn't validate.
	var badAssignee *string
	if err := h.db.QueryRow(`SELECT assignee_id FROM missions WHERE id = ?`, badID).Scan(&badAssignee); err != nil {
		t.Fatalf("check assignee: %v", err)
	}
	if badAssignee != nil {
		t.Errorf("assignee_id = %v, want nil", *badAssignee)
	}
}

// ── RecurringIssueHandler ─────────────────────────────────────────────────

func TestRecurringCreate_RejectsCrossWorkspaceAssignee(t *testing.T) {
	h, userID, wsID, crewID := newRecurringHandler(t)

	foreignWsID := "foreign-ws-recur"
	foreignAgentID := "foreign-agent-recur"
	if _, err := h.db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Foreign', 'foreign-recur')`, foreignWsID); err != nil {
		t.Fatalf("seed foreign workspace: %v", err)
	}
	if _, err := h.db.Exec(`INSERT INTO agents (id, workspace_id, crew_id, name, slug, agent_role, status, cli_adapter, temperature, timeout_seconds, tool_profile, memory_enabled)
		 VALUES (?, ?, NULL, 'Foreign Agent', 'foreign-agent-recur', 'AGENT', 'IDLE', 'CLAUDE_CODE', 0.7, 1800, 'CODING', 0)`,
		foreignAgentID, foreignWsID); err != nil {
		t.Fatalf("seed foreign agent: %v", err)
	}

	body := bytes.NewBufferString(`{"crew_id":"` + crewID + `","title":"x","cron_expression":"0 9 * * *","assignee_type":"agent","assignee_id":"` + foreignAgentID + `"}`)
	req := httptest.NewRequest("POST", "/", body)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.Create(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
	var count int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM recurring_issues WHERE assignee_id = ?`, foreignAgentID).Scan(&count); err != nil {
		t.Fatalf("count check: %v", err)
	}
	if count != 0 {
		t.Errorf("expected no recurring issue created with the foreign assignee_id, found %d", count)
	}
}

func TestRecurringCreate_AllowsSameWorkspaceAssignee(t *testing.T) {
	h, userID, wsID, crewID := newRecurringHandler(t)

	body := bytes.NewBufferString(`{"crew_id":"` + crewID + `","title":"x","cron_expression":"0 9 * * *","assignee_type":"agent","assignee_id":"agent-worker"}`)
	req := httptest.NewRequest("POST", "/", body)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.Create(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (same-workspace assignee must be allowed); body=%s", rr.Code, rr.Body.String())
	}
	var resp recurringIssueResponse
	mustUnmarshal(t, rr, &resp)
	if resp.AssigneeID == nil || *resp.AssigneeID != "agent-worker" {
		t.Errorf("assignee_id = %v, want agent-worker", resp.AssigneeID)
	}
}

func TestRecurringUpdate_RejectsCrossWorkspaceAssignee(t *testing.T) {
	h, userID, wsID, crewID := newRecurringHandler(t)

	body := bytes.NewBufferString(`{"crew_id":"` + crewID + `","title":"x","cron_expression":"0 9 * * *"}`)
	req := httptest.NewRequest("POST", "/", body)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	var created recurringIssueResponse
	mustUnmarshal(t, rr, &created)

	foreignWsID := "foreign-ws-recur-upd"
	foreignUserID := "foreign-user-recur-upd"
	if _, err := h.db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Foreign', 'foreign-recur-upd')`, foreignWsID); err != nil {
		t.Fatalf("seed foreign workspace: %v", err)
	}
	if _, err := h.db.Exec(`INSERT INTO users (id, email, full_name) VALUES (?, 'foreign-recur-upd@example.com', 'Foreign Person')`, foreignUserID); err != nil {
		t.Fatalf("seed foreign user: %v", err)
	}
	if _, err := h.db.Exec(`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('fm-recur-upd', ?, ?, 'OWNER')`,
		foreignWsID, foreignUserID); err != nil {
		t.Fatalf("seed foreign membership: %v", err)
	}

	body2 := bytes.NewBufferString(`{"assignee_type":"user","assignee_id":"` + foreignUserID + `"}`)
	req2 := httptest.NewRequest("PATCH", "/", body2)
	req2.SetPathValue("recurringId", created.ID)
	req2 = req2.WithContext(withWorkspace(withUser(req2.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr2 := httptest.NewRecorder()
	h.Update(rr2, req2)

	if rr2.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr2.Code, rr2.Body.String())
	}
	var assigneeID *string
	if err := h.db.QueryRow(`SELECT assignee_id FROM recurring_issues WHERE id = ?`, created.ID).Scan(&assigneeID); err != nil {
		t.Fatalf("check assignee: %v", err)
	}
	if assigneeID != nil {
		t.Errorf("assignee_id = %v, want nil (foreign assignee must not persist)", *assigneeID)
	}
}

func TestRecurringUpdate_AllowsSameWorkspaceAssignee(t *testing.T) {
	h, userID, wsID, crewID := newRecurringHandler(t)

	body := bytes.NewBufferString(`{"crew_id":"` + crewID + `","title":"x","cron_expression":"0 9 * * *"}`)
	req := httptest.NewRequest("POST", "/", body)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	var created recurringIssueResponse
	mustUnmarshal(t, rr, &created)

	body2 := bytes.NewBufferString(`{"assignee_type":"agent","assignee_id":"agent-worker"}`)
	req2 := httptest.NewRequest("PATCH", "/", body2)
	req2.SetPathValue("recurringId", created.ID)
	req2 = req2.WithContext(withWorkspace(withUser(req2.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr2 := httptest.NewRecorder()
	h.Update(rr2, req2)

	if rr2.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (same-workspace assignee must be allowed); body=%s", rr2.Code, rr2.Body.String())
	}
	var resp recurringIssueResponse
	mustUnmarshal(t, rr2, &resp)
	if resp.AssigneeID == nil || *resp.AssigneeID != "agent-worker" {
		t.Errorf("assignee_id = %v, want agent-worker", resp.AssigneeID)
	}
}

// ── TriageHandler ───────────────────────────────────────────────────────────

func TestTriageCreateRule_RejectsCrossWorkspaceAssignee(t *testing.T) {
	h, userID, wsID, _, _ := newTriageHandler(t)

	foreignWsID := "foreign-ws-triage"
	foreignAgentID := "foreign-agent-triage"
	if _, err := h.db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Foreign', 'foreign-triage')`, foreignWsID); err != nil {
		t.Fatalf("seed foreign workspace: %v", err)
	}
	if _, err := h.db.Exec(`INSERT INTO agents (id, workspace_id, crew_id, name, slug, agent_role, status, cli_adapter, temperature, timeout_seconds, tool_profile, memory_enabled)
		 VALUES (?, ?, NULL, 'Foreign Agent', 'foreign-agent-triage', 'AGENT', 'IDLE', 'CLAUDE_CODE', 0.7, 1800, 'CODING', 0)`,
		foreignAgentID, foreignWsID); err != nil {
		t.Fatalf("seed foreign agent: %v", err)
	}

	body := bytes.NewBufferString(`{"name":"r","pattern":"bug","match_type":"contains","assignee_id":"` + foreignAgentID + `"}`)
	req := httptest.NewRequest("POST", "/", body)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.CreateRule(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
	var count int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM triage_rules WHERE assignee_id = ?`, foreignAgentID).Scan(&count); err != nil {
		t.Fatalf("count check: %v", err)
	}
	if count != 0 {
		t.Errorf("expected no triage rule created with the foreign assignee_id, found %d", count)
	}
}

func TestTriageCreateRule_AllowsSameWorkspaceAssignee(t *testing.T) {
	h, userID, wsID, _, _ := newTriageHandler(t)

	body := bytes.NewBufferString(`{"name":"r","pattern":"bug","match_type":"contains","assignee_id":"agent-worker"}`)
	req := httptest.NewRequest("POST", "/", body)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.CreateRule(rr, req)

	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (same-workspace assignee must be allowed); body=%s", rr.Code, rr.Body.String())
	}
	var resp triageRuleResponse
	mustUnmarshal(t, rr, &resp)
	if resp.AssigneeID == nil || *resp.AssigneeID != "agent-worker" {
		t.Errorf("assignee_id = %v, want agent-worker", resp.AssigneeID)
	}
}

func TestTriageUpdateRule_RejectsCrossWorkspaceAssignee(t *testing.T) {
	h, userID, wsID, _, _ := newTriageHandler(t)

	body := bytes.NewBufferString(`{"name":"r","pattern":"bug","match_type":"contains"}`)
	req := httptest.NewRequest("POST", "/", body)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.CreateRule(rr, req)
	var tr triageRuleResponse
	mustUnmarshal(t, rr, &tr)

	foreignWsID := "foreign-ws-triage-upd"
	foreignAgentID := "foreign-agent-triage-upd"
	if _, err := h.db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Foreign', 'foreign-triage-upd')`, foreignWsID); err != nil {
		t.Fatalf("seed foreign workspace: %v", err)
	}
	if _, err := h.db.Exec(`INSERT INTO agents (id, workspace_id, crew_id, name, slug, agent_role, status, cli_adapter, temperature, timeout_seconds, tool_profile, memory_enabled)
		 VALUES (?, ?, NULL, 'Foreign Agent', 'foreign-agent-triage-upd', 'AGENT', 'IDLE', 'CLAUDE_CODE', 0.7, 1800, 'CODING', 0)`,
		foreignAgentID, foreignWsID); err != nil {
		t.Fatalf("seed foreign agent: %v", err)
	}

	body2 := bytes.NewBufferString(`{"assignee_id":"` + foreignAgentID + `"}`)
	req2 := httptest.NewRequest("PATCH", "/", body2)
	req2.SetPathValue("ruleId", tr.ID)
	req2 = req2.WithContext(withWorkspace(withUser(req2.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr2 := httptest.NewRecorder()
	h.UpdateRule(rr2, req2)

	if rr2.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr2.Code, rr2.Body.String())
	}
	var assigneeID *string
	if err := h.db.QueryRow(`SELECT assignee_id FROM triage_rules WHERE id = ?`, tr.ID).Scan(&assigneeID); err != nil {
		t.Fatalf("check assignee: %v", err)
	}
	if assigneeID != nil {
		t.Errorf("assignee_id = %v, want nil (foreign assignee must not persist)", *assigneeID)
	}
}

func TestTriageUpdateRule_AllowsSameWorkspaceAssignee(t *testing.T) {
	h, userID, wsID, _, _ := newTriageHandler(t)

	body := bytes.NewBufferString(`{"name":"r","pattern":"bug","match_type":"contains"}`)
	req := httptest.NewRequest("POST", "/", body)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.CreateRule(rr, req)
	var tr triageRuleResponse
	mustUnmarshal(t, rr, &tr)

	body2 := bytes.NewBufferString(`{"assignee_id":"agent-worker"}`)
	req2 := httptest.NewRequest("PATCH", "/", body2)
	req2.SetPathValue("ruleId", tr.ID)
	req2 = req2.WithContext(withWorkspace(withUser(req2.Context(), &AuthUser{ID: userID}), wsID, "OWNER"))
	rr2 := httptest.NewRecorder()
	h.UpdateRule(rr2, req2)

	if rr2.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (same-workspace assignee must be allowed); body=%s", rr2.Code, rr2.Body.String())
	}
	var resp triageRuleResponse
	mustUnmarshal(t, rr2, &resp)
	if resp.AssigneeID == nil || *resp.AssigneeID != "agent-worker" {
		t.Errorf("assignee_id = %v, want agent-worker", resp.AssigneeID)
	}
}
