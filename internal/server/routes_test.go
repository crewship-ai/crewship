package server

import (
	"context"
	"encoding/json"
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
func (m *mockState) Delete(_ context.Context, _, _ string) error                  { return nil }
func (m *mockState) List(_ context.Context, _ string) (map[string][]byte, error)  { return nil, nil }
func (m *mockState) ListByPrefix(_ context.Context, _, _ string) (map[string][]byte, error) {
	return nil, nil
}
func (m *mockState) Close() error { return nil }

// mock container provider
type mockContainer struct{}

func (m *mockContainer) EnsureTeamRuntime(_ context.Context, cfg provider.TeamConfig) (string, error) {
	return "container-" + cfg.ID, nil
}
func (m *mockContainer) StopTeamRuntime(_ context.Context, _ string) error { return nil }
func (m *mockContainer) RemoveTeamRuntime(_ context.Context, _ string) error { return nil }
func (m *mockContainer) ContainerStatus(_ context.Context, id string) (*provider.ContainerStatus, error) {
	return &provider.ContainerStatus{ID: id, State: "running", Uptime: "1h"}, nil
}
func (m *mockContainer) Exec(_ context.Context, _ provider.ExecConfig) (*provider.ExecResult, error) {
	return nil, nil
}
func (m *mockContainer) ExecInspect(_ context.Context, _ string) (bool, int, error) {
	return false, 0, nil
}

func newTestServerWithDeps() *Server {
	cfg := config.Default()
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

	req := httptest.NewRequest("GET", "/teams/team-1/container/status", nil)
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

	req := httptest.NewRequest("POST", "/teams/team-1/container/start", nil)
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

	req := httptest.NewRequest("POST", "/teams/team-1/container/stop", nil)
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
	logger := logging.New("error", "json", nil)
	s := New(cfg, logger, nil)
	s.startedAt = time.Now()

	store := conversation.NewStore(dir, logger)
	s.convStore = store
	store.Append(context.Background(), "sess-1", conversation.Message{
		ID:        "msg-1",
		Role:      "user",
		Content:   "hello",
		Timestamp: time.Now(),
	})

	req := httptest.NewRequest("GET", "/sessions/sess-1/messages", nil)
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
