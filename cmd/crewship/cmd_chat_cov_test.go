package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

const covChatID = "cchat00000000000000000aa"

// ─── chat transcript ────────────────────────────────────────────────────

func TestChatCmd_TranscriptPretty(t *testing.T) {
	stub := covStub(t)
	covResetFlags(t, chatCmd)
	stub.OnGet("/api/v1/chats/"+covChatID+"/messages",
		clitest.JSONResponse(200, map[string]any{"messages": []map[string]any{
			{"role": "user", "content": "hello there", "created_at": "2026-06-01T10:00:00Z"},
			{"role": "assistant", "content": "## answer", "created_at": "2026-06-01T10:00:05Z"},
			{"role": "system", "content": "sys note", "created_at": "2026-06-01T10:00:06Z"},
		}}))

	out := covCaptureStdoutCli3(t, func() {
		if err := chatCmd.RunE(chatCmd, []string{covChatID}); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	for _, want := range []string{"hello there", "answer", "sys note", "user", "assistant", "2026-06-01 10:00:00"} {
		if !strings.Contains(out, want) {
			t.Errorf("transcript missing %q in %q", want, out)
		}
	}
	calls := stub.CallsFor("GET", "/api/v1/chats/"+covChatID+"/messages")
	if len(calls) != 1 || !strings.Contains(calls[0].Query, "limit=500") {
		t.Errorf("expected one GET with limit=500, got %+v", calls)
	}
}

func TestChatCmd_SinceFilterAndJSON(t *testing.T) {
	stub := covStub(t)
	covResetFlags(t, chatCmd)
	cliCfg.Format = "json"
	stub.OnGet("/api/v1/chats/"+covChatID+"/messages",
		clitest.JSONResponse(200, map[string]any{"messages": []map[string]any{
			{"role": "user", "content": "ancient", "created_at": "2020-01-01T00:00:00Z"},
			{"role": "user", "content": "unparseable-ts-kept", "created_at": "not-a-time"},
			{"role": "assistant", "content": "recent", "created_at": "2099-01-01T00:00:00Z"},
		}}))

	if err := chatCmd.Flags().Set("since", "1h"); err != nil {
		t.Fatal(err)
	}
	out := covCaptureStdoutCli3(t, func() {
		if err := chatCmd.RunE(chatCmd, []string{covChatID}); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if strings.Contains(out, "ancient") {
		t.Errorf("--since should drop old messages: %q", out)
	}
	if !strings.Contains(out, "recent") || !strings.Contains(out, "unparseable-ts-kept") {
		t.Errorf("--since dropped messages it should keep: %q", out)
	}
}

func TestChatCmd_BadSince(t *testing.T) {
	stub := covStub(t)
	covResetFlags(t, chatCmd)
	stub.OnGet("/api/v1/chats/"+covChatID+"/messages",
		clitest.JSONResponse(200, map[string]any{"messages": []map[string]any{}}))

	if err := chatCmd.Flags().Set("since", "@@@not-a-time@@@"); err != nil {
		t.Fatal(err)
	}
	err := chatCmd.RunE(chatCmd, []string{covChatID})
	if err == nil || !strings.Contains(err.Error(), "bad --since") {
		t.Fatalf("expected bad --since error, got %v", err)
	}
}

func TestChatCmd_APIError(t *testing.T) {
	stub := covStub(t)
	covResetFlags(t, chatCmd)
	stub.OnGet("/api/v1/chats/"+covChatID+"/messages",
		clitest.ErrorResponse(404, "chat not found"))

	err := chatCmd.RunE(chatCmd, []string{covChatID})
	if err == nil || !strings.Contains(err.Error(), "chat not found") {
		t.Fatalf("expected 404 error, got %v", err)
	}
}

func TestPrintChatTranscript_Empty(t *testing.T) {
	out := covCaptureStdoutCli3(t, func() { printChatTranscript(nil, nil) })
	if !strings.Contains(out, "No messages.") {
		t.Errorf("expected 'No messages.', got %q", out)
	}
}

// ─── reactions ──────────────────────────────────────────────────────────

func TestChatReactAddCmd(t *testing.T) {
	stub := covStub(t)
	path := "/api/v1/chats/c1/messages/m1/reactions"
	stub.OnPost(path, clitest.JSONResponse(200, map[string]any{"ok": true}))

	if err := chatReactAddCmd.RunE(chatReactAddCmd, []string{"c1", "m1", "🔥"}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	calls := stub.CallsFor("POST", path)
	if len(calls) != 1 {
		t.Fatalf("expected 1 POST, got %d", len(calls))
	}
	var body map[string]string
	clitest.MustDecodeJSONBody(calls[0].Body, &body)
	if body["emoji"] != "🔥" {
		t.Errorf("emoji body: got %q", body["emoji"])
	}
}

func TestChatReactRemoveCmd(t *testing.T) {
	stub := covStub(t)
	path := "/api/v1/chats/c1/messages/m1/reactions/🔥"
	stub.OnDelete(path, clitest.EmptyResponse(204))

	if err := chatReactRemoveCmd.RunE(chatReactRemoveCmd, []string{"c1", "m1", "🔥"}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if calls := stub.CallsFor("DELETE", path); len(calls) != 1 {
		t.Errorf("expected 1 DELETE, got %d", len(calls))
	}
}

func TestChatReactListCmd(t *testing.T) {
	stub := covStub(t)
	path := "/api/v1/chats/c1/messages/m1/reactions"

	t.Run("table with rows", func(t *testing.T) {
		stub.OnGet(path, clitest.JSONResponse(200, map[string]any{"reactions": []map[string]any{
			{"emoji": "🔥", "count": 2, "mine": true},
			{"emoji": "👀", "count": 1, "mine": false},
		}}))
		out := covCaptureStdoutCli3(t, func() {
			if err := chatReactListCmd.RunE(chatReactListCmd, []string{"c1", "m1"}); err != nil {
				t.Errorf("RunE: %v", err)
			}
		})
		if !strings.Contains(out, "🔥") || !strings.Contains(out, "yes") || !strings.Contains(out, "no") {
			t.Errorf("reaction table wrong: %q", out)
		}
	})

	t.Run("empty", func(t *testing.T) {
		stub.OnGet(path, clitest.JSONResponse(200, map[string]any{"reactions": []map[string]any{}}))
		out := covCaptureStdoutCli3(t, func() {
			if err := chatReactListCmd.RunE(chatReactListCmd, []string{"c1", "m1"}); err != nil {
				t.Errorf("RunE: %v", err)
			}
		})
		if !strings.Contains(out, "No reactions.") {
			t.Errorf("expected 'No reactions.', got %q", out)
		}
	})

	t.Run("json format", func(t *testing.T) {
		cliCfg.Format = "json"
		t.Cleanup(func() { cliCfg.Format = "" })
		stub.OnGet(path, clitest.JSONResponse(200, map[string]any{"reactions": []map[string]any{
			{"emoji": "🔥", "count": 2, "mine": true},
		}}))
		out := covCaptureStdoutCli3(t, func() {
			if err := chatReactListCmd.RunE(chatReactListCmd, []string{"c1", "m1"}); err != nil {
				t.Errorf("RunE: %v", err)
			}
		})
		if !strings.Contains(out, `"emoji"`) {
			t.Errorf("json output wrong: %q", out)
		}
	})
}

// ─── chat list ──────────────────────────────────────────────────────────

func TestChatListCmd(t *testing.T) {
	stub := covStub(t)
	title := "Fix the flaky test suite once and for all please"
	stub.OnGet("/api/v1/agents/"+covAgentIDCli3+"/chats",
		clitest.JSONResponse(200, []map[string]any{
			{"id": "c1", "title": title, "status": "ENDED", "message_count": 12,
				"started_at": "2026-06-01T10:00:00Z", "origin": "web"},
			{"id": "c2", "status": "ACTIVE", "message_count": 1, "started_at": "bad-ts"},
		}))

	out := covCaptureStdoutCli3(t, func() {
		if err := chatListCmd.RunE(chatListCmd, []string{covAgentIDCli3}); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if !strings.Contains(out, "c1") || !strings.Contains(out, "c2") {
		t.Errorf("chat list missing rows: %q", out)
	}
	if !strings.Contains(out, "2026-06-01 10:00") {
		t.Errorf("started_at not reformatted: %q", out)
	}
	// nil title/origin render as "-"
	if !strings.Contains(out, "-") {
		t.Errorf("expected '-' placeholders: %q", out)
	}
	if strings.Contains(out, title) {
		t.Errorf("long title should be truncated to 36 chars: %q", out)
	}
}

func TestChatListCmd_APIError(t *testing.T) {
	stub := covStub(t)
	stub.OnGet("/api/v1/agents/"+covAgentIDCli3+"/chats",
		clitest.ErrorResponse(500, "db gone"))
	err := chatListCmd.RunE(chatListCmd, []string{covAgentIDCli3})
	if err == nil || !strings.Contains(err.Error(), "db gone") {
		t.Fatalf("expected error, got %v", err)
	}
}

// ─── lookupChatAgentID ──────────────────────────────────────────────────

func TestLookupChatAgentID(t *testing.T) {
	otherAgent := "cother0000000000000000aa"

	t.Run("found on second agent", func(t *testing.T) {
		stub := covStub(t)
		stub.OnGet("/api/v1/agents", clitest.JSONResponse(200, []map[string]any{
			{"id": otherAgent, "slug": "eva"},
			{"id": covAgentIDCli3, "slug": "viktor"},
		}))
		// First agent errors → skipped; second owns the chat.
		stub.OnGet("/api/v1/agents/"+otherAgent+"/chats", clitest.ErrorResponse(403, "denied"))
		stub.OnGet("/api/v1/agents/"+covAgentIDCli3+"/chats",
			clitest.JSONResponse(200, []map[string]any{{"id": covChatID}}))

		got, err := lookupChatAgentID(newAPIClient(), covChatID)
		if err != nil {
			t.Fatalf("lookupChatAgentID: %v", err)
		}
		if got != covAgentIDCli3 {
			t.Errorf("agent id: got %q want %q", got, covAgentIDCli3)
		}
	})

	t.Run("not found", func(t *testing.T) {
		stub := covStub(t)
		stub.OnGet("/api/v1/agents", clitest.JSONResponse(200, []map[string]any{
			{"id": covAgentIDCli3, "slug": "viktor"},
		}))
		stub.OnGet("/api/v1/agents/"+covAgentIDCli3+"/chats",
			clitest.JSONResponse(200, []map[string]any{{"id": "c_unrelated"}}))

		_, err := lookupChatAgentID(newAPIClient(), covChatID)
		if err == nil || !strings.Contains(err.Error(), "not found in any agent") {
			t.Fatalf("expected not-found error, got %v", err)
		}
	})

	t.Run("agents list error", func(t *testing.T) {
		stub := covStub(t)
		stub.OnGet("/api/v1/agents", clitest.ErrorResponse(500, "list broke"))
		_, err := lookupChatAgentID(newAPIClient(), covChatID)
		if err == nil || !strings.Contains(err.Error(), "list broke") {
			t.Fatalf("expected list error, got %v", err)
		}
	})
}

// ─── chat attach ────────────────────────────────────────────────────────

func TestChatAttachCmd_WithAgentOverride(t *testing.T) {
	stub := covStub(t)
	covResetFlags(t, chatAttachCmd)
	attachPath := "/api/v1/agents/" + covAgentIDCli3 + "/chats/" + covChatID + "/attachments"
	stub.OnPost(attachPath, clitest.JSONResponse(200, map[string]any{
		"filename": "diagram.png", "size": 11, "agent_path": "/output/viktor/attachments/x/diagram.png",
	}))

	src := filepath.Join(t.TempDir(), "diagram.png")
	if err := os.WriteFile(src, []byte("png-content"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := chatAttachCmd.Flags().Set("agent", covAgentIDCli3); err != nil {
		t.Fatal(err)
	}
	if err := chatAttachCmd.RunE(chatAttachCmd, []string{covChatID, src}); err != nil {
		t.Fatalf("RunE: %v", err)
	}

	calls := stub.CallsFor("POST", attachPath)
	if len(calls) != 1 {
		t.Fatalf("expected 1 POST, got %d", len(calls))
	}
	ct := calls[0].Headers.Get("Content-Type")
	if !strings.HasPrefix(ct, "multipart/form-data") {
		t.Errorf("content type: got %q", ct)
	}
	if !strings.Contains(string(calls[0].Body), "png-content") {
		t.Errorf("multipart body missing file bytes")
	}
	if !strings.Contains(string(calls[0].Body), `filename="diagram.png"`) {
		t.Errorf("multipart body missing filename part: %q", calls[0].Body)
	}
	if !strings.Contains(calls[0].Query, "workspace_id="+covWSCli3) {
		t.Errorf("workspace_id not injected: %q", calls[0].Query)
	}
	if auth := calls[0].Headers.Get("Authorization"); auth != "Bearer test-token" {
		t.Errorf("auth header: got %q", auth)
	}
}

func TestChatAttachCmd_AutoResolveAgent(t *testing.T) {
	stub := covStub(t)
	covResetFlags(t, chatAttachCmd)
	stub.OnGet("/api/v1/agents", clitest.JSONResponse(200, []map[string]any{
		{"id": covAgentIDCli3, "slug": "viktor"},
	}))
	stub.OnGet("/api/v1/agents/"+covAgentIDCli3+"/chats",
		clitest.JSONResponse(200, []map[string]any{{"id": covChatID}}))
	attachPath := "/api/v1/agents/" + covAgentIDCli3 + "/chats/" + covChatID + "/attachments"
	// Response without agent_path exercises the fallback success message.
	stub.OnPost(attachPath, clitest.JSONResponse(200, map[string]any{"filename": "f.txt", "size": 3}))

	src := filepath.Join(t.TempDir(), "f.txt")
	if err := os.WriteFile(src, []byte("abc"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := chatAttachCmd.RunE(chatAttachCmd, []string{covChatID, src}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if calls := stub.CallsFor("POST", attachPath); len(calls) != 1 {
		t.Errorf("expected 1 POST after auto-resolve, got %d", len(calls))
	}
}

func TestChatAttachCmd_ResolveFails(t *testing.T) {
	stub := covStub(t)
	covResetFlags(t, chatAttachCmd)
	stub.OnGet("/api/v1/agents", clitest.JSONResponse(200, []map[string]any{}))

	err := chatAttachCmd.RunE(chatAttachCmd, []string{covChatID, "/tmp/ignored"})
	if err == nil || !strings.Contains(err.Error(), "pass --agent to override") {
		t.Fatalf("expected resolve-failure hint, got %v", err)
	}
}

func TestChatAttachCmd_MissingLocalFile(t *testing.T) {
	covStub(t)
	covResetFlags(t, chatAttachCmd)
	if err := chatAttachCmd.Flags().Set("agent", covAgentIDCli3); err != nil {
		t.Fatal(err)
	}
	err := chatAttachCmd.RunE(chatAttachCmd, []string{covChatID, filepath.Join(t.TempDir(), "ghost.bin")})
	if err == nil || !strings.Contains(err.Error(), "open") {
		t.Fatalf("expected open error, got %v", err)
	}
}

func TestChatAttachCmd_ServerRejects(t *testing.T) {
	stub := covStub(t)
	covResetFlags(t, chatAttachCmd)
	attachPath := "/api/v1/agents/" + covAgentIDCli3 + "/chats/" + covChatID + "/attachments"
	stub.OnPost(attachPath, clitest.ErrorResponse(413, "attachment too large"))

	src := filepath.Join(t.TempDir(), "big.bin")
	if err := os.WriteFile(src, []byte("xxxx"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := chatAttachCmd.Flags().Set("agent", covAgentIDCli3); err != nil {
		t.Fatal(err)
	}
	err := chatAttachCmd.RunE(chatAttachCmd, []string{covChatID, src})
	if err == nil || !strings.Contains(err.Error(), "attachment too large") {
		t.Fatalf("expected 413 error, got %v", err)
	}
}

// ─── postMultipart unit ─────────────────────────────────────────────────

func TestPostMultipart_NilContextAndExistingWorkspaceParam(t *testing.T) {
	stub := covStub(t)
	stub.OnPost("/api/v1/x", clitest.JSONResponse(200, map[string]any{"ok": true}))

	// Path already carries workspace_id → must NOT be overwritten.
	resp, err := postMultipart(nil, newAPIClient(), "/api/v1/x?workspace_id=explicit", "text/plain", strings.NewReader("body"))
	if err != nil {
		t.Fatalf("postMultipart: %v", err)
	}
	defer resp.Body.Close()
	calls := stub.CallsFor("POST", "/api/v1/x")
	if len(calls) != 1 {
		t.Fatalf("expected 1 POST, got %d", len(calls))
	}
	if !strings.Contains(calls[0].Query, "workspace_id=explicit") || strings.Contains(calls[0].Query, covWSCli3) {
		t.Errorf("explicit workspace_id should win: %q", calls[0].Query)
	}
}

func TestPostMultipart_BadURL(t *testing.T) {
	covStub(t)
	client := newAPIClient()
	client.BaseURL = "http://[::1]:bad"
	if _, err := postMultipart(nil, client, "/x", "text/plain", strings.NewReader("b")); err == nil {
		t.Fatal("expected parse error for invalid base URL")
	}
}
