package api

import (
	"bytes"
	"net/http/httptest"
	"testing"
	"time"
)

// TestAgentCred_Add_AutoResolvesMatchingEscalation covers issue #1198:
// a human granting an agent's credential need via `credential create` +
// `credential assign` (rather than `escalation resolve --action approve`)
// must close out the matching PENDING escalation, not leave it stuck
// forever.
func TestAgentCred_Add_AutoResolvesMatchingEscalation(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	_, wsID, agentID, credID := seedAgentCredEnv(t, db)
	h := newAgentHandlerForCred(t, db)

	// seedAgentCredEnv names the credential "test-cred" (see seedCredentialEnc
	// call in agent_credentials_test.go).
	crewID, chatID := "crew-1", "chat-1"
	now := time.Now().UTC().Format(time.RFC3339)
	escID := "esc-match"
	if _, err := db.Exec(`INSERT INTO escalations(id,workspace_id,crew_id,chat_id,from_agent_id,reason,status,created_at)
		VALUES (?, ?, ?, ?, ?, ?, 'PENDING', ?)`,
		escID, wsID, crewID, chatID, agentID, "Need test-cred to call the API, I don't have it", now); err != nil {
		t.Fatalf("seed escalation: %v", err)
	}

	body := bytes.NewBufferString(`{"credential_id":"` + credID + `","env_var_name":"GH_TOKEN"}`)
	req := httptest.NewRequest("POST", "/api/v1/agents/"+agentID+"/credentials", body)
	req.SetPathValue("agentId", agentID)
	ctx := withWorkspace(req.Context(), wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.AddCredential(rr, req)
	if rr.Code != 201 {
		t.Fatalf("AddCredential status = %d, body: %s", rr.Code, rr.Body.String())
	}

	var status, resolution, resolvedBy string
	if err := db.QueryRow(`SELECT status, COALESCE(resolution,''), COALESCE(resolved_by,'') FROM escalations WHERE id = ?`, escID).
		Scan(&status, &resolution, &resolvedBy); err != nil {
		t.Fatalf("query escalation: %v", err)
	}
	if status != "RESOLVED" {
		t.Errorf("escalation status = %q, want RESOLVED", status)
	}
	if resolvedBy != "system" {
		t.Errorf("resolved_by = %q, want system", resolvedBy)
	}
	if resolution == "" {
		t.Errorf("resolution should be populated, got empty")
	}
}

// TestAgentCred_Add_DoesNotAutoResolveOtherAgentEscalation ensures the
// matching is scoped to the SAME agent the credential was assigned to — a
// PENDING escalation from a different agent (even one that happens to
// mention the same credential name) must not be touched.
func TestAgentCred_Add_DoesNotAutoResolveOtherAgentEscalation(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	userID, wsID, agentID, credID := seedAgentCredEnv(t, db)
	h := newAgentHandlerForCred(t, db)

	otherAgentID := "agent-other"
	if _, err := db.Exec(`INSERT INTO agents (id, workspace_id, name, slug) VALUES (?, ?, 'B', 'b')`, otherAgentID, wsID); err != nil {
		t.Fatalf("seed other agent: %v", err)
	}
	_ = userID

	crewID, chatID := "crew-1", "chat-1"
	now := time.Now().UTC().Format(time.RFC3339)
	escID := "esc-other-agent"
	if _, err := db.Exec(`INSERT INTO escalations(id,workspace_id,crew_id,chat_id,from_agent_id,reason,status,created_at)
		VALUES (?, ?, ?, ?, ?, ?, 'PENDING', ?)`,
		escID, wsID, crewID, chatID, otherAgentID, "Need test-cred to call the API", now); err != nil {
		t.Fatalf("seed escalation: %v", err)
	}

	body := bytes.NewBufferString(`{"credential_id":"` + credID + `","env_var_name":"GH_TOKEN"}`)
	req := httptest.NewRequest("POST", "/api/v1/agents/"+agentID+"/credentials", body)
	req.SetPathValue("agentId", agentID)
	ctx := withWorkspace(req.Context(), wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.AddCredential(rr, req)
	if rr.Code != 201 {
		t.Fatalf("AddCredential status = %d, body: %s", rr.Code, rr.Body.String())
	}

	var status string
	if err := db.QueryRow(`SELECT status FROM escalations WHERE id = ?`, escID).Scan(&status); err != nil {
		t.Fatalf("query escalation: %v", err)
	}
	if status != "PENDING" {
		t.Errorf("escalation status = %q, want PENDING (different agent, must not auto-resolve)", status)
	}
}

// TestAgentCred_Add_DoesNotAutoResolveUnrelatedEscalation ensures a PENDING
// escalation from the SAME agent that doesn't mention the credential name
// is left untouched — matching requires a whole-word name match, not just
// "same agent has any pending escalation".
func TestAgentCred_Add_DoesNotAutoResolveUnrelatedEscalation(t *testing.T) {
	t.Parallel()
	db := setupTestDB(t)
	_, wsID, agentID, credID := seedAgentCredEnv(t, db)
	h := newAgentHandlerForCred(t, db)

	crewID, chatID := "crew-1", "chat-1"
	now := time.Now().UTC().Format(time.RFC3339)
	escID := "esc-unrelated"
	if _, err := db.Exec(`INSERT INTO escalations(id,workspace_id,crew_id,chat_id,from_agent_id,reason,status,created_at)
		VALUES (?, ?, ?, ?, ?, ?, 'PENDING', ?)`,
		escID, wsID, crewID, chatID, agentID, "Not sure how to proceed with the task, please advise", now); err != nil {
		t.Fatalf("seed escalation: %v", err)
	}

	body := bytes.NewBufferString(`{"credential_id":"` + credID + `","env_var_name":"GH_TOKEN"}`)
	req := httptest.NewRequest("POST", "/api/v1/agents/"+agentID+"/credentials", body)
	req.SetPathValue("agentId", agentID)
	ctx := withWorkspace(req.Context(), wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.AddCredential(rr, req)
	if rr.Code != 201 {
		t.Fatalf("AddCredential status = %d, body: %s", rr.Code, rr.Body.String())
	}

	var status string
	if err := db.QueryRow(`SELECT status FROM escalations WHERE id = ?`, escID).Scan(&status); err != nil {
		t.Fatalf("query escalation: %v", err)
	}
	if status != "PENDING" {
		t.Errorf("escalation status = %q, want PENDING (reason doesn't mention credential name)", status)
	}
}
