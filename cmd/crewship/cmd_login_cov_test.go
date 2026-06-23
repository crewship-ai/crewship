package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

// tempCLIConfig points CREWSHIP_CONFIG at a throwaway file so login
// paths that call cli.SaveConfig never touch the developer's real
// ~/.crewship/cli-config.yaml. Returns the config path.
func tempCLIConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "cli-config.yaml")
	t.Setenv("CREWSHIP_CONFIG", path)
	return path
}

// ─── loginWithPairing ───────────────────────────────────────────────

func TestLoginWithPairing_CodeRequired(t *testing.T) {
	tempCLIConfig(t)
	err := loginWithPairing("http://localhost:1", "  ", "")
	if err == nil || !strings.Contains(err.Error(), "--code is required") {
		t.Errorf("got %v", err)
	}
}

func TestLoginWithPairing_TransportError(t *testing.T) {
	tempCLIConfig(t)
	// Port 1 is essentially never listening — connection refused, fast.
	err := loginWithPairing("http://127.0.0.1:1", "K3F9-X2NM", "")
	if err == nil || !strings.Contains(err.Error(), "could not reach http://127.0.0.1:1") {
		t.Errorf("got %v", err)
	}
}

func TestLoginWithPairing_ExpiredCodeFriendlyError(t *testing.T) {
	tempCLIConfig(t)
	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnPost("/api/v1/auth/pair/redeem", clitest.ErrorResponse(401, "Invalid or expired code"))

	err := loginWithPairing(stub.URL(), "DEAD-CODE", "")
	if err == nil || !strings.Contains(err.Error(), "codes expire after 10 minutes") {
		t.Errorf("got %v", err)
	}
}

func TestLoginWithPairing_OtherAPIError(t *testing.T) {
	tempCLIConfig(t)
	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnPost("/api/v1/auth/pair/redeem", clitest.ErrorResponse(500, "boom"))

	err := loginWithPairing(stub.URL(), "K3F9-X2NM", "")
	if err == nil || !strings.Contains(err.Error(), "pair:") || !strings.Contains(err.Error(), "boom") {
		t.Errorf("got %v", err)
	}
}

func TestLoginWithPairing_EmptyTokenInResponse(t *testing.T) {
	tempCLIConfig(t)
	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnPost("/api/v1/auth/pair/redeem", clitest.JSONResponse(200, map[string]string{
		"cli_token": "", "email": "a@b.c",
	}))

	err := loginWithPairing(stub.URL(), "K3F9-X2NM", "")
	if err == nil || !strings.Contains(err.Error(), "empty cli_token") {
		t.Errorf("got %v", err)
	}
}

func TestLoginWithPairing_SuccessSavesConfig(t *testing.T) {
	cfgPath := tempCLIConfig(t)
	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnPost("/api/v1/auth/pair/redeem", clitest.JSONResponse(200, map[string]string{
		"cli_token": "cli-tok-123", "email": "pilot@b.c",
	}))

	_ = captureStdoutCovCli2(t, func() {
		if err := loginWithPairing(stub.URL(), " K3F9-X2NM ", "CLAUDE_CODE"); err != nil {
			t.Errorf("pairing: %v", err)
		}
	})

	// The redeem request carries the trimmed code + adapter hint.
	calls := stub.CallsFor("POST", "/api/v1/auth/pair/redeem")
	if len(calls) != 1 {
		t.Fatalf("redeem calls = %d", len(calls))
	}
	var body map[string]string
	clitest.MustDecodeJSONBody(calls[0].Body, &body)
	if body["code"] != "K3F9-X2NM" || body["adapter_hint"] != "CLAUDE_CODE" {
		t.Errorf("redeem body = %v", body)
	}

	cfg, err := cli.LoadConfig()
	if err != nil {
		t.Fatalf("load saved config: %v", err)
	}
	if cfg.Token != "cli-tok-123" || cfg.Server != stub.URL() {
		t.Errorf("saved config = %+v", cfg)
	}
	if _, err := os.Stat(cfgPath); err != nil {
		t.Errorf("config file missing: %v", err)
	}
}

// ─── loginWithToken ─────────────────────────────────────────────────

func TestLoginWithToken_Success(t *testing.T) {
	tempCLIConfig(t)
	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnGet("/api/v1/auth/cli-token/validate", clitest.JSONResponse(200, map[string]string{
		"user_email": "a@b.c", "user_id": "u1",
	}))

	_ = captureStdoutCovCli2(t, func() {
		if err := loginWithToken(stub.URL(), "tok-abc"); err != nil {
			t.Errorf("loginWithToken: %v", err)
		}
	})

	calls := stub.CallsFor("GET", "/api/v1/auth/cli-token/validate")
	if len(calls) != 1 {
		t.Fatalf("validate calls = %d", len(calls))
	}
	if got := calls[0].Headers.Get("Authorization"); got != "Bearer tok-abc" {
		t.Errorf("Authorization = %q", got)
	}
	cfg, err := cli.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Token != "tok-abc" || cfg.Server != stub.URL() {
		t.Errorf("saved config = %+v", cfg)
	}
}

func TestLoginWithToken_ValidationFailure(t *testing.T) {
	cfgPath := tempCLIConfig(t)
	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnGet("/api/v1/auth/cli-token/validate", clitest.ErrorResponse(401, "bad token"))

	err := loginWithToken(stub.URL(), "tok-bad")
	if err == nil || !strings.Contains(err.Error(), "token validation failed") {
		t.Errorf("got %v", err)
	}
	if _, statErr := os.Stat(cfgPath); statErr == nil {
		t.Error("config must not be written on failed validation")
	}
}

func TestLoginWithToken_TransportError(t *testing.T) {
	tempCLIConfig(t)
	err := loginWithToken("http://127.0.0.1:1", "tok")
	if err == nil || !strings.Contains(err.Error(), "failed to connect to server") {
		t.Errorf("got %v", err)
	}
}

func TestLoginWithToken_RefusesMalformedExistingConfig(t *testing.T) {
	path := tempCLIConfig(t)
	if err := os.WriteFile(path, []byte("\t: not yaml ["), 0o600); err != nil {
		t.Fatal(err)
	}
	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnGet("/api/v1/auth/cli-token/validate", clitest.JSONResponse(200, map[string]string{"user_id": "u1"}))

	err := loginWithToken(stub.URL(), "tok")
	if err == nil || !strings.Contains(err.Error(), "refusing to overwrite a malformed file") {
		t.Errorf("got %v", err)
	}
}

// ─── loginWithGoogle ────────────────────────────────────────────────

func TestLoginWithGoogle_NotConfigured(t *testing.T) {
	tempCLIConfig(t)
	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnGet("/api/v1/auth/google/status", clitest.JSONResponse(200, map[string]bool{"enabled": false}))

	err := loginWithGoogle(stub.URL())
	if err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Errorf("got %v", err)
	}
}

func TestLoginWithGoogle_StatusErrors(t *testing.T) {
	tempCLIConfig(t)

	// Transport failure.
	if err := loginWithGoogle("http://127.0.0.1:1"); err == nil || !strings.Contains(err.Error(), "contact server") {
		t.Errorf("transport: got %v", err)
	}

	stub := clitest.NewStubServer()
	defer stub.Close()

	// API error envelope.
	stub.OnGet("/api/v1/auth/google/status", clitest.ErrorResponse(500, "down"))
	if err := loginWithGoogle(stub.URL()); err == nil || !strings.Contains(err.Error(), "google status") {
		t.Errorf("status error: got %v", err)
	}

	// Unparseable body.
	stub.OnGet("/api/v1/auth/google/status", clitest.TextResponse(200, "not json"))
	if err := loginWithGoogle(stub.URL()); err == nil || !strings.Contains(err.Error(), "parse google status") {
		t.Errorf("parse error: got %v", err)
	}
}

// ─── login command dispatch + whoami ────────────────────────────────

// saveLoginFlags snapshots the login flag globals mutated by tests.
func saveLoginFlags(t *testing.T) {
	t.Helper()
	tok, goog, pair, code, hint := loginTokenFlag, loginGoogleFlag, loginPairFlag, loginCodeFlag, loginAdapterHint
	t.Cleanup(func() {
		loginTokenFlag, loginGoogleFlag, loginPairFlag, loginCodeFlag, loginAdapterHint = tok, goog, pair, code, hint
	})
}

func TestLoginCmd_PreflightBlocksBrokenURL(t *testing.T) {
	saveCLIState(t)
	saveLoginFlags(t)
	t.Setenv("CREWSHIP_SERVER", "")
	flagServer = ""
	cliCfg = &cli.CLIConfig{Server: "://bad"}

	err := loginCmd.RunE(loginCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "invalid --server URL") {
		t.Errorf("got %v", err)
	}

	cliCfg = &cli.CLIConfig{Server: "http://"}
	err = loginCmd.RunE(loginCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "missing a host") {
		t.Errorf("missing host: got %v", err)
	}

	cliCfg = &cli.CLIConfig{Server: "ftp://example.com"}
	err = loginCmd.RunE(loginCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Errorf("bad scheme: got %v", err)
	}
}

func TestLoginCmd_DispatchesPairing(t *testing.T) {
	saveCLIState(t)
	saveLoginFlags(t)
	tempCLIConfig(t)
	t.Setenv("CREWSHIP_SERVER", "")
	flagServer = ""
	cliCfg = &cli.CLIConfig{Server: "http://127.0.0.1:1"}
	loginPairFlag = true
	loginCodeFlag = "" // --pair without --code

	err := loginCmd.RunE(loginCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "--code is required") {
		t.Errorf("got %v", err)
	}
}

func TestLoginCmd_DispatchesToken(t *testing.T) {
	saveCLIState(t)
	saveLoginFlags(t)
	tempCLIConfig(t)
	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnGet("/api/v1/auth/cli-token/validate", clitest.JSONResponse(200, map[string]string{"user_id": "u1"}))
	t.Setenv("CREWSHIP_SERVER", "")
	flagServer = ""
	cliCfg = &cli.CLIConfig{Server: stub.URL()}
	loginPairFlag = false
	loginTokenFlag = "tok-dispatch"

	_ = captureStdoutCovCli2(t, func() {
		if err := loginCmd.RunE(loginCmd, nil); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if len(stub.CallsFor("GET", "/api/v1/auth/cli-token/validate")) != 1 {
		t.Error("token dispatch did not hit the validate endpoint")
	}
	cfg, _ := cli.LoadConfig()
	if cfg.Token != "tok-dispatch" {
		t.Errorf("token not saved, cfg=%+v", cfg)
	}
}

func TestLogoutCmd_ClearsToken(t *testing.T) {
	tempCLIConfig(t)
	if err := cli.SaveConfig(&cli.CLIConfig{Token: "tok", Server: "http://x", Workspace: "ws"}); err != nil {
		t.Fatal(err)
	}
	_ = captureStdoutCovCli2(t, func() {
		if err := logoutCmd.RunE(logoutCmd, nil); err != nil {
			t.Errorf("logout: %v", err)
		}
	})
	cfg, err := cli.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Token != "" {
		t.Errorf("token not cleared: %+v", cfg)
	}
	if cfg.Workspace != "ws" {
		t.Errorf("logout must keep the rest of the config: %+v", cfg)
	}
}

func TestWhoamiRunE_NoAuth(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{}
	err := whoamiCmd.RunE(whoamiCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("got %v", err)
	}
}

func TestWhoamiRunE_JSONOutput(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnGet("/api/v1/workspaces", clitest.JSONResponse(200, []map[string]string{
		{"id": covWS, "name": "Acme", "slug": "acme", "currentUserRole": "OWNER"},
		{"id": "cother0000000000000z", "name": "Beta", "slug": "beta", "currentUserRole": "MEMBER"},
	}))
	stub.OnGet("/api/v1/auth/cli-token/validate", clitest.JSONResponse(200, map[string]string{
		"user_email": "pilot@b.c", "user_id": "u1",
	}))
	setStubCLI(t, stub.URL())

	buf := new(bytes.Buffer)
	whoamiCmd.SetOut(buf)
	t.Cleanup(func() {
		whoamiCmd.SetOut(nil)
		_ = whoamiCmd.Flags().Set("json", "false")
	})
	if err := whoamiCmd.Flags().Set("json", "true"); err != nil {
		t.Fatal(err)
	}
	if err := whoamiCmd.RunE(whoamiCmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}

	var payload struct {
		UserEmail string `json:"user_email"`
		Server    string `json:"server"`
		Workspace *struct {
			ID   string `json:"id"`
			Slug string `json:"slug"`
			Role string `json:"role"`
		} `json:"workspace"`
		WorkspacesCount int `json:"workspaces_count"`
	}
	if err := json.Unmarshal(buf.Bytes(), &payload); err != nil {
		t.Fatalf("parse JSON: %v\n%s", err, buf.String())
	}
	if payload.UserEmail != "pilot@b.c" || payload.Server != stub.URL() || payload.WorkspacesCount != 2 {
		t.Errorf("payload = %+v", payload)
	}
	if payload.Workspace == nil || payload.Workspace.ID != covWS || payload.Workspace.Role != "OWNER" {
		t.Errorf("workspace = %+v", payload.Workspace)
	}
}

func TestWhoamiRunE_HumanOutput(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnGet("/api/v1/workspaces", clitest.JSONResponse(200, []map[string]string{
		{"id": covWS, "name": "Acme", "slug": "acme", "currentUserRole": "OWNER"},
	}))
	// validate endpoint not registered → 404 → userEmail stays empty.
	setStubCLI(t, stub.URL())
	t.Cleanup(func() { _ = whoamiCmd.Flags().Set("json", "false") })
	_ = whoamiCmd.Flags().Set("json", "false")

	out := captureStdoutCovCli2(t, func() {
		if err := whoamiCmd.RunE(whoamiCmd, nil); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if !strings.Contains(out, "Acme (acme)") || !strings.Contains(out, "OWNER") {
		t.Errorf("human output:\n%s", out)
	}

	// No workspace selected → count hint instead.
	cliCfg = &cli.CLIConfig{Token: "tok", Server: stub.URL()}
	out2 := captureStdoutCovCli2(t, func() {
		if err := whoamiCmd.RunE(whoamiCmd, nil); err != nil {
			t.Errorf("RunE no-ws: %v", err)
		}
	})
	if !strings.Contains(out2, "1 available (none selected") {
		t.Errorf("no-workspace output:\n%s", out2)
	}
}

// failWriter always errors, to drive encoder failure branches.
type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("sink closed") }

func TestEmitWhoamiJSON_EncodeError(t *testing.T) {
	t.Parallel()
	err := emitWhoamiJSON(failWriter{}, "a@b.c", "http://x", "", nil)
	if err == nil || !strings.Contains(err.Error(), "encode whoami output") {
		t.Errorf("got %v", err)
	}
}

// swapStdin replaces os.Stdin with a pipe pre-loaded with content.
// Restores the original at cleanup. Only affects code that reads the
// os.Stdin *variable* (bufio readers); term.ReadPassword reads fd 0
// directly and still sees go test's /dev/null.
func swapStdin(t *testing.T, content string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	if _, err := w.WriteString(content); err != nil {
		t.Fatalf("write: %v", err)
	}
	_ = w.Close()
	old := os.Stdin
	os.Stdin = r
	t.Cleanup(func() {
		os.Stdin = old
		_ = r.Close()
	})
}

func TestLoginWithGoogle_FullFlowWithPastedToken(t *testing.T) {
	tempCLIConfig(t)
	t.Setenv("PATH", "/nonexistent") // browserOpen("open", …) must fail, not launch a browser
	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnGet("/api/v1/auth/google/status", clitest.JSONResponse(200, map[string]bool{"enabled": true}))
	stub.OnGet("/api/v1/auth/cli-token/validate", clitest.JSONResponse(200, map[string]string{"user_id": "u1"}))

	swapStdin(t, "pasted-google-tok\n")
	out := captureStdoutCovCli2(t, func() {
		if err := loginWithGoogle(stub.URL()); err != nil {
			t.Errorf("loginWithGoogle: %v", err)
		}
	})
	if !strings.Contains(out, stub.URL()+"/api/v1/auth/google/redirect") {
		t.Errorf("auth URL missing from output:\n%s", out)
	}
	if !strings.Contains(out, "Could not auto-open browser") {
		t.Errorf("expected manual-open fallback note:\n%s", out)
	}
	cfg, err := cli.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Token != "pasted-google-tok" {
		t.Errorf("pasted token not stored, cfg=%+v", cfg)
	}
}

func TestLoginWithGoogle_EmptyPastedToken(t *testing.T) {
	tempCLIConfig(t)
	t.Setenv("PATH", "/nonexistent")
	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnGet("/api/v1/auth/google/status", clitest.JSONResponse(200, map[string]bool{"enabled": true}))

	swapStdin(t, "   \n")
	_ = captureStdoutCovCli2(t, func() {
		if err := loginWithGoogle(stub.URL()); err == nil || !strings.Contains(err.Error(), "no token entered") {
			t.Errorf("got %v", err)
		}
	})
}

func TestLoginInteractive_PasswordReadFailsOffTTY(t *testing.T) {
	tempCLIConfig(t)
	// Email comes from the swapped os.Stdin pipe; the password read uses
	// term.ReadPassword on fd 0, which under go test is /dev/null (not a
	// terminal) and errors instead of blocking.
	swapStdin(t, "pilot@b.c\n")
	_ = captureStdoutCovCli2(t, func() {
		err := loginInteractive("http://127.0.0.1:1")
		if err == nil || !strings.Contains(err.Error(), "read password") {
			t.Errorf("got %v", err)
		}
	})
}

func TestLoginCmd_DispatchesGoogleAndInteractive(t *testing.T) {
	saveCLIState(t)
	saveLoginFlags(t)
	tempCLIConfig(t)
	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnGet("/api/v1/auth/google/status", clitest.JSONResponse(200, map[string]bool{"enabled": false}))
	t.Setenv("CREWSHIP_SERVER", "")
	flagServer = ""
	cliCfg = &cli.CLIConfig{Server: stub.URL()}

	// --google route.
	loginPairFlag, loginTokenFlag, loginGoogleFlag = false, "", true
	if err := loginCmd.RunE(loginCmd, nil); err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Errorf("google dispatch: got %v", err)
	}

	// Default route → interactive. Under go test the password read on
	// fd 0 (/dev/null) fails instead of blocking, so the dispatch is
	// observable through the "read password" error.
	loginGoogleFlag = false
	swapStdin(t, "pilot@b.c\n")
	_ = captureStdoutCovCli2(t, func() {
		if err := loginCmd.RunE(loginCmd, nil); err == nil || !strings.Contains(err.Error(), "read password") {
			t.Errorf("interactive dispatch: got %v", err)
		}
	})
}

func TestLogoutCmd_MalformedConfigError(t *testing.T) {
	path := tempCLIConfig(t)
	if err := os.WriteFile(path, []byte("\t: not yaml ["), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := logoutCmd.RunE(logoutCmd, nil); err == nil || !strings.Contains(err.Error(), "parse config") {
		t.Errorf("got %v", err)
	}
}

func TestWhoamiRunE_TransportAndAPIErrors(t *testing.T) {
	saveCLIState(t)
	t.Setenv("CREWSHIP_SERVER", "")
	flagServer = ""

	// Transport error.
	cliCfg = &cli.CLIConfig{Token: "tok", Server: "http://127.0.0.1:1"}
	if err := whoamiCmd.RunE(whoamiCmd, nil); err == nil || !strings.Contains(err.Error(), "failed to connect to server") {
		t.Errorf("transport: got %v", err)
	}

	stub := clitest.NewStubServer()
	defer stub.Close()
	setStubCLI(t, stub.URL())

	// CheckError branch.
	stub.OnGet("/api/v1/workspaces", clitest.ErrorResponse(401, "expired"))
	if err := whoamiCmd.RunE(whoamiCmd, nil); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Errorf("CheckError: got %v", err)
	}

	// ReadJSON branch.
	stub.OnGet("/api/v1/workspaces", clitest.TextResponse(200, "{not json"))
	if err := whoamiCmd.RunE(whoamiCmd, nil); err == nil {
		t.Error("expected decode error")
	}
}

func TestLoginWithPairing_UnparseableRedeemResponse(t *testing.T) {
	tempCLIConfig(t)
	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnPost("/api/v1/auth/pair/redeem", clitest.TextResponse(200, "{not json"))

	err := loginWithPairing(stub.URL(), "K3F9-X2NM", "")
	if err == nil || !strings.Contains(err.Error(), "parse redeem response") {
		t.Errorf("got %v", err)
	}
}

func TestLoginWithPairing_MalformedConfigAndSaveError(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnPost("/api/v1/auth/pair/redeem", clitest.JSONResponse(200, map[string]string{
		"cli_token": "tok", "email": "a@b.c",
	}))

	// Existing config file is malformed YAML → refuse to overwrite.
	path := tempCLIConfig(t)
	if err := os.WriteFile(path, []byte("\t: not yaml ["), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := loginWithPairing(stub.URL(), "CODE", ""); err == nil ||
		!strings.Contains(err.Error(), "refusing to overwrite a malformed file") {
		t.Errorf("malformed: got %v", err)
	}

	// Save fails because the config dir is read-only: LoadConfig sees a
	// missing file (fine), the write then hits EACCES.
	roDir := t.TempDir()
	if err := os.Chmod(roDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(roDir, 0o700) })
	t.Setenv("CREWSHIP_CONFIG", filepath.Join(roDir, "cli-config.yaml"))
	if err := loginWithPairing(stub.URL(), "CODE", ""); err == nil ||
		!strings.Contains(err.Error(), "save config") {
		t.Errorf("save error: got %v", err)
	}
}

func TestLoginWithToken_SaveConfigError(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnGet("/api/v1/auth/cli-token/validate", clitest.JSONResponse(200, map[string]string{"user_id": "u1"}))

	roDir := t.TempDir()
	if err := os.Chmod(roDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(roDir, 0o700) })
	t.Setenv("CREWSHIP_CONFIG", filepath.Join(roDir, "cli-config.yaml"))

	if err := loginWithToken(stub.URL(), "tok"); err == nil || !strings.Contains(err.Error(), "save config") {
		t.Errorf("got %v", err)
	}
}

func TestLoginWithGoogle_TokenReadEOF(t *testing.T) {
	tempCLIConfig(t)
	t.Setenv("PATH", "/nonexistent")
	stub := clitest.NewStubServer()
	defer stub.Close()
	stub.OnGet("/api/v1/auth/google/status", clitest.JSONResponse(200, map[string]bool{"enabled": true}))

	// Pipe closed without a newline → ReadString returns EOF.
	swapStdin(t, "")
	_ = captureStdoutCovCli2(t, func() {
		if err := loginWithGoogle(stub.URL()); err == nil || !strings.Contains(err.Error(), "read token") {
			t.Errorf("got %v", err)
		}
	})
}
