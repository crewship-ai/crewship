package main

// The members endpoint nests the person under `user` — the CLI read a flat
// `email`, so `crewship workspace member list` printed empty EMAIL and NAME
// columns, and `crewship audit --user <email>` could never resolve anyone.
// That resolution is the whole reason the flag accepts an email (#1207): an
// operator holding a request from a person has their address, not their cuid.

import (
	"net/http"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
)

const membersBody = `[
  {"id":"m-1","user_id":"u-1","role":"OWNER","created_at":"2026-07-23T13:15:24Z",
   "user":{"id":"u-1","email":"demo@crewship.ai","full_name":"Demo User"}},
  {"id":"m-2","user_id":"u-2","role":"MEMBER","created_at":"2026-07-29T13:06:17Z",
   "user":{"id":"u-2","email":"fredy@example.com","full_name":"Fredy"}}
]`

func TestFindWorkspaceMemberUserIDByEmail_ReadsTheNestedUser(t *testing.T) {
	stub := covStub(t)
	stub.OnGet("/api/v1/workspaces/"+covWSCli3+"/members", func(*http.Request, []byte) (int, []byte, string) {
		return 200, []byte(membersBody), "application/json"
	})
	client := cli.NewClient(stub.URL(), "test-token", covWSCli3)

	got, err := findWorkspaceMemberUserIDByEmail(client, covWSCli3, "fredy@example.com")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != "u-2" {
		t.Fatalf("user id = %q, want u-2", got)
	}
}

func TestFindWorkspaceMemberUserIDByEmail_StillAcceptsAFlatShape(t *testing.T) {
	// Older builds (and the admin users endpoint) return the email flat.
	// Reading one shape must not mean refusing the other.
	stub := covStub(t)
	stub.OnGet("/api/v1/workspaces/"+covWSCli3+"/members", func(*http.Request, []byte) (int, []byte, string) {
		return 200, []byte(`[{"user_id":"u-9","email":"flat@example.com"}]`), "application/json"
	})
	client := cli.NewClient(stub.URL(), "test-token", covWSCli3)

	got, err := findWorkspaceMemberUserIDByEmail(client, covWSCli3, "flat@example.com")
	if err != nil || got != "u-9" {
		t.Fatalf("got %q, err %v — want u-9", got, err)
	}
}

func TestWorkspaceMemberList_PrintsTheEmail(t *testing.T) {
	stub := covStub(t)
	stub.OnGet("/api/v1/workspaces/"+covWSCli3+"/members", func(*http.Request, []byte) (int, []byte, string) {
		return 200, []byte(membersBody), "application/json"
	})
	covResetFlags(t, workspaceMemberListCmd)
	out := covCaptureAll(t, func() {
		if err := workspaceMemberListCmd.RunE(workspaceMemberListCmd, nil); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	for _, want := range []string{"demo@crewship.ai", "Fredy"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}
