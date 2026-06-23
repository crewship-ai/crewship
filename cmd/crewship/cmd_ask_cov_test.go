package main

// Coverage tests for cmd_ask.go. The streaming happy path needs a live
// websocket, so coverage stops at the WS-token fetch; everything before
// that (offline modes, agent resolution, chat creation) is exercised
// against the clitest stub.
//
// pickAgentInteractive's huh prompt requires a TTY; only its pre-prompt
// error paths are covered.

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

func TestAskCmdStructure(t *testing.T) {
	if askCmd.Use != "ask [prompt]" {
		t.Errorf("Use = %q", askCmd.Use)
	}
	for _, name := range []string{
		"agent", "agents", "prompt", "quiet", "no-stream", "timeout",
		"with-git-diff", "with-git-staged", "with-git-log", "with-git-status",
		"with-file", "with-cmd", "paste", "dry-run", "estimate",
		"markdown", "no-markdown", "save", "plan", "effort", "show-thinking",
	} {
		if askCmd.Flags().Lookup(name) == nil {
			t.Errorf("ask missing --%s flag", name)
		}
	}
}

func TestAskRunE_DryRunPrintsPromptOffline(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{} // not logged in — dry-run must not care
	covSetFlagCli6(t, askCmd, "dry-run", "true")
	covSetFlagCli6(t, askCmd, "prompt", "review this change")

	out, err := covCaptureStdoutCli6(t, func() error {
		return askCmd.RunE(askCmd, nil)
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "review this change") {
		t.Errorf("assembled prompt not printed: %q", out)
	}
	if !strings.HasSuffix(out, "\n") {
		t.Errorf("dry-run output must end with newline: %q", out)
	}
}

func TestAskRunE_DryRunWithPlanPrefix(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{}
	covSetFlagCli6(t, askCmd, "dry-run", "true")
	covSetFlagCli6(t, askCmd, "prompt", "do the thing")
	covSetFlagCli6(t, askCmd, "plan", "true")

	out, err := covCaptureStdoutCli6(t, func() error {
		return askCmd.RunE(askCmd, nil)
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "[plan-mode]") {
		t.Errorf("plan prefix missing from prompt: %q", out)
	}
	if planModeRequested {
		t.Error("planModeRequested latch must be reset after RunE")
	}
}

func TestAskRunE_EstimatePrints(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{}
	covSetFlagCli6(t, askCmd, "estimate", "true")
	covSetFlagCli6(t, askCmd, "prompt", "hello world")

	out, err := covCaptureStdoutCli6(t, func() error {
		return askCmd.RunE(askCmd, nil)
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "prompt size:") {
		t.Errorf("estimate output missing: %q", out)
	}
}

func TestAskRunE_EmptyPromptError(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{}
	covSetFlagCli6(t, askCmd, "dry-run", "true")
	covResetFlagsCli6(t, askCmd, "prompt")

	err := askCmd.RunE(askCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "prompt is required") {
		t.Errorf("expected prompt-required error, got %v", err)
	}
}

func TestAskRunE_InvalidEffort(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{}
	covSetFlagCli6(t, askCmd, "dry-run", "true")
	covSetFlagCli6(t, askCmd, "prompt", "x")
	covSetFlagCli6(t, askCmd, "effort", "bogus")

	err := askCmd.RunE(askCmd, nil)
	if err == nil || !strings.Contains(err.Error(), `invalid --effort "bogus"`) {
		t.Errorf("expected effort validation error, got %v", err)
	}
	if effortMode != "" {
		t.Errorf("effortMode latch must stay clean, got %q", effortMode)
	}
}

func TestAskRunE_NoAuth(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{}

	err := askCmd.RunE(askCmd, []string{"hi"})
	if err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("expected 'not logged in', got %v", err)
	}
}

func TestAskRunE_NoWorkspace(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{Token: "fake-token"}
	flagWorkspace = ""
	t.Setenv("CREWSHIP_WORKSPACE", "")

	err := askCmd.RunE(askCmd, []string{"hi"})
	if err == nil || !strings.Contains(err.Error(), "workspace") {
		t.Errorf("expected workspace error, got %v", err)
	}
}

func TestAskRunE_NoDefaultAgentNonTTY(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)
	t.Setenv("CREWSHIP_DEFAULT_AGENT", "")
	covResetFlagsCli6(t, askCmd, "agent", "agents")

	// go test stdin is not a TTY, so the interactive picker must be
	// refused with an actionable error.
	err := askCmd.RunE(askCmd, []string{"hi"})
	if err == nil || !strings.Contains(err.Error(), "no default agent set") {
		t.Errorf("expected no-default-agent error, got %v", err)
	}
}

func TestAskRunE_ResolveAgentError(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)
	covSetFlagCli6(t, askCmd, "agent", "ghost")

	stub.OnGet("/api/v1/agents", clitest.JSONResponse(200, []map[string]string{}))

	err := askCmd.RunE(askCmd, []string{"hi"})
	if err == nil || !strings.Contains(err.Error(), "agent not found: ghost") {
		t.Errorf("expected agent-not-found, got %v", err)
	}
}

func TestAskRunE_CreateChatError(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)
	covSetFlagCli6(t, askCmd, "agent", covAgentIDCli6) // CUID → skips resolution

	stub.OnPost("/api/v1/agents/"+covAgentIDCli6+"/chats", clitest.ErrorResponse(500, "chat backend down"))

	err := askCmd.RunE(askCmd, []string{"hi"})
	if err == nil || !strings.Contains(err.Error(), "chat backend down") {
		t.Errorf("expected chat-creation error surfaced, got %v", err)
	}
	// The chat creation body must tag CLI origin.
	calls := stub.CallsFor("POST", "/api/v1/agents/"+covAgentIDCli6+"/chats")
	if len(calls) != 1 {
		t.Fatalf("expected 1 chat POST, got %d", len(calls))
	}
	body := covDecodeBody(t, calls[0].Body)
	if body["origin"] != "CLI" || body["mode"] != "CHAT" {
		t.Errorf("chat body wrong: %v", body)
	}
}

func TestAskRunE_WSTokenError(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)
	covSetFlagCli6(t, askCmd, "agent", covAgentIDCli6)

	stub.OnPost("/api/v1/agents/"+covAgentIDCli6+"/chats", clitest.JSONResponse(201, map[string]string{"id": "cchat0123456789abcdef"}))
	stub.OnGet("/api/v1/ws-token", clitest.ErrorResponse(500, "no ws"))

	err := askCmd.RunE(askCmd, []string{"hi"})
	if err == nil || !strings.Contains(err.Error(), "get WS token") {
		t.Errorf("expected WS token error, got %v", err)
	}
}

func TestAskRunE_FanoutResolveError(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)
	covSetFlagCli6(t, askCmd, "agents", "ghost")

	stub.OnGet("/api/v1/agents", clitest.JSONResponse(200, []map[string]string{}))

	err := askCmd.RunE(askCmd, []string{"hi"})
	if err == nil || !strings.Contains(err.Error(), `resolve "ghost"`) {
		t.Errorf("expected fan-out resolve error, got %v", err)
	}
}

func TestAskRunE_BuildPromptError(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{}
	covSetFlagCli6(t, askCmd, "dry-run", "true")
	covSetFlagCli6(t, askCmd, "with-file", "/nonexistent/context-file.md")

	err := askCmd.RunE(askCmd, []string{"hi"})
	if err == nil || !strings.Contains(err.Error(), "context-file.md") {
		t.Errorf("expected with-file read error, got %v", err)
	}
}

func TestAskRunE_ShowThinkingLatchReset(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{}
	covSetFlagCli6(t, askCmd, "dry-run", "true")
	covSetFlagCli6(t, askCmd, "prompt", "x")
	covSetFlagCli6(t, askCmd, "show-thinking", "true")
	covSetFlagCli6(t, askCmd, "effort", "high")

	if _, err := covCaptureStdoutCli6(t, func() error {
		return askCmd.RunE(askCmd, nil)
	}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	// Both latches were set inside RunE and must be reset by the
	// deferred ResetAIFirstLatches.
	if showThinking {
		t.Error("showThinking latch leaked past RunE")
	}
	if effortMode != "" {
		t.Errorf("effortMode latch leaked: %q", effortMode)
	}
}

func TestAskRunE_SaveFileOpenError(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)
	covSetFlagCli6(t, askCmd, "agent", covAgentIDCli6)
	covSetFlagCli6(t, askCmd, "save", "/nonexistent-dir/answer.md")

	err := askCmd.RunE(askCmd, []string{"hi"})
	if err == nil || !strings.Contains(err.Error(), "open save file") {
		t.Errorf("expected save-file open error, got %v", err)
	}
	if n := len(stub.Calls()); n != 0 {
		t.Errorf("save-file failure must precede any HTTP call, got %d", n)
	}
}

func TestAskRunE_SaveFileAndTimeoutThenChatError(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)
	covSetFlagCli6(t, askCmd, "agent", covAgentIDCli6)
	covSetFlagCli6(t, askCmd, "save", t.TempDir()+"/answer.md")
	covSetFlagCli6(t, askCmd, "timeout", "30")

	stub.OnPost("/api/v1/agents/"+covAgentIDCli6+"/chats", clitest.ErrorResponse(500, "down"))

	err := askCmd.RunE(askCmd, []string{"hi"})
	if err == nil || !strings.Contains(err.Error(), "down") {
		t.Errorf("expected chat error after save/timeout setup, got %v", err)
	}
}

func TestAskRunE_ChatDecodeError(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)
	covSetFlagCli6(t, askCmd, "agent", covAgentIDCli6)

	stub.OnPost("/api/v1/agents/"+covAgentIDCli6+"/chats", clitest.TextResponse(200, "not json"))

	err := askCmd.RunE(askCmd, []string{"hi"})
	if err == nil || !strings.Contains(err.Error(), "decode response") {
		t.Errorf("expected decode error, got %v", err)
	}
}

func TestAskRunE_FanoutWSTokenError(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)
	// Trailing/blank entries must be skipped by the fan-out loop.
	covSetFlagCli6(t, askCmd, "agents", "viktor, ,eva")

	stub.OnGet("/api/v1/agents", clitest.JSONResponse(200, []map[string]string{
		{"id": "cagentviktor123456789", "slug": "viktor"},
		{"id": "cagenteva123456789012", "slug": "eva"},
	}))
	stub.OnGet("/api/v1/ws-token", clitest.ErrorResponse(500, "no ws"))

	err := askCmd.RunE(askCmd, []string{"hi"})
	if err == nil || !strings.Contains(err.Error(), "get WS token") {
		t.Errorf("expected WS token error after fan-out resolution, got %v", err)
	}
	// Both non-blank slugs resolved against the same list endpoint.
	if n := len(stub.CallsFor("GET", "/api/v1/agents")); n != 2 {
		t.Errorf("expected 2 resolution calls, got %d", n)
	}
}

func TestAskRunE_NoStreamWSDialFails(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)
	covSetFlagCli6(t, askCmd, "agent", covAgentIDCli6)
	covSetFlagCli6(t, askCmd, "no-stream", "true")

	stub.OnPost("/api/v1/agents/"+covAgentIDCli6+"/chats", clitest.JSONResponse(201, map[string]string{"id": "cchat0123456789abcdef"}))
	stub.OnGet("/api/v1/ws-token", clitest.JSONResponse(200, map[string]string{"token": "tok-1"}))

	// The stub HTTP server has no /ws upgrade endpoint, so the WS
	// handshake fails — exercising the full pre-stream pipeline.
	err := askCmd.RunE(askCmd, []string{"hi"})
	if err == nil {
		t.Fatal("expected WS dial failure, got nil")
	}
	if n := len(stub.CallsFor("GET", "/api/v1/ws-token")); n != 1 {
		t.Errorf("expected 1 ws-token call, got %d", n)
	}
}

// ─── pickAgentInteractive (pre-prompt error paths) ───────────────────────

func TestPickAgentInteractive_DecodeError(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnGet("/api/v1/agents", clitest.TextResponse(200, "not json"))

	client := cli.NewClient(stub.URL(), "fake-token", covWorkspaceIDCli6)
	_, _, err := pickAgentInteractive(client)
	if err == nil || !strings.Contains(err.Error(), "decode response") {
		t.Errorf("expected decode error, got %v", err)
	}
}

func TestPickAgentInteractive_TransportError(t *testing.T) {
	stub := clitest.NewStubServer()
	stub.Close() // connection refused

	client := cli.NewClient(stub.URL(), "fake-token", covWorkspaceIDCli6)
	_, _, err := pickAgentInteractive(client)
	if err == nil || !strings.Contains(err.Error(), "request failed") {
		t.Errorf("expected transport error, got %v", err)
	}
}

func TestPickAgentInteractive_APIError(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnGet("/api/v1/agents", clitest.ErrorResponse(500, "boom"))

	client := cli.NewClient(stub.URL(), "fake-token", covWorkspaceIDCli6)
	_, _, err := pickAgentInteractive(client)
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("expected 500 surfaced, got %v", err)
	}
}

func TestPickAgentInteractive_NoAgents(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnGet("/api/v1/agents", clitest.JSONResponse(200, []map[string]string{}))

	client := cli.NewClient(stub.URL(), "fake-token", covWorkspaceIDCli6)
	_, _, err := pickAgentInteractive(client)
	if err == nil || !strings.Contains(err.Error(), "no agents available in this workspace") {
		t.Errorf("expected empty-workspace error, got %v", err)
	}
}
