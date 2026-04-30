package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/config"
	"github.com/crewship-ai/crewship/internal/conversation"
	"github.com/crewship-ai/crewship/internal/logging"
	"github.com/crewship-ai/crewship/internal/provider"
)

// mock state provider
type mockState struct {
	data map[string]map[string][]byte
}

func newMockState() *mockState {
	return &mockState{data: make(map[string]map[string][]byte)}
}
func (m *mockState) Get(_ context.Context, bucket, key string) ([]byte, error) {
	if b, ok := m.data[bucket]; ok {
		return b[key], nil
	}
	return nil, nil
}
func (m *mockState) Set(_ context.Context, bucket, key string, value []byte) error {
	if m.data[bucket] == nil {
		m.data[bucket] = make(map[string][]byte)
	}
	m.data[bucket][key] = value
	return nil
}
func (m *mockState) Delete(_ context.Context, _, _ string) error                 { return nil }
func (m *mockState) List(_ context.Context, _ string) (map[string][]byte, error) { return nil, nil }
func (m *mockState) ListByPrefix(_ context.Context, _, _ string) (map[string][]byte, error) {
	return nil, nil
}
func (m *mockState) Close() error { return nil }

// mock container provider
type mockContainer struct{}

func (m *mockContainer) EnsureCrewRuntime(_ context.Context, cfg provider.CrewConfig) (string, error) {
	return "container-" + cfg.ID, nil
}
func (m *mockContainer) StopCrewRuntime(_ context.Context, _ string) error   { return nil }
func (m *mockContainer) RemoveCrewRuntime(_ context.Context, _ string) error { return nil }
func (m *mockContainer) ContainerStatus(_ context.Context, id string) (*provider.ContainerStatus, error) {
	return &provider.ContainerStatus{ID: id, State: "running", Uptime: "1h"}, nil
}
func (m *mockContainer) Exec(_ context.Context, _ provider.ExecConfig) (*provider.ExecResult, error) {
	return nil, nil
}
func (m *mockContainer) ExecInspect(_ context.Context, _ string) (bool, int, error) {
	return false, 0, nil
}
func (m *mockContainer) ContainerStats(_ context.Context, _ string) (*provider.ContainerMetrics, error) {
	return nil, nil
}
func (m *mockContainer) CrewContainerName(slug string) string {
	return "crewship-team-" + slug
}
func (m *mockContainer) CopyToContainer(_ context.Context, _ string, _ string, _ io.Reader) error {
	return nil
}

func newTestServerWithDeps() *Server {
	cfg := config.Default()
	// JWT validator is required by ws.NewHub since the security
	// hardening pass — provide a fixed test secret instead of
	// letting the panic-on-missing fire and abort the whole suite.
	cfg.Auth.JWTSecret = "test-secret-for-server-routes-test-32"
	logger := logging.New("error", "json", nil)
	deps := &Deps{
		Container: &mockContainer{},
		State:     newMockState(),
	}
	s := New(cfg, logger, deps)
	s.startedAt = time.Now()
	return s
}

func TestAgentStatusWithState(t *testing.T) {
	s := newTestServerWithDeps()

	stateData := `{"agent_id":"a1","status":"running","started_at":"2026-01-01T00:00:00Z"}`
	s.state.Set(context.Background(), "agent_runs", "a1", []byte(stateData))

	req := httptest.NewRequest("GET", "/agents/a1/status", nil)
	w := httptest.NewRecorder()
	s.ipcMux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var body map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &body)
	if body["status"] != "running" {
		t.Errorf("expected running status, got %v", body["status"])
	}
}

func TestAgentStatusInvalidJSON(t *testing.T) {
	s := newTestServerWithDeps()
	s.state.Set(context.Background(), "agent_runs", "a1", []byte("not json"))

	req := httptest.NewRequest("GET", "/agents/a1/status", nil)
	w := httptest.NewRecorder()
	s.ipcMux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var body map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &body)
	if body["status"] != "idle" {
		t.Errorf("expected idle for invalid JSON, got %v", body["status"])
	}
}

func TestContainerStatusWithProvider(t *testing.T) {
	s := newTestServerWithDeps()

	req := httptest.NewRequest("GET", "/crews/crew-1/container/status", nil)
	w := httptest.NewRecorder()
	s.ipcMux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var body map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &body)
	if body["status"] != "running" {
		t.Errorf("expected running, got %v", body["status"])
	}
}

func TestContainerStartWithProvider(t *testing.T) {
	s := newTestServerWithDeps()

	req := httptest.NewRequest("POST", "/crews/crew-1/container/start", nil)
	w := httptest.NewRecorder()
	s.ipcMux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var body map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &body)
	if body["status"] != "running" {
		t.Errorf("expected running, got %v", body["status"])
	}
}

func TestContainerStopWithProvider(t *testing.T) {
	s := newTestServerWithDeps()

	req := httptest.NewRequest("POST", "/crews/crew-1/container/stop", nil)
	w := httptest.NewRecorder()
	s.ipcMux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestSessionMessagesWithStore(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Default()
	cfg.Storage.BasePath = dir
	cfg.Auth.JWTSecret = "test-secret-for-server-routes-test-32"
	logger := logging.New("error", "json", nil)
	s := New(cfg, logger, nil)
	s.startedAt = time.Now()

	store := conversation.NewStore(dir, logger)
	s.convStore = store
	store.Append(context.Background(), "chat-1", conversation.Message{
		ID:        "msg-1",
		Role:      "user",
		Content:   "hello",
		Timestamp: time.Now(),
	})

	req := httptest.NewRequest("GET", "/chats/chat-1/messages", nil)
	w := httptest.NewRecorder()
	s.ipcMux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var body map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &body)
	msgs, ok := body["messages"].([]interface{})
	if !ok || len(msgs) != 1 {
		t.Errorf("expected 1 message, got %v", body["messages"])
	}
}

func TestDebugInfoEndpoint(t *testing.T) {
	s := newTestServerWithDeps()

	req := httptest.NewRequest("GET", "/debug/info", nil)
	w := httptest.NewRecorder()
	s.ipcMux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var body map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}

	if body["status"] != "ok" {
		t.Errorf("expected status ok, got %v", body["status"])
	}
	if _, ok := body["uptime"]; !ok {
		t.Error("expected uptime field")
	}
	if _, ok := body["providers"]; !ok {
		t.Error("expected providers field")
	}

	// Verify no secrets are leaked
	cfg, ok := body["config"].(map[string]interface{})
	if !ok {
		t.Fatal("expected config map")
	}
	if _, hasSecret := cfg["jwt_secret"]; hasSecret {
		t.Error("SECURITY: jwt_secret must not be in response")
	}
	if _, hasToken := cfg["internal_token"]; hasToken {
		t.Error("SECURITY: internal_token must not be in response")
	}
	// jwt_configured should be boolean, not the secret
	if jwtCfg, ok := cfg["jwt_configured"]; ok {
		if _, isBool := jwtCfg.(bool); !isBool {
			t.Error("SECURITY: jwt_configured must be boolean, not the secret value")
		}
	}
}

func TestDebugLogsEmpty(t *testing.T) {
	s := newTestServerWithDeps()
	// s.debugLogs is nil -- no ring buffer

	req := httptest.NewRequest("GET", "/debug/logs", nil)
	w := httptest.NewRecorder()
	s.ipcMux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var body map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &body)
	logs, ok := body["logs"].([]interface{})
	if !ok || len(logs) != 0 {
		t.Errorf("expected empty logs array, got %v", body["logs"])
	}
}

func TestDebugLogsWithBuffer(t *testing.T) {
	s := newTestServerWithDeps()
	rb := logging.NewRingBuffer(100)
	s.debugLogs = rb

	rb.Append(logging.LogRecord{Time: time.Now(), Level: "INFO", Message: "server started"})
	rb.Append(logging.LogRecord{Time: time.Now(), Level: "ERROR", Message: "ws auth failed", Attrs: map[string]string{"error": "invalid token"}})
	rb.Append(logging.LogRecord{Time: time.Now(), Level: "INFO", Message: "agent started", Attrs: map[string]string{"agent_id": "a1"}})

	req := httptest.NewRequest("GET", "/debug/logs", nil)
	w := httptest.NewRecorder()
	s.ipcMux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var body map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &body)
	logs, ok := body["logs"].([]interface{})
	if !ok {
		t.Fatalf("expected logs array, got %T", body["logs"])
	}
	if len(logs) != 3 {
		t.Errorf("expected 3 log entries, got %d", len(logs))
	}
}

func TestDebugLogsFiltering(t *testing.T) {
	s := newTestServerWithDeps()
	rb := logging.NewRingBuffer(100)
	s.debugLogs = rb

	rb.Append(logging.LogRecord{Time: time.Now(), Level: "INFO", Message: "startup"})
	rb.Append(logging.LogRecord{Time: time.Now(), Level: "ERROR", Message: "auth error"})
	rb.Append(logging.LogRecord{Time: time.Now(), Level: "INFO", Message: "agent a1 started", Attrs: map[string]string{"agent_id": "a1"}})
	rb.Append(logging.LogRecord{Time: time.Now(), Level: "INFO", Message: "agent a2 started", Attrs: map[string]string{"agent_id": "a2"}})

	// Filter by level=ERROR
	req := httptest.NewRequest("GET", "/debug/logs?level=ERROR", nil)
	w := httptest.NewRecorder()
	s.ipcMux.ServeHTTP(w, req)

	var body map[string]interface{}
	json.Unmarshal(w.Body.Bytes(), &body)
	logs := body["logs"].([]interface{})
	if len(logs) != 1 {
		t.Errorf("level filter: expected 1 ERROR entry, got %d", len(logs))
	}

	// Filter by agent_id=a1 -- should include service-level logs (no agent_id) + a1 logs
	req2 := httptest.NewRequest("GET", "/debug/logs?agent_id=a1", nil)
	w2 := httptest.NewRecorder()
	s.ipcMux.ServeHTTP(w2, req2)

	var body2 map[string]interface{}
	json.Unmarshal(w2.Body.Bytes(), &body2)
	logs2 := body2["logs"].([]interface{})
	// Should be: "startup" (no agent_id), "auth error" (no agent_id), "agent a1 started" (matches) = 3
	// Should NOT include "agent a2 started"
	if len(logs2) != 3 {
		t.Errorf("agent_id filter: expected 3 entries (2 service + 1 matching), got %d", len(logs2))
	}
}
