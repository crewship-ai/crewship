package main

// Coverage tests for cmd_backup_admin.go. Also hosts the shared cov*
// helpers used by the other *_cov_test.go files in this package.
//
// None of these tests run in parallel: they mutate the package-level
// cliCfg / flag globals and swap os.Stdout/os.Stderr.

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

// covWorkspaceIDCli6 is a CUID-shaped workspace id (c + 20 lowercase alnum)
// so the client's GetWorkspaceID short-circuits without an HTTP lookup.
const covWorkspaceIDCli6 = "cworkspace0123456789a"

// covCrewIDCli6 / covAgentIDCli6 / covMissionIDCli6 are CUID-shaped so the
// resolve* helpers skip slug resolution where the test wants them to.
const (
	covCrewIDCli6    = "ccrew0123456789abcdef"
	covAgentIDCli6   = "cagent0123456789abcde"
	covMissionIDCli6 = "cmission0123456789abc"
)

// covSetupCli6 wires the package globals at a stub server with a logged-in,
// workspace-scoped config. Restores everything via saveCLIState.
func covSetupCli6(t *testing.T, stub *clitest.StubServer) {
	t.Helper()
	saveCLIState(t)
	flagServer = ""
	flagWorkspace = ""
	cliCfg = &cli.CLIConfig{Token: "fake-token", Workspace: covWorkspaceIDCli6, Server: stub.URL()}
}

// covRestoreFlag resets a single pflag to its default value and clears
// the Changed bit (Changed otherwise sticks for the whole test binary).
func covRestoreFlag(f *pflag.Flag) {
	if sv, ok := f.Value.(pflag.SliceValue); ok {
		_ = sv.Replace(nil)
	} else {
		_ = f.Value.Set(f.DefValue)
	}
	f.Changed = false
}

// covSetFlagCli6 sets a flag for the duration of the test and restores the
// default (value + Changed) at cleanup.
func covSetFlagCli6(t *testing.T, cmd *cobra.Command, name, value string) {
	t.Helper()
	f := cmd.Flags().Lookup(name)
	if f == nil {
		t.Fatalf("command %s has no --%s flag", cmd.Name(), name)
	}
	if err := cmd.Flags().Set(name, value); err != nil {
		t.Fatalf("set --%s=%s: %v", name, value, err)
	}
	t.Cleanup(func() { covRestoreFlag(f) })
}

// covResetFlagsCli6 forces the given flags back to defaults right now AND at
// cleanup — used by tests that need "flag never touched" semantics.
func covResetFlagsCli6(t *testing.T, cmd *cobra.Command, names ...string) {
	t.Helper()
	for _, name := range names {
		f := cmd.Flags().Lookup(name)
		if f == nil {
			t.Fatalf("command %s has no --%s flag", cmd.Name(), name)
		}
		covRestoreFlag(f)
		t.Cleanup(func() { covRestoreFlag(f) })
	}
}

// covCaptureStdoutCli6 swaps os.Stdout for a pipe while fn runs and returns
// everything written plus fn's error.
func covCaptureStdoutCli6(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	return covCaptureFD(t, &os.Stdout, fn)
}

// covCaptureStderrCli6 is covCaptureStdoutCli6 for os.Stderr.
func covCaptureStderrCli6(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	return covCaptureFD(t, &os.Stderr, fn)
}

func covCaptureFD(t *testing.T, target **os.File, fn func() error) (string, error) {
	t.Helper()
	old := *target
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	*target = w
	defer func() { *target = old }()

	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	runErr := fn()
	_ = w.Close()
	out := <-done
	_ = r.Close()
	return out, runErr
}

// covDecodeBody decodes a recorded request body into a generic map.
func covDecodeBody(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("decode recorded body %q: %v", body, err)
	}
	return m
}

// ─── backup verify ───────────────────────────────────────────────────────

func TestBackupVerifyRunE_Valid(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)

	stub.OnGet("/api/v1/admin/backups/verify", clitest.JSONResponse(200, map[string]any{
		"valid":      true,
		"size_bytes": 2048,
	}))

	if err := backupVerifyCmd.RunE(backupVerifyCmd, []string{"/srv/backups/x.tar.zst"}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	calls := stub.CallsFor("GET", "/api/v1/admin/backups/verify")
	if len(calls) != 1 {
		t.Fatalf("expected 1 verify call, got %d", len(calls))
	}
	if !strings.Contains(calls[0].Query, "path=%2Fsrv%2Fbackups%2Fx.tar.zst") {
		t.Errorf("path not URL-encoded into query: %q", calls[0].Query)
	}
}

func TestBackupVerifyRunE_Invalid(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)

	stub.OnGet("/api/v1/admin/backups/verify", clitest.JSONResponse(200, map[string]any{
		"valid": false,
		"error": "checksum mismatch",
	}))

	err := backupVerifyCmd.RunE(backupVerifyCmd, []string{"b.tar.zst"})
	if err == nil || !strings.Contains(err.Error(), "bundle verification failed") {
		t.Errorf("expected verification failure, got %v", err)
	}
}

func TestBackupVerifyRunE_APIError(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)

	stub.OnGet("/api/v1/admin/backups/verify", clitest.ErrorResponse(500, "boom"))

	err := backupVerifyCmd.RunE(backupVerifyCmd, []string{"b.tar.zst"})
	if err == nil || !strings.Contains(err.Error(), "API error (500): boom") {
		t.Errorf("expected API error, got %v", err)
	}
}

func TestBackupVerifyRunE_NoAuth(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{}

	err := backupVerifyCmd.RunE(backupVerifyCmd, []string{"b.tar.zst"})
	if err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("expected 'not logged in', got %v", err)
	}
}

func TestBackupVerifyRunE_NoWorkspace(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{Token: "fake-token"}
	flagWorkspace = ""
	t.Setenv("CREWSHIP_WORKSPACE", "")

	err := backupVerifyCmd.RunE(backupVerifyCmd, []string{"b.tar.zst"})
	if err == nil || !strings.Contains(err.Error(), "workspace") {
		t.Errorf("expected workspace error, got %v", err)
	}
}

// ─── backup unlock ───────────────────────────────────────────────────────

func TestBackupUnlockRunE_NonInteractiveRequiresForce(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)
	covResetFlagsCli6(t, backupUnlockCmd, "force")

	// go test runs with a non-TTY stdin, so the interactive prompt is
	// unreachable and the command must refuse outright.
	err := backupUnlockCmd.RunE(backupUnlockCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "refusing to unlock without --force") {
		t.Fatalf("expected refusal, got %v", err)
	}
	if n := len(stub.Calls()); n != 0 {
		t.Errorf("no HTTP call expected before the force check, got %d", n)
	}
}

func TestBackupUnlockRunE_Force(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)
	covSetFlagCli6(t, backupUnlockCmd, "force", "true")

	stub.OnDelete("/api/v1/admin/backups/status", clitest.EmptyResponse(200))

	if err := backupUnlockCmd.RunE(backupUnlockCmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if n := len(stub.CallsFor("DELETE", "/api/v1/admin/backups/status")); n != 1 {
		t.Errorf("expected exactly 1 DELETE, got %d", n)
	}
}

func TestBackupUnlockRunE_ForceAPIError(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)
	covSetFlagCli6(t, backupUnlockCmd, "force", "true")

	stub.OnDelete("/api/v1/admin/backups/status", clitest.ErrorResponse(409, "lock not held"))

	err := backupUnlockCmd.RunE(backupUnlockCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "lock not held") {
		t.Errorf("expected 409 surfaced, got %v", err)
	}
}

// ─── backup metrics ──────────────────────────────────────────────────────

func TestBackupMetricsRunE_PrintsJSON(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)

	stub.OnGet("/api/v1/admin/backups/metrics", clitest.JSONResponse(200, map[string]any{
		"created_total": 3,
		"failed_total":  1,
	}))

	out, err := covCaptureStdoutCli6(t, func() error {
		return backupMetricsCmd.RunE(backupMetricsCmd, nil)
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "created_total") || !strings.Contains(out, "3") {
		t.Errorf("metrics JSON not printed: %q", out)
	}
}

func TestBackupMetricsRunE_APIError(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)

	stub.OnGet("/api/v1/admin/backups/metrics", clitest.ErrorResponse(403, "owner only"))

	err := backupMetricsCmd.RunE(backupMetricsCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "owner only") {
		t.Errorf("expected 403 surfaced, got %v", err)
	}
}

// ─── backup download ─────────────────────────────────────────────────────

func TestBackupDownloadRunE_WritesFile(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)

	stub.OnGet("/api/v1/admin/backups/download", clitest.TextResponse(200, "bundle-bytes"))

	dest := filepath.Join(t.TempDir(), "out.tar.zst")
	covSetFlagCli6(t, backupDownloadCmd, "out", dest)

	if err := backupDownloadCmd.RunE(backupDownloadCmd, []string{"/srv/backups/b.tar.zst"}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read dest: %v", err)
	}
	if string(got) != "bundle-bytes" {
		t.Errorf("dest content = %q, want %q", got, "bundle-bytes")
	}
	calls := stub.CallsFor("GET", "/api/v1/admin/backups/download")
	if len(calls) != 1 || !strings.Contains(calls[0].Query, "path=%2Fsrv%2Fbackups%2Fb.tar.zst") {
		t.Errorf("download query wrong: %+v", calls)
	}
}

func TestBackupDownloadRunE_DefaultDestIsBasename(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)
	covResetFlagsCli6(t, backupDownloadCmd, "out", "force")

	stub.OnGet("/api/v1/admin/backups/download", clitest.TextResponse(200, "x"))

	t.Chdir(t.TempDir())
	if err := backupDownloadCmd.RunE(backupDownloadCmd, []string{"/srv/backups/basename.bin"}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if _, err := os.Stat("basename.bin"); err != nil {
		t.Errorf("expected basename.bin in cwd: %v", err)
	}
}

func TestBackupDownloadRunE_RefusesOverwrite(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)

	stub.OnGet("/api/v1/admin/backups/download", clitest.TextResponse(200, "new"))

	dest := filepath.Join(t.TempDir(), "exists.bin")
	if err := os.WriteFile(dest, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	covSetFlagCli6(t, backupDownloadCmd, "out", dest)
	covResetFlagsCli6(t, backupDownloadCmd, "force")

	err := backupDownloadCmd.RunE(backupDownloadCmd, []string{"b.bin"})
	if err == nil || !strings.Contains(err.Error(), "already exists; pass --force") {
		t.Fatalf("expected clobber refusal, got %v", err)
	}
	got, _ := os.ReadFile(dest)
	if string(got) != "old" {
		t.Errorf("existing file must be untouched, got %q", got)
	}
}

func TestBackupDownloadRunE_ForceOverwrites(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)

	stub.OnGet("/api/v1/admin/backups/download", clitest.TextResponse(200, "new"))

	dest := filepath.Join(t.TempDir(), "exists.bin")
	if err := os.WriteFile(dest, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	covSetFlagCli6(t, backupDownloadCmd, "out", dest)
	covSetFlagCli6(t, backupDownloadCmd, "force", "true")

	if err := backupDownloadCmd.RunE(backupDownloadCmd, []string{"b.bin"}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	got, _ := os.ReadFile(dest)
	if string(got) != "new" {
		t.Errorf("dest = %q, want overwritten %q", got, "new")
	}
}

func TestBackupDownloadRunE_APIError(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)

	stub.OnGet("/api/v1/admin/backups/download", clitest.ErrorResponse(404, "bundle not found"))

	err := backupDownloadCmd.RunE(backupDownloadCmd, []string{"ghost.bin"})
	if err == nil || !strings.Contains(err.Error(), "bundle not found") {
		t.Errorf("expected 404 surfaced, got %v", err)
	}
}

// ─── backup self-test ────────────────────────────────────────────────────

func TestBackupSelfTestRunE_RequiresCrew(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)
	covResetFlagsCli6(t, backupSelfTestCmd, "crew")

	err := backupSelfTestCmd.RunE(backupSelfTestCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "--crew is required") {
		t.Errorf("expected '--crew is required', got %v", err)
	}
}

func TestBackupSelfTestRunE_Happy(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)
	covSetFlagCli6(t, backupSelfTestCmd, "crew", "engineering")

	stub.OnGet("/api/v1/crews", clitest.JSONResponse(200, []map[string]string{
		{"id": covCrewIDCli6, "slug": "engineering"},
	}))
	stub.OnPost("/api/v1/admin/backups/self-test", clitest.JSONResponse(200, map[string]any{
		"ok": true, "crew_slug": "engineering",
	}))

	out, err := covCaptureStdoutCli6(t, func() error {
		return backupSelfTestCmd.RunE(backupSelfTestCmd, nil)
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, `"ok": true`) {
		t.Errorf("self-test result not printed: %q", out)
	}
	calls := stub.CallsFor("POST", "/api/v1/admin/backups/self-test")
	if len(calls) != 1 {
		t.Fatalf("expected 1 self-test POST, got %d", len(calls))
	}
	body := covDecodeBody(t, calls[0].Body)
	if body["crew_id"] != covCrewIDCli6 {
		t.Errorf("crew_id = %v, want resolved %s", body["crew_id"], covCrewIDCli6)
	}
}

func TestBackupSelfTestRunE_CrewResolveError(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)
	covSetFlagCli6(t, backupSelfTestCmd, "crew", "ghost")

	stub.OnGet("/api/v1/crews", clitest.JSONResponse(200, []map[string]string{}))

	err := backupSelfTestCmd.RunE(backupSelfTestCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "crew not found: ghost") {
		t.Errorf("expected crew-not-found, got %v", err)
	}
}

// ─── backup rotate ───────────────────────────────────────────────────────

func TestBackupRotateRunE_RequiresPolicy(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)
	covResetFlagsCli6(t, backupRotateCmd, "keep-last", "keep-days", "dry-run")

	err := backupRotateCmd.RunE(backupRotateCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "at least one of --keep-last or --keep-days") {
		t.Errorf("expected policy validation error, got %v", err)
	}
	if n := len(stub.Calls()); n != 0 {
		t.Errorf("validation must fire before any HTTP call, got %d calls", n)
	}
}

func TestBackupRotateRunE_DeletesBundles(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)
	covSetFlagCli6(t, backupRotateCmd, "keep-last", "3")

	stub.OnPost("/api/v1/admin/backups/rotate", clitest.JSONResponse(200, map[string]any{
		"deleted": []string{"a.tar.zst", "b.tar.zst"},
		"dry_run": false,
	}))

	out, err := covCaptureStderrCli6(t, func() error {
		return backupRotateCmd.RunE(backupRotateCmd, nil)
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "Deleted 2 bundle(s):") || !strings.Contains(out, "a.tar.zst") {
		t.Errorf("deletion summary missing: %q", out)
	}

	calls := stub.CallsFor("POST", "/api/v1/admin/backups/rotate")
	if len(calls) != 1 {
		t.Fatalf("expected 1 rotate POST, got %d", len(calls))
	}
	body := covDecodeBody(t, calls[0].Body)
	if body["keep_last"] != float64(3) || body["keep_days"] != float64(0) || body["dry_run"] != false {
		t.Errorf("rotate body wrong: %v", body)
	}
}

func TestBackupRotateRunE_DryRunVerb(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)
	covSetFlagCli6(t, backupRotateCmd, "keep-days", "7")
	covSetFlagCli6(t, backupRotateCmd, "dry-run", "true")

	stub.OnPost("/api/v1/admin/backups/rotate", clitest.JSONResponse(200, map[string]any{
		"deleted": []string{"x.tar.zst"},
		"dry_run": true,
	}))

	out, err := covCaptureStderrCli6(t, func() error {
		return backupRotateCmd.RunE(backupRotateCmd, nil)
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "Would delete 1 bundle(s):") {
		t.Errorf("dry-run verb missing: %q", out)
	}
}

func TestBackupRotateRunE_NoMatches(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)
	covSetFlagCli6(t, backupRotateCmd, "keep-last", "10")

	stub.OnPost("/api/v1/admin/backups/rotate", clitest.JSONResponse(200, map[string]any{
		"deleted": []string{},
		"dry_run": false,
	}))

	out, err := covCaptureStderrCli6(t, func() error {
		return backupRotateCmd.RunE(backupRotateCmd, nil)
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "No bundles matched the retention policy.") {
		t.Errorf("no-match message missing: %q", out)
	}
}

func TestBackupRotateRunE_APIError(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)
	covSetFlagCli6(t, backupRotateCmd, "keep-last", "1")

	stub.OnPost("/api/v1/admin/backups/rotate", clitest.ErrorResponse(500, "disk error"))

	err := backupRotateCmd.RunE(backupRotateCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "disk error") {
		t.Errorf("expected 500 surfaced, got %v", err)
	}
}

// ─── shared error-path tables ────────────────────────────────────────────

// backupAdminRunners enumerates each backup-admin RunE with the setup it
// needs to get past flag validation, so the auth / workspace / transport
// short-circuits can be exercised uniformly.
func backupAdminRunners(t *testing.T) map[string]func() error {
	t.Helper()
	covSetFlagCli6(t, backupUnlockCmd, "force", "true")
	covSetFlagCli6(t, backupRotateCmd, "keep-last", "1")
	covSetFlagCli6(t, backupSelfTestCmd, "crew", covCrewIDCli6)
	covSetFlagCli6(t, backupDownloadCmd, "out", filepath.Join(t.TempDir(), "dl.bin"))
	return map[string]func() error{
		"verify":    func() error { return backupVerifyCmd.RunE(backupVerifyCmd, []string{"b.bin"}) },
		"unlock":    func() error { return backupUnlockCmd.RunE(backupUnlockCmd, nil) },
		"metrics":   func() error { return backupMetricsCmd.RunE(backupMetricsCmd, nil) },
		"download":  func() error { return backupDownloadCmd.RunE(backupDownloadCmd, []string{"b.bin"}) },
		"self-test": func() error { return backupSelfTestCmd.RunE(backupSelfTestCmd, nil) },
		"rotate":    func() error { return backupRotateCmd.RunE(backupRotateCmd, nil) },
	}
}

func TestBackupAdminCmds_NoAuth(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{}
	for name, run := range backupAdminRunners(t) {
		if err := run(); err == nil || !strings.Contains(err.Error(), "not logged in") {
			t.Errorf("%s: expected 'not logged in', got %v", name, err)
		}
	}
}

func TestBackupAdminCmds_NoWorkspace(t *testing.T) {
	saveCLIState(t)
	cliCfg = &cli.CLIConfig{Token: "fake-token"}
	flagWorkspace = ""
	t.Setenv("CREWSHIP_WORKSPACE", "")
	for name, run := range backupAdminRunners(t) {
		if err := run(); err == nil || !strings.Contains(err.Error(), "workspace") {
			t.Errorf("%s: expected workspace error, got %v", name, err)
		}
	}
}

func TestBackupAdminCmds_TransportError(t *testing.T) {
	stub := clitest.NewStubServer()
	stub.Close() // connection refused for every command
	covSetupCli6(t, stub)
	for name, run := range backupAdminRunners(t) {
		if err := run(); err == nil || !strings.Contains(err.Error(), "request failed") {
			t.Errorf("%s: expected transport error, got %v", name, err)
		}
	}
}

func TestBackupVerifyRunE_DecodeError(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)
	stub.OnGet("/api/v1/admin/backups/verify", clitest.TextResponse(200, "not json"))

	err := backupVerifyCmd.RunE(backupVerifyCmd, []string{"b.bin"})
	if err == nil || !strings.Contains(err.Error(), "decode response") {
		t.Errorf("expected decode error, got %v", err)
	}
}

func TestBackupMetricsRunE_DecodeError(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)
	stub.OnGet("/api/v1/admin/backups/metrics", clitest.TextResponse(200, "not json"))

	err := backupMetricsCmd.RunE(backupMetricsCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "decode response") {
		t.Errorf("expected decode error, got %v", err)
	}
}

func TestBackupSelfTestRunE_APIError(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)
	covSetFlagCli6(t, backupSelfTestCmd, "crew", covCrewIDCli6)
	stub.OnPost("/api/v1/admin/backups/self-test", clitest.ErrorResponse(503, "docker down"))

	err := backupSelfTestCmd.RunE(backupSelfTestCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "docker down") {
		t.Errorf("expected 503 surfaced, got %v", err)
	}
}

func TestBackupSelfTestRunE_DecodeError(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)
	covSetFlagCli6(t, backupSelfTestCmd, "crew", covCrewIDCli6)
	stub.OnPost("/api/v1/admin/backups/self-test", clitest.TextResponse(200, "not json"))

	err := backupSelfTestCmd.RunE(backupSelfTestCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "decode response") {
		t.Errorf("expected decode error, got %v", err)
	}
}

func TestBackupRotateRunE_DecodeError(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)
	covSetFlagCli6(t, backupRotateCmd, "keep-last", "1")
	stub.OnPost("/api/v1/admin/backups/rotate", clitest.TextResponse(200, "not json"))

	err := backupRotateCmd.RunE(backupRotateCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "decode response") {
		t.Errorf("expected decode error, got %v", err)
	}
}

func TestBackupDownloadRunE_CreateOutputError(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli6(t, stub)
	stub.OnGet("/api/v1/admin/backups/download", clitest.TextResponse(200, "x"))
	covSetFlagCli6(t, backupDownloadCmd, "out", filepath.Join(t.TempDir(), "no-such-dir", "f.bin"))

	err := backupDownloadCmd.RunE(backupDownloadCmd, []string{"b.bin"})
	if err == nil || !strings.Contains(err.Error(), "create output") {
		t.Errorf("expected create-output error, got %v", err)
	}
}

// ─── command wiring ──────────────────────────────────────────────────────

func TestBackupAdminCommandWiring(t *testing.T) {
	have := map[string]bool{}
	for _, sub := range backupCmd.Commands() {
		have[sub.Name()] = true
	}
	for _, want := range []string{"verify", "unlock", "metrics", "download", "self-test", "rotate"} {
		if !have[want] {
			t.Errorf("backup missing subcommand %q", want)
		}
	}
	if f := backupDownloadCmd.Flags().Lookup("out"); f == nil {
		t.Error("download missing --out")
	}
	if f := backupSelfTestCmd.Flags().Lookup("crew"); f == nil {
		t.Error("self-test missing --crew")
	}
}
