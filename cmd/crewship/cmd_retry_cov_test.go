package main

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

// stubRunsList registers GET /api/v1/runs with a single-run page.
func stubRunsList(s *clitest.StubServer, id, agentID string, agentSlug, chatID *string) {
	s.OnGet("/api/v1/runs", clitest.JSONResponse(200, map[string]any{
		"data": []map[string]any{{
			"id": id, "agent_id": agentID, "agent_slug": agentSlug, "chat_id": chatID,
		}},
	}))
}

func TestRetryRunE_NoAuth(t *testing.T) {
	covSetupCli10(t, "http://127.0.0.1:0")
	cliCfg = &cli.CLIConfig{}
	if err := retryCmd.RunE(retryCmd, []string{"r_1"}); err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("expected not-logged-in, got %v", err)
	}
}

func TestRetryRunE_RunNotFound(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/runs", clitest.JSONResponse(200, map[string]any{"data": []map[string]any{}}))
	covSetupCli10(t, s.URL())

	err := retryCmd.RunE(retryCmd, []string{"r_missing"})
	if err == nil || !strings.Contains(err.Error(), "not found in last 100 runs") {
		t.Errorf("expected not-found, got %v", err)
	}
}

func TestRetryRunE_RunWithoutAgentID(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	stubRunsList(s, "r_1", "", nil, nil)
	covSetupCli10(t, s.URL())

	err := retryCmd.RunE(retryCmd, []string{"r_1"})
	if err == nil || !strings.Contains(err.Error(), "has no agent_id") {
		t.Errorf("expected no-agent error, got %v", err)
	}
}

func TestRetryRunE_NoChatNoNewPrompt(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	stubRunsList(s, "r_1", "cagent7890abcdefghijklm", nil, nil)
	covSetupCli10(t, s.URL())

	err := retryCmd.RunE(retryCmd, []string{"r_1"})
	if err == nil || !strings.Contains(err.Error(), "has no chat_id; pass --new-prompt") {
		t.Errorf("expected chat-missing hint, got %v", err)
	}
}

func TestRetryRunE_PromptUnrecoverable(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	chat := "c_old"
	stubRunsList(s, "r_1", "cagent7890abcdefghijklm", nil, &chat)
	// Messages exist but none with a user role → prompt recovery fails.
	s.OnGet("/api/v1/chats/c_old/messages", clitest.JSONResponse(200, map[string]any{
		"messages": []map[string]string{{"role": "ASSISTANT", "content": "hi"}},
	}))
	covSetupCli10(t, s.URL())

	err := retryCmd.RunE(retryCmd, []string{"r_1"})
	if err == nil || !strings.Contains(err.Error(), "could not recover original prompt") {
		t.Errorf("expected recovery failure, got %v", err)
	}
}

func TestRetryRunE_ContinueRequiresChatID(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	stubRunsList(s, "r_1", "cagent7890abcdefghijklm", nil, nil)
	covSetupCli10(t, s.URL())
	setFlagCovCli10(t, retryCmd, "new-prompt", "try again")
	setFlagCovCli10(t, retryCmd, "continue", "true")

	err := retryCmd.RunE(retryCmd, []string{"r_1"})
	if err == nil || !strings.Contains(err.Error(), "--continue requires the original run to have a chat_id") {
		t.Errorf("expected continue guard, got %v", err)
	}
}

func TestRetryRunE_CreateChatFails(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	agentID := "cagent7890abcdefghijklm"
	stubRunsList(s, "r_1", agentID, nil, nil)
	s.OnPost("/api/v1/agents/"+agentID+"/chats", clitest.ErrorResponse(500, "chat svc down"))
	covSetupCli10(t, s.URL())
	setFlagCovCli10(t, retryCmd, "new-prompt", "try again")

	err := retryCmd.RunE(retryCmd, []string{"r_1"})
	if err == nil || !strings.Contains(err.Error(), "chat svc down") {
		t.Errorf("expected chat-create failure surfaced, got %v", err)
	}
	calls := s.CallsFor("POST", "/api/v1/agents/"+agentID+"/chats")
	if len(calls) != 1 || !strings.Contains(string(calls[0].Body), `"origin":"CLI"`) {
		t.Errorf("chat body wrong: %+v", calls)
	}
}

// TestRetryRunE_NoStreamBannerAndDispatch drives retry all the way to
// runNoStream. The stub HTTP server rejects the websocket upgrade, so
// the dispatch fails fast — but the retry banner and the full RunE
// pipeline up to that point are exercised.
func TestRetryRunE_NoStreamBannerAndDispatch(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	agentID := "cagent7890abcdefghijklm"
	slug := "viktor"
	stubRunsList(s, "r_1", agentID, &slug, nil)
	s.OnPost("/api/v1/agents/"+agentID+"/chats", clitest.JSONResponse(201, map[string]string{"id": "c_new"}))
	s.OnGet("/api/v1/ws-token", clitest.JSONResponse(200, map[string]string{"token": "ws-tok"}))
	covSetupCli10(t, s.URL())
	setFlagCovCli10(t, retryCmd, "new-prompt", "shorter please")
	setFlagCovCli10(t, retryCmd, "no-stream", "true")

	stderr, err := captureStderrCov(t, func() error {
		return retryCmd.RunE(retryCmd, []string{"r_1"})
	})
	if err == nil {
		t.Fatal("expected websocket dial failure from stub server")
	}
	if !strings.Contains(stderr, "[retry r_1 → viktor]") {
		t.Errorf("retry banner missing: %q", stderr)
	}
	if !strings.Contains(stderr, `"shorter please"`) {
		t.Errorf("prompt summary missing: %q", stderr)
	}
}

func TestRetryRunE_WSTokenFails(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	agentID := "cagent7890abcdefghijklm"
	slug := "viktor"
	stubRunsList(s, "r_1", agentID, &slug, nil)
	s.OnPost("/api/v1/agents/"+agentID+"/chats", clitest.JSONResponse(201, map[string]string{"id": "c_new"}))
	s.OnGet("/api/v1/ws-token", clitest.ErrorResponse(503, "ws issuer down"))
	covSetupCli10(t, s.URL())
	setFlagCovCli10(t, retryCmd, "new-prompt", "try again")

	err := retryCmd.RunE(retryCmd, []string{"r_1"})
	if err == nil || !strings.Contains(err.Error(), "get WS token") {
		t.Errorf("expected ws-token failure, got %v", err)
	}
}
