package server

// Coverage tests for routes_agent.go: the agent-start happy path
// (container ensured, stats registered, 202 accepted), its provider
// error paths, agent-status with no state provider, agent-logs reading
// from disk plus offset clamping, and chat-messages degradation paths.

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/logcollector"
	"github.com/crewship-ai/crewship/internal/provider"
)

// covFailingEnsureContainer fails EnsureCrewRuntime; everything else
// behaves like mockContainer.
type covFailingEnsureContainer struct {
	mockContainer
}

func (c *covFailingEnsureContainer) EnsureCrewRuntime(_ context.Context, _ provider.CrewConfig) (string, error) {
	return "", os.ErrDeadlineExceeded
}

func TestHandleAgentStart_HappyPathAccepted(t *testing.T) {
	s := newTestServerWithDeps(t)
	// Give the async run goroutine a parent context, and make RunAgent
	// fail immediately ("not accepting") so the goroutine exits
	// deterministically without a real container runtime.
	runCtx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	s.runCtx = runCtx
	s.orchestrator.StopAccepting()

	body := `{
		"workspace_id": "ws1",
		"crew_id": "crew1",
		"crew_slug": "alpha",
		"agent_slug": "bob",
		"session_id": "sess1",
		"timeout_seconds": 1
	}`
	req := httptest.NewRequest("POST", "/agents/agent-7/start", strings.NewReader(body))
	w := httptest.NewRecorder()
	s.ipcMux.ServeHTTP(w, req)

	if w.Code != 202 {
		t.Fatalf("status = %d, want 202; body: %s", w.Code, w.Body.String())
	}
	resp := parseJSON(t, w.Body.Bytes())
	if resp["agent_id"] != "agent-7" {
		t.Errorf("agent_id = %v, want agent-7", resp["agent_id"])
	}
	if resp["container_id"] != "container-crew1" {
		t.Errorf("container_id = %v, want container-crew1 (mock provider contract)", resp["container_id"])
	}
	if resp["status"] != "starting" {
		t.Errorf("status = %v, want starting", resp["status"])
	}

	// The handler must have registered the ensured container for stats.
	tracked := s.statsCollector.Tracked()
	found := false
	for _, tc := range tracked {
		if tc.ContainerID == "container-crew1" && tc.CrewID == "crew1" && tc.WorkspaceID == "ws1" {
			found = true
		}
	}
	if !found {
		t.Errorf("stats collector tracked = %+v, want container-crew1/crew1/ws1 registered", tracked)
	}

	// Second start with no timeout_seconds: drives the default-timeout
	// branch of the run goroutine. Same accepted contract.
	req = httptest.NewRequest("POST", "/agents/agent-8/start",
		strings.NewReader(`{"workspace_id":"ws1","crew_id":"crew1","crew_slug":"alpha","agent_slug":"bob"}`))
	w = httptest.NewRecorder()
	s.ipcMux.ServeHTTP(w, req)
	if w.Code != 202 {
		t.Fatalf("status (no timeout) = %d, want 202", w.Code)
	}

	// Let the async goroutines run their "orchestrator not accepting" exit.
	time.Sleep(60 * time.Millisecond)
}

// covErrState fails every Get so the status handler's degraded branch
// executes; other methods behave like the no-op mockState.
type covErrState struct {
	mockState
}

func (c *covErrState) Get(_ context.Context, _, _ string) ([]byte, error) {
	return nil, os.ErrPermission
}

func TestHandleAgentStatus_StateGetErrorIsIdle(t *testing.T) {
	s := newTestServerWithDeps(t)
	s.state = &covErrState{}
	req := httptest.NewRequest("GET", "/agents/agent-e/status", nil)
	w := httptest.NewRecorder()
	s.ipcMux.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	resp := parseJSON(t, w.Body.Bytes())
	if resp["status"] != "idle" {
		t.Errorf("status = %v, want idle on state error", resp["status"])
	}
}

func TestHandleAgentStart_NoContainerProvider503(t *testing.T) {
	s := newTestServerForT(t) // no container provider
	req := httptest.NewRequest("POST", "/agents/a1/start", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	s.ipcMux.ServeHTTP(w, req)
	if w.Code != 503 {
		t.Fatalf("status = %d, want 503", w.Code)
	}
	resp := parseJSON(t, w.Body.Bytes())
	if resp["error"] != "container provider not configured" {
		t.Errorf("error = %v, want provider-not-configured message", resp["error"])
	}
}

func TestHandleAgentStart_EnsureRuntimeError500(t *testing.T) {
	s := newTestServerWithDeps(t)
	s.container = &covFailingEnsureContainer{}

	req := httptest.NewRequest("POST", "/agents/a1/start", strings.NewReader(`{"crew_id":"c1"}`))
	w := httptest.NewRecorder()
	s.ipcMux.ServeHTTP(w, req)
	if w.Code != 500 {
		t.Fatalf("status = %d, want 500", w.Code)
	}
	resp := parseJSON(t, w.Body.Bytes())
	if resp["error"] != "failed to start container" {
		t.Errorf("error = %v, want failed-to-start message", resp["error"])
	}
}

func TestHandleAgentStatus_NoStateProviderIsIdle(t *testing.T) {
	s := newTestServerForT(t) // state provider nil
	req := httptest.NewRequest("GET", "/agents/agent-x/status", nil)
	w := httptest.NewRecorder()
	s.ipcMux.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	resp := parseJSON(t, w.Body.Bytes())
	if resp["status"] != "idle" || resp["agent_id"] != "agent-x" {
		t.Errorf("resp = %v, want idle/agent-x", resp)
	}
}

func TestHandleAgentLogs_ReadsEntriesFromDisk(t *testing.T) {
	s := newTestServerForT(t)
	base := t.TempDir()
	logDir := filepath.Join(base, "crews", "crew-l", "agents", "agent-l")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	lines := []string{
		`{"level":"info","agent":"agent-l","event":"text","content":"first line"}`,
		`{"level":"info","agent":"agent-l","event":"text","content":"second line"}`,
	}
	if err := os.WriteFile(filepath.Join(logDir, "current.jsonl"),
		[]byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	s.logReader = logcollector.NewReader(base)

	req := httptest.NewRequest("GET", "/agents/agent-l/logs?crew_id=crew-l", nil)
	w := httptest.NewRecorder()
	s.ipcMux.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var resp struct {
		AgentID string                `json:"agent_id"`
		Logs    []logcollector.LogEntry `json:"logs"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v; body: %s", err, w.Body.String())
	}
	if len(resp.Logs) != 2 {
		t.Fatalf("logs = %d entries, want 2", len(resp.Logs))
	}
	if resp.Logs[0].Content != "first line" || resp.Logs[1].Content != "second line" {
		t.Errorf("log contents = %q,%q — want the two seeded lines",
			resp.Logs[0].Content, resp.Logs[1].Content)
	}

	// Negative offset is clamped to 0 and limit applies.
	req = httptest.NewRequest("GET", "/agents/agent-l/logs?crew_id=crew-l&offset=-9&limit=1", nil)
	w = httptest.NewRecorder()
	s.ipcMux.ServeHTTP(w, req)
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Logs) != 1 || resp.Logs[0].Content != "first line" {
		t.Errorf("clamped read = %+v, want exactly the first line", resp.Logs)
	}

	// Oversized offset is clamped to the cap (past EOF → empty, not 4xx).
	req = httptest.NewRequest("GET", "/agents/agent-l/logs?crew_id=crew-l&offset=99999999", nil)
	w = httptest.NewRecorder()
	s.ipcMux.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200 for clamped offset", w.Code)
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Logs) != 0 {
		t.Errorf("logs past EOF = %d entries, want 0", len(resp.Logs))
	}
}

func TestHandleAgentLogs_ReaderErrorDegradesToEmpty(t *testing.T) {
	s := newTestServerForT(t)
	s.logReader = logcollector.NewReader(t.TempDir())

	// "bad agent" (space) fails the reader's path-segment validation —
	// the handler must degrade to an empty list, not a 5xx.
	req := httptest.NewRequest("GET", "/agents/bad%20agent/logs?crew_id=crew-l", nil)
	w := httptest.NewRecorder()
	s.ipcMux.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	resp := parseJSON(t, w.Body.Bytes())
	logs, ok := resp["logs"].([]interface{})
	if !ok || len(logs) != 0 {
		t.Errorf("logs = %v, want empty array on reader error", resp["logs"])
	}
}

func TestHandleChatMessages_NilStoreReturnsEmpty(t *testing.T) {
	s := newTestServerForT(t)
	s.convStore = nil

	req := httptest.NewRequest("GET", "/chats/sess-1/messages", nil)
	w := httptest.NewRecorder()
	s.ipcMux.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	resp := parseJSON(t, w.Body.Bytes())
	if resp["chat_id"] != "sess-1" {
		t.Errorf("chat_id = %v, want sess-1", resp["chat_id"])
	}
	msgs, ok := resp["messages"].([]interface{})
	if !ok || len(msgs) != 0 {
		t.Errorf("messages = %v, want empty array without a store", resp["messages"])
	}
}

func TestHandleChatMessages_ReadErrorDegradesToEmpty(t *testing.T) {
	s := newTestServerForT(t)
	// "x..y" fails the conversation store's session-ID validation
	// (contains ".."), driving the error branch.
	req := httptest.NewRequest("GET", "/chats/x..y/messages", nil)
	w := httptest.NewRecorder()
	s.ipcMux.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	resp := parseJSON(t, w.Body.Bytes())
	msgs, ok := resp["messages"].([]interface{})
	if !ok || len(msgs) != 0 {
		t.Errorf("messages = %v, want empty array on store error", resp["messages"])
	}
}
