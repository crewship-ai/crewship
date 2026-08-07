package main

// `workspace member add` takes a USER id. `workspace member remove` and
// `member role` take the workspace_members ROW id. Nothing said so — the
// remove command's own usage string read `<user-id>` while it PATH-joined the
// argument onto an endpoint that only ever matches a membership row, so the
// obvious call answered `API error (404): Member not found` (#1829, reproduced
// against dev2).
//
// A 404 is the worst possible answer here: it is indistinguishable from "that
// person is not in this workspace", which is what the harness concluded and
// then skipped on. These tests hold the fix — either id resolves, and an id
// that is neither says which two things it was compared against.

import (
	"net/http"
	"strings"
	"testing"
)

// memberIDRoster: m-viewer is the MEMBERSHIP row, u-viewer the USER behind it.
const memberIDRoster = `[
  {"id":"m-owner","user_id":"u-owner","role":"OWNER",
   "user":{"id":"u-owner","email":"demo@crewship.ai","full_name":"Demo User"}},
  {"id":"m-viewer","user_id":"u-viewer","role":"VIEWER",
   "user":{"id":"u-viewer","email":"viewer1@crewship.local","full_name":"Ivana Viewer"}}
]`

// TestWorkspaceMemberRemove_AcceptsAUserID is the red-first case: the exact
// call the harness made, which the shipped CLI turned into a 404.
func TestWorkspaceMemberRemove_AcceptsAUserID(t *testing.T) {
	stub := covStub(t)
	stub.OnGet("/api/v1/workspaces/"+covWSCli3+"/members", func(*http.Request, []byte) (int, []byte, string) {
		return 200, []byte(memberIDRoster), "application/json"
	})
	stub.OnDelete("/api/v1/workspaces/"+covWSCli3+"/members/m-viewer", func(*http.Request, []byte) (int, []byte, string) {
		return 200, []byte(`{"success":true}`), "application/json"
	})

	c := newFlagCmd(nil, map[string]bool{"yes": true})
	out := covCaptureAll(t, func() {
		if err := workspaceMemberRemoveCmd.RunE(c, []string{"u-viewer"}); err != nil {
			t.Errorf("RunE with a user id: %v", err)
		}
	})
	if !strings.Contains(out, "Member removed.") {
		t.Errorf("output:\n%s", out)
	}
	if n := len(stub.CallsFor("DELETE", "/api/v1/workspaces/"+covWSCli3+"/members/m-viewer")); n != 1 {
		t.Errorf("DELETE on the membership row = %d, want 1 (the user id must resolve, not 404)", n)
	}
	if n := len(stub.CallsFor("DELETE", "/api/v1/workspaces/"+covWSCli3+"/members/u-viewer")); n != 0 {
		t.Errorf("CLI sent the USER id to an endpoint that consumes the MEMBERSHIP id (%d calls)", n)
	}
}

// The membership row id must keep working exactly as before — this fix adds a
// second accepted form, it does not swap one for the other.
func TestWorkspaceMemberRemove_StillAcceptsTheMembershipID(t *testing.T) {
	stub := covStub(t)
	stub.OnGet("/api/v1/workspaces/"+covWSCli3+"/members", func(*http.Request, []byte) (int, []byte, string) {
		return 200, []byte(memberIDRoster), "application/json"
	})
	stub.OnDelete("/api/v1/workspaces/"+covWSCli3+"/members/m-viewer", func(*http.Request, []byte) (int, []byte, string) {
		return 200, []byte(`{"success":true}`), "application/json"
	})

	c := newFlagCmd(nil, map[string]bool{"yes": true})
	covCaptureAll(t, func() {
		if err := workspaceMemberRemoveCmd.RunE(c, []string{"m-viewer"}); err != nil {
			t.Errorf("RunE with a membership id: %v", err)
		}
	})
	if n := len(stub.CallsFor("DELETE", "/api/v1/workspaces/"+covWSCli3+"/members/m-viewer")); n != 1 {
		t.Errorf("DELETE = %d, want 1", n)
	}
}

// An id that is neither must not become a bare "Member not found": say which
// two columns were searched, because that is the whole confusion.
func TestWorkspaceMemberRemove_UnknownIDNamesBothColumns(t *testing.T) {
	stub := covStub(t)
	stub.OnGet("/api/v1/workspaces/"+covWSCli3+"/members", func(*http.Request, []byte) (int, []byte, string) {
		return 200, []byte(memberIDRoster), "application/json"
	})

	c := newFlagCmd(nil, map[string]bool{"yes": true})
	err := workspaceMemberRemoveCmd.RunE(c, []string{"nobody"})
	if err == nil {
		t.Fatal("expected an error for an id in neither column")
	}
	for _, want := range []string{"nobody", "MEMBER ID", "USER ID", "member list"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err.Error(), want)
		}
	}
	if n := len(stub.Calls()); n != 1 {
		t.Errorf("calls = %d, want 1 (the roster read only — no DELETE should go out)", n)
	}
}

// `member role` consumes the same id and must resolve it the same way.
func TestWorkspaceMemberRole_AcceptsAUserID(t *testing.T) {
	stub := covStub(t)
	stub.OnGet("/api/v1/workspaces/"+covWSCli3+"/members", func(*http.Request, []byte) (int, []byte, string) {
		return 200, []byte(memberIDRoster), "application/json"
	})
	stub.OnPatch("/api/v1/workspaces/"+covWSCli3+"/members/m-viewer", func(*http.Request, []byte) (int, []byte, string) {
		return 200, []byte(`{"success":true}`), "application/json"
	})

	covCaptureAll(t, func() {
		if err := workspaceMemberRoleCmd.RunE(workspaceMemberRoleCmd, []string{"u-viewer", "member"}); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if n := len(stub.CallsFor("PATCH", "/api/v1/workspaces/"+covWSCli3+"/members/m-viewer")); n != 1 {
		t.Errorf("PATCH on the membership row = %d, want 1", n)
	}
}

// Resolution is a convenience, never a new failure mode: when the roster
// cannot be read at all (permissions, a dead server, an older build), the
// argument goes through untouched and the server answers as it always did.
func TestWorkspaceMemberRemove_UnreadableRosterPassesTheArgThrough(t *testing.T) {
	stub := covStub(t)
	// No roster handler registered → the stub's default 404.
	stub.OnDelete("/api/v1/workspaces/"+covWSCli3+"/members/whatever", func(*http.Request, []byte) (int, []byte, string) {
		return 200, []byte(`{"success":true}`), "application/json"
	})

	c := newFlagCmd(nil, map[string]bool{"yes": true})
	covCaptureAll(t, func() {
		if err := workspaceMemberRemoveCmd.RunE(c, []string{"whatever"}); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if n := len(stub.CallsFor("DELETE", "/api/v1/workspaces/"+covWSCli3+"/members/whatever")); n != 1 {
		t.Errorf("DELETE = %d, want 1 — an unreadable roster must not block the call", n)
	}
}

// The usage string is part of the contract: it said `<user-id>` while the
// endpoint consumed a membership id, which is how the trap was set.
func TestWorkspaceMemberRemove_UsageNamesBothForms(t *testing.T) {
	t.Parallel()
	if strings.Contains(workspaceMemberRemoveCmd.Use, "<user-id>") {
		t.Errorf("Use = %q still claims a bare user id", workspaceMemberRemoveCmd.Use)
	}
	if !strings.Contains(workspaceMemberRemoveCmd.Long, "member add") {
		t.Error("Long help must name the add/remove id asymmetry it exists to defuse")
	}
}
