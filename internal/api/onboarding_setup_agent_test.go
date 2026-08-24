package api

// Tests for onboarding_setup_agent.go — POST /api/v1/onboarding/setup-agent/start.
// See that file's own doc comment for the sequencing decision these tests
// pin: refuse with a machine-readable reason when the workspace has no
// credential yet, rather than open a chat the setup agent could never
// answer.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestStartSetupAgent_Unauthenticated(t *testing.T) {
	db := setupTestDB(t)
	h := NewOnboardingHandler(db, nil, testLogger())
	req := httptest.NewRequest(http.MethodPost, "/api/v1/onboarding/setup-agent/start", nil)
	w := httptest.NewRecorder()

	h.StartSetupAgent(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d; body=%s", w.Code, http.StatusUnauthorized, w.Body.String())
	}
}

func TestStartSetupAgent_NoCredential_RefusesWithReasonAndCreatesNoRows(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	h := NewOnboardingHandler(db, nil, testLogger())
	req := httptest.NewRequest(http.MethodPost, "/api/v1/onboarding/setup-agent/start", nil)
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: userID}))
	w := httptest.NewRecorder()

	h.StartSetupAgent(w, req)

	if w.Code != http.StatusPreconditionRequired {
		t.Fatalf("status = %d, want %d (Precondition Required); body=%s", w.Code, http.StatusPreconditionRequired, w.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if body["reason"] != "credential_required" {
		t.Errorf("reason = %q, want %q", body["reason"], "credential_required")
	}
	if body["error"] == "" {
		t.Error("expected a human-readable error message alongside the machine reason")
	}

	// The one property this endpoint exists to guarantee: refusing must not
	// leave behind a crew/agent/chat the agent could never have answered in.
	var crewCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM crews WHERE workspace_id = ? AND kind = ?", wsID, setupCrewKindSetup).Scan(&crewCount); err != nil {
		t.Fatalf("count crews: %v", err)
	}
	if crewCount != 0 {
		t.Errorf("crew count = %d, want 0 — a refusal must not create the setup crew", crewCount)
	}
}

func TestStartSetupAgent_WithCredential_ReturnsAgentAndSession(t *testing.T) {
	setTestEncryptionKeyParallelSafe(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	if err := insertOnboardingCredential(context.Background(), db, userID, wsID, "API Key", "ANTHROPIC", "ANTHROPIC_API_KEY", "sk-ant-oat01-fake", isoMillisNow()); err != nil {
		t.Fatalf("insert credential: %v", err)
	}

	h := NewOnboardingHandler(db, nil, testLogger())
	req := httptest.NewRequest(http.MethodPost, "/api/v1/onboarding/setup-agent/start", nil)
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: userID}))
	w := httptest.NewRecorder()

	h.StartSetupAgent(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", w.Code, http.StatusOK, w.Body.String())
	}
	var resp onboardingSetupAgentStartResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.AgentID == "" {
		t.Error("agent_id is empty")
	}
	if resp.SessionID == "" {
		t.Error("session_id is empty")
	}

	// session_id must be the setup chat's own id — not a fresh, unrelated
	// value — since that is the exact identifier the WS layer resolves
	// messages against (chats.id / conversation_messages.session_id).
	var chatAgentID string
	if err := db.QueryRow("SELECT agent_id FROM chats WHERE id = ?", resp.SessionID).Scan(&chatAgentID); err != nil {
		t.Fatalf("read chat by returned session_id: %v", err)
	}
	if chatAgentID != resp.AgentID {
		t.Errorf("chat.agent_id = %q, want %q (the returned agent_id)", chatAgentID, resp.AgentID)
	}

	var agentSlug string
	if err := db.QueryRow("SELECT slug FROM agents WHERE id = ?", resp.AgentID).Scan(&agentSlug); err != nil {
		t.Fatalf("read agent: %v", err)
	}
	if agentSlug != setupAgentSlug {
		t.Errorf("agent slug = %q, want %q", agentSlug, setupAgentSlug)
	}
}

func TestStartSetupAgent_Idempotent_SecondCallReturnsSameAgentAndSession(t *testing.T) {
	setTestEncryptionKeyParallelSafe(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	if err := insertOnboardingCredential(context.Background(), db, userID, wsID, "API Key", "ANTHROPIC", "ANTHROPIC_API_KEY", "sk-ant-oat01-fake", isoMillisNow()); err != nil {
		t.Fatalf("insert credential: %v", err)
	}

	h := NewOnboardingHandler(db, nil, testLogger())
	call := func() onboardingSetupAgentStartResponse {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/onboarding/setup-agent/start", nil)
		req = req.WithContext(withUser(req.Context(), &AuthUser{ID: userID}))
		w := httptest.NewRecorder()
		h.StartSetupAgent(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d; body=%s", w.Code, http.StatusOK, w.Body.String())
		}
		var resp onboardingSetupAgentStartResponse
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("unmarshal response: %v", err)
		}
		return resp
	}

	first := call()
	second := call()

	if first.AgentID != second.AgentID {
		t.Errorf("agent_id changed across calls: first=%q second=%q", first.AgentID, second.AgentID)
	}
	if first.SessionID != second.SessionID {
		t.Errorf("session_id changed across calls: first=%q second=%q", first.SessionID, second.SessionID)
	}

	var crewCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM crews WHERE workspace_id = ? AND kind = ?", wsID, setupCrewKindSetup).Scan(&crewCount); err != nil {
		t.Fatalf("count crews: %v", err)
	}
	if crewCount != 1 {
		t.Errorf("crew count after two calls = %d, want 1 (must not duplicate)", crewCount)
	}
}

func TestStartSetupAgent_NoWorkspace_BadRequest(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	// Deliberately no seedTestWorkspace call: this user has no
	// workspace_members row at all.

	h := NewOnboardingHandler(db, nil, testLogger())
	req := httptest.NewRequest(http.MethodPost, "/api/v1/onboarding/setup-agent/start", nil)
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: userID}))
	w := httptest.NewRecorder()

	h.StartSetupAgent(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", w.Code, http.StatusBadRequest, w.Body.String())
	}
}
