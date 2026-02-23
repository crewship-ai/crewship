package sidecar

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// mockCrewshipdKeeperExecute starts a test HTTP server that responds to /keeper/execute.
func mockCrewshipdKeeperExecute(t *testing.T, statusCode int, response map[string]interface{}) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/internal/keeper/execute" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("X-Internal-Token") != "test-token" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(statusCode)
		json.NewEncoder(w).Encode(response)
	}))
}

// TestHandleKeeperExecute_NoIPC verifies 503 when IPC is not configured.
func TestHandleKeeperExecute_NoIPC(t *testing.T) {
	srv := NewServer(ServerConfig{Addr: "127.0.0.1:0"})

	body := strings.NewReader(`{"credential_id":"cred1","intent":"I need to deploy","command":"gh pr list"}`)
	req := httptest.NewRequest(http.MethodPost, "/keeper/execute", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	srv.handleKeeperExecute(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 (no IPC), got %d: %s", w.Code, w.Body.String())
	}
}

// TestHandleKeeperExecute_MissingCommand_Rejected verifies that requests without
// a command field are rejected with 400.
func TestHandleKeeperExecute_MissingCommand_Rejected(t *testing.T) {
	srv := NewServer(ServerConfig{
		Addr: "127.0.0.1:0",
		IPC: &IPCConfig{
			BaseURL:     "http://127.0.0.1:19998",
			Token:       "tok",
			AgentID:     "agent1",
			ContainerID: "container1",
		},
	})

	// Missing command field
	body := strings.NewReader(`{"credential_id":"cred1","intent":"I need to deploy","command":""}`)
	req := httptest.NewRequest(http.MethodPost, "/keeper/execute", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	srv.handleKeeperExecute(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for missing command, got %d: %s", w.Code, w.Body.String())
	}
}

// TestHandleKeeperExecute_OversizedCommand_Rejected verifies that commands
// exceeding maxCommandLength are rejected with 400.
func TestHandleKeeperExecute_OversizedCommand_Rejected(t *testing.T) {
	srv := NewServer(ServerConfig{
		Addr: "127.0.0.1:0",
		IPC: &IPCConfig{
			BaseURL:     "http://127.0.0.1:19998",
			Token:       "tok",
			AgentID:     "agent1",
			ContainerID: "container1",
		},
	})

	oversized, _ := json.Marshal(map[string]string{
		"credential_id": "cred1",
		"intent":        "deploy",
		"command":       strings.Repeat("x", maxCommandLength+1),
	})
	req := httptest.NewRequest(http.MethodPost, "/keeper/execute", bytes.NewReader(oversized))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	srv.handleKeeperExecute(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for oversized command, got %d: %s", w.Code, w.Body.String())
	}
}

// TestHandleKeeperExecute_AgentIDNotOverrideable verifies that the agent_id
// forwarded to crewshipd is always the IPC config value, not whatever the
// agent sends in the request body.
func TestHandleKeeperExecute_AgentIDNotOverrideable(t *testing.T) {
	var capturedBody map[string]string

	fakeSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&capturedBody)
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{"decision": "DENY"})
	}))
	defer fakeSrv.Close()

	srv := NewServer(ServerConfig{
		Addr: "127.0.0.1:0",
		IPC: &IPCConfig{
			BaseURL:     fakeSrv.URL,
			Token:       "test-token",
			AgentID:     "real-agent-id",
			CrewID:      "crew1",
			WorkspaceID: "ws1",
			ContainerID: "container-abc",
		},
	})

	// Agent tries to claim a different agent_id
	reqBody, _ := json.Marshal(map[string]string{
		"credential_id":       "cred1",
		"intent":              "I need to list PRs for my project",
		"command":             "gh pr list",
		"requesting_agent_id": "evil-agent-id", // should be ignored
	})
	req := httptest.NewRequest(http.MethodPost, "/keeper/execute", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	srv.handleKeeperExecute(w, req)

	if capturedBody["requesting_agent_id"] != "real-agent-id" {
		t.Errorf("expected requesting_agent_id='real-agent-id', got %q",
			capturedBody["requesting_agent_id"])
	}
}

// TestHandleKeeperExecute_ForwardsContainerID_FromIPCConfig verifies that the
// container_id forwarded to crewshipd always comes from the IPC config, never
// from the agent's request body.
func TestHandleKeeperExecute_ForwardsContainerID_FromIPCConfig(t *testing.T) {
	var capturedBody map[string]string

	fakeSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&capturedBody)
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{"decision": "DENY"})
	}))
	defer fakeSrv.Close()

	const ipcContainerID = "secure-container-from-ipc"

	srv := NewServer(ServerConfig{
		Addr: "127.0.0.1:0",
		IPC: &IPCConfig{
			BaseURL:     fakeSrv.URL,
			Token:       "test-token",
			AgentID:     "agent1",
			CrewID:      "crew1",
			WorkspaceID: "ws1",
			ContainerID: ipcContainerID,
		},
	})

	// Agent tries to inject a different container_id (attack vector)
	reqBody, _ := json.Marshal(map[string]string{
		"credential_id": "cred1",
		"intent":        "I need to list pull requests from the repo",
		"command":       "gh pr list",
		"container_id":  "evil-container-id", // should be ignored
	})
	req := httptest.NewRequest(http.MethodPost, "/keeper/execute", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	srv.handleKeeperExecute(w, req)

	if capturedBody["container_id"] != ipcContainerID {
		t.Errorf("expected container_id=%q from IPC, got %q", ipcContainerID, capturedBody["container_id"])
	}
}

// TestHandleKeeperExecute_ForwardsToCrewshipd verifies the happy path: valid request
// is forwarded to crewshipd and the response is proxied back.
func TestHandleKeeperExecute_ForwardsToCrewshipd(t *testing.T) {
	expectedResponse := map[string]interface{}{
		"request_id": "req-execute-123",
		"decision":   "ALLOW",
		"output":     "PR #1 feat: add feature\nPR #2 fix: fix bug",
		"exit_code":  float64(0),
	}
	fakeSrv := mockCrewshipdKeeperExecute(t, 200, expectedResponse)
	defer fakeSrv.Close()

	srv := NewServer(ServerConfig{
		Addr: "127.0.0.1:0",
		IPC: &IPCConfig{
			BaseURL:     fakeSrv.URL,
			Token:       "test-token",
			AgentID:     "agent1",
			CrewID:      "crew1",
			WorkspaceID: "ws1",
			ContainerID: "container-abc",
		},
	})

	reqBody, _ := json.Marshal(map[string]string{
		"credential_id": "cred-gh",
		"intent":        "List pull requests to review team progress",
		"command":       "gh pr list --repo org/repo",
	})
	req := httptest.NewRequest(http.MethodPost, "/keeper/execute", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	srv.handleKeeperExecute(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var result map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if result["decision"] != "ALLOW" {
		t.Errorf("expected ALLOW, got %v", result["decision"])
	}
	if result["output"] == "" {
		t.Error("expected non-empty output")
	}
}

// TestHandleKeeperExecute_NullBytesInCommand_Rejected verifies that null bytes
// in the command field are rejected.
func TestHandleKeeperExecute_NullBytesInCommand_Rejected(t *testing.T) {
	srv := NewServer(ServerConfig{
		Addr: "127.0.0.1:0",
		IPC:  &IPCConfig{BaseURL: "http://x", Token: "tok", AgentID: "a1", ContainerID: "c1"},
	})

	// JSON with null byte in command
	raw := []byte(`{"credential_id":"cred1","intent":"I need to list PRs now","command":"gh\x00inject"}`)
	req := httptest.NewRequest(http.MethodPost, "/keeper/execute", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	srv.handleKeeperExecute(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for null bytes in command, got %d: %s", w.Code, w.Body.String())
	}
}
