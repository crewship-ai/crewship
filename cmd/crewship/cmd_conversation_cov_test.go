package main

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

const covConvAgentID = "cagent7890abcdefghijklm"

func TestConversationSearchRunE_TableOutput(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnPost("/api/v1/conversations/search", clitest.JSONResponse(200, map[string]any{
		"count": 2,
		"query": "deploy pipeline",
		"hits": []map[string]any{
			{"id": "m1", "session_id": "sess-1", "agent_id": covConvAgentID, "role": "user",
				"content": "how does the deploy pipeline work?\nsecond line", "ts": "2026-06-10T10:00:00Z"},
			{"id": "m2", "session_id": "sess-2", "agent_id": covConvAgentID, "role": "assistant",
				"content": "", "tool_summary": strings.Repeat("y", 200), "ts": "2026-06-10T11:00:00Z"},
		},
	}))
	covSetupCli10(t, s.URL())

	out, err := captureStdoutCovCli10(t, func() error {
		return conversationSearchCmd.RunE(conversationSearchCmd, []string{covConvAgentID, "deploy", "pipeline"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, `2 match(es) for "deploy pipeline"`) {
		t.Errorf("header missing:\n%s", out)
	}
	// Multi-word args join into one query; newlines collapse to spaces.
	if !strings.Contains(out, "how does the deploy pipeline work? second line") {
		t.Errorf("snippet not flattened:\n%s", out)
	}
	// Empty content falls back to tool summary, truncated to 157+ellipsis.
	if !strings.Contains(out, strings.Repeat("y", 157)+"...") {
		t.Errorf("tool-summary fallback/truncation missing:\n%s", out)
	}
	calls := s.CallsFor("POST", "/api/v1/conversations/search")
	if len(calls) != 1 {
		t.Fatalf("search calls = %d", len(calls))
	}
	body := string(calls[0].Body)
	if !strings.Contains(body, `"agent_id":"`+covConvAgentID+`"`) || !strings.Contains(body, `"query":"deploy pipeline"`) {
		t.Errorf("search body wrong: %s", body)
	}
	if !strings.Contains(body, `"limit":20`) {
		t.Errorf("default limit missing: %s", body)
	}
}

func TestConversationSearchRunE_NoMatches(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnPost("/api/v1/conversations/search", clitest.JSONResponse(200, map[string]any{
		"count": 0, "query": "ghost", "hits": []map[string]any{},
	}))
	covSetupCli10(t, s.URL())

	out, err := captureStdoutCovCli10(t, func() error {
		return conversationSearchCmd.RunE(conversationSearchCmd, []string{covConvAgentID, "ghost"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, `No conversation matches for "ghost".`) {
		t.Errorf("empty message missing: %q", out)
	}
}

func TestConversationSearchRunE_JSONFormat(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnPost("/api/v1/conversations/search", clitest.JSONResponse(200, map[string]any{
		"count": 1, "query": "auth",
		"hits": []map[string]any{{"id": "m1", "session_id": "sess-1", "role": "user", "content": "auth", "ts": "t"}},
	}))
	covSetupCli10(t, s.URL())
	flagFormat = "json"

	out, err := captureStdoutCovCli10(t, func() error {
		return conversationSearchCmd.RunE(conversationSearchCmd, []string{covConvAgentID, "auth"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, `"count": 1`) || !strings.Contains(out, `"sess-1"`) {
		t.Errorf("json output missing fields:\n%s", out)
	}
}

func TestConversationSearchRunE_NoAuth(t *testing.T) {
	covSetupCli10(t, "http://127.0.0.1:0")
	cliCfg = &cli.CLIConfig{}
	err := conversationSearchCmd.RunE(conversationSearchCmd, []string{covConvAgentID, "q"})
	if err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("expected not-logged-in, got %v", err)
	}
}

func TestConversationSearchRunE_AgentResolutionFailure(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnGet("/api/v1/agents", clitest.JSONResponse(200, []map[string]string{}))
	covSetupCli10(t, s.URL())
	err := conversationSearchCmd.RunE(conversationSearchCmd, []string{"ghost-agent", "q"})
	if err == nil || !strings.Contains(err.Error(), "agent not found") {
		t.Errorf("expected agent resolution failure, got %v", err)
	}
}

func TestConversationSearchRunE_NoWorkspaceAndYAML(t *testing.T) {
	covSetupCli10(t, "http://127.0.0.1:0")
	cliCfg = &cli.CLIConfig{Token: "tok"}
	if err := conversationSearchCmd.RunE(conversationSearchCmd, []string{covConvAgentID, "q"}); err == nil || !strings.Contains(err.Error(), "workspace") {
		t.Errorf("expected workspace error, got %v", err)
	}

	s := clitest.NewStubServer()
	defer s.Close()
	s.OnPost("/api/v1/conversations/search", clitest.JSONResponse(200, map[string]any{
		"count": 1, "query": "q",
		"hits": []map[string]any{{"id": "m1", "session_id": "sess-1", "role": "user", "content": "q", "ts": "t"}},
	}))
	covSetupCli10(t, s.URL())
	flagFormat = "yaml"
	out, err := captureStdoutCovCli10(t, func() error {
		return conversationSearchCmd.RunE(conversationSearchCmd, []string{covConvAgentID, "q"})
	})
	if err != nil {
		t.Fatalf("yaml search: %v", err)
	}
	if !strings.Contains(out, "sess-1") {
		t.Errorf("yaml output missing hit: %q", out)
	}
}

func TestConversationSearchRunE_MalformedJSON(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnPost("/api/v1/conversations/search", clitest.TextResponse(200, `}{`))
	covSetupCli10(t, s.URL())
	if err := conversationSearchCmd.RunE(conversationSearchCmd, []string{covConvAgentID, "q"}); err == nil {
		t.Error("expected decode error")
	}
}

func TestConversationSearchRunE_ServerError(t *testing.T) {
	s := clitest.NewStubServer()
	defer s.Close()
	s.OnPost("/api/v1/conversations/search", clitest.ErrorResponse(500, "fts down"))
	covSetupCli10(t, s.URL())
	err := conversationSearchCmd.RunE(conversationSearchCmd, []string{covConvAgentID, "q"})
	if err == nil || !strings.Contains(err.Error(), "fts down") {
		t.Errorf("expected 500 surfaced, got %v", err)
	}
}
