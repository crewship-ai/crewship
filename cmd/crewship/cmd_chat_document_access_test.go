package main

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

func TestChatDocumentAccessCLIReadsCanonicalMetadata(t *testing.T) {
	stub := covStub(t)
	stub.OnGet("/api/v1/chats/chat-1/document-access", clitest.JSONResponse(200, map[string]any{"documents": []any{map[string]any{"page_id": "original-page", "document_grant": "write", "task_status": "unavailable"}}}))
	cmd := newChatDocumentAccessCommand()
	out := covCaptureStdoutCli3(t, func() {
		if err := cmd.RunE(cmd, []string{"chat-1"}); err != nil {
			t.Error(err)
		}
	})
	if !strings.Contains(out, "original-page") || !strings.Contains(out, "unavailable") || len(stub.CallsFor("GET", "/api/v1/chats/chat-1/document-access")) != 1 {
		t.Fatal(out)
	}
}

func TestChatDocumentAccessCLIPropagatesDenialAndRefusesRoutingIDs(t *testing.T) {
	stub := covStub(t)
	stub.OnGet("/api/v1/chats/chat-1/document-access", clitest.ErrorResponse(403, "current pages:read access required"))
	cmd := newChatDocumentAccessCommand()
	if err := cmd.RunE(cmd, []string{"chat-1"}); err == nil {
		t.Fatal("authorization failure hidden")
	}
	for _, id := range []string{"..", "a/b", "a?workspace_id=other"} {
		if err := cmd.RunE(cmd, []string{id}); err == nil || !strings.Contains(err.Error(), "invalid identifier") {
			t.Fatal("routing identifier accepted", id, err)
		}
	}
	if len(stub.CallsFor("GET", "/api/v1/chats/chat-1/document-access")) != 1 {
		t.Fatal("invalid IDs made additional requests")
	}
}
