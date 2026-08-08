package main

// `workspace member list` renders the SAME rows through two formats, and
// until #1829 only one of them was true. The table resolved the address out
// of the nested `user` object the server actually sends; `--format json`
// re-marshalled the CLI's own struct, whose flat Email/FullName fields the
// server never fills — so every JSON consumer got `"email": ""` next to a
// populated `user.email`.
//
// That is worse than omitting the field. An absent key makes a consumer look
// for the real one; a present-but-empty key makes `select(.email==$e)` return
// nothing and look like "no such member". scripts/test-harness/test-run-stream.sh
// read it exactly that way and skipped its most important assertion with a
// factually false reason for as long as the case has existed.
//
// The invariant these tests hold: whatever the table prints in EMAIL/NAME,
// the machine formats carry in `email`/`full_name`.

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// memberJSONBody is the shape the live server sends (verified against dev2):
// no top-level email, the person nested under `user`.
const memberJSONBody = `[
  {"id":"m-owner","user_id":"u-owner","role":"OWNER","created_at":"2026-07-23T13:15:24Z",
   "user":{"id":"u-owner","email":"demo@crewship.ai","full_name":"Demo User"}},
  {"id":"m-viewer","user_id":"u-viewer","role":"VIEWER","created_at":"2026-08-07T19:05:04Z",
   "user":{"id":"u-viewer","email":"viewer1@crewship.local","full_name":"Ivana Viewer"}}
]`

func memberListJSON(t *testing.T, body string) []map[string]any {
	t.Helper()
	stub := covStub(t)
	stub.OnGet("/api/v1/workspaces/"+covWSCli3+"/members", func(*http.Request, []byte) (int, []byte, string) {
		return 200, []byte(body), "application/json"
	})
	covResetFlags(t, workspaceMemberListCmd)
	origFormat := flagFormat
	t.Cleanup(func() { flagFormat = origFormat })
	flagFormat = "json"

	out := covCaptureStdoutCli5(t, func() {
		if err := workspaceMemberListCmd.RunE(workspaceMemberListCmd, nil); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	var rows []map[string]any
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("decode --format json output: %v\n%s", err, out)
	}
	return rows
}

// TestWorkspaceMemberList_JSONCarriesTheEmail is the red-first test for the
// #1829 root cause: the selector every consumer reaches for first.
func TestWorkspaceMemberList_JSONCarriesTheEmail(t *testing.T) {
	rows := memberListJSON(t, memberJSONBody)
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	want := map[string]string{"m-owner": "demo@crewship.ai", "m-viewer": "viewer1@crewship.local"}
	for _, r := range rows {
		id, _ := r["id"].(string)
		if got, _ := r["email"].(string); got != want[id] {
			t.Errorf("row %s: email = %q, want %q — a flat field that exists must not be empty", id, got, want[id])
		}
	}
}

// The name column has the same problem, and the same fix must cover it.
func TestWorkspaceMemberList_JSONCarriesTheFullName(t *testing.T) {
	rows := memberListJSON(t, memberJSONBody)
	want := map[string]string{"m-owner": "Demo User", "m-viewer": "Ivana Viewer"}
	for _, r := range rows {
		id, _ := r["id"].(string)
		if got, _ := r["full_name"].(string); got != want[id] {
			t.Errorf("row %s: full_name = %q, want %q", id, got, want[id])
		}
	}
}

// Normalising the flat fields must not delete the nested object: consumers
// written against the server shape (`.user.email`) keep working.
func TestWorkspaceMemberList_JSONKeepsTheNestedUser(t *testing.T) {
	rows := memberListJSON(t, memberJSONBody)
	for _, r := range rows {
		u, ok := r["user"].(map[string]any)
		if !ok {
			t.Fatalf("row %v lost its nested user object", r["id"])
		}
		if u["email"] == "" || u["email"] == nil {
			t.Errorf("row %v: user.email = %v", r["id"], u["email"])
		}
	}
}

// A server (or an older build) that already sends the email flat must survive
// normalisation unchanged — the flat value is authoritative when there is no
// nested user to prefer.
func TestWorkspaceMemberList_JSONPreservesAFlatShape(t *testing.T) {
	rows := memberListJSON(t, `[{"id":"m-9","user_id":"u-9","email":"flat@example.com","full_name":"Flat","role":"MEMBER"}]`)
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if got, _ := rows[0]["email"].(string); got != "flat@example.com" {
		t.Errorf("email = %q, want flat@example.com", got)
	}
	if got, _ := rows[0]["full_name"].(string); got != "Flat" {
		t.Errorf("full_name = %q, want Flat", got)
	}
}

// The two formats must agree. This is the invariant that would have caught the
// bug without anyone having to guess which field a consumer reads.
func TestWorkspaceMemberList_TableAndJSONAgreeOnTheEmail(t *testing.T) {
	rows := memberListJSON(t, memberJSONBody)

	stub := covStub(t)
	stub.OnGet("/api/v1/workspaces/"+covWSCli3+"/members", func(*http.Request, []byte) (int, []byte, string) {
		return 200, []byte(memberJSONBody), "application/json"
	})
	covResetFlags(t, workspaceMemberListCmd)
	table := covCaptureStdoutCli5(t, func() {
		if err := workspaceMemberListCmd.RunE(workspaceMemberListCmd, nil); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	for _, r := range rows {
		email, _ := r["email"].(string)
		if email == "" {
			t.Fatalf("row %v has no email in JSON; nothing to compare", r["id"])
		}
		if !strings.Contains(table, email) {
			t.Errorf("table output does not contain %q printed by --format json:\n%s", email, table)
		}
	}
}
