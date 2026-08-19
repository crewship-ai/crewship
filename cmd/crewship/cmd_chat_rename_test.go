package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
)

// Acceptance coverage for `crewship chat rename <chat-id> <title>` — the CLI
// parity command for PATCH /api/v1/agents/{agentId}/chats/{chatId} (PRD
// chat-as-a-primary-surface, Step 2). Drives the real cobra RunE against a mock
// server, the same pattern as cmd_chat_read_test.

type chatRenameServerMock struct {
	mu     sync.Mutex
	method string
	path   string
	body   map[string]any
}

func (m *chatRenameServerMock) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Agent resolution list (slug → id).
		if r.Method == http.MethodGet && r.URL.Path == "/api/v1/agents" {
			_, _ = w.Write([]byte(`[{"id":"cagentagentagentagent","slug":"atlas"}]`))
			return
		}
		raw, _ := io.ReadAll(r.Body)
		m.mu.Lock()
		m.method = r.Method
		m.path = r.URL.Path
		m.body = map[string]any{}
		_ = json.Unmarshal(raw, &m.body)
		m.mu.Unlock()
		_, _ = w.Write([]byte(`{"id":"c_abc123","agent_id":"cagentagentagentagent",` +
			`"workspace_id":"cabcdefghijklmnopqrs","title":"Refactor the queue worker",` +
			`"mode":"CHAT","status":"ACTIVE","message_count":3,` +
			`"started_at":"2026-07-02T10:00:00.000Z","ended_at":null,` +
			`"created_at":"2026-07-02T10:00:00.000Z","origin":"UI",` +
			`"last_activity_at":"2026-07-02T10:00:00.000Z","unread_count":0}`))
	}
}

func TestChatRenameCmd_Structure(t *testing.T) {
	if !strings.HasPrefix(chatRenameCmd.Use, "rename") {
		t.Errorf("rename Use: got %q, want rename <chat-id> <title>", chatRenameCmd.Use)
	}
	if chatRenameCmd.Flags().Lookup("agent") == nil {
		t.Fatal("chat rename missing --agent flag")
	}
	var found bool
	for _, c := range chatCmd.Commands() {
		if c.Name() == "rename" {
			found = true
		}
	}
	if !found {
		t.Error("rename not registered under chat")
	}
}

func TestChatRenameCmd_PatchesTheChatRoute(t *testing.T) {
	m := &chatRenameServerMock{}
	srv := httptest.NewServer(m.handler())
	defer srv.Close()

	saveCLIState(t)
	t.Setenv("CREWSHIP_SERVER", "") // shell env must not re-target the mock
	cliCfg = &cli.CLIConfig{
		Token:     "fake-token",
		Workspace: "cabcdefghijklmnopqrs",
		Server:    srv.URL,
	}
	if err := chatRenameCmd.Flags().Set("agent", "atlas"); err != nil {
		t.Fatalf("set --agent: %v", err)
	}
	t.Cleanup(func() { _ = chatRenameCmd.Flags().Set("agent", "") })

	if err := chatRenameCmd.RunE(chatRenameCmd, []string{"c_abc123", "Refactor the queue worker"}); err != nil {
		t.Fatalf("RunE: %v", err)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.method != http.MethodPatch {
		t.Errorf("method = %q, want PATCH", m.method)
	}
	want := "/api/v1/agents/cagentagentagentagent/chats/c_abc123"
	if m.path != want {
		t.Errorf("path = %q, want %q", m.path, want)
	}
	if m.body["title"] != "Refactor the queue worker" {
		t.Errorf("body title = %v, want the new title", m.body["title"])
	}
}

// A title with spaces must survive as ONE argument rather than being silently
// joined from argv — `crewship chat rename c_x one two` should tell the user to
// quote it, not invent the title "one".
func TestChatRenameCmd_RequiresQuotedTitle(t *testing.T) {
	if err := chatRenameCmd.Args(chatRenameCmd, []string{"c_abc123"}); err == nil {
		t.Error("one argument must be rejected — the title is required")
	}
	if err := chatRenameCmd.Args(chatRenameCmd, []string{"c_abc123", "one", "two"}); err == nil {
		t.Error("three arguments must be rejected — the title needs quoting")
	}
	if err := chatRenameCmd.Args(chatRenameCmd, []string{"c_abc123", "one two"}); err != nil {
		t.Errorf("chat-id + quoted title must be accepted: %v", err)
	}
}
