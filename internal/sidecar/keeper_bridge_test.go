package sidecar

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// mockCrewshipdKeeper starts a test HTTP server that responds to keeper requests.
func mockCrewshipdKeeper(t *testing.T, statusCode int, response map[string]interface{}) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/internal/keeper/request" {
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

func newKeeperServer(t *testing.T, ipc *IPCConfig) *Server {
	t.Helper()
	return NewServer(ServerConfig{
		Addr: "127.0.0.1:0",
		IPC:  ipc,
	})
}

func TestHandleKeeperRequest_NoIPC(t *testing.T) {
	srv := newKeeperServer(t, nil)

	body := strings.NewReader(`{"credential_id":"cred1","intent":"I need to deploy"}`)
	req := httptest.NewRequest(http.MethodPost, "/keeper/request", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	srv.handleKeeperRequest(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 (no IPC), got %d: %s", w.Code, w.Body.String())
	}
}

func TestHandleKeeperRequest_InvalidJSON(t *testing.T) {
	srv := newKeeperServer(t, &IPCConfig{BaseURL: "http://x", Token: "tok"})

	req := httptest.NewRequest(http.MethodPost, "/keeper/request", strings.NewReader("not-json"))
	w := httptest.NewRecorder()
	srv.handleKeeperRequest(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestHandleKeeperRequest_MissingFields(t *testing.T) {
	srv := newKeeperServer(t, &IPCConfig{BaseURL: "http://x", Token: "tok", AgentID: "a1"})

	cases := []string{
		`{}`,
		`{"credential_id":"cred1"}`,
		`{"intent":"some intent"}`,
	}

	for _, c := range cases {
		req := httptest.NewRequest(http.MethodPost, "/keeper/request", strings.NewReader(c))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		srv.handleKeeperRequest(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("expected 400 for body %s, got %d", c, w.Code)
		}
	}
}

func TestHandleKeeperRequest_ForwardsToCrewshipd(t *testing.T) {
	expectedDecision := map[string]interface{}{
		"request_id": "req-123",
		"decision":   "ALLOW",
		"reason":     "task matches intent",
		"risk_score": float64(2),
	}
	fakeSrv := mockCrewshipdKeeper(t, 200, expectedDecision)
	defer fakeSrv.Close()

	srv := newKeeperServer(t, &IPCConfig{
		BaseURL:     fakeSrv.URL,
		Token:       "test-token",
		AgentID:     "agent1",
		CrewID:      "crew1",
		WorkspaceID: "ws1",
		ChatID:      "chat1",
	})

	body, _ := json.Marshal(map[string]string{
		"credential_id": "cred-ssh",
		"intent":        "Deploy to staging using SSH key",
	})
	req := httptest.NewRequest(http.MethodPost, "/keeper/request", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.handleKeeperRequest(w, req)

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
}

func TestHandleKeeperRequest_CrewshipdDown_Returns502(t *testing.T) {
	// Point to a port that is not listening
	srv := newKeeperServer(t, &IPCConfig{
		BaseURL:     "http://127.0.0.1:19997",
		Token:       "test-token",
		AgentID:     "agent1",
		CrewID:      "crew1",
		WorkspaceID: "ws1",
		ChatID:      "chat1",
	})

	body := strings.NewReader(`{"credential_id":"cred-ssh","intent":"Deploy"}`)
	req := httptest.NewRequest(http.MethodPost, "/keeper/request", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.handleKeeperRequest(w, req)

	if w.Code != http.StatusBadGateway {
		t.Errorf("expected 502 (crewshipd down), got %d: %s", w.Code, w.Body.String())
	}
}

func TestHandleKeeperRequest_TrimsWhitespace(t *testing.T) {
	fakeSrv := mockCrewshipdKeeper(t, 200, map[string]interface{}{"decision": "DENY"})
	defer fakeSrv.Close()

	srv := newKeeperServer(t, &IPCConfig{
		BaseURL:     fakeSrv.URL,
		Token:       "test-token",
		AgentID:     "agent1",
		CrewID:      "crew1",
		WorkspaceID: "ws1",
		ChatID:      "chat1",
	})

	// Whitespace-only intent should be rejected as empty
	body := strings.NewReader(`{"credential_id":"  cred-ssh  ","intent":"   "}`)
	req := httptest.NewRequest(http.MethodPost, "/keeper/request", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.handleKeeperRequest(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for whitespace-only intent, got %d", w.Code)
	}
}
