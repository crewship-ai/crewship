package main

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

func TestChatRefsCLIUsesAuthorizedAPIAndPreservesCursor(t *testing.T) {
	stub := covStub(t)
	cmd := newChatRefsCommand()
	stub.OnGet("/api/v1/chats/c_1/entity-refs", clitest.JSONResponse(200, map[string]any{"reset": true, "refs": []any{}, "next_cursor": "opaque", "snapshot_complete": true}))
	if err := cmd.Flags().Set("cursor", "opaque/+=="); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Flags().Set("limit", "7"); err != nil {
		t.Fatal(err)
	}
	out := covCaptureStdoutCli3(t, func() {
		if err := cmd.RunE(cmd, []string{"c_1"}); err != nil {
			t.Error(err)
		}
	})
	if !strings.Contains(out, "snapshot_complete") {
		t.Fatal(out)
	}
	calls := stub.CallsFor("GET", "/api/v1/chats/c_1/entity-refs")
	if len(calls) != 1 || !strings.Contains(calls[0].Query, "limit=7") || !strings.Contains(calls[0].Query, "cursor=opaque%2F%2B%3D%3D") {
		t.Fatalf("calls %+v", calls)
	}
}
func TestChatRefsCLIRejectsUnboundedLimit(t *testing.T) {
	cmd := newChatRefsCommand()
	_ = cmd.Flags().Set("limit", "101")
	if err := cmd.RunE(cmd, []string{"chat"}); err == nil {
		t.Fatal("accepted unbounded limit")
	}
}
func TestOperationCLIPropagatesAuthorizationFailure(t *testing.T) {
	stub := covStub(t)
	stub.OnGet("/api/v1/operations/op_1", clitest.ErrorResponse(404, "Operation not found"))
	cmd := newOperationCommand().Commands()[0]
	if err := cmd.RunE(cmd, []string{"op_1"}); err == nil || !strings.Contains(err.Error(), "Operation not found") {
		t.Fatalf("%v", err)
	}
}
