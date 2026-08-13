package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
)

// Acceptance coverage for `crewship chat attachments list|delete` — the CLI
// parity for the two lifecycle routes a chat attachment never had. Drives the
// real cobra RunE against a mock server, the same pattern as
// cmd_chat_rename_test.

type chatAttachmentsServerMock struct {
	mu      sync.Mutex
	calls   []string // "METHOD path"
	listing string
}

func (m *chatAttachmentsServerMock) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet && r.URL.Path == "/api/v1/agents" {
			_, _ = w.Write([]byte(`[{"id":"cagentagentagentagent","slug":"atlas"}]`))
			return
		}
		m.mu.Lock()
		m.calls = append(m.calls, r.Method+" "+r.URL.Path)
		m.mu.Unlock()
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		_, _ = w.Write([]byte(m.listing))
	}
}

func (m *chatAttachmentsServerMock) seen() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.calls...)
}

func useMockServer(t *testing.T, url string) {
	t.Helper()
	saveCLIState(t)
	t.Setenv("CREWSHIP_SERVER", "") // shell env must not re-target the mock
	cliCfg = &cli.CLIConfig{
		Token:     "fake-token",
		Workspace: "cabcdefghijklmnopqrs",
		Server:    url,
	}
}

func TestChatAttachmentsCmd_Structure(t *testing.T) {
	var found bool
	for _, c := range chatCmd.Commands() {
		if c.Name() == "attachments" {
			found = true
		}
	}
	if !found {
		t.Fatal("attachments not registered under chat — every endpoint gets a CLI command")
	}
	names := map[string]bool{}
	for _, c := range chatAttachmentsCmd.Commands() {
		names[c.Name()] = true
	}
	for _, want := range []string{"list", "delete"} {
		if !names[want] {
			t.Errorf("chat attachments has no %q subcommand", want)
		}
	}
	if chatAttachmentsListCmd.Flags().Lookup("agent") == nil {
		t.Error("chat attachments list missing --agent flag")
	}
	if chatAttachmentsDeleteCmd.Flags().Lookup("yes") == nil {
		t.Error("chat attachments delete missing --yes flag — it removes bytes")
	}
}

func TestChatAttachmentsListCmd_CallsTheListRoute(t *testing.T) {
	m := &chatAttachmentsServerMock{listing: `[{"id":"att_1","filename":"evidence.pdf",
		"size_bytes":7,"sha256":"abc123abc123abc123","path":"attachments/c_abc123/att_1/evidence.pdf",
		"agent_path":"/output/atlas/attachments/c_abc123/att_1/evidence.pdf",
		"created_at":"2026-08-13T10:00:00Z"}]`}
	srv := httptest.NewServer(m.handler())
	defer srv.Close()
	useMockServer(t, srv.URL)

	if err := chatAttachmentsListCmd.Flags().Set("agent", "atlas"); err != nil {
		t.Fatalf("set --agent: %v", err)
	}
	t.Cleanup(func() { _ = chatAttachmentsListCmd.Flags().Set("agent", "") })

	if err := chatAttachmentsListCmd.RunE(chatAttachmentsListCmd, []string{"c_abc123"}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	want := "GET /api/v1/agents/cagentagentagentagent/chats/c_abc123/attachments"
	if got := m.seen(); len(got) != 1 || got[0] != want {
		t.Errorf("calls = %v, want exactly [%s]", got, want)
	}
}

func TestChatAttachmentsDeleteCmd_CallsTheDeleteRoute(t *testing.T) {
	m := &chatAttachmentsServerMock{}
	srv := httptest.NewServer(m.handler())
	defer srv.Close()
	useMockServer(t, srv.URL)

	if err := chatAttachmentsDeleteCmd.Flags().Set("agent", "atlas"); err != nil {
		t.Fatalf("set --agent: %v", err)
	}
	if err := chatAttachmentsDeleteCmd.Flags().Set("yes", "true"); err != nil {
		t.Fatalf("set --yes: %v", err)
	}
	t.Cleanup(func() {
		_ = chatAttachmentsDeleteCmd.Flags().Set("agent", "")
		_ = chatAttachmentsDeleteCmd.Flags().Set("yes", "false")
	})

	if err := chatAttachmentsDeleteCmd.RunE(chatAttachmentsDeleteCmd, []string{"c_abc123", "att_1"}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	want := "DELETE /api/v1/agents/cagentagentagentagent/chats/c_abc123/attachments/att_1"
	if got := m.seen(); len(got) != 1 || got[0] != want {
		t.Errorf("calls = %v, want exactly [%s]", got, want)
	}
}

// The command refuses to remove bytes without --yes or a confirmation, the
// same gate `chat delete` applies to a transcript.
func TestChatAttachmentsDeleteCmd_RequiresConfirmation(t *testing.T) {
	m := &chatAttachmentsServerMock{}
	srv := httptest.NewServer(m.handler())
	defer srv.Close()
	useMockServer(t, srv.URL)

	if err := chatAttachmentsDeleteCmd.Flags().Set("yes", "false"); err != nil {
		t.Fatalf("set --yes: %v", err)
	}
	err := chatAttachmentsDeleteCmd.RunE(chatAttachmentsDeleteCmd, []string{"c_abc123", "att_1"})
	if err == nil || !strings.Contains(err.Error(), "aborted") {
		t.Fatalf("err = %v, want an abort — a non-interactive run must not delete bytes unasked", err)
	}
	if got := m.seen(); len(got) != 0 {
		t.Errorf("calls = %v, want none", got)
	}
}
