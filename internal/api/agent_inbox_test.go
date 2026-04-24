package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestAgentInbox_Consolidated(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewAgentInboxHandler(db, newTestLogger())

	seedCrewRow(t, db, "crew-inbox", wsID, "Inbox", "inbox")
	seedAgentRow(t, db, "agent-alpha", wsID, "crew-inbox", "Alpha", "alpha", "AGENT")
	seedAgentRow(t, db, "agent-beta", wsID, "crew-inbox", "Beta", "beta", "AGENT")

	// Empty inbox — agent with no rows in any of the four tables
	req := httptest.NewRequest("GET", "/api/v1/agents/agent-alpha/inbox", nil)
	req.SetPathValue("agentId", "agent-alpha")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Handle(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("empty inbox status = %d, body: %s", rr.Code, rr.Body.String())
	}
	var resp agentInboxResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.ApprovalsPending != 0 || resp.AssignmentsOpen != 0 || resp.EscalationsOpen != 0 {
		t.Errorf("empty inbox counts = approvals=%d assignments=%d escalations=%d, want 0/0/0",
			resp.ApprovalsPending, resp.AssignmentsOpen, resp.EscalationsOpen)
	}
	if len(resp.PeerMessages) != 0 {
		t.Errorf("peer messages = %d, want 0", len(resp.PeerMessages))
	}

	// Seed 2 pending approvals + 1 decided approval — count should be 2
	_, err := db.Exec(`INSERT INTO approvals_queue (id, workspace_id, crew_id, agent_id, mission_id, requested_by, kind, reason, status, created_at)
		VALUES ('ap1', ?, 'crew-inbox', 'agent-alpha', NULL, 'alpha', 'tool_call', 'needs review', 'pending', ?),
		       ('ap2', ?, 'crew-inbox', 'agent-alpha', NULL, 'alpha', 'tool_call', 'needs review', 'pending', ?),
		       ('ap3', ?, 'crew-inbox', 'agent-alpha', NULL, 'alpha', 'tool_call', 'already decided', 'approved', ?),
		       ('ap4', ?, 'crew-inbox', 'agent-beta', NULL, 'beta', 'tool_call', 'other agent', 'pending', ?)`,
		wsID, time.Now().Format(time.RFC3339),
		wsID, time.Now().Format(time.RFC3339),
		wsID, time.Now().Format(time.RFC3339),
		wsID, time.Now().Format(time.RFC3339))
	if err != nil {
		t.Fatalf("seed approvals: %v", err)
	}

	// Seed chats so assignments + peer_conversations FK constraints pass
	_, err = db.Exec(`INSERT INTO chats (id, workspace_id, agent_id, status, created_at)
		VALUES ('chat-1', ?, 'agent-alpha', 'ACTIVE', ?),
		       ('chat-2', ?, 'agent-alpha', 'ACTIVE', ?)`,
		wsID, time.Now().Format(time.RFC3339),
		wsID, time.Now().Format(time.RFC3339))
	if err != nil {
		t.Fatalf("seed chats: %v", err)
	}

	// Seed 1 open assignment (queued) + 1 completed — only open counted
	_, err = db.Exec(`INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status, created_at)
		VALUES ('as1', ?, 'chat-1', 'agent-beta', 'agent-alpha', 'do stuff', 'queued', ?),
		       ('as2', ?, 'chat-2', 'agent-beta', 'agent-alpha', 'completed task', 'completed', ?)`,
		wsID, time.Now().Format(time.RFC3339),
		wsID, time.Now().Format(time.RFC3339))
	if err != nil {
		t.Fatalf("seed assignments: %v", err)
	}

	// Seed 1 peer conversation incoming (beta -> alpha) + 1 outgoing (alpha -> beta)
	_, err = db.Exec(`INSERT INTO peer_conversations (id, workspace_id, crew_id, chat_id, from_agent_id, to_agent_id, question, status, created_at)
		VALUES ('pc1', ?, 'crew-inbox', 'chat-1', 'agent-beta', 'agent-alpha', 'Can you help?', 'open', ?),
		       ('pc2', ?, 'crew-inbox', 'chat-2', 'agent-alpha', 'agent-beta', 'Here is the answer', 'answered', ?)`,
		wsID, time.Now().Format(time.RFC3339),
		wsID, time.Now().Add(time.Second).Format(time.RFC3339))
	if err != nil {
		t.Fatalf("seed peer_conversations: %v", err)
	}

	req2 := httptest.NewRequest("GET", "/api/v1/agents/agent-alpha/inbox", nil)
	req2.SetPathValue("agentId", "agent-alpha")
	req2 = withWorkspaceUser(req2, userID, wsID, "OWNER")
	rr2 := httptest.NewRecorder()
	h.Handle(rr2, req2)
	if rr2.Code != http.StatusOK {
		t.Fatalf("populated inbox status = %d, body: %s", rr2.Code, rr2.Body.String())
	}
	var resp2 agentInboxResponse
	json.Unmarshal(rr2.Body.Bytes(), &resp2)
	if resp2.ApprovalsPending != 2 {
		t.Errorf("approvals pending = %d, want 2 (1 decided, 1 belongs to beta)", resp2.ApprovalsPending)
	}
	if resp2.AssignmentsOpen != 1 {
		t.Errorf("assignments open = %d, want 1 (queued only, completed skipped)", resp2.AssignmentsOpen)
	}
	if len(resp2.PeerMessages) != 2 {
		t.Errorf("peer messages = %d, want 2 (both directions)", len(resp2.PeerMessages))
	}
	// Most recent first — pc2 is 1s newer than pc1
	if len(resp2.PeerMessages) >= 1 && resp2.PeerMessages[0].ID != "pc2" {
		t.Errorf("first peer message ID = %s, want pc2 (newer)", resp2.PeerMessages[0].ID)
	}
	if len(resp2.PeerMessages) >= 2 {
		if resp2.PeerMessages[0].Direction != "outgoing" {
			t.Errorf("pc2 direction = %s, want outgoing (alpha is sender)", resp2.PeerMessages[0].Direction)
		}
		if resp2.PeerMessages[1].Direction != "incoming" {
			t.Errorf("pc1 direction = %s, want incoming (alpha is recipient)", resp2.PeerMessages[1].Direction)
		}
	}

	// Cross-tenant isolation: unknown agent in this workspace → 404
	req3 := httptest.NewRequest("GET", "/api/v1/agents/unknown-agent/inbox", nil)
	req3.SetPathValue("agentId", "unknown-agent")
	req3 = withWorkspaceUser(req3, userID, wsID, "OWNER")
	rr3 := httptest.NewRecorder()
	h.Handle(rr3, req3)
	if rr3.Code != http.StatusNotFound {
		t.Errorf("unknown agent status = %d, want 404", rr3.Code)
	}

	// Missing agentId → 400
	req4 := httptest.NewRequest("GET", "/api/v1/agents//inbox", nil)
	req4 = withWorkspaceUser(req4, userID, wsID, "OWNER")
	rr4 := httptest.NewRecorder()
	h.Handle(rr4, req4)
	if rr4.Code != http.StatusBadRequest {
		t.Errorf("missing agentId status = %d, want 400", rr4.Code)
	}
}
