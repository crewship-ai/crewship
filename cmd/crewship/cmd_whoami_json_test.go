package main

import (
	"bytes"
	"encoding/json"
	"testing"
)

// wsRow mirrors the anonymous struct emitWhoamiJSON accepts so the
// test can build inputs without dragging in the production parsing
// path. The names + json tags must match the production literal
// EXACTLY — otherwise the slice the test builds doesn't satisfy the
// parameter type.
//
// (Go doesn't let an anonymous struct in a function signature be
// satisfied by a named type with the same shape unless the literals
// match. The test repeats the literal verbatim; if the production
// shape ever changes, both sites move together.)
func TestEmitWhoamiJSON_HappyPath_ActiveWorkspaceFound(t *testing.T) {
	t.Parallel()
	rows := []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Slug string `json:"slug"`
		Role string `json:"currentUserRole"`
	}{
		{ID: "w_a", Name: "Engineering", Slug: "eng", Role: "OWNER"},
		{ID: "w_b", Name: "Marketing", Slug: "mkt", Role: "MEMBER"},
	}
	buf := &bytes.Buffer{}
	if err := emitWhoamiJSON(buf, "petra@example.com", "https://crewship.example.com", "eng", rows); err != nil {
		t.Fatalf("emit: %v", err)
	}

	var got whoamiJSON
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (raw=%q)", err, buf.String())
	}

	if got.UserEmail != "petra@example.com" {
		t.Errorf("user_email = %q, want %q", got.UserEmail, "petra@example.com")
	}
	if got.Server != "https://crewship.example.com" {
		t.Errorf("server = %q, want url", got.Server)
	}
	if got.WorkspacesCount != 2 {
		t.Errorf("workspaces_count = %d, want 2", got.WorkspacesCount)
	}
	if got.Workspace == nil {
		t.Fatal("workspace = nil, want populated")
	}
	if got.Workspace.Slug != "eng" || got.Workspace.Role != "OWNER" {
		t.Errorf("workspace = %+v, want slug=eng role=OWNER", got.Workspace)
	}
}

// activeWS that doesn't match any workspace must produce a nil
// Workspace in JSON (rendered as omitted, not "workspace": null).
// Catches the regression where a typo in the saved active workspace
// silently picks an arbitrary one.
func TestEmitWhoamiJSON_ActiveWorkspaceMissing(t *testing.T) {
	t.Parallel()
	rows := []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Slug string `json:"slug"`
		Role string `json:"currentUserRole"`
	}{
		{ID: "w_a", Name: "Engineering", Slug: "eng", Role: "OWNER"},
	}
	buf := &bytes.Buffer{}
	if err := emitWhoamiJSON(buf, "p@example.com", "https://x", "no-such-ws", rows); err != nil {
		t.Fatalf("emit: %v", err)
	}
	var got whoamiJSON
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Workspace != nil {
		t.Errorf("workspace = %+v, want nil (active workspace not in list)", got.Workspace)
	}
	if got.WorkspacesCount != 1 {
		t.Errorf("workspaces_count = %d, want 1 (the user does have access)", got.WorkspacesCount)
	}
}

// No active workspace at all — JSON has no workspace field but
// workspaces_count still reflects how many the user has access to.
func TestEmitWhoamiJSON_NoActiveWorkspace(t *testing.T) {
	t.Parallel()
	rows := []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Slug string `json:"slug"`
		Role string `json:"currentUserRole"`
	}{
		{ID: "w_a", Name: "Engineering", Slug: "eng", Role: "OWNER"},
		{ID: "w_b", Name: "Marketing", Slug: "mkt", Role: "MEMBER"},
	}
	buf := &bytes.Buffer{}
	if err := emitWhoamiJSON(buf, "p@example.com", "https://x", "", rows); err != nil {
		t.Fatalf("emit: %v", err)
	}
	var got whoamiJSON
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Workspace != nil {
		t.Errorf("workspace = %+v, want nil (no active workspace)", got.Workspace)
	}
	if got.WorkspacesCount != 2 {
		t.Errorf("workspaces_count = %d, want 2", got.WorkspacesCount)
	}
}

// Empty userEmail (session-cookie auth path, no CLI token to validate)
// must NOT appear in the JSON — the field is omitempty so a CI script
// can branch on existence rather than emptiness.
func TestEmitWhoamiJSON_OmitsEmptyUserEmail(t *testing.T) {
	t.Parallel()
	rows := []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Slug string `json:"slug"`
		Role string `json:"currentUserRole"`
	}{}
	buf := &bytes.Buffer{}
	if err := emitWhoamiJSON(buf, "", "https://x", "", rows); err != nil {
		t.Fatalf("emit: %v", err)
	}
	if bytes.Contains(buf.Bytes(), []byte("user_email")) {
		t.Errorf("output contains user_email key when value is empty (should be omitted): %s", buf.String())
	}
}

// Lookup matches by ID as well as slug — saved cli-config.yaml may
// store either form depending on which command set the active
// workspace. The lookup logic in emitWhoamiJSON tries both.
func TestEmitWhoamiJSON_ActiveWorkspaceByID(t *testing.T) {
	t.Parallel()
	rows := []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Slug string `json:"slug"`
		Role string `json:"currentUserRole"`
	}{
		{ID: "w_xyz", Name: "Engineering", Slug: "eng", Role: "OWNER"},
	}
	buf := &bytes.Buffer{}
	if err := emitWhoamiJSON(buf, "p@example.com", "https://x", "w_xyz", rows); err != nil {
		t.Fatalf("emit: %v", err)
	}
	var got whoamiJSON
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Workspace == nil || got.Workspace.Slug != "eng" {
		t.Errorf("lookup by ID failed: workspace = %+v", got.Workspace)
	}
}

// TestWhoamiCmd_JSONFlag guards the flag wiring.
func TestWhoamiCmd_JSONFlag(t *testing.T) {
	t.Parallel()
	f := whoamiCmd.Flags().Lookup("json")
	if f == nil {
		t.Fatal("crewship whoami missing --json flag")
	}
	if f.Value.Type() != "bool" {
		t.Errorf("--json type = %s, want bool", f.Value.Type())
	}
	if f.DefValue != "false" {
		t.Errorf("--json default = %s, want false (human output is the default)", f.DefValue)
	}
}
