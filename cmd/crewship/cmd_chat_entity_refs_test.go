package main

import (
	"github.com/spf13/cobra"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

func TestChatRefsCLIUsesAuthorizedAPIAndPreservesCursor(t *testing.T) {
	stub := covStub(t)
	cmd := newChatRefsCommand()
	stub.OnGet("/api/v1/chats/c_1/entity-refs", clitest.JSONResponse(200, map[string]any{"contract_version": 1, "reset": true, "refs": []any{}, "next_cursor": "opaque", "snapshot_complete": true}))
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
	if !strings.Contains(out, "contract_version") || !strings.Contains(out, "snapshot_complete") {
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

func TestEntityReportingCommandsHonourMachineFormats(t *testing.T) {
	for _, format := range []string{"json", "yaml", "ndjson"} {
		t.Run(format, func(t *testing.T) {
			stub := covStub(t)
			cliCfg.Format = format
			stub.OnGet("/api/v1/operations/op_1", clitest.JSONResponse(200, map[string]any{"id": "op_1", "status": "completed"}))
			stub.OnGet("/api/v1/chats/c_1/entity-refs", clitest.JSONResponse(200, map[string]any{"refs": []any{}, "snapshot_complete": true}))
			operation := newOperationCommand().Commands()[0]
			out := covCaptureStdoutCli3(t, func() {
				if err := operation.RunE(operation, []string{"op_1"}); err != nil {
					t.Error(err)
				}
			})
			if format == "yaml" {
				if !strings.Contains(out, "id: op_1") {
					t.Fatal(out)
				}
			} else if !strings.Contains(out, `"id"`) {
				t.Fatal(out)
			}
			if format == "ndjson" && strings.Count(out, "\n") != 1 {
				t.Fatalf("not NDJSON: %s", out)
			}
			refs := newChatRefsCommand()
			out = covCaptureStdoutCli3(t, func() {
				if err := refs.RunE(refs, []string{"c_1"}); err != nil {
					t.Error(err)
				}
			})
			if format == "yaml" && !strings.Contains(out, "snapshot_complete: true") {
				t.Fatal(out)
			}
			if format == "ndjson" && strings.Count(out, "\n") != 1 {
				t.Fatalf("not NDJSON: %s", out)
			}
		})
	}
}

func TestEntityCLIRejectsRoutingIdentifiersBeforeRequests(t *testing.T) {
	for _, id := range []string{"", ".", "..", "a/b", "a\\b", "a?workspace_id=other", "a#fragment", "a\nheader"} {
		for name, command := range map[string]*cobra.Command{"refs": newChatRefsCommand(), "operation": newOperationCommand().Commands()[0], "revision": newIssueRevisionCommand()} {
			t.Run(name+id, func(t *testing.T) {
				if err := command.RunE(command, []string{id}); err == nil || !strings.Contains(err.Error(), "invalid identifier") {
					t.Fatalf("accepted routing identifier %q: %v", id, err)
				}
			})
		}
	}
	for _, id := range []string{"ENG-42", "operation-1", "c_1", "revision-1"} {
		if err := validateEntityCLIIdentifier(id); err != nil {
			t.Fatal(err)
		}
	}
}
