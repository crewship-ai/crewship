package main

// Coverage tests for cmd_resume.go — the non-interactive resolution
// paths of RunE plus pickRecentChat / findChatForPR / deref. The final
// dispatch into runCmd (which opens a WebSocket stream) is intentionally
// not exercised; every test here stops at a resolution error.

import (
	"context"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

func TestDeref(t *testing.T) {
	if got := deref(nil); got != "" {
		t.Errorf("deref(nil) = %q", got)
	}
	s := "x"
	if got := deref(&s); got != "x" {
		t.Errorf("deref(&x) = %q", got)
	}
}

func TestResumeRunE_NoArgsNonTTY(t *testing.T) {
	covSetupCli8(t, "http://127.0.0.1:0")
	resumeCmd.SetContext(context.Background())

	// Force a non-TTY stdin so the interactive picker refuses.
	covWithStdinCli8(t, "", func() {
		err := resumeCmd.RunE(resumeCmd, nil)
		if err == nil || !strings.Contains(err.Error(), "requires a TTY") {
			t.Errorf("expected TTY error; got %v", err)
		}
	})
}

func TestResumeRunE_RunIDWithoutChat(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())
	stub.OnGet("/api/v1/runs/r_orphan", clitest.JSONResponse(200, map[string]any{
		"id": "r_orphan", "status": "COMPLETED",
	}))
	resumeCmd.SetContext(context.Background())

	err := resumeCmd.RunE(resumeCmd, []string{"r_orphan"})
	if err == nil || !strings.Contains(err.Error(), "no associated chat") {
		t.Errorf("expected no-chat error; got %v", err)
	}
}

func TestResumeRunE_RunIDLookupError(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())
	stub.OnGet("/api/v1/runs/run_missing", clitest.ErrorResponse(404, "run not found"))
	resumeCmd.SetContext(context.Background())

	err := resumeCmd.RunE(resumeCmd, []string{"run_missing"})
	if err == nil || !strings.Contains(err.Error(), "run not found") {
		t.Errorf("expected run-not-found; got %v", err)
	}
}

func TestResumeRunE_ChatIDAgentUnresolvable(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())
	// /chats/{id} 404s → agent slug stays empty → resolution error.
	resumeCmd.SetContext(context.Background())

	err := resumeCmd.RunE(resumeCmd, []string{"chat-unknown"})
	if err == nil || !strings.Contains(err.Error(), "could not determine agent for chat chat-unknown") {
		t.Errorf("expected agent-resolution error; got %v", err)
	}
}

func TestResumeRunE_PRURLNoSession(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())
	stub.OnGet("/api/v1/journal", clitest.JSONResponse(200, map[string]any{"entries": []any{}}))
	resumeCmd.SetContext(context.Background())

	err := resumeCmd.RunE(resumeCmd, []string{"https://github.com/foo/bar/pull/42"})
	if err == nil || !strings.Contains(err.Error(), "no session found for PR foo/bar#42") {
		t.Errorf("expected no-session error; got %v", err)
	}

	calls := stub.CallsFor("GET", "/api/v1/journal")
	if len(calls) != 1 {
		t.Fatalf("expected 1 journal search, got %d", len(calls))
	}
	if !strings.Contains(calls[0].Query, "query=foo%2Fbar%2342") {
		t.Errorf("journal query missing PR needle: %q", calls[0].Query)
	}
}

func TestResumeRunE_NoAuth(t *testing.T) {
	covSetupCli8(t, "http://127.0.0.1:0")
	cliCfg = &cli.CLIConfig{}
	err := resumeCmd.RunE(resumeCmd, []string{"chat-1"})
	if err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("expected not-logged-in; got %v", err)
	}
}

func TestPickRecentChat_NonTTY(t *testing.T) {
	covSetupCli8(t, "http://127.0.0.1:0")
	client := newAPIClient()
	covWithStdinCli8(t, "", func() {
		_, _, err := pickRecentChat(client)
		if err == nil || !strings.Contains(err.Error(), "requires a TTY") {
			t.Errorf("expected TTY error; got %v", err)
		}
	})
}

func TestFindChatForPR_Found(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())
	stub.OnGet("/api/v1/journal", clitest.JSONResponse(200, map[string]any{
		"entries": []map[string]any{
			{"trace_id": "t1", "chat_id": "", "agent_id": "a1"},
			{"trace_id": "t2", "chat_id": "chat-42", "agent_id": "a2"},
		},
	}))
	client := newAPIClient()

	chatID, slug, err := findChatForPR(client, "foo", "bar", 42)
	if err != nil {
		t.Fatalf("findChatForPR: %v", err)
	}
	if chatID != "chat-42" || slug != "" {
		t.Errorf("got (%q,%q)", chatID, slug)
	}
}

// covResetRunCmdFlags restores the runCmd flags the resume dispatch
// mutates (--chat / --interactive) so later tests see defaults.
func covResetRunCmdFlags(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		for _, name := range []string{"chat", "interactive"} {
			if fl := runCmd.Flags().Lookup(name); fl != nil {
				_ = fl.Value.Set(fl.DefValue)
				fl.Changed = false
			}
		}
	})
}

// TestResumeRunE_RunIDDispatches resolves a run to its chat and dispatches
// into runCmd; the stub serves agent resolution but fails the ws-token
// step, so the test proves the dispatch happened (chat threaded through)
// without opening a real stream.
func TestResumeRunE_RunIDDispatches(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())
	covResetRunCmdFlags(t)
	stub.OnGet("/api/v1/runs/r_ok", clitest.JSONResponse(200, map[string]any{
		"id": "r_ok", "status": "COMPLETED", "chat_id": "chat-55", "agent_slug": "viktor",
	}))
	stub.OnGet("/api/v1/agents", clitest.JSONResponse(200, []map[string]any{
		{"id": covAgentIDCli8, "slug": "viktor"},
	}))
	stub.OnGet("/api/v1/ws-token", clitest.ErrorResponse(500, "no ws for you"))
	resumeCmd.SetContext(context.Background())

	var err error
	covWithStdinCli8(t, "", func() {
		err = resumeCmd.RunE(resumeCmd, []string{"r_ok"})
	})
	if err == nil || !strings.Contains(err.Error(), "get WS token") {
		t.Errorf("expected dispatch to fail at ws-token; got %v", err)
	}
	if got, _ := runCmd.Flags().GetString("chat"); got != "chat-55" {
		t.Errorf("chat not threaded into run command: %q", got)
	}
	if got, _ := runCmd.Flags().GetBool("interactive"); !got {
		t.Error("interactive flag not set by resume dispatch")
	}
}

// TestResumeRunE_ChatIDLooksUpAgent covers the /chats/{id} agent lookup
// (agent_id fallback when agent_slug is empty) before dispatch.
func TestResumeRunE_ChatIDLooksUpAgent(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())
	covResetRunCmdFlags(t)
	stub.OnGet("/api/v1/chats/chat-77", clitest.JSONResponse(200, map[string]any{
		"agent_slug": "", "agent_id": covAgentIDCli8,
	}))
	stub.OnGet("/api/v1/ws-token", clitest.ErrorResponse(500, "no ws for you"))
	resumeCmd.SetContext(context.Background())

	var err error
	covWithStdinCli8(t, "", func() {
		err = resumeCmd.RunE(resumeCmd, []string{"chat-77"})
	})
	if err == nil || !strings.Contains(err.Error(), "get WS token") {
		t.Errorf("expected dispatch to fail at ws-token; got %v", err)
	}
	if got, _ := runCmd.Flags().GetString("chat"); got != "chat-77" {
		t.Errorf("chat not threaded into run command: %q", got)
	}
}

func TestFindChatForPR_APIError(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())
	stub.OnGet("/api/v1/journal", clitest.ErrorResponse(500, "Internal server error"))
	client := newAPIClient()

	_, _, err := findChatForPR(client, "foo", "bar", 7)
	if err == nil || !strings.Contains(err.Error(), "journal search") {
		t.Errorf("expected journal-search error; got %v", err)
	}
}
