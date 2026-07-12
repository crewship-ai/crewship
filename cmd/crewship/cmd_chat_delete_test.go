package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
)

// Acceptance coverage for `crewship chat delete <chat-id>` — the CLI parity
// command for DELETE /api/v1/agents/{agentId}/chats/{chatId} (#998). Drives
// the real cobra RunE against a mock server (same pattern as chat read).

type chatDeleteServerMock struct {
	mu     sync.Mutex
	method string
	path   string
}

func (m *chatDeleteServerMock) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet && r.URL.Path == "/api/v1/agents" {
			_, _ = w.Write([]byte(`[{"id":"cagentagentagentagent","slug":"atlas"}]`))
			return
		}
		m.mu.Lock()
		m.method = r.Method
		m.path = r.URL.Path
		m.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}
}

func TestChatDeleteCmd_Structure(t *testing.T) {
	if !strings.HasPrefix(chatDeleteCmd.Use, "delete") {
		t.Errorf("delete Use: got %q, want delete <chat-id>", chatDeleteCmd.Use)
	}
	if chatDeleteCmd.Flags().Lookup("agent") == nil {
		t.Fatal("chat delete missing --agent flag")
	}
	if chatDeleteCmd.Flags().Lookup("yes") == nil {
		t.Fatal("chat delete missing --yes flag (destructive command needs a confirm bypass)")
	}
	var found bool
	for _, c := range chatCmd.Commands() {
		if c.Name() == "delete" {
			found = true
		}
	}
	if !found {
		t.Error("delete not registered under chat")
	}
}

func TestChatDeleteCmd_DeletesEndpoint(t *testing.T) {
	m := &chatDeleteServerMock{}
	srv := httptest.NewServer(m.handler())
	defer srv.Close()

	saveCLIState(t)
	t.Setenv("CREWSHIP_SERVER", "") // shell env must not re-target the mock
	cliCfg = &cli.CLIConfig{
		Token:     "fake-token",
		Workspace: "cabcdefghijklmnopqrs",
		Server:    srv.URL,
	}
	if err := chatDeleteCmd.Flags().Set("agent", "atlas"); err != nil {
		t.Fatalf("set --agent: %v", err)
	}
	if err := chatDeleteCmd.Flags().Set("yes", "true"); err != nil {
		t.Fatalf("set --yes: %v", err)
	}
	t.Cleanup(func() {
		_ = chatDeleteCmd.Flags().Set("agent", "")
		_ = chatDeleteCmd.Flags().Set("yes", "false")
	})

	if err := chatDeleteCmd.RunE(chatDeleteCmd, []string{"c_abc123"}); err != nil {
		t.Fatalf("RunE: %v", err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.method != http.MethodDelete {
		t.Errorf("method = %q, want DELETE", m.method)
	}
	want := "/api/v1/agents/cagentagentagentagent/chats/c_abc123"
	if m.path != want {
		t.Errorf("path = %q, want %q", m.path, want)
	}
}
