package main

// Coverage tests for cmd_backup.go (list / inspect / status RunE paths +
// the local formatting helpers). Also hosts the shared test scaffolding
// (covSetupCli8 / covSetFlagCli8 / covCaptureStdoutCli8) used by the other
// *_cov_test.go files in this package.

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
)

// covWSCli8 is a workspace id that passes both looksLikeCUID implementations
// (cmd/crewship needs >=21 chars, internal/cli needs >=20) so the client
// never tries to resolve it over the network.
const covWSCli8 = "cws0123456789abcdefghij"

// covSetupCli8 wires the package globals at a stub server and neutralises
// env vars that would override the config. NOT safe for t.Parallel —
// it mutates package-level state (cliCfg & friends).
func covSetupCli8(t *testing.T, serverURL string) {
	t.Helper()
	saveCLIState(t)
	t.Setenv("CREWSHIP_SERVER", "")
	t.Setenv("CREWSHIP_WORKSPACE", "")
	t.Setenv("CREWSHIP_DEFAULT_AGENT", "")
	flagServer = ""
	flagWorkspace = ""
	cliCfg = &cli.CLIConfig{Token: "tok-test", Workspace: covWSCli8, Server: serverURL}
}

// covSetFlagCli8 sets a cobra flag and restores its default value AND its
// Changed marker at test end (several RunE paths branch on Changed).
func covSetFlagCli8(t *testing.T, cmd *cobra.Command, name, value string) {
	t.Helper()
	fl := cmd.Flags().Lookup(name)
	if fl == nil {
		t.Fatalf("flag --%s not registered on %s", name, cmd.Name())
	}
	if err := cmd.Flags().Set(name, value); err != nil {
		t.Fatalf("set --%s=%s: %v", name, value, err)
	}
	t.Cleanup(func() {
		_ = fl.Value.Set(fl.DefValue)
		fl.Changed = false
	})
}

// covCaptureStdoutCli8 captures everything fn writes to os.Stdout.
func covCaptureStdoutCli8(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	defer func() {
		os.Stdout = old
	}()
	fn()
	_ = w.Close()
	os.Stdout = old
	return <-done
}

// covWithStdinCli8 swaps os.Stdin for a pipe carrying `input` for the
// duration of fn. The pipe write end is closed before fn runs so
// readers observe EOF after the payload.
func covWithStdinCli8(t *testing.T, input string, fn func()) {
	t.Helper()
	old := os.Stdin
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	if _, err := w.WriteString(input); err != nil {
		t.Fatalf("write stdin payload: %v", err)
	}
	_ = w.Close()
	os.Stdin = r
	defer func() {
		os.Stdin = old
		_ = r.Close()
	}()
	fn()
}

// covAbort returns a stub Handler that kills the connection mid-response
// (net/http recovers ErrAbortHandler quietly) so the CLI observes a
// transport-level error rather than an HTTP status.
func covAbort() clitest.Handler {
	return func(*http.Request, []byte) (int, []byte, string) { panic(http.ErrAbortHandler) }
}

// covNotJSON returns a 200 whose body fails JSON decoding — exercises
// the cli.ReadJSON error branches.
func covNotJSON() clitest.Handler { return clitest.TextResponse(200, "not json at all") }

// ─── helper functions ────────────────────────────────────────────────

func TestBackupFormatBytes(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{512, "512 B"},
		{2048, "2.0 KiB"},
		{5 * 1 << 20, "5.0 MiB"},
		{3 * 1 << 30, "3.0 GiB"},
		{0, "0 B"},
	}
	for _, c := range cases {
		if got := formatBytes(c.in); got != c.want {
			t.Errorf("formatBytes(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestBackupYesNo(t *testing.T) {
	if got := yesNo(true); got != "yes" {
		t.Errorf("yesNo(true) = %q", got)
	}
	if got := yesNo(false); got != "no" {
		t.Errorf("yesNo(false) = %q", got)
	}
}

func TestBackupTruncateLong(t *testing.T) {
	if got := truncateLong("short", 10); got != "short" {
		t.Errorf("truncateLong unmodified: got %q", got)
	}
	if got := truncateLong("abcdefghij", 4); got != "abcd…" {
		t.Errorf("truncateLong: got %q want %q", got, "abcd…")
	}
}

func TestBackupEncodeQuery(t *testing.T) {
	// Must escape %, = and / — the motivating bug for delegating to
	// url.QueryEscape.
	got := encodeQuery("/tmp/a b%c=d")
	if strings.ContainsAny(got, " %=/") && !strings.Contains(got, "%2F") {
		t.Errorf("encodeQuery left reserved chars unescaped: %q", got)
	}
	if got != "%2Ftmp%2Fa+b%25c%3Dd" {
		t.Errorf("encodeQuery: got %q", got)
	}
}

func TestReadPassphraseFromFile(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "pass.txt")
	if err := os.WriteFile(file, []byte("  s3cret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := readPassphrase(file, true)
	if err != nil {
		t.Fatalf("readPassphrase(file): %v", err)
	}
	if got != "s3cret" {
		t.Errorf("passphrase: got %q want %q (must be trimmed)", got, "s3cret")
	}
}

func TestReadPassphraseFileMissing(t *testing.T) {
	_, err := readPassphrase(filepath.Join(t.TempDir(), "nope.txt"), false)
	if err == nil || !strings.Contains(err.Error(), "read passphrase file") {
		t.Errorf("expected read passphrase file error; got %v", err)
	}
}

func TestReadPassphraseNonTTYStdin(t *testing.T) {
	covWithStdinCli8(t, "from-stdin\n", func() {
		got, err := readPassphrase("", false)
		if err != nil {
			t.Fatalf("readPassphrase(stdin): %v", err)
		}
		if got != "from-stdin" {
			t.Errorf("passphrase: got %q", got)
		}
	})
}

func TestReadPassphraseNonTTYStdinEmpty(t *testing.T) {
	covWithStdinCli8(t, "", func() {
		_, err := readPassphrase("", false)
		if err == nil || !strings.Contains(err.Error(), "no passphrase on stdin") {
			t.Errorf("expected 'no passphrase on stdin'; got %v", err)
		}
	})
}

// ─── backup list ─────────────────────────────────────────────────────

func TestBackupListRunE_NoAuth(t *testing.T) {
	covSetupCli8(t, "http://127.0.0.1:0")
	cliCfg = &cli.CLIConfig{}
	err := backupListCmd.RunE(backupListCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("expected not-logged-in error; got %v", err)
	}
}

func TestBackupListRunE_NoWorkspace(t *testing.T) {
	covSetupCli8(t, "http://127.0.0.1:0")
	cliCfg = &cli.CLIConfig{Token: "tok"}
	err := backupListCmd.RunE(backupListCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "workspace") {
		t.Errorf("expected workspace error; got %v", err)
	}
}

func TestBackupListRunE_Empty(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())
	stub.OnGet("/api/v1/admin/backups", clitest.JSONResponse(200, map[string]any{"data": []any{}}))

	if err := backupListCmd.RunE(backupListCmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if got := stub.CallsFor("GET", "/api/v1/admin/backups"); len(got) != 1 {
		t.Errorf("expected 1 GET /api/v1/admin/backups, got %d", len(got))
	}
}

func TestBackupListRunE_Rows(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())
	stub.OnGet("/api/v1/admin/backups", clitest.JSONResponse(200, map[string]any{
		"data": []map[string]any{{
			"path":           "/srv/backups/b1.tar.zst",
			"file_name":      "b1.tar.zst",
			"size_bytes":     2048,
			"scope":          "workspace",
			"encrypted":      true,
			"created_at":     "2026-06-01T00:00:00Z",
			"format_version": 2,
		}},
	}))

	out := covCaptureStdoutCli8(t, func() {
		if err := backupListCmd.RunE(backupListCmd, nil); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	for _, want := range []string{"b1.tar.zst", "workspace", "2.0 KiB", "yes", "v2"} {
		if !strings.Contains(out, want) {
			t.Errorf("list output missing %q:\n%s", want, out)
		}
	}
}

func TestBackupListRunE_APIError(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())
	stub.OnGet("/api/v1/admin/backups", clitest.ErrorResponse(403, "Forbidden: requires OWNER or ADMIN role"))

	err := backupListCmd.RunE(backupListCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "Forbidden") {
		t.Errorf("expected forbidden error; got %v", err)
	}
}

// ─── backup inspect ──────────────────────────────────────────────────

func TestBackupInspectRunE_HappyPath(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())
	stub.OnGet("/api/v1/admin/backups/inspect", clitest.JSONResponse(200, map[string]any{
		"scope": "crew", "format_version": 2,
	}))

	out := covCaptureStdoutCli8(t, func() {
		if err := backupInspectCmd.RunE(backupInspectCmd, []string{"/tmp/my backup.tar.zst"}); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if !strings.Contains(out, `"scope"`) || !strings.Contains(out, "crew") {
		t.Errorf("inspect output missing manifest fields:\n%s", out)
	}

	calls := stub.CallsFor("GET", "/api/v1/admin/backups/inspect")
	if len(calls) != 1 {
		t.Fatalf("expected 1 inspect call, got %d", len(calls))
	}
	// Space must be query-escaped, not raw.
	if !strings.Contains(calls[0].Query, "path=%2Ftmp%2Fmy+backup.tar.zst") {
		t.Errorf("path not query-escaped: %q", calls[0].Query)
	}
}

func TestBackupInspectRunE_NoAuth(t *testing.T) {
	covSetupCli8(t, "http://127.0.0.1:0")
	cliCfg = &cli.CLIConfig{}
	err := backupInspectCmd.RunE(backupInspectCmd, []string{"x"})
	if err == nil || !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("expected not-logged-in error; got %v", err)
	}
}

// ─── backup status ───────────────────────────────────────────────────

func TestBackupStatusRunE_NotHeld(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())
	stub.OnGet("/api/v1/admin/backups/status", clitest.JSONResponse(200, map[string]any{"held": false}))

	if err := backupStatusCmd.RunE(backupStatusCmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}
}

func TestBackupStatusRunE_Held(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())
	stub.OnGet("/api/v1/admin/backups/status", clitest.JSONResponse(200, map[string]any{
		"held":         true,
		"acquired_by":  "user-1",
		"acquired_at":  "2026-06-01T10:00:00Z",
		"expires_at":   "2026-06-01T10:15:00Z",
		"workspace_id": covWSCli8,
	}))

	out := covCaptureStdoutCli8(t, func() {
		if err := backupStatusCmd.RunE(backupStatusCmd, nil); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	for _, want := range []string{"user-1", covWSCli8, "2026-06-01T10:15:00Z"} {
		if !strings.Contains(out, want) {
			t.Errorf("status output missing %q:\n%s", want, out)
		}
	}
}

// TestBackupRunE_ErrorBranches sweeps the remaining transport / decode /
// no-workspace branches of list / inspect / status.
func TestBackupRunE_ErrorBranches(t *testing.T) {
	type tc struct {
		name  string
		cmd   *cobra.Command
		args  []string
		route func(*clitest.StubServer)
		noWS  bool
	}
	cases := []tc{
		{"inspect no workspace", backupInspectCmd, []string{"x"}, nil, true},
		{"status no workspace", backupStatusCmd, nil, nil, true},
		{"list transport", backupListCmd, nil, func(s *clitest.StubServer) {
			s.OnGet("/api/v1/admin/backups", covAbort())
		}, false},
		{"list decode", backupListCmd, nil, func(s *clitest.StubServer) {
			s.OnGet("/api/v1/admin/backups", covNotJSON())
		}, false},
		{"inspect transport", backupInspectCmd, []string{"x"}, func(s *clitest.StubServer) {
			s.OnGet("/api/v1/admin/backups/inspect", covAbort())
		}, false},
		{"inspect api error", backupInspectCmd, []string{"x"}, func(s *clitest.StubServer) {
			s.OnGet("/api/v1/admin/backups/inspect", clitest.ErrorResponse(404, "no such bundle"))
		}, false},
		{"inspect decode", backupInspectCmd, []string{"x"}, func(s *clitest.StubServer) {
			s.OnGet("/api/v1/admin/backups/inspect", covNotJSON())
		}, false},
		{"status transport", backupStatusCmd, nil, func(s *clitest.StubServer) {
			s.OnGet("/api/v1/admin/backups/status", covAbort())
		}, false},
		{"status decode", backupStatusCmd, nil, func(s *clitest.StubServer) {
			s.OnGet("/api/v1/admin/backups/status", covNotJSON())
		}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			stub := clitest.NewStubServer()
			defer stub.Close()
			covSetupCli8(t, stub.URL())
			if c.noWS {
				cliCfg = &cli.CLIConfig{Token: "tok", Server: stub.URL()}
			}
			if c.route != nil {
				c.route(stub)
			}
			if err := c.cmd.RunE(c.cmd, c.args); err == nil {
				t.Errorf("%s: expected error, got nil", c.name)
			}
		})
	}
}

func TestBackupStatusRunE_APIError(t *testing.T) {
	stub := clitest.NewStubServer()
	defer stub.Close()
	covSetupCli8(t, stub.URL())
	stub.OnGet("/api/v1/admin/backups/status", clitest.ErrorResponse(500, "Internal server error"))

	err := backupStatusCmd.RunE(backupStatusCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "Internal server error") {
		t.Errorf("expected 500 error; got %v", err)
	}
}
