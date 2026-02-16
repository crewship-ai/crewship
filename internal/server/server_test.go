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
	s := New(cfg, logger, nil)
	s.startedAt = time.Now()
	return s
}

func parseJSON(t *testing.T, data []byte) map[string]interface{} {
	t.Helper()
	var body map[string]interface{}
	if err := json.Unmarshal(data, &body); err != nil {
		t.Fatalf("invalid JSON response: %v, body: %s", err, string(data))
	}
	return body
}

func TestHealthz(t *testing.T) {
	s := newTestServer()
	req := httptest.NewRequest("GET", "/healthz", nil)
	w := httptest.NewRecorder()

	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	body := parseJSON(t, w.Body.Bytes())
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

	body := parseJSON(t, w.Body.Bytes())
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

	output := w.Body.String()
	expectedMetrics := []string{
		"crewshipd_uptime_seconds",
		"crewshipd_goroutines",
		"crewshipd_memory_alloc_bytes",
		"crewshipd_ws_connections",
	}
	for _, m := range expectedMetrics {
		if !strings.Contains(output, m) {
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

func TestIPCEndpoints(t *testing.T) {
	s := newTestServer()

	tests := []struct {
		name       string
		method     string
		path       string
		wantStatus int
		wantField  string
		wantValue  string
	}{
		{"health", "GET", "/health", http.StatusOK, "status", "ok"},
		{"agent status", "GET", "/agents/test-uuid/status", http.StatusOK, "agent_id", "test-uuid"},
		{"agent start", "POST", "/agents/test-uuid/start", http.StatusAccepted, "agent_id", "test-uuid"},
		{"agent stop", "POST", "/agents/test-uuid/stop", http.StatusOK, "agent_id", "test-uuid"},
		{"container status", "GET", "/teams/team-uuid/container/status", http.StatusOK, "team_id", "team-uuid"},
		{"container start", "POST", "/teams/team-uuid/container/start", http.StatusServiceUnavailable, "error", "container provider not configured"},
		{"container stop", "POST", "/teams/team-uuid/container/stop", http.StatusServiceUnavailable, "error", "container provider not configured"},
		{"file list", "GET", "/teams/team-uuid/files", http.StatusOK, "team_id", "team-uuid"},
		{"session messages", "GET", "/sessions/session-uuid/messages", http.StatusOK, "session_id", "session-uuid"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, nil)
			w := httptest.NewRecorder()

			s.ipcMux.ServeHTTP(w, req)

			if w.Code != tt.wantStatus {
				t.Errorf("expected status %d, got %d", tt.wantStatus, w.Code)
			}

			body := parseJSON(t, w.Body.Bytes())
			if body[tt.wantField] != tt.wantValue {
				t.Errorf("expected %s=%q, got %v", tt.wantField, tt.wantValue, body[tt.wantField])
			}
		})
	}
}
