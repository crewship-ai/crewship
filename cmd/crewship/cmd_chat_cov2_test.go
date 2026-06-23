package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

func TestChatCmds_NoAuth(t *testing.T) {
	covRunNoAuth(t, []covCmdCase{
		{name: "chat", cmd: chatCmd, args: []string{covChatID}},
		{name: "react add", cmd: chatReactAddCmd, args: []string{"c1", "m1", "👍"}},
		{name: "react remove", cmd: chatReactRemoveCmd, args: []string{"c1", "m1", "👍"}},
		{name: "react list", cmd: chatReactListCmd, args: []string{"c1", "m1"}},
		{name: "attach", cmd: chatAttachCmd, args: []string{covChatID, "/tmp/x"}},
		{name: "list", cmd: chatListCmd, args: []string{covAgentIDCli3}},
	})
}

func TestChatCmd_YAMLFormat(t *testing.T) {
	stub := covStub(t)
	covResetFlags(t, chatCmd)
	cliCfg.Format = "yaml"
	stub.OnGet("/api/v1/chats/"+covChatID+"/messages",
		clitest.JSONResponse(200, map[string]any{"messages": []map[string]any{
			{"role": "user", "content": "yaml-body"},
		}}))
	out := covCaptureStdoutCli3(t, func() {
		if err := chatCmd.RunE(chatCmd, []string{covChatID}); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if !strings.Contains(out, "yaml-body") {
		t.Errorf("yaml output missing: %q", out)
	}
}

func TestChatCmd_FilterFlag(t *testing.T) {
	stub := covStub(t)
	covResetFlags(t, chatCmd)
	stub.OnGet("/api/v1/chats/"+covChatID+"/messages",
		clitest.JSONResponse(200, map[string]any{"messages": []map[string]any{
			{"role": "user", "content": "filter-me"},
		}}))
	covSwapJQ(t,
		func(string) (string, error) { return "/fake/jq", nil },
		func(string, string) jqRunner { return &fakeJQCov{out: []byte("\"filter-me\"\n")} })

	if err := chatCmd.Flags().Set("filter", ".[0].content"); err != nil {
		t.Fatal(err)
	}
	out := covCaptureStdoutCli3(t, func() {
		if err := chatCmd.RunE(chatCmd, []string{covChatID}); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if out != "\"filter-me\"\n" {
		t.Errorf("filtered output: %q", out)
	}
}

func TestPrintChatTranscript_MarkdownRenderer(t *testing.T) {
	md := cli.NewMarkdownRenderer()
	msgs := []map[string]any{
		{"role": "assistant", "content": "plain words", "created_at": "2026-06-01T10:00:00Z"},
	}
	out := covCaptureStdoutCli3(t, func() { printChatTranscript(msgs, md) })
	if !strings.Contains(out, "plain words") {
		t.Errorf("rendered assistant message missing: %q", out)
	}
}

func TestChatReactCmds_ServerErrors(t *testing.T) {
	t.Run("add", func(t *testing.T) {
		stub := covStub(t)
		stub.OnPost("/api/v1/chats/c1/messages/m1/reactions", clitest.ErrorResponse(404, "message gone"))
		err := chatReactAddCmd.RunE(chatReactAddCmd, []string{"c1", "m1", "👍"})
		if err == nil || !strings.Contains(err.Error(), "message gone") {
			t.Fatalf("expected 404, got %v", err)
		}
	})
	t.Run("remove", func(t *testing.T) {
		stub := covStub(t)
		stub.OnDelete("/api/v1/chats/c1/messages/m1/reactions/👍", clitest.ErrorResponse(404, "reaction gone"))
		err := chatReactRemoveCmd.RunE(chatReactRemoveCmd, []string{"c1", "m1", "👍"})
		if err == nil || !strings.Contains(err.Error(), "reaction gone") {
			t.Fatalf("expected 404, got %v", err)
		}
	})
	t.Run("list", func(t *testing.T) {
		stub := covStub(t)
		stub.OnGet("/api/v1/chats/c1/messages/m1/reactions", clitest.ErrorResponse(500, "boom"))
		if err := chatReactListCmd.RunE(chatReactListCmd, []string{"c1", "m1"}); err == nil {
			t.Fatal("expected error")
		}
	})
}

func TestChatReactListCmd_YAML(t *testing.T) {
	stub := covStub(t)
	cliCfg.Format = "yaml"
	stub.OnGet("/api/v1/chats/c1/messages/m1/reactions",
		clitest.JSONResponse(200, map[string]any{"reactions": []map[string]any{
			{"emoji": "🚀", "count": 1, "mine": false},
		}}))
	out := covCaptureStdoutCli3(t, func() {
		if err := chatReactListCmd.RunE(chatReactListCmd, []string{"c1", "m1"}); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	// YAML escapes the emoji to a unicode literal — assert on the
	// stable fields instead.
	if !strings.Contains(out, "count: 1") || !strings.Contains(out, "mine: false") {
		t.Errorf("yaml reactions missing: %q", out)
	}
}

func TestChatAttachCmd_AgentOverrideResolveFails(t *testing.T) {
	stub := covStub(t)
	covResetFlags(t, chatAttachCmd)
	// Non-CUID override forces resolveAgentID's GET /agents — which fails.
	stub.OnGet("/api/v1/agents", clitest.ErrorResponse(500, "agents broke"))
	if err := chatAttachCmd.Flags().Set("agent", "viktor"); err != nil {
		t.Fatal(err)
	}
	err := chatAttachCmd.RunE(chatAttachCmd, []string{covChatID, "/tmp/whatever"})
	if err == nil || !strings.Contains(err.Error(), "agents broke") {
		t.Fatalf("expected resolver error, got %v", err)
	}
}

func TestChatAttachCmd_TransportError(t *testing.T) {
	saveCLIState(t)
	covResetFlags(t, chatAttachCmd)
	t.Setenv("CREWSHIP_SERVER", "")
	t.Setenv("CREWSHIP_WORKSPACE", "")
	flagServer = ""
	flagWorkspace = ""
	cliCfg = &cli.CLIConfig{Token: "tk", Workspace: covWSCli3, Server: "http://127.0.0.1:1"}

	src := filepath.Join(t.TempDir(), "f.txt")
	if err := os.WriteFile(src, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := chatAttachCmd.Flags().Set("agent", covAgentIDCli3); err != nil {
		t.Fatal(err)
	}
	if err := chatAttachCmd.RunE(chatAttachCmd, []string{covChatID, src}); err == nil {
		t.Fatal("expected transport error")
	}
}

func TestChatListCmd_ResolveFails(t *testing.T) {
	stub := covStub(t)
	stub.OnGet("/api/v1/agents", clitest.ErrorResponse(500, "agents broke"))
	err := chatListCmd.RunE(chatListCmd, []string{"viktor"})
	if err == nil || !strings.Contains(err.Error(), "agents broke") {
		t.Fatalf("expected resolver error, got %v", err)
	}
}

func TestLookupChatAgentID_TransportAndDecodeErrors(t *testing.T) {
	t.Run("transport", func(t *testing.T) {
		saveCLIState(t)
		t.Setenv("CREWSHIP_SERVER", "")
		flagServer = ""
		cliCfg = &cli.CLIConfig{Token: "tk", Workspace: covWSCli3, Server: "http://127.0.0.1:1"}
		if _, err := lookupChatAgentID(newAPIClient(), covChatID); err == nil {
			t.Fatal("expected transport error")
		}
	})
	t.Run("malformed agents list", func(t *testing.T) {
		stub := covStub(t)
		stub.OnGet("/api/v1/agents", clitest.TextResponse(200, "[broken"))
		if _, err := lookupChatAgentID(newAPIClient(), covChatID); err == nil {
			t.Fatal("expected decode error")
		}
	})
	_ = fmt.Sprint // keep fmt import if unused elsewhere
}
