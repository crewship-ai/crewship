package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/config"
	"github.com/crewship-ai/crewship/internal/logging"
)

func newTestServer() *Server {
	cfg := config.Default()
	logger := logging.New("error", "json", nil)
	s := New(cfg, logger)
	s.startedAt = time.Now()
	return s
}

func TestHealthz(t *testing.T) {
	s := newTestServer()
	req := httptest.NewRequest("GET", "/healthz", nil)
	w := httptest.NewRecorder()

	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	var body map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["status"] != "ok" {
		t.Errorf("expected status ok, got %v", body["status"])
	}
	if body["service"] != "crewshipd" {
		t.Errorf("expected service crewshipd, got %v", body["service"])
	}
}

func TestReadyz(t *testing.T) {
	s := newTestServer()
	req := httptest.NewRequest("GET", "/readyz", nil)
	w := httptest.NewRecorder()

	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	var body map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &body)
	if body["status"] != "ready" {
		t.Errorf("expected status ready, got %v", body["status"])
	}
}

func TestMetrics(t *testing.T) {
	s := newTestServer()
	req := httptest.NewRequest("GET", "/metrics", nil)
	w := httptest.NewRecorder()

	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	ct := w.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("expected text/plain content type, got %s", ct)
	}

	body := w.Body.String()
	expectedMetrics := []string{
		"crewshipd_uptime_seconds",
		"crewshipd_goroutines",
		"crewshipd_memory_alloc_bytes",
		"crewshipd_ws_connections",
	}
	for _, m := range expectedMetrics {
		if !strings.Contains(body, m) {
			t.Errorf("expected metric %s in output", m)
		}
	}
}

func TestWebSocketMissingToken(t *testing.T) {
	s := newTestServer()
	req := httptest.NewRequest("GET", "/ws", nil)
	w := httptest.NewRecorder()

	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", w.Code)
	}
}

func TestIPCHealth(t *testing.T) {
	s := newTestServer()
	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()

	s.ipcMux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestIPCAgentStatus(t *testing.T) {
	s := newTestServer()
	req := httptest.NewRequest("GET", "/agents/test-uuid/status", nil)
	w := httptest.NewRecorder()

	s.ipcMux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	var body map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &body)
	if body["agent_id"] != "test-uuid" {
		t.Errorf("expected agent_id test-uuid, got %v", body["agent_id"])
	}
}

func TestIPCAgentStart(t *testing.T) {
	s := newTestServer()
	req := httptest.NewRequest("POST", "/agents/test-uuid/start", nil)
	w := httptest.NewRecorder()

	s.ipcMux.ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Errorf("expected 202, got %d", w.Code)
	}
}

func TestIPCContainerStatus(t *testing.T) {
	s := newTestServer()
	req := httptest.NewRequest("GET", "/teams/team-uuid/container/status", nil)
	w := httptest.NewRecorder()

	s.ipcMux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestIPCFileList(t *testing.T) {
	s := newTestServer()
	req := httptest.NewRequest("GET", "/teams/team-uuid/files", nil)
	w := httptest.NewRecorder()

	s.ipcMux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	var body map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &body)
	if body["team_id"] != "team-uuid" {
		t.Errorf("expected team_id team-uuid, got %v", body["team_id"])
	}
}

func TestIPCSessionMessages(t *testing.T) {
	s := newTestServer()
	req := httptest.NewRequest("GET", "/sessions/session-uuid/messages", nil)
	w := httptest.NewRecorder()

	s.ipcMux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}
