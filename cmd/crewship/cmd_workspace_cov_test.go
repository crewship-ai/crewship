package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

// covWS is a CUID-shaped workspace id: the client uses it verbatim and
// never tries slug→id resolution over HTTP.
const covWS = "cabcdefghijklmnopqrs"

// setStubCLI points the package-level CLI state at the given stub
// server and neutralises ambient env overrides. NOT parallel-safe —
// the cmd/crewship RunE paths read package globals.
func setStubCLI(t *testing.T, serverURL string) {
	t.Helper()
	saveCLIState(t)
	t.Setenv("CREWSHIP_SERVER", "")
	t.Setenv("CREWSHIP_WORKSPACE", "")
	flagServer = ""
	flagWorkspace = ""
	cliCfg = &cli.CLIConfig{Token: "test-token", Server: serverURL, Workspace: covWS}
}

// captureStdoutCovCli2 redirects BOTH os.Stdout and os.Stderr for the
// duration of fn and returns everything written, interleaved. Needed
// because RunE paths print tables to os.Stdout while cli.PrintSuccess
// and seed progress lines go to os.Stderr.
func captureStdoutCovCli2(t *testing.T, fn func()) string {
	t.Helper()
	oldOut, oldErr := os.Stdout, os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	os.Stderr = w
	done := make(chan string, 1)
	go func() {
		defer r.Close()
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	defer func() {
		os.Stdout = oldOut
		os.Stderr = oldErr
	}()
	fn()
	_ = w.Close()
	os.Stdout = oldOut
	os.Stderr = oldErr
	return <-done
}

func TestTruncateID(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		n    int
		want string
	}{
		{"abcdef", 4, "abcd"},
		{"abc", 4, "abc"},
		{"", 4, ""},
		{"abcd", 4, "abcd"}, // len == n is NOT truncated (strictly-less check)
	}
	for _, tc := range cases {
		if got := truncateID(tc.in, tc.n); got != tc.want {
			t.Errorf("truncateID(%q, %d) = %q, want %q", tc.in, tc.n, got, tc.want)
		}
	}
}

func TestWorkspaceList_RunE(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnGet("/api/v1/workspaces", clitest.JSONResponse(200, []map[string]string{
		{"id": covWS, "name": "Acme", "slug": "acme", "currentUserRole": "OWNER"},
		{"id": "cother0000000000000z", "name": "Beta", "slug": "beta", "currentUserRole": "MEMBER"},
	}))
	setStubCLI(t, stub.URL())

	out := captureStdoutCovCli2(t, func() {
		if err := workspaceListCmd.RunE(workspaceListCmd, nil); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	// Active workspace (matches cliCfg.Workspace) gets the * marker.
	if !strings.Contains(out, "acme *") {
		t.Errorf("expected active-workspace marker, got:\n%s", out)
	}
	if !strings.Contains(out, "beta") || strings.Contains(out, "beta *") {
		t.Errorf("beta must be listed without marker, got:\n%s", out)
	}
	calls := stub.CallsFor("GET", "/api/v1/workspaces")
	if len(calls) != 1 {
		t.Fatalf("expected exactly 1 GET /api/v1/workspaces, got %d", len(calls))
	}
	if strings.Contains(calls[0].Query, "workspace_id") {
		t.Errorf("list must not send workspace_id param, query=%q", calls[0].Query)
	}
}

func TestWorkspaceList_NoAuthAndAPIError(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{}
	if err := workspaceListCmd.RunE(workspaceListCmd, nil); err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("no auth: got %v", err)
	}

	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnGet("/api/v1/workspaces", clitest.ErrorResponse(401, "session expired"))
	setStubCLI(t, stub.URL())
	if err := workspaceListCmd.RunE(workspaceListCmd, nil); err == nil || !strings.Contains(err.Error(), "session expired") {
		t.Errorf("API error: got %v", err)
	}
}

func TestWorkspaceUse_SavesWithoutToken(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "cli-config.yaml")
	t.Setenv("CREWSHIP_CONFIG", cfgPath)
	saveCLIState(t)
	flagServer = ""

	out := captureStdoutCovCli2(t, func() {
		if err := workspaceUseCmd.RunE(workspaceUseCmd, []string{"acme"}); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if !strings.Contains(out, "Default workspace set to: acme") {
		t.Errorf("output:\n%s", out)
	}
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !strings.Contains(string(data), "workspace: acme") {
		t.Errorf("config not persisted:\n%s", data)
	}
}

func TestWorkspaceUse_ValidatesAgainstServerWhenLoggedIn(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnGet("/api/v1/workspaces", clitest.JSONResponse(200, []map[string]string{
		{"id": covWS, "slug": "acme", "name": "Acme"},
	}))

	cfgPath := filepath.Join(t.TempDir(), "cli-config.yaml")
	t.Setenv("CREWSHIP_CONFIG", cfgPath)
	t.Setenv("CREWSHIP_SERVER", "")
	saveCLIState(t)
	flagServer = ""
	if err := cli.SaveConfig(&cli.CLIConfig{Token: "tok", Server: stub.URL()}); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	// Unknown workspace → rejected, config keeps no workspace.
	err := workspaceUseCmd.RunE(workspaceUseCmd, []string{"ghost"})
	if err == nil || !strings.Contains(err.Error(), `workspace "ghost" not found`) {
		t.Fatalf("expected not-found error, got %v", err)
	}

	// Known slug → accepted and saved.
	_ = captureStdoutCovCli2(t, func() {
		if err := workspaceUseCmd.RunE(workspaceUseCmd, []string{"acme"}); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	cfg, err := cli.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Workspace != "acme" {
		t.Errorf("workspace = %q, want acme", cfg.Workspace)
	}
}

// newFlagCmd builds a throwaway command with the given string flags so
// the global commands' RunE closures can be exercised without sticky
// flag state on the shared cobra vars.
func newFlagCmd(stringFlags map[string]string, boolFlags map[string]bool) *cobra.Command {
	c := &cobra.Command{Use: "t"}
	for k, v := range stringFlags {
		c.Flags().String(k, v, "")
	}
	for k, v := range boolFlags {
		c.Flags().Bool(k, v, "")
	}
	c.SetOut(new(bytes.Buffer))
	return c
}

func TestWorkspaceCreate_RunE(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnPost("/api/v1/workspaces", clitest.JSONResponse(201, map[string]string{
		"id": "cnewws00000000000000", "name": "New WS", "slug": "new-ws",
	}))
	setStubCLI(t, stub.URL())

	// Missing --name.
	c := newFlagCmd(map[string]string{"name": "", "slug": "", "language": ""}, nil)
	if err := workspaceCreateCmd.RunE(c, nil); err == nil || !strings.Contains(err.Error(), "--name is required") {
		t.Errorf("missing name: got %v", err)
	}

	// Happy path with slug + language in the body.
	c2 := newFlagCmd(map[string]string{"name": "New WS", "slug": "new-ws", "language": "cs"}, nil)
	out := captureStdoutCovCli2(t, func() {
		if err := workspaceCreateCmd.RunE(c2, nil); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if !strings.Contains(out, "Workspace created: new-ws (cnewws00000000000000)") {
		t.Errorf("output:\n%s", out)
	}
	calls := stub.CallsFor("POST", "/api/v1/workspaces")
	if len(calls) != 1 {
		t.Fatalf("POST calls = %d, want 1", len(calls))
	}
	var body map[string]any
	clitest.MustDecodeJSONBody(calls[0].Body, &body)
	if body["name"] != "New WS" || body["slug"] != "new-ws" || body["preferred_language"] != "cs" {
		t.Errorf("request body = %v", body)
	}
}

func TestWorkspaceGet_RunE(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	lang := "en"
	stub.OnGet("/api/v1/workspaces/"+covWS, clitest.JSONResponse(200, map[string]any{
		"id": covWS, "name": "Acme", "slug": "acme", "created_at": "2026-01-01", "preferred_language": lang,
	}))
	setStubCLI(t, stub.URL())

	out := captureStdoutCovCli2(t, func() {
		if err := workspaceGetCmd.RunE(workspaceGetCmd, nil); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	for _, want := range []string{"Acme", "acme", "en"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}

	// Explicit positional arg wins over the configured workspace.
	stub.OnGet("/api/v1/workspaces/cexplicit0000000000z", clitest.JSONResponse(200, map[string]any{
		"id": "cexplicit0000000000z", "name": "Other", "slug": "other",
	}))
	_ = captureStdoutCovCli2(t, func() {
		if err := workspaceGetCmd.RunE(workspaceGetCmd, []string{"cexplicit0000000000z"}); err != nil {
			t.Errorf("RunE explicit: %v", err)
		}
	})
	if len(stub.CallsFor("GET", "/api/v1/workspaces/cexplicit0000000000z")) != 1 {
		t.Errorf("explicit workspace id was not used")
	}

	// No workspace anywhere → error.
	cliCfg = &cli.CLIConfig{Token: "tok", Server: stub.URL()}
	if err := workspaceGetCmd.RunE(workspaceGetCmd, nil); err == nil || !strings.Contains(err.Error(), "no workspace specified") {
		t.Errorf("no workspace: got %v", err)
	}
}

func TestWorkspaceUpdate_RunE(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnPatch("/api/v1/workspaces/"+covWS, clitest.JSONResponse(200, map[string]string{"id": covWS}))
	setStubCLI(t, stub.URL())

	// No changed flags → "no fields to update".
	c := newFlagCmd(map[string]string{"name": "", "slug": "", "language": ""}, nil)
	if err := workspaceUpdateCmd.RunE(c, nil); err == nil || !strings.Contains(err.Error(), "no fields to update") {
		t.Errorf("no fields: got %v", err)
	}

	c2 := newFlagCmd(map[string]string{"name": "", "slug": "", "language": ""}, nil)
	if err := c2.Flags().Set("name", "Renamed"); err != nil {
		t.Fatal(err)
	}
	if err := c2.Flags().Set("language", "cs"); err != nil {
		t.Fatal(err)
	}
	out := captureStdoutCovCli2(t, func() {
		if err := workspaceUpdateCmd.RunE(c2, nil); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if !strings.Contains(out, "Workspace updated.") {
		t.Errorf("output:\n%s", out)
	}
	calls := stub.CallsFor("PATCH", "/api/v1/workspaces/"+covWS)
	if len(calls) != 1 {
		t.Fatalf("PATCH calls = %d, want 1", len(calls))
	}
	var body map[string]any
	clitest.MustDecodeJSONBody(calls[0].Body, &body)
	if body["name"] != "Renamed" || body["preferred_language"] != "cs" {
		t.Errorf("body = %v", body)
	}
	if _, ok := body["slug"]; ok {
		t.Errorf("slug not flagged as changed but sent: %v", body)
	}
}

func TestWorkspaceMemberList_RunE(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnGet("/api/v1/workspaces/"+covWS+"/members", clitest.JSONResponse(200, []map[string]string{
		{"id": "m1", "user_id": "cuser000000000000001", "email": "a@b.c", "full_name": "Alice", "role": "OWNER", "created_at": "2026-01-01"},
	}))
	setStubCLI(t, stub.URL())

	out := captureStdoutCovCli2(t, func() {
		if err := workspaceMemberListCmd.RunE(workspaceMemberListCmd, nil); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	// user_id truncated to 12 chars in the ID column.
	if !strings.Contains(out, "cuser0000000") || !strings.Contains(out, "a@b.c") {
		t.Errorf("output:\n%s", out)
	}
}

func TestWorkspaceMemberAdd_RunE(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnPost("/api/v1/workspaces/"+covWS+"/members", clitest.JSONResponse(201, map[string]string{"id": "m2"}))
	setStubCLI(t, stub.URL())

	c := newFlagCmd(map[string]string{"role": ""}, nil)
	out := captureStdoutCovCli2(t, func() {
		if err := workspaceMemberAddCmd.RunE(c, []string{"cuser000000000000002"}); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if !strings.Contains(out, "Member added with role MEMBER.") {
		t.Errorf("output:\n%s", out)
	}
	calls := stub.CallsFor("POST", "/api/v1/workspaces/"+covWS+"/members")
	if len(calls) != 1 {
		t.Fatalf("calls = %d", len(calls))
	}
	var body map[string]string
	clitest.MustDecodeJSONBody(calls[0].Body, &body)
	if body["user_id"] != "cuser000000000000002" || body["role"] != "MEMBER" {
		t.Errorf("body = %v", body)
	}
}

func TestWorkspaceMemberRemove_RunE(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnDelete("/api/v1/workspaces/"+covWS+"/members/u9", clitest.EmptyResponse(204))
	setStubCLI(t, stub.URL())

	// --yes skips the prompt.
	c := newFlagCmd(nil, map[string]bool{"yes": true})
	out := captureStdoutCovCli2(t, func() {
		if err := workspaceMemberRemoveCmd.RunE(c, []string{"u9"}); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if !strings.Contains(out, "Member removed.") {
		t.Errorf("output:\n%s", out)
	}
	if len(stub.CallsFor("DELETE", "/api/v1/workspaces/"+covWS+"/members/u9")) != 1 {
		t.Errorf("DELETE not issued")
	}

	// Without --yes on a non-TTY stdin the confirm read gets EOF → abort,
	// and no DELETE goes out.
	stub.ResetCalls()
	c2 := newFlagCmd(nil, map[string]bool{"yes": false})
	err := workspaceMemberRemoveCmd.RunE(c2, []string{"u9"})
	if err == nil || !strings.Contains(err.Error(), "aborted") {
		t.Errorf("expected aborted, got %v", err)
	}
	if len(stub.Calls()) != 0 {
		t.Errorf("aborted remove must not call the API, got %d calls", len(stub.Calls()))
	}
}

func TestSendWorkspaceInvitation(t *testing.T) {
	// Auth/workspace guards first.
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{}
	if err := sendWorkspaceInvitation("a@b.c", ""); err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("no auth: got %v", err)
	}
	cliCfg = &cli.CLIConfig{Token: "tok"}
	flagWorkspace = ""
	t.Setenv("CREWSHIP_WORKSPACE", "")
	if err := sendWorkspaceInvitation("a@b.c", ""); err == nil || !strings.Contains(err.Error(), "workspace") {
		t.Errorf("no workspace: got %v", err)
	}

	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnPost("/api/v1/workspaces/"+covWS+"/invitations", clitest.JSONResponse(201, map[string]string{
		"id": "inv1", "email": "a@b.c", "role": "ADMIN",
	}))
	setStubCLI(t, stub.URL())

	out := captureStdoutCovCli2(t, func() {
		if err := sendWorkspaceInvitation("a@b.c", "ADMIN"); err != nil {
			t.Errorf("send: %v", err)
		}
	})
	if !strings.Contains(out, "Invitation sent to a@b.c (ADMIN role).") {
		t.Errorf("output:\n%s", out)
	}
	calls := stub.CallsFor("POST", "/api/v1/workspaces/"+covWS+"/invitations")
	if len(calls) != 1 {
		t.Fatalf("calls = %d", len(calls))
	}
	var body map[string]string
	clitest.MustDecodeJSONBody(calls[0].Body, &body)
	if body["email"] != "a@b.c" || body["role"] != "ADMIN" {
		t.Errorf("body = %v", body)
	}

	// Default role + API error path.
	stub.OnPost("/api/v1/workspaces/"+covWS+"/invitations", clitest.ErrorResponse(403, "viewers cannot invite"))
	if err := sendWorkspaceInvitation("b@b.c", ""); err == nil || !strings.Contains(err.Error(), "viewers cannot invite") {
		t.Errorf("API error: got %v", err)
	}
}

func TestWorkspaceInviteList_RunE(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnGet("/api/v1/workspaces/"+covWS+"/invitations", clitest.JSONResponse(200, []map[string]string{
		{"id": "cinvite0000000000001", "email": "x@y.z", "role": "MEMBER", "expires_at": "2026-07-01", "created_at": "2026-06-01"},
	}))
	setStubCLI(t, stub.URL())

	out := captureStdoutCovCli2(t, func() {
		if err := workspaceInviteListCmd.RunE(workspaceInviteListCmd, nil); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if !strings.Contains(out, "x@y.z") || !strings.Contains(out, "cinvite00000") {
		t.Errorf("output:\n%s", out)
	}
}

func TestWorkspaceInviteCmd_GroupModeShowsHelp(t *testing.T) {
	// Zero positional args → help text, no API calls. Use the real
	// command (it is runnable and has subcommands, so cobra's help
	// template produces output) with buffers swapped in.
	buf := new(bytes.Buffer)
	workspaceInviteCmd.SetOut(buf)
	workspaceInviteCmd.SetErr(buf)
	t.Cleanup(func() {
		workspaceInviteCmd.SetOut(nil)
		workspaceInviteCmd.SetErr(nil)
	})
	if err := workspaceInviteCmd.RunE(workspaceInviteCmd, nil); err != nil {
		t.Fatalf("help mode: %v", err)
	}
	if !strings.Contains(buf.String(), "invite") {
		t.Errorf("expected help output mentioning the command, got: %q", buf.String())
	}
}

func TestWorkspaceInviteCmd_ShortcutAndCreateSubcommand(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnPost("/api/v1/workspaces/"+covWS+"/invitations", clitest.JSONResponse(201, map[string]string{
		"id": "inv2", "email": "short@cut.io", "role": "MEMBER",
	}))
	setStubCLI(t, stub.URL())

	c := newFlagCmd(map[string]string{"role": "MEMBER"}, nil)
	_ = captureStdoutCovCli2(t, func() {
		if err := workspaceInviteCmd.RunE(c, []string{"short@cut.io"}); err != nil {
			t.Errorf("shortcut: %v", err)
		}
	})
	c2 := newFlagCmd(map[string]string{"role": "MEMBER"}, nil)
	_ = captureStdoutCovCli2(t, func() {
		if err := workspaceInviteCreateCmd.RunE(c2, []string{"short@cut.io"}); err != nil {
			t.Errorf("create subcommand: %v", err)
		}
	})
	if got := len(stub.CallsFor("POST", "/api/v1/workspaces/"+covWS+"/invitations")); got != 2 {
		t.Errorf("invitation POSTs = %d, want 2", got)
	}
}

// TestWorkspaceCmds_APIErrorPropagation drives each workspace RunE
// against a stub whose fallback answers 500 with a recognisable error
// envelope, pinning the cli.CheckError branch in every closure.
func TestWorkspaceCmds_APIErrorPropagation(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.SetFallback(clitest.ErrorResponse(500, "boom-fallback"))
	setStubCLI(t, stub.URL())

	cases := []struct {
		name string
		run  func() error
	}{
		{"get", func() error { return workspaceGetCmd.RunE(workspaceGetCmd, nil) }},
		{"create", func() error {
			c := newFlagCmd(map[string]string{"name": "X", "slug": "", "language": ""}, nil)
			return workspaceCreateCmd.RunE(c, nil)
		}},
		{"update", func() error {
			c := newFlagCmd(map[string]string{"name": "", "slug": "", "language": ""}, nil)
			_ = c.Flags().Set("name", "Y")
			return workspaceUpdateCmd.RunE(c, nil)
		}},
		{"member list", func() error { return workspaceMemberListCmd.RunE(workspaceMemberListCmd, nil) }},
		{"member add", func() error {
			c := newFlagCmd(map[string]string{"role": "MEMBER"}, nil)
			return workspaceMemberAddCmd.RunE(c, []string{"u1"})
		}},
		{"member remove", func() error {
			c := newFlagCmd(nil, map[string]bool{"yes": true})
			return workspaceMemberRemoveCmd.RunE(c, []string{"u1"})
		}},
		{"invite list", func() error { return workspaceInviteListCmd.RunE(workspaceInviteListCmd, nil) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.run()
			if err == nil || !strings.Contains(err.Error(), "boom-fallback") {
				t.Errorf("expected fallback error, got %v", err)
			}
		})
	}
}

func TestWorkspaceUse_MalformedConfigErrorsWithoutClobber(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "cli-config.yaml")
	t.Setenv("CREWSHIP_CONFIG", cfgPath)
	malformed := []byte("\t: not yaml [")
	if err := os.WriteFile(cfgPath, malformed, 0o600); err != nil {
		t.Fatal(err)
	}
	saveCLIState(t)
	flagServer = ""

	// A malformed config is a real read/parse failure (LoadConfig only swallows
	// a missing file). `workspace use` must surface it and leave the file
	// untouched, never silently overwrite the user's saved config.
	err := workspaceUseCmd.RunE(workspaceUseCmd, []string{"fresh"})
	if err == nil || !strings.Contains(err.Error(), "load CLI config: parse config:") {
		t.Fatalf("expected parse-config error, got %v", err)
	}
	got, _ := os.ReadFile(cfgPath)
	if string(got) != string(malformed) {
		t.Errorf("malformed config was clobbered: %q", string(got))
	}
}

func TestWorkspaceUse_Non200ValidationSkipped(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnGet("/api/v1/workspaces", clitest.ErrorResponse(500, "down"))

	cfgPath := filepath.Join(t.TempDir(), "cli-config.yaml")
	t.Setenv("CREWSHIP_CONFIG", cfgPath)
	t.Setenv("CREWSHIP_SERVER", "")
	saveCLIState(t)
	flagServer = ""
	if err := cli.SaveConfig(&cli.CLIConfig{Token: "tok", Server: stub.URL()}); err != nil {
		t.Fatal(err)
	}

	// Validation endpoint failing must not block `workspace use`.
	_ = captureStdoutCovCli2(t, func() {
		if err := workspaceUseCmd.RunE(workspaceUseCmd, []string{"anything"}); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	cfg, _ := cli.LoadConfig()
	if cfg.Workspace != "anything" {
		t.Errorf("workspace = %q", cfg.Workspace)
	}
}

func TestWorkspaceMemberRemove_ConfirmYesViaStdin(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnDelete("/api/v1/workspaces/"+covWS+"/members/u7", clitest.EmptyResponse(204))
	setStubCLI(t, stub.URL())

	swapStdin(t, "y\n")
	c := newFlagCmd(nil, map[string]bool{"yes": false})
	_ = captureStdoutCovCli2(t, func() {
		if err := workspaceMemberRemoveCmd.RunE(c, []string{"u7"}); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if len(stub.CallsFor("DELETE", "/api/v1/workspaces/"+covWS+"/members/u7")) != 1 {
		t.Error("confirmed remove must DELETE")
	}
}

// deadServerURL points at a port that refuses connections, driving the
// transport-error branch right after each client call.
const deadServerURL = "http://127.0.0.1:1"

func setDeadCLI(t *testing.T) {
	t.Helper()
	saveCLIState(t)
	t.Setenv("CREWSHIP_SERVER", "")
	t.Setenv("CREWSHIP_WORKSPACE", "")
	flagServer = ""
	flagWorkspace = ""
	cliCfg = &cli.CLIConfig{Token: "tok", Server: deadServerURL, Workspace: covWS}
}

// TestWorkspaceCmds_GuardsAndTransport sweeps every workspace RunE
// through the three early-exit branches: no auth, no workspace (where
// applicable), and a dead server.
func TestWorkspaceCmds_GuardsAndTransport(t *testing.T) {
	type cmdCase struct {
		name    string
		needsWS bool
		run     func() error
	}
	cases := []cmdCase{
		{"list", false, func() error { return workspaceListCmd.RunE(workspaceListCmd, nil) }},
		{"get", false, func() error { return workspaceGetCmd.RunE(workspaceGetCmd, []string{covWS}) }},
		{"create", false, func() error {
			c := newFlagCmd(map[string]string{"name": "X", "slug": "", "language": ""}, nil)
			return workspaceCreateCmd.RunE(c, nil)
		}},
		{"update", true, func() error {
			c := newFlagCmd(map[string]string{"name": "", "slug": "", "language": ""}, nil)
			_ = c.Flags().Set("name", "Y")
			return workspaceUpdateCmd.RunE(c, nil)
		}},
		{"member list", true, func() error { return workspaceMemberListCmd.RunE(workspaceMemberListCmd, nil) }},
		{"member add", true, func() error {
			c := newFlagCmd(map[string]string{"role": "MEMBER"}, nil)
			return workspaceMemberAddCmd.RunE(c, []string{"u1"})
		}},
		{"member remove", true, func() error {
			c := newFlagCmd(nil, map[string]bool{"yes": true})
			return workspaceMemberRemoveCmd.RunE(c, []string{"u1"})
		}},
		{"invite list", true, func() error { return workspaceInviteListCmd.RunE(workspaceInviteListCmd, nil) }},
		{"invite send", true, func() error { return sendWorkspaceInvitation("a@b.c", "") }},
	}
	for _, tc := range cases {
		t.Run(tc.name+"/no auth", func(t *testing.T) {
			saveCLIState(t)
			cliCfg = &cli.CLIConfig{}
			if err := tc.run(); err == nil || !strings.Contains(err.Error(), "not logged in") {
				t.Errorf("got %v", err)
			}
		})
		if tc.needsWS {
			t.Run(tc.name+"/no workspace", func(t *testing.T) {
				saveCLIState(t)
				t.Setenv("CREWSHIP_WORKSPACE", "")
				flagWorkspace = ""
				cliCfg = &cli.CLIConfig{Token: "tok"}
				if err := tc.run(); err == nil || !strings.Contains(err.Error(), "workspace") {
					t.Errorf("got %v", err)
				}
			})
		}
		t.Run(tc.name+"/dead server", func(t *testing.T) {
			setDeadCLI(t)
			if err := tc.run(); err == nil || !strings.Contains(err.Error(), "connection refused") {
				t.Errorf("got %v", err)
			}
		})
	}
}

// TestWorkspaceCmds_MalformedJSONResponses pins the cli.ReadJSON error
// branch in each closure that decodes a response body.
func TestWorkspaceCmds_MalformedJSONResponses(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.SetFallback(clitest.TextResponse(200, "{not json"))
	setStubCLI(t, stub.URL())

	cases := []struct {
		name string
		run  func() error
	}{
		{"list", func() error { return workspaceListCmd.RunE(workspaceListCmd, nil) }},
		{"get", func() error { return workspaceGetCmd.RunE(workspaceGetCmd, []string{covWS}) }},
		{"create", func() error {
			c := newFlagCmd(map[string]string{"name": "X", "slug": "", "language": ""}, nil)
			return workspaceCreateCmd.RunE(c, nil)
		}},
		{"member list", func() error { return workspaceMemberListCmd.RunE(workspaceMemberListCmd, nil) }},
		{"invite list", func() error { return workspaceInviteListCmd.RunE(workspaceInviteListCmd, nil) }},
		{"invite send", func() error { return sendWorkspaceInvitation("a@b.c", "") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.run(); err == nil {
				t.Error("expected JSON decode error, got nil")
			}
		})
	}
}

func TestWorkspaceUse_ConfigIOErrorSurfaces(t *testing.T) {
	// Config path nested under a regular file → the config is both unreadable
	// (ENOTDIR) and unsaveable. `workspace use` must surface the error rather
	// than continue with an empty config and clobber on save.
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CREWSHIP_CONFIG", filepath.Join(blocker, "sub", "cli-config.yaml"))
	saveCLIState(t)
	flagServer = ""

	if err := workspaceUseCmd.RunE(workspaceUseCmd, []string{"acme"}); err == nil || !strings.Contains(err.Error(), "load CLI config: read config:") {
		t.Errorf("expected config read error, got %v", err)
	}
}
