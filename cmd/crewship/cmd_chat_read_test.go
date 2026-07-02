package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
)

// Acceptance coverage for `crewship chat read <chat-id>` — the CLI parity
// command for PUT /api/v1/agents/{agentId}/chats/{chatId}/read. Drives the
// real cobra RunE against a mock server (same pattern as cmd_chat_steer_test).

type chatReadServerMock struct {
	mu     sync.Mutex
	method string
	path   string
}

func (m *chatReadServerMock) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Agent resolution list (slug → id).
		if r.Method == http.MethodGet && r.URL.Path == "/api/v1/agents" {
			_, _ = w.Write([]byte(`[{"id":"cagentagentagentagent","slug":"atlas"}]`))
			return
		}
		m.mu.Lock()
		m.method = r.Method
		m.path = r.URL.Path
		m.mu.Unlock()
		_, _ = w.Write([]byte(`{"chat_id":"c_abc123","last_read_at":"2026-07-02T10:00:00.000Z"}`))
	}
}

func TestChatReadCmd_Structure(t *testing.T) {
	if !strings.HasPrefix(chatReadCmd.Use, "read") {
		t.Errorf("read Use: got %q, want read <chat-id>", chatReadCmd.Use)
	}
	if chatReadCmd.Flags().Lookup("agent") == nil {
		t.Fatal("chat read missing --agent flag")
	}
	var found bool
	for _, c := range chatCmd.Commands() {
		if c.Name() == "read" {
			found = true
		}
	}
	if !found {
		t.Error("read not registered under chat")
	}
}

func TestChatReadCmd_PutsToReadEndpoint(t *testing.T) {
	m := &chatReadServerMock{}
	srv := httptest.NewServer(m.handler())
	defer srv.Close()

	saveCLIState(t)
	t.Setenv("CREWSHIP_SERVER", "") // shell env must not re-target the mock
	cliCfg = &cli.CLIConfig{
		Token:     "fake-token",
		Workspace: "cabcdefghijklmnopqrs",
		Server:    srv.URL,
	}
	if err := chatReadCmd.Flags().Set("agent", "atlas"); err != nil {
		t.Fatalf("set --agent: %v", err)
	}
	t.Cleanup(func() { _ = chatReadCmd.Flags().Set("agent", "") })

	if err := chatReadCmd.RunE(chatReadCmd, []string{"c_abc123"}); err != nil {
		t.Fatalf("RunE: %v", err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.method != http.MethodPut {
		t.Errorf("method = %q, want PUT", m.method)
	}
	want := "/api/v1/agents/cagentagentagentagent/chats/c_abc123/read"
	if m.path != want {
		t.Errorf("path = %q, want %q", m.path, want)
	}
}
