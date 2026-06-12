package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

// backupDefaultKeyringForTest opens the same HOME-scoped keyring the CLI
// uses, so tests can seed entries through the production code path.
func backupDefaultKeyringForTest(t *testing.T) (*backup.Keyring, error) {
	t.Helper()
	return backup.DefaultKeyring(t.Context())
}

// covResetBackupCreateFlags pins every backup-create flag to a known
// baseline (and restores the previous values afterwards).
func covResetBackupCreateFlags(t *testing.T) {
	t.Helper()
	covSetFlagCli5(t, backupCreateCmd, "scope", "workspace")
	covSetFlagCli5(t, backupCreateCmd, "crew", "")
	covSetFlagCli5(t, backupCreateCmd, "no-encrypt", "false")
	covSetFlagCli5(t, backupCreateCmd, "passphrase-file", "")
	covSetFlagCli5(t, backupCreateCmd, "use-keyring", "false")
	covSetFlagCli5(t, backupCreateCmd, "recipient", "")
	covSetFlagCli5(t, backupCreateCmd, "output", "")
}

func covPassphraseFile(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "pass.txt")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatalf("write passphrase file: %v", err)
	}
	return p
}

// ─── backup create: validation ───────────────────────────────────────────

func TestBackupCreateRunE_InvalidScope(t *testing.T) {
	covSetupCli5(t)
	covResetBackupCreateFlags(t)
	covSetFlagCli5(t, backupCreateCmd, "scope", "galaxy")

	err := backupCreateCmd.RunE(backupCreateCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "--scope must be 'workspace' or 'crew'") {
		t.Errorf("expected scope error; got %v", err)
	}
}

func TestBackupCreateRunE_CrewScopeNeedsCrew(t *testing.T) {
	covSetupCli5(t)
	covResetBackupCreateFlags(t)
	covSetFlagCli5(t, backupCreateCmd, "scope", "crew")

	err := backupCreateCmd.RunE(backupCreateCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "--crew <slug-or-id> is required") {
		t.Errorf("expected crew-required error; got %v", err)
	}
}

func TestBackupCreateRunE_RecipientExclusions(t *testing.T) {
	covSetupCli5(t)
	covResetBackupCreateFlags(t)
	covSetFlagCli5(t, backupCreateCmd, "recipient", "age1xyz")
	covSetFlagCli5(t, backupCreateCmd, "no-encrypt", "true")

	err := backupCreateCmd.RunE(backupCreateCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "--recipient and --no-encrypt are mutually exclusive") {
		t.Errorf("expected recipient/no-encrypt exclusion; got %v", err)
	}

	covSetFlagCli5(t, backupCreateCmd, "no-encrypt", "false")
	covSetFlagCli5(t, backupCreateCmd, "passphrase-file", "/tmp/x")
	err = backupCreateCmd.RunE(backupCreateCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "--recipient and --passphrase-file are mutually exclusive") {
		t.Errorf("expected recipient/passphrase-file exclusion; got %v", err)
	}
}

func TestBackupCreateRunE_RecipientMustBeAge1(t *testing.T) {
	covSetupCli5(t)
	covResetBackupCreateFlags(t)
	covSetFlagCli5(t, backupCreateCmd, "recipient", "ssh-rsa AAAA")

	err := backupCreateCmd.RunE(backupCreateCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "age1") {
		t.Errorf("expected age1 validation error; got %v", err)
	}
}

func TestBackupCreateRunE_NoAuth(t *testing.T) {
	covSetupCli5(t)
	cliCfg = &cli.CLIConfig{}
	err := backupCreateCmd.RunE(backupCreateCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("expected not-logged-in; got %v", err)
	}
}

// ─── backup create: happy paths ──────────────────────────────────────────

func TestBackupCreateRunE_NoEncrypt(t *testing.T) {
	stub := covSetupCli5(t)
	covResetBackupCreateFlags(t)
	covSetFlagCli5(t, backupCreateCmd, "no-encrypt", "true")
	covSetFlagCli5(t, backupCreateCmd, "output", "/backups")
	stub.OnPost("/api/v1/admin/backups", clitest.JSONResponse(200, map[string]any{
		"path": "/backups/ws.tar", "size_bytes": 2048, "payload_sha256": strings.Repeat("ab", 32),
		"format_version": 2, "scope": "workspace", "encrypted": false,
	}))

	var err error
	out := covCaptureAll(t, func() { err = backupCreateCmd.RunE(backupCreateCmd, nil) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "Backup created: /backups/ws.tar") {
		t.Errorf("missing success line; got:\n%s", out)
	}
	if !strings.Contains(out, "plaintext data") {
		t.Errorf("missing no-encrypt warning; got:\n%s", out)
	}
	calls := stub.CallsFor("POST", "/api/v1/admin/backups")
	if len(calls) != 1 {
		t.Fatalf("expected 1 POST, got %d", len(calls))
	}
	var body map[string]any
	clitest.MustDecodeJSONBody(calls[0].Body, &body)
	if body["no_encrypt"] != true || body["scope"] != "workspace" || body["output_dir"] != "/backups" {
		t.Errorf("body = %v", body)
	}
	if body["passphrase"] != "" {
		t.Errorf("no-encrypt must not send a passphrase; got %v", body["passphrase"])
	}
}

func TestBackupCreateRunE_Recipient(t *testing.T) {
	stub := covSetupCli5(t)
	covResetBackupCreateFlags(t)
	covSetFlagCli5(t, backupCreateCmd, "recipient", "age1qqqqqqqq")
	stub.OnPost("/api/v1/admin/backups", clitest.JSONResponse(200, map[string]any{
		"path": "/b.tar", "scope": "workspace", "encrypted": true, "format_version": 2,
	}))

	var err error
	covCaptureAll(t, func() { err = backupCreateCmd.RunE(backupCreateCmd, nil) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	var body map[string]any
	clitest.MustDecodeJSONBody(stub.CallsFor("POST", "/api/v1/admin/backups")[0].Body, &body)
	if body["recipient"] != "age1qqqqqqqq" || body["passphrase"] != "" {
		t.Errorf("body = %v", body)
	}
}

func TestBackupCreateRunE_PassphraseFileAndCrewScope(t *testing.T) {
	stub := covSetupCli5(t)
	covResetBackupCreateFlags(t)
	covSetFlagCli5(t, backupCreateCmd, "scope", "crew")
	covSetFlagCli5(t, backupCreateCmd, "crew", "backend")
	covSetFlagCli5(t, backupCreateCmd, "passphrase-file", covPassphraseFile(t, "hunter2 \n"))
	stub.OnGet("/api/v1/crews", clitest.JSONResponse(200,
		[]map[string]string{{"id": covCrewIDCli5, "slug": "backend"}}))
	stub.OnPost("/api/v1/admin/backups", clitest.JSONResponse(200, map[string]any{
		"path": "/b.tar", "scope": "crew", "encrypted": true, "format_version": 2,
	}))

	var err error
	covCaptureAll(t, func() { err = backupCreateCmd.RunE(backupCreateCmd, nil) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	var body map[string]any
	clitest.MustDecodeJSONBody(stub.CallsFor("POST", "/api/v1/admin/backups")[0].Body, &body)
	if body["passphrase"] != "hunter2" {
		t.Errorf("passphrase = %v, want trimmed hunter2", body["passphrase"])
	}
	if body["scope"] != "crew" || body["crew_id"] != covCrewIDCli5 {
		t.Errorf("crew scope body = %v", body)
	}
}

func TestBackupCreateRunE_PassphraseFromStdinFallback(t *testing.T) {
	stub := covSetupCli5(t)
	covResetBackupCreateFlags(t)
	covSwapStdin(t, "piped-secret\n")
	stub.OnPost("/api/v1/admin/backups", clitest.JSONResponse(200, map[string]any{
		"path": "/b.tar", "scope": "workspace", "encrypted": true, "format_version": 2,
	}))

	var err error
	covCaptureAll(t, func() { err = backupCreateCmd.RunE(backupCreateCmd, nil) })
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	var body map[string]any
	clitest.MustDecodeJSONBody(stub.CallsFor("POST", "/api/v1/admin/backups")[0].Body, &body)
	if body["passphrase"] != "piped-secret" {
		t.Errorf("passphrase = %v, want piped-secret", body["passphrase"])
	}
}

func TestBackupCreateRunE_NoPassphraseOnStdin(t *testing.T) {
	covSetupCli5(t)
	covResetBackupCreateFlags(t)
	covSwapStdin(t, "")

	err := backupCreateCmd.RunE(backupCreateCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "no passphrase on stdin") {
		t.Errorf("expected stdin-passphrase error; got %v", err)
	}
}

func TestBackupCreateRunE_APIError(t *testing.T) {
	stub := covSetupCli5(t)
	covResetBackupCreateFlags(t)
	covSetFlagCli5(t, backupCreateCmd, "no-encrypt", "true")
	stub.OnPost("/api/v1/admin/backups", clitest.ErrorResponse(500, "disk full"))

	var err error
	covCaptureAll(t, func() { err = backupCreateCmd.RunE(backupCreateCmd, nil) })
	if err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Errorf("expected disk-full error; got %v", err)
	}
}

// ─── backup restore ──────────────────────────────────────────────────────

func covResetBackupRestoreFlags(t *testing.T) {
	t.Helper()
	covSetFlagCli5(t, backupRestoreCmd, "as-workspace", "")
	covSetFlagCli5(t, backupRestoreCmd, "as-crew", "")
	covSetFlagCli5(t, backupRestoreCmd, "passphrase-file", "")
	covSetFlagCli5(t, backupRestoreCmd, "use-keyring", "false")
	covSetFlagCli5(t, backupRestoreCmd, "dry-run", "false")
	covSetFlagCli5(t, backupRestoreCmd, "replace", "false")
}

func TestBackupRestoreRunE_HappyPath(t *testing.T) {
	stub := covSetupCli5(t)
	covResetBackupRestoreFlags(t)
	stub.OnPost("/api/v1/admin/backups/restore", clitest.JSONResponse(200, map[string]any{
		"restored_ws": "acme", "restored_workspace_id": covWSCli5,
		"crews_count": 2, "rows_inserted": 150,
	}))

	var err error
	out := covCaptureAll(t, func() {
		err = backupRestoreCmd.RunE(backupRestoreCmd, []string{"/backups/ws.tar"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "Restore complete — workspace=acme crews=2 rows=150 id="+covWSCli5) {
		t.Errorf("missing restore summary; got:\n%s", out)
	}
	var body map[string]any
	clitest.MustDecodeJSONBody(stub.CallsFor("POST", "/api/v1/admin/backups/restore")[0].Body, &body)
	if body["path"] != "/backups/ws.tar" || body["dry_run"] != false {
		t.Errorf("restore body = %v", body)
	}
	// Non-interactive without --passphrase-file → empty passphrase rides along.
	if body["passphrase"] != "" {
		t.Errorf("passphrase = %v, want empty in non-interactive restore", body["passphrase"])
	}
}

func TestBackupRestoreRunE_DryRun(t *testing.T) {
	stub := covSetupCli5(t)
	covResetBackupRestoreFlags(t)
	covSetFlagCli5(t, backupRestoreCmd, "dry-run", "true")
	stub.OnPost("/api/v1/admin/backups/restore", clitest.JSONResponse(200, map[string]any{
		"restored_ws": "acme", "crews_count": 1, "rows_inserted": 10,
		"docker_phase_skipped": true,
	}))

	var err error
	out := covCaptureAll(t, func() {
		err = backupRestoreCmd.RunE(backupRestoreCmd, []string{"/b.tar"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "dry-run; no workspace/crew data changes applied") {
		t.Errorf("missing dry-run prefix; got:\n%s", out)
	}
	if strings.Contains(out, "Docker phase skipped") {
		t.Errorf("dry-run must suppress the docker-phase warning; got:\n%s", out)
	}
}

func TestBackupRestoreRunE_DockerPhaseSkippedWarning(t *testing.T) {
	stub := covSetupCli5(t)
	covResetBackupRestoreFlags(t)
	covSetFlagCli5(t, backupRestoreCmd, "as-workspace", "acme-copy")
	covSetFlagCli5(t, backupRestoreCmd, "passphrase-file", covPassphraseFile(t, "pw"))
	stub.OnPost("/api/v1/admin/backups/restore", clitest.JSONResponse(200, map[string]any{
		"restored_ws": "acme-copy", "crews_count": 1, "rows_inserted": 9,
		"docker_phase_skipped": true,
	}))

	var err error
	out := covCaptureAll(t, func() {
		err = backupRestoreCmd.RunE(backupRestoreCmd, []string{"/b.tar"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "Docker phase skipped") {
		t.Errorf("expected docker-phase warning; got:\n%s", out)
	}
	var body map[string]any
	clitest.MustDecodeJSONBody(stub.CallsFor("POST", "/api/v1/admin/backups/restore")[0].Body, &body)
	if body["as_workspace"] != "acme-copy" || body["passphrase"] != "pw" {
		t.Errorf("restore body = %v", body)
	}
}

func TestBackupRestoreRunE_APIError(t *testing.T) {
	stub := covSetupCli5(t)
	covResetBackupRestoreFlags(t)
	stub.OnPost("/api/v1/admin/backups/restore", clitest.ErrorResponse(400, "bundle is encrypted"))

	err := backupRestoreCmd.RunE(backupRestoreCmd, []string{"/b.tar"})
	if err == nil || !strings.Contains(err.Error(), "bundle is encrypted") {
		t.Errorf("expected 400 to bubble; got %v", err)
	}
}

func TestBackupRestoreRunE_NoWorkspace(t *testing.T) {
	covSetupCli5(t)
	cliCfg = &cli.CLIConfig{Token: "fake-token"}
	err := backupRestoreCmd.RunE(backupRestoreCmd, []string{"/b.tar"})
	if err == nil || !strings.Contains(err.Error(), "workspace") {
		t.Errorf("expected workspace error; got %v", err)
	}
}

// ─── backup delete ───────────────────────────────────────────────────────

func TestBackupDeleteRunE_RefusesWithoutForceNonInteractive(t *testing.T) {
	covSetupCli5(t)
	covSetFlagCli5(t, backupDeleteCmd, "force", "false")

	err := backupDeleteCmd.RunE(backupDeleteCmd, []string{"/backups/old.tar"})
	if err == nil || !strings.Contains(err.Error(), "refusing to delete") {
		t.Errorf("expected non-interactive refusal; got %v", err)
	}
}

func TestBackupDeleteRunE_Force(t *testing.T) {
	stub := covSetupCli5(t)
	covSetFlagCli5(t, backupDeleteCmd, "force", "true")
	stub.OnDelete("/api/v1/admin/backups", clitest.EmptyResponse(204))

	var err error
	out := covCaptureAll(t, func() {
		err = backupDeleteCmd.RunE(backupDeleteCmd, []string{"/backups/my bundle.tar"})
	})
	if err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !strings.Contains(out, "Backup deleted: /backups/my bundle.tar") {
		t.Errorf("missing success line; got:\n%s", out)
	}
	calls := stub.CallsFor("DELETE", "/api/v1/admin/backups")
	if len(calls) != 1 {
		t.Fatalf("expected 1 DELETE, got %d", len(calls))
	}
	// Path must be query-escaped (space → %2F-safe form handled by QueryEscape).
	if !strings.Contains(calls[0].Query, "path=%2Fbackups%2Fmy+bundle.tar") {
		t.Errorf("delete query not escaped: %q", calls[0].Query)
	}
}

func TestBackupDeleteRunE_APIError(t *testing.T) {
	stub := covSetupCli5(t)
	covSetFlagCli5(t, backupDeleteCmd, "force", "true")
	stub.OnDelete("/api/v1/admin/backups", clitest.ErrorResponse(404, "bundle not found"))

	err := backupDeleteCmd.RunE(backupDeleteCmd, []string{"/ghost.tar"})
	if err == nil || !strings.Contains(err.Error(), "bundle not found") {
		t.Errorf("expected 404; got %v", err)
	}
}

// ─── keyring round trip ──────────────────────────────────────────────────

// TestBackupKeyringRoundTrip drives the --use-keyring flow end to end:
// first create prompts (stdin fallback) and persists the passphrase in the
// HOME-scoped keyring file; restore then reads the same passphrase back
// without prompting.
func TestBackupKeyringRoundTrip(t *testing.T) {
	stub := covSetupCli5(t)
	t.Setenv("HOME", t.TempDir()) // isolate ~/.crewship/backup-keyring
	t.Setenv("ENCRYPTION_KEY", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	covResetBackupCreateFlags(t)
	covResetBackupRestoreFlags(t)
	covSetFlagCli5(t, backupCreateCmd, "use-keyring", "true")
	covSetFlagCli5(t, backupRestoreCmd, "use-keyring", "true")
	covSwapStdin(t, "kr-secret\n")
	stub.OnPost("/api/v1/admin/backups", clitest.JSONResponse(200, map[string]any{
		"path": "/b.tar", "scope": "workspace", "encrypted": true, "format_version": 2,
	}))
	stub.OnPost("/api/v1/admin/backups/restore", clitest.JSONResponse(200, map[string]any{
		"restored_ws": "acme", "crews_count": 1, "rows_inserted": 1,
	}))

	var err error
	covCaptureAll(t, func() { err = backupCreateCmd.RunE(backupCreateCmd, nil) })
	if err != nil {
		t.Fatalf("create with --use-keyring: %v", err)
	}
	var createBody map[string]any
	clitest.MustDecodeJSONBody(stub.CallsFor("POST", "/api/v1/admin/backups")[0].Body, &createBody)
	if createBody["passphrase"] != "kr-secret" {
		t.Fatalf("create passphrase = %v, want kr-secret", createBody["passphrase"])
	}

	// Restore must read the stored passphrase from the keyring — stdin is
	// already drained, so a prompt fallback would produce "".
	covCaptureAll(t, func() { err = backupRestoreCmd.RunE(backupRestoreCmd, []string{"/b.tar"}) })
	if err != nil {
		t.Fatalf("restore with --use-keyring: %v", err)
	}
	var restoreBody map[string]any
	clitest.MustDecodeJSONBody(stub.CallsFor("POST", "/api/v1/admin/backups/restore")[0].Body, &restoreBody)
	if restoreBody["passphrase"] != "kr-secret" {
		t.Errorf("restore passphrase = %v, want kr-secret from keyring", restoreBody["passphrase"])
	}
}

// TestBackupCreateRunE_KeyringSecondCreateSkipsPrompt verifies the
// fromKeyring path: a second create with --use-keyring must reuse the
// stored passphrase without consuming stdin.
func TestBackupCreateRunE_KeyringSecondCreateSkipsPrompt(t *testing.T) {
	stub := covSetupCli5(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ENCRYPTION_KEY", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	covResetBackupCreateFlags(t)
	covSetFlagCli5(t, backupCreateCmd, "use-keyring", "true")
	covSwapStdin(t, "first-secret\n")
	stub.OnPost("/api/v1/admin/backups", clitest.JSONResponse(200, map[string]any{
		"path": "/b.tar", "scope": "workspace", "encrypted": true, "format_version": 2,
	}))

	var err error
	covCaptureAll(t, func() { err = backupCreateCmd.RunE(backupCreateCmd, nil) })
	if err != nil {
		t.Fatalf("first create: %v", err)
	}
	// Stdin pipe is exhausted; second run must come from the keyring.
	covCaptureAll(t, func() { err = backupCreateCmd.RunE(backupCreateCmd, nil) })
	if err != nil {
		t.Fatalf("second create should reuse keyring: %v", err)
	}
	calls := stub.CallsFor("POST", "/api/v1/admin/backups")
	if len(calls) != 2 {
		t.Fatalf("expected 2 POSTs, got %d", len(calls))
	}
	var body map[string]any
	clitest.MustDecodeJSONBody(calls[1].Body, &body)
	if body["passphrase"] != "first-secret" {
		t.Errorf("second create passphrase = %v, want first-secret from keyring", body["passphrase"])
	}
}

// ─── remaining error branches ────────────────────────────────────────────

func TestBackupCreateRunE_UnknownCrewSlug(t *testing.T) {
	stub := covSetupCli5(t)
	covResetBackupCreateFlags(t)
	covSetFlagCli5(t, backupCreateCmd, "scope", "crew")
	covSetFlagCli5(t, backupCreateCmd, "crew", "ghost")
	covSetFlagCli5(t, backupCreateCmd, "no-encrypt", "true")
	stub.OnGet("/api/v1/crews", clitest.JSONResponse(200, []map[string]string{}))

	var err error
	covCaptureAll(t, func() { err = backupCreateCmd.RunE(backupCreateCmd, nil) })
	if err == nil || !strings.Contains(err.Error(), "crew not found") {
		t.Errorf("expected crew-not-found; got %v", err)
	}
}

func TestBackupCreateRunE_PreflightRejectsBrokenServer(t *testing.T) {
	covSetupCli5(t)
	covResetBackupCreateFlags(t)
	covSetFlagCli5(t, backupCreateCmd, "passphrase-file", covPassphraseFile(t, "pw"))
	cliCfg.Server = "http://" // host-less URL → preflight must block

	err := backupCreateCmd.RunE(backupCreateCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "missing a host") {
		t.Errorf("expected preflight host error; got %v", err)
	}
}

func TestBackupCreateRunE_TransportError(t *testing.T) {
	stub := covSetupCli5(t)
	covResetBackupCreateFlags(t)
	covSetFlagCli5(t, backupCreateCmd, "no-encrypt", "true")
	stub.Close()

	var err error
	covCaptureAll(t, func() { err = backupCreateCmd.RunE(backupCreateCmd, nil) })
	if err == nil || !strings.Contains(err.Error(), "request failed") {
		t.Errorf("expected transport error; got %v", err)
	}
}

func TestBackupCreateRunE_MalformedResponse(t *testing.T) {
	stub := covSetupCli5(t)
	covResetBackupCreateFlags(t)
	covSetFlagCli5(t, backupCreateCmd, "no-encrypt", "true")
	stub.OnPost("/api/v1/admin/backups", clitest.TextResponse(200, "not json at all"))

	var err error
	covCaptureAll(t, func() { err = backupCreateCmd.RunE(backupCreateCmd, nil) })
	if err == nil || !strings.Contains(err.Error(), "decode response") {
		t.Errorf("expected decode error; got %v", err)
	}
}

func TestBackupRestoreRunE_NoAuth(t *testing.T) {
	covSetupCli5(t)
	cliCfg = &cli.CLIConfig{}
	err := backupRestoreCmd.RunE(backupRestoreCmd, []string{"/b.tar"})
	if err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("expected not-logged-in; got %v", err)
	}
}

func TestBackupRestoreRunE_PassphraseFileMissing(t *testing.T) {
	covSetupCli5(t)
	covResetBackupRestoreFlags(t)
	covSetFlagCli5(t, backupRestoreCmd, "passphrase-file", "/definitely/not/here.txt")

	err := backupRestoreCmd.RunE(backupRestoreCmd, []string{"/b.tar"})
	if err == nil || !strings.Contains(err.Error(), "read passphrase file") {
		t.Errorf("expected passphrase-file error; got %v", err)
	}
}

func TestBackupRestoreRunE_TransportError(t *testing.T) {
	stub := covSetupCli5(t)
	covResetBackupRestoreFlags(t)
	stub.Close()

	err := backupRestoreCmd.RunE(backupRestoreCmd, []string{"/b.tar"})
	if err == nil || !strings.Contains(err.Error(), "request failed") {
		t.Errorf("expected transport error; got %v", err)
	}
}

func TestBackupRestoreRunE_MalformedResponse(t *testing.T) {
	stub := covSetupCli5(t)
	covResetBackupRestoreFlags(t)
	stub.OnPost("/api/v1/admin/backups/restore", clitest.TextResponse(200, "garbage"))

	err := backupRestoreCmd.RunE(backupRestoreCmd, []string{"/b.tar"})
	if err == nil || !strings.Contains(err.Error(), "decode response") {
		t.Errorf("expected decode error; got %v", err)
	}
}

func TestBackupDeleteRunE_AuthGates(t *testing.T) {
	covSetupCli5(t)
	cliCfg = &cli.CLIConfig{}
	err := backupDeleteCmd.RunE(backupDeleteCmd, []string{"/b.tar"})
	if err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("expected not-logged-in; got %v", err)
	}

	cliCfg = &cli.CLIConfig{Token: "fake-token"}
	err = backupDeleteCmd.RunE(backupDeleteCmd, []string{"/b.tar"})
	if err == nil || !strings.Contains(err.Error(), "workspace") {
		t.Errorf("expected workspace error; got %v", err)
	}
}

func TestBackupCreateRunE_NoWorkspace(t *testing.T) {
	covSetupCli5(t)
	cliCfg = &cli.CLIConfig{Token: "fake-token"}
	err := backupCreateCmd.RunE(backupCreateCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "workspace") {
		t.Errorf("expected workspace error; got %v", err)
	}
}

func TestBackupKeyring_OpenFailure(t *testing.T) {
	covSetupCli5(t)
	t.Setenv("HOME", "") // os.UserHomeDir fails → DefaultKeyring errors
	covResetBackupCreateFlags(t)
	covSetFlagCli5(t, backupCreateCmd, "use-keyring", "true")

	err := backupCreateCmd.RunE(backupCreateCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "open backup keyring") {
		t.Errorf("create: expected keyring-open error; got %v", err)
	}

	covResetBackupRestoreFlags(t)
	covSetFlagCli5(t, backupRestoreCmd, "use-keyring", "true")
	err = backupRestoreCmd.RunE(backupRestoreCmd, []string{"/b.tar"})
	if err == nil || !strings.Contains(err.Error(), "open backup keyring") {
		t.Errorf("restore: expected keyring-open error; got %v", err)
	}
}

// TestBackupKeyring_DecryptFailureSurfaces stores an entry under one
// ENCRYPTION_KEY then flips the key: GetPassphrase must fail with a real
// error (not ErrKeyringEntryNotFound) and both create and restore must
// abort instead of silently degrading to a prompt.
func TestBackupKeyring_DecryptFailureSurfaces(t *testing.T) {
	covSetupCli5(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ENCRYPTION_KEY", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")
	covResetBackupCreateFlags(t)
	covResetBackupRestoreFlags(t)
	covSetFlagCli5(t, backupCreateCmd, "use-keyring", "true")
	covSetFlagCli5(t, backupRestoreCmd, "use-keyring", "true")

	// Seed the keyring directly through the same library the CLI uses.
	kr, err := backupDefaultKeyringForTest(t)
	if err != nil {
		t.Fatalf("open keyring: %v", err)
	}
	if err := kr.StorePassphrase(t.Context(), covWSCli5, "secret"); err != nil {
		t.Fatalf("seed keyring: %v", err)
	}

	// Flip the master key → decrypt of the stored entry now fails.
	t.Setenv("ENCRYPTION_KEY", "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff")

	err = backupCreateCmd.RunE(backupCreateCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "read backup keyring") {
		t.Errorf("create: expected read-keyring error; got %v", err)
	}
	err = backupRestoreCmd.RunE(backupRestoreCmd, []string{"/b.tar"})
	if err == nil || !strings.Contains(err.Error(), "read backup keyring") {
		t.Errorf("restore: expected read-keyring error; got %v", err)
	}
}

// TestBackupCreateRunE_KeyringStoreWarningDoesNotAbort: prompt succeeds but
// StorePassphrase fails (no ENCRYPTION_KEY) — the CLI must warn and still
// write the backup.
func TestBackupCreateRunE_KeyringStoreWarningDoesNotAbort(t *testing.T) {
	stub := covSetupCli5(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ENCRYPTION_KEY", "") // encrypt-on-store will fail
	covResetBackupCreateFlags(t)
	covSetFlagCli5(t, backupCreateCmd, "use-keyring", "true")
	covSwapStdin(t, "warn-secret\n")
	stub.OnPost("/api/v1/admin/backups", clitest.JSONResponse(200, map[string]any{
		"path": "/b.tar", "scope": "workspace", "encrypted": true, "format_version": 2,
	}))

	var err error
	out := covCaptureAll(t, func() { err = backupCreateCmd.RunE(backupCreateCmd, nil) })
	if err != nil {
		t.Fatalf("store failure must not abort the backup: %v", err)
	}
	if !strings.Contains(out, "Failed to store passphrase in keyring") {
		t.Errorf("expected store warning; got:\n%s", out)
	}
	var body map[string]any
	clitest.MustDecodeJSONBody(stub.CallsFor("POST", "/api/v1/admin/backups")[0].Body, &body)
	if body["passphrase"] != "warn-secret" {
		t.Errorf("passphrase = %v, want warn-secret", body["passphrase"])
	}
}

func TestBackupDeleteRunE_TransportError(t *testing.T) {
	stub := covSetupCli5(t)
	covSetFlagCli5(t, backupDeleteCmd, "force", "true")
	stub.Close()

	err := backupDeleteCmd.RunE(backupDeleteCmd, []string{"/b.tar"})
	if err == nil || !strings.Contains(err.Error(), "request failed") {
		t.Errorf("expected transport error; got %v", err)
	}
}
