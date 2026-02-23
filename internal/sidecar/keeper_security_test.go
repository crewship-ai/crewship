package sidecar

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestHandleKeeperRequest_OversizedIntent_Rejected verifies that intents larger
// than maxIntentLength are rejected to prevent DoS and prompt injection via huge payloads.
func TestHandleKeeperRequest_OversizedIntent_Rejected(t *testing.T) {
	srv := newKeeperServer(t, &IPCConfig{BaseURL: "http://x", Token: "tok", AgentID: "a1"})

	// 100,000 character intent — well above the 4096 limit
	oversizedIntent := strings.Repeat("A", 100_000)
	body, _ := json.Marshal(map[string]string{
		"credential_id": "cred1",
		"intent":        oversizedIntent,
	})
	req := httptest.NewRequest(http.MethodPost, "/keeper/request", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.handleKeeperRequest(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for oversized intent (%d chars), got %d: %s",
			len(oversizedIntent), w.Code, w.Body.String())
	}
}

// TestHandleKeeperRequest_CredentialID_SQLInjection_Attempt verifies that credential
// IDs containing SQL meta-characters are rejected at the format validation layer.
// Note: prepared statements already prevent SQL injection, but format validation
// provides defence-in-depth and catches injection attempts early.
func TestHandleKeeperRequest_CredentialID_SQLInjection_Attempt(t *testing.T) {
	srv := newKeeperServer(t, &IPCConfig{BaseURL: "http://x", Token: "tok", AgentID: "a1"})

	maliciousIDs := []string{
		"'; DROP TABLE credentials; --",
		"1 OR 1=1",
		"cred' UNION SELECT * FROM users--",
		"cred\"; UPDATE credentials SET security_level=1; --",
	}

	for _, malID := range maliciousIDs {
		body, _ := json.Marshal(map[string]string{
			"credential_id": malID,
			"intent":        "I need this credential to deploy",
		})
		req := httptest.NewRequest(http.MethodPost, "/keeper/request", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		srv.handleKeeperRequest(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("expected 400 for SQL injection attempt %q, got %d: %s",
				malID, w.Code, w.Body.String())
		}
	}
}

// TestHandleKeeperRequest_CredentialID_PathTraversal_Attempt verifies that
// path traversal sequences in credential IDs are rejected.
func TestHandleKeeperRequest_CredentialID_PathTraversal_Attempt(t *testing.T) {
	srv := newKeeperServer(t, &IPCConfig{BaseURL: "http://x", Token: "tok", AgentID: "a1"})

	traversalIDs := []string{
		"../../../etc/passwd",
		"..%2F..%2Fetc%2Fshadow",
		"/etc/passwd",
		"cred/../../../etc/hosts",
	}

	for _, malID := range traversalIDs {
		body, _ := json.Marshal(map[string]string{
			"credential_id": malID,
			"intent":        "I need this credential to deploy",
		})
		req := httptest.NewRequest(http.MethodPost, "/keeper/request", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		srv.handleKeeperRequest(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("expected 400 for path traversal attempt %q, got %d: %s",
				malID, w.Code, w.Body.String())
		}
	}
}

// TestHandleKeeperRequest_NullBytesInFields_Rejected verifies that null bytes
// in the intent field are rejected (can indicate binary injection attempts).
func TestHandleKeeperRequest_NullBytesInFields_Rejected(t *testing.T) {
	srv := newKeeperServer(t, &IPCConfig{BaseURL: "http://x", Token: "tok", AgentID: "a1"})

	// Build the JSON manually to embed a null byte — json.Marshal would escape it
	body := fmt.Sprintf(`{"credential_id":"valid-cred","intent":"deploy%s injected"}`, "\x00")
	req := httptest.NewRequest(http.MethodPost, "/keeper/request", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.handleKeeperRequest(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for null byte in intent, got %d: %s", w.Code, w.Body.String())
	}
}

// TestHandleKeeperRequest_AgentIDNotOverrideable verifies that the sidecar always
// uses its configured IPC agent_id, not any agent_id supplied in the request body.
// This is critical: the agent cannot escalate privileges by claiming a different identity.
func TestHandleKeeperRequest_AgentIDNotOverrideable(t *testing.T) {
	var capturedBody map[string]string

	fakeSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Internal-Token") != "test-token" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		json.NewDecoder(r.Body).Decode(&capturedBody)
		json.NewEncoder(w).Encode(map[string]interface{}{"decision": "DENY", "reason": "test"})
	}))
	defer fakeSrv.Close()

	srv := newKeeperServer(t, &IPCConfig{
		BaseURL:     fakeSrv.URL,
		Token:       "test-token",
		AgentID:     "real-agent-id",
		CrewID:      "real-crew",
		WorkspaceID: "real-ws",
		ChatID:      "real-chat",
	})

	// The request body includes a requesting_agent_id field attempting to override
	// the sidecar's configured identity
	body := `{"credential_id":"valid-cred","intent":"I need this cred to deploy","requesting_agent_id":"evil-agent-override"}`
	req := httptest.NewRequest(http.MethodPost, "/keeper/request", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.handleKeeperRequest(w, req)

	// The forwarded IPC request must use the sidecar's configured agent_id
	if capturedBody["requesting_agent_id"] != "real-agent-id" {
		t.Errorf("expected forwarded requesting_agent_id='real-agent-id', got %q",
			capturedBody["requesting_agent_id"])
	}
	// The evil override must not appear
	if capturedBody["requesting_agent_id"] == "evil-agent-override" {
		t.Error("agent identity override was not blocked — critical security violation")
	}
}

// TestHandleKeeperRequest_ValidCredentialID_Allowed verifies that credential IDs
// with only valid characters (alphanumeric, hyphen, underscore) are accepted.
func TestHandleKeeperRequest_ValidCredentialID_Allowed(t *testing.T) {
	fakeSrv := mockCrewshipdKeeper(t, 200, map[string]interface{}{"decision": "DENY"})
	defer fakeSrv.Close()

	srv := newKeeperServer(t, &IPCConfig{
		BaseURL: fakeSrv.URL, Token: "test-token",
		AgentID: "a1", CrewID: "c1", WorkspaceID: "ws1",
	})

	validIDs := []string{
		"cred-123",
		"my_credential",
		"CRED-ABC-xyz",
		"cred123",
	}

	for _, credID := range validIDs {
		body, _ := json.Marshal(map[string]string{
			"credential_id": credID,
			"intent":        "I need this credential to deploy the service",
		})
		req := httptest.NewRequest(http.MethodPost, "/keeper/request", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		srv.handleKeeperRequest(w, req)

		if w.Code == http.StatusBadRequest {
			t.Errorf("expected valid cred_id %q to be accepted, got 400: %s", credID, w.Body.String())
		}
	}
}
