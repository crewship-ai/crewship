//go:build !clionly

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/cli/clitest"
	"github.com/crewship-ai/crewship/internal/crashreport"
	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/update"
)

// openTestDB stands up a fully-migrated SQLite database in t.TempDir().
// We deliberately use database.Open + database.Migrate (rather than a raw
// sql.Open) so the app_settings table exists — the doctor checks read
// telemetry consent through crashreport.Status, which queries that table.
func openTestDB(t *testing.T) *database.DB {
	t.Helper()
	dir := t.TempDir()
	db, err := database.Open("file:" + filepath.Join(dir, "doctor.db"))
	if err != nil {
		t.Fatalf("database.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := database.Migrate(context.Background(), db.DB, logger); err != nil {
		t.Fatalf("database.Migrate: %v", err)
	}
	return db
}

func TestCheckTelemetryStatus(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()

	t.Run("not asked → WARN", func(t *testing.T) {
		// Fresh DB has no row in app_settings for telemetry_opt_in.
		got := checkTelemetryStatus(ctx, db.DB, "https://k@sentry.io/1")
		if got.status != "WARN" {
			t.Errorf("status = %q, want WARN; detail=%q", got.status, got.detail)
		}
		if !strings.Contains(got.detail, "not yet configured") {
			t.Errorf("detail mismatch: %q", got.detail)
		}
	})

	t.Run("enabled with DSN → PASS", func(t *testing.T) {
		if _, _, err := crashreport.SetOptIn(ctx, db.DB, true); err != nil {
			t.Fatalf("SetOptIn(true): %v", err)
		}
		got := checkTelemetryStatus(ctx, db.DB, "https://k@sentry.example.com/1")
		if got.status != "PASS" {
			t.Errorf("status = %q, want PASS; detail=%q", got.status, got.detail)
		}
		if !strings.Contains(got.detail, "sentry.example.com") {
			t.Errorf("expected endpoint host in detail, got %q", got.detail)
		}
	})

	t.Run("enabled without DSN → WARN", func(t *testing.T) {
		// Explicit setup: subtests must be order-independent so that
		// running a single subtest with `-run` produces the same result
		// as the full suite. CodeRabbit flagged the implicit "still
		// opted-in from the prior subtest" coupling.
		if _, _, err := crashreport.SetOptIn(ctx, db.DB, true); err != nil {
			t.Fatalf("SetOptIn(true): %v", err)
		}
		got := checkTelemetryStatus(ctx, db.DB, "")
		if got.status != "WARN" {
			t.Errorf("status = %q, want WARN; detail=%q", got.status, got.detail)
		}
	})

	t.Run("opted out → PASS", func(t *testing.T) {
		if _, _, err := crashreport.SetOptIn(ctx, db.DB, false); err != nil {
			t.Fatalf("SetOptIn(false): %v", err)
		}
		got := checkTelemetryStatus(ctx, db.DB, "https://k@sentry.io/1")
		if got.status != "PASS" || !strings.Contains(got.detail, "disabled") {
			t.Errorf("got %+v, want PASS + 'disabled'", got)
		}
	})
}

func TestCheckSentryDSNWiring(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()

	t.Run("not asked → INFO with hint", func(t *testing.T) {
		// Fresh DB: app_settings has no row for telemetry_opt_in. We still
		// want to nudge the operator toward wiring a DSN now so the next
		// `crewship start` (which flips consent on by beta default) doesn't
		// silently route crashes to /dev/null.
		got := checkSentryDSNWiring(ctx, db.DB, "")
		if got.status != "INFO" {
			t.Errorf("status = %q, want INFO; detail=%q", got.status, got.detail)
		}
		if got.hint == "" {
			t.Errorf("expected DSN wiring hint, got empty")
		}
		if !strings.Contains(got.hint, "CREWSHIP_SENTRY_DSN") {
			t.Errorf("hint missing env var name: %q", got.hint)
		}
	})

	t.Run("enabled + DSN set → PASS", func(t *testing.T) {
		if _, _, err := crashreport.SetOptIn(ctx, db.DB, true); err != nil {
			t.Fatalf("SetOptIn(true): %v", err)
		}
		got := checkSentryDSNWiring(ctx, db.DB, "https://k@sentry.example.com/42")
		if got.status != "PASS" {
			t.Errorf("status = %q, want PASS; detail=%q", got.status, got.detail)
		}
		if !strings.Contains(got.detail, "sentry.example.com") {
			t.Errorf("expected endpoint host in detail, got %q", got.detail)
		}
	})

	t.Run("enabled + DSN empty → WARN (the gap we're closing)", func(t *testing.T) {
		// Re-set consent explicitly so the subtest is order-independent
		// (same pattern CodeRabbit enforced on the neighbouring tests).
		if _, _, err := crashreport.SetOptIn(ctx, db.DB, true); err != nil {
			t.Fatalf("SetOptIn(true): %v", err)
		}
		got := checkSentryDSNWiring(ctx, db.DB, "")
		if got.status != "WARN" {
			t.Errorf("status = %q, want WARN; detail=%q", got.status, got.detail)
		}
		if !strings.Contains(got.detail, "enabled") || !strings.Contains(got.detail, "DSN") {
			t.Errorf("WARN detail should mention enabled + DSN: %q", got.detail)
		}
		if !strings.Contains(got.hint, "CREWSHIP_SENTRY_DSN") {
			t.Errorf("hint missing env var name: %q", got.hint)
		}
		if !strings.Contains(got.hint, "-X") {
			t.Errorf("hint should mention ldflag rebuild path: %q", got.hint)
		}
	})

	t.Run("opted out → INFO, no hint, no warn", func(t *testing.T) {
		if _, _, err := crashreport.SetOptIn(ctx, db.DB, false); err != nil {
			t.Fatalf("SetOptIn(false): %v", err)
		}
		got := checkSentryDSNWiring(ctx, db.DB, "")
		if got.status != "INFO" {
			t.Errorf("status = %q, want INFO; detail=%q", got.status, got.detail)
		}
		if got.hint != "" {
			// Don't second-guess the operator's opt-out by hinting at DSN
			// setup — they already declined.
			t.Errorf("opted-out path should not push a hint, got %q", got.hint)
		}
	})
}

func TestCheckDsnReachability(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	ctx := context.Background()

	t.Run("telemetry off → INFO skipped", func(t *testing.T) {
		// Default state on a fresh DB: not asked = skip.
		got := checkDsnReachability(ctx, db.DB, "https://k@example.invalid/1")
		if got.status != "INFO" {
			t.Errorf("status = %q, want INFO; detail=%q", got.status, got.detail)
		}
	})

	t.Run("opted in + DSN unreachable → WARN", func(t *testing.T) {
		// The check dials host:443; we point it at a DNS name guaranteed
		// not to resolve so the dialer fails fast. WARN (not FAIL) because
		// Sentry being unreachable is not a Crewship health signal —
		// it's an external service outage. CodeRabbit flagged the prior
		// version: subtest title said "reachable → PASS" but the body
		// asserted the unreachable path. Renamed to match what it
		// actually tests, and removed the leftover net.Listen setup that
		// the comment explained but the code never used.
		if _, _, err := crashreport.SetOptIn(ctx, db.DB, true); err != nil {
			t.Fatalf("SetOptIn: %v", err)
		}
		got := checkDsnReachability(ctx, db.DB, "https://k@127.0.0.1.unreachable.invalid/1")
		if got.status != "WARN" {
			t.Errorf("status = %q, want WARN for unreachable host; detail=%q", got.status, got.detail)
		}
	})

	t.Run("opted in + empty DSN → INFO", func(t *testing.T) {
		// Same isolation rationale as TestCheckTelemetryStatus subtests:
		// don't rely on prior subtest leaving opt-in=true.
		if _, _, err := crashreport.SetOptIn(ctx, db.DB, true); err != nil {
			t.Fatalf("SetOptIn(true): %v", err)
		}
		got := checkDsnReachability(ctx, db.DB, "")
		if got.status != "INFO" {
			t.Errorf("status = %q, want INFO; detail=%q", got.status, got.detail)
		}
	})
}

func TestCheckDataDirPerms(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("POSIX perm bits don't apply on Windows")
	}

	t.Run("happy path: 0700 dir + 0600 file → PASS", func(t *testing.T) {
		root := t.TempDir()
		// t.TempDir defaults to 0700 already, but make it explicit so the
		// assertion is self-documenting.
		if err := os.Chmod(root, 0o700); err != nil {
			t.Fatalf("chmod dir: %v", err)
		}
		dbPath := filepath.Join(root, "crewship.db")
		if err := os.WriteFile(dbPath, []byte("sqlite-stub"), 0o600); err != nil {
			t.Fatalf("write db file: %v", err)
		}
		got := checkDataDirPerms(root, dbPath)
		if got.status != "PASS" {
			t.Errorf("status = %q, want PASS; detail=%q hint=%q", got.status, got.detail, got.hint)
		}
	})

	t.Run("dir mode loosened → WARN", func(t *testing.T) {
		root := t.TempDir()
		if err := os.Chmod(root, 0o755); err != nil {
			t.Fatalf("chmod dir: %v", err)
		}
		dbPath := filepath.Join(root, "crewship.db")
		if err := os.WriteFile(dbPath, []byte("x"), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
		got := checkDataDirPerms(root, dbPath)
		if got.status != "WARN" {
			t.Errorf("status = %q, want WARN", got.status)
		}
		if !strings.Contains(got.detail, "0700") {
			t.Errorf("expected '0700' in detail, got %q", got.detail)
		}
		// Restore for cleanup
		_ = os.Chmod(root, 0o700)
	})

	t.Run("db file mode loosened → WARN", func(t *testing.T) {
		root := t.TempDir()
		if err := os.Chmod(root, 0o700); err != nil {
			t.Fatalf("chmod dir: %v", err)
		}
		dbPath := filepath.Join(root, "crewship.db")
		if err := os.WriteFile(dbPath, []byte("x"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		got := checkDataDirPerms(root, dbPath)
		if got.status != "WARN" {
			t.Errorf("status = %q, want WARN", got.status)
		}
		if !strings.Contains(got.detail, "0600") {
			t.Errorf("expected '0600' in detail, got %q", got.detail)
		}
	})

	t.Run("db file missing → PASS (dir only)", func(t *testing.T) {
		root := t.TempDir()
		if err := os.Chmod(root, 0o700); err != nil {
			t.Fatalf("chmod: %v", err)
		}
		got := checkDataDirPerms(root, filepath.Join(root, "crewship.db"))
		if got.status != "PASS" {
			t.Errorf("status = %q, want PASS", got.status)
		}
	})
}

func TestCheckUpdateAvailable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("dev build → INFO skipped", func(t *testing.T) {
		got := checkUpdateAvailable(ctx, "dev", func(context.Context, string) (*update.Result, error) {
			t.Fatal("update.Check must not be called for dev builds")
			return nil, nil
		})
		if got.status != "INFO" {
			t.Errorf("status = %q, want INFO", got.status)
		}
	})

	t.Run("newer available → WARN with hint", func(t *testing.T) {
		got := checkUpdateAvailable(ctx, "v0.1.0-beta.1", func(context.Context, string) (*update.Result, error) {
			return &update.Result{Current: "v0.1.0-beta.1", Latest: "v0.1.0", Newer: true}, nil
		})
		if got.status != "WARN" {
			t.Errorf("status = %q, want WARN", got.status)
		}
		if !strings.Contains(got.detail, "v0.1.0") || !strings.Contains(got.detail, "v0.1.0-beta.1") {
			t.Errorf("detail missing current/latest: %q", got.detail)
		}
		if got.hint == "" {
			t.Errorf("expected upgrade hint, got empty")
		}
	})

	t.Run("at latest → PASS", func(t *testing.T) {
		got := checkUpdateAvailable(ctx, "v0.1.0", func(context.Context, string) (*update.Result, error) {
			return &update.Result{Current: "v0.1.0", Latest: "v0.1.0", Newer: false}, nil
		})
		if got.status != "PASS" {
			t.Errorf("status = %q, want PASS", got.status)
		}
	})

	t.Run("network failure → INFO (not WARN)", func(t *testing.T) {
		got := checkUpdateAvailable(ctx, "v0.1.0", func(context.Context, string) (*update.Result, error) {
			return nil, errors.New("dial tcp: lookup api.github.com: no such host")
		})
		if got.status != "INFO" {
			t.Errorf("status = %q, want INFO (network failures are not health signals)", got.status)
		}
	})

	t.Run("nil result, nil err → INFO skipped", func(t *testing.T) {
		got := checkUpdateAvailable(ctx, "v0.1.0", func(context.Context, string) (*update.Result, error) {
			return nil, nil
		})
		if got.status != "INFO" {
			t.Errorf("status = %q, want INFO", got.status)
		}
	})
}

func TestDoctorCmdStructure(t *testing.T) {
	t.Parallel()
	if doctorCmd.Use != "doctor" {
		t.Errorf("doctorCmd.Use = %q, want 'doctor'", doctorCmd.Use)
	}
	if fix := doctorCmd.Flags().Lookup("fix"); fix == nil {
		t.Errorf("doctorCmd missing --fix flag")
	}
}

// installHintForOS must give each supported OS its own actionable install
// pointer — the generic get-docker URL is only acceptable as the fallback
// for platforms we don't specifically know about.
func TestInstallHintForOS(t *testing.T) {
	tests := []struct {
		goos string
		want string
	}{
		{"darwin", "orbstack"},
		{"darwin", "mac-install"},
		{"linux", "get.docker.com"},
		{"linux", "systemctl enable --now docker"},
		{"windows", "windows-install"},
		{"windows", "WSL 2"},
		{"freebsd", "https://docs.docker.com/get-docker/"},
	}
	for _, tt := range tests {
		t.Run(tt.goos+"_"+tt.want, func(t *testing.T) {
			got := installHintForOS(tt.goos)
			if !strings.Contains(got, tt.want) {
				t.Errorf("installHintForOS(%q) = %q, want it to contain %q", tt.goos, got, tt.want)
			}
		})
	}
}

// ─── exit-code contract ───────────────────────────────────────────────
//
// docs/cli/doctor.mdx documents exit 1 whenever a check FAILs and calls
// it a hard gate. Until #1305 the RunE returned nil unconditionally, so
// the documented gate was a no-op and every CI consumer that "asserted"
// on the exit code was asserting nothing. These tests pin the contract
// on both output paths.

func TestDoctorExitErr(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		failed   int
		wantErr  bool
		wantCode int
	}{
		{name: "clean run exits 0", failed: 0, wantErr: false, wantCode: cli.ExitOK},
		{name: "one FAIL exits 1", failed: 1, wantErr: true, wantCode: cli.ExitGeneric},
		{name: "many FAILs still exit 1", failed: 7, wantErr: true, wantCode: cli.ExitGeneric},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := doctorExitErr(tt.failed)
			if (err != nil) != tt.wantErr {
				t.Fatalf("doctorExitErr(%d) = %v, wantErr=%v", tt.failed, err, tt.wantErr)
			}
			if got := cli.ExitCodeFor(err); got != tt.wantCode {
				t.Errorf("ExitCodeFor = %d, want %d", got, tt.wantCode)
			}
			if err != nil && !strings.Contains(err.Error(), "FAIL") {
				t.Errorf("error text should point at the FAIL rows, got %q", err.Error())
			}
		})
	}
}

// TestFinishDoctor covers the render+gate split for both formats: the
// JSON payload must be written in FULL before the non-zero exit is
// signalled (a CI job pipes stdout into jq and would otherwise get an
// empty document on the exact runs it most needs to inspect).
func TestFinishDoctor(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		results []checkResult
		jsonOut bool
		wantErr bool
	}{
		{
			name:    "json with FAIL emits payload and gates",
			results: []checkResult{{name: "container runtime", status: "FAIL", detail: "none"}},
			jsonOut: true,
			wantErr: true,
		},
		{
			name:    "json without FAIL emits payload and passes",
			results: []checkResult{{name: "a", status: "WARN", detail: "meh"}},
			jsonOut: true,
			wantErr: false,
		},
		{
			name:    "table with FAIL gates",
			results: []checkResult{{name: "a", status: "FAIL", detail: "boom"}},
			wantErr: true,
		},
		{
			name:    "table all green passes",
			results: []checkResult{{name: "a", status: "PASS", detail: "ok"}},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buf := &bytes.Buffer{}
			var err error
			out := captureStdoutCovCli2(t, func() {
				err = finishDoctor(buf, tt.results, tt.jsonOut)
			})
			if (err != nil) != tt.wantErr {
				t.Fatalf("finishDoctor err = %v, wantErr=%v", err, tt.wantErr)
			}
			if tt.jsonOut {
				var payload doctorJSON
				if decErr := json.Unmarshal(buf.Bytes(), &payload); decErr != nil {
					t.Fatalf("payload is not valid JSON even on the gated path: %v (raw=%q)", decErr, buf.String())
				}
				if len(payload.Checks) != len(tt.results) {
					t.Errorf("payload has %d checks, want %d", len(payload.Checks), len(tt.results))
				}
				if out != "" {
					t.Errorf("json mode must not print the human table, got %q", out)
				}
				return
			}
			if !strings.Contains(out, tt.results[0].name) {
				t.Errorf("human table missing the check row: %q", out)
			}
		})
	}
}

func TestCountDoctorStatuses(t *testing.T) {
	t.Parallel()
	failed, warned := countDoctorStatuses([]checkResult{
		{status: "PASS"}, {status: "WARN"}, {status: "FAIL"},
		{status: "INFO"}, {status: "FAIL"}, {status: "WARN"},
	})
	if failed != 2 || warned != 2 {
		t.Errorf("failed=%d warned=%d, want 2/2 (INFO must not count as either)", failed, warned)
	}
}

// ─── port binding ─────────────────────────────────────────────────────

func TestDoctorBindTarget(t *testing.T) {
	tests := []struct {
		name     string
		port     string
		host     string
		wantPort int
		wantHost string
	}{
		{name: "defaults match internal/config", wantPort: 8080, wantHost: "0.0.0.0"},
		{name: "CREWSHIP_PORT wins", port: "9090", wantPort: 9090, wantHost: "0.0.0.0"},
		{name: "invalid port falls back to the default", port: "not-a-port", wantPort: 8080, wantHost: "0.0.0.0"},
		{name: "out-of-range port falls back", port: "70000", wantPort: 8080, wantHost: "0.0.0.0"},
		{name: "CREWSHIP_HOST honored", host: "127.0.0.1", wantPort: 8080, wantHost: "127.0.0.1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("CREWSHIP_PORT", tt.port)
			t.Setenv("CREWSHIP_HOST", tt.host)
			host, port := doctorBindTarget()
			if port != tt.wantPort || host != tt.wantHost {
				t.Errorf("doctorBindTarget() = %s:%d, want %s:%d", host, port, tt.wantHost, tt.wantPort)
			}
		})
	}
}

func TestCheckPortBinding(t *testing.T) {
	// Occupy a real port so the "in use" branches assert against the
	// same syscall the server would hit, not a mocked error.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	busyPort := ln.Addr().(*net.TCPAddr).Port

	// A port that was bound and released is (near-certainly) free again.
	free, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	freePort := free.Addr().(*net.TCPAddr).Port
	_ = free.Close()

	noOwner := func(int) bool { return false }
	isOwner := func(int) bool { return true }

	tests := []struct {
		name       string
		server     string
		port       int
		owns       func(int) bool
		wantStatus string
		wantDetail string
		wantHint   string
	}{
		{
			name:       "free port passes",
			server:     "http://localhost:8080",
			port:       freePort,
			owns:       noOwner,
			wantStatus: "PASS",
			wantDetail: "bindable",
		},
		{
			name:       "occupied by a stranger fails with the lsof remediation",
			server:     "http://localhost:8080",
			port:       busyPort,
			owns:       noOwner,
			wantStatus: "FAIL",
			wantDetail: "in use",
			wantHint:   "lsof -i :",
		},
		{
			name:       "occupied by our own crewshipd is not a failure",
			server:     "http://localhost:8080",
			port:       busyPort,
			owns:       isOwner,
			wantStatus: "PASS",
			wantDetail: "crewshipd",
		},
		{
			name:       "remote server skips the local bind test",
			server:     "https://crewship.example.com",
			port:       busyPort,
			owns:       noOwner,
			wantStatus: "INFO",
			wantDetail: "remote",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := checkPortBinding(tt.server, "127.0.0.1", tt.port, tt.owns)
			if got.status != tt.wantStatus {
				t.Fatalf("status = %q, want %q (detail=%q)", got.status, tt.wantStatus, got.detail)
			}
			if !strings.Contains(got.detail, tt.wantDetail) {
				t.Errorf("detail %q does not contain %q", got.detail, tt.wantDetail)
			}
			if tt.wantHint != "" && !strings.Contains(got.hint, tt.wantHint) {
				t.Errorf("hint %q does not contain %q", got.hint, tt.wantHint)
			}
		})
	}
}

// ─── sidecar hash ─────────────────────────────────────────────────────

func TestSidecarHashVerdict(t *testing.T) {
	t.Parallel()
	const path = "/opt/crewship/crewship-sidecar"
	tests := []struct {
		name       string
		onDisk     string
		expected   string
		wantStatus string
		wantDetail string
		wantHint   string
	}{
		{
			name:       "no build-time hash injected: presence only",
			onDisk:     "aaaaaaaaaaaa",
			expected:   "",
			wantStatus: "PASS",
			wantDetail: path,
		},
		{
			name:       "hashes match",
			onDisk:     "aaaaaaaaaaaa",
			expected:   "aaaaaaaaaaaa",
			wantStatus: "PASS",
			wantDetail: "aaaaaaaaaaaa",
		},
		{
			name:       "stale artifact warns with the rebuild remediation",
			onDisk:     "aaaaaaaaaaaa",
			expected:   "bbbbbbbbbbbb",
			wantStatus: "WARN",
			wantDetail: "stale",
			wantHint:   "build:sidecar",
		},
		{
			name:       "unreadable binary does not raise a false alarm",
			onDisk:     "",
			expected:   "bbbbbbbbbbbb",
			wantStatus: "PASS",
			wantDetail: path,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sidecarHashVerdict(path, tt.onDisk, tt.expected)
			if got.status != tt.wantStatus {
				t.Fatalf("status = %q, want %q (detail=%q)", got.status, tt.wantStatus, got.detail)
			}
			if !strings.Contains(got.detail, tt.wantDetail) {
				t.Errorf("detail %q does not contain %q", got.detail, tt.wantDetail)
			}
			if tt.wantHint != "" && !strings.Contains(got.hint, tt.wantHint) {
				t.Errorf("hint %q does not contain %q", got.hint, tt.wantHint)
			}
		})
	}
}

// ─── CLI ↔ server version parity ──────────────────────────────────────

func TestServerVersionVerdict(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		cliVersion string
		srvVersion string
		wantStatus string
		wantDetail string
		wantHint   string
	}{
		{
			name:       "match",
			cliVersion: "v0.1.0",
			srvVersion: "v0.1.0",
			wantStatus: "PASS",
			wantDetail: "v0.1.0",
		},
		{
			name:       "mismatch warns and points at self-update",
			cliVersion: "v0.1.0",
			srvVersion: "v0.2.0",
			wantStatus: "WARN",
			wantDetail: "v0.2.0",
			wantHint:   "crewship self-update",
		},
		{
			name:       "v-prefix difference is not a mismatch",
			cliVersion: "v0.1.0",
			srvVersion: "0.1.0",
			wantStatus: "PASS",
		},
		{
			name:       "dev CLI build skips",
			cliVersion: "dev",
			srvVersion: "v0.1.0",
			wantStatus: "INFO",
			wantDetail: "development build",
		},
		{
			name:       "server did not report a version",
			cliVersion: "v0.1.0",
			srvVersion: "",
			wantStatus: "INFO",
			wantDetail: "skipped",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := serverVersionVerdict(tt.cliVersion, tt.srvVersion)
			if got.status != tt.wantStatus {
				t.Fatalf("status = %q, want %q (detail=%q)", got.status, tt.wantStatus, got.detail)
			}
			if tt.wantDetail != "" && !strings.Contains(got.detail, tt.wantDetail) {
				t.Errorf("detail %q does not contain %q", got.detail, tt.wantDetail)
			}
			if tt.wantHint != "" && !strings.Contains(got.hint, tt.wantHint) {
				t.Errorf("hint %q does not contain %q", got.hint, tt.wantHint)
			}
		})
	}
}

// checkServerVersionMatch must never double-report a dead daemon: the
// dedicated "server reachable" check owns that failure, so every
// transport/auth condition here degrades to INFO.
func TestCheckServerVersionMatch_ServerConditions(t *testing.T) {
	tests := []struct {
		name       string
		handler    clitest.Handler
		wantStatus string
		wantDetail string
	}{
		{
			name:       "matching version passes",
			handler:    clitest.JSONResponse(200, map[string]any{"current": "v9.9.9"}),
			wantStatus: "PASS",
			wantDetail: "v9.9.9",
		},
		{
			name:       "unauthenticated skips",
			handler:    clitest.ErrorResponse(401, "unauthorized"),
			wantStatus: "INFO",
			wantDetail: "skipped",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stub := covStub(t)
			stub.OnGet("/api/v1/system/version", tt.handler)
			got := checkServerVersionMatch(context.Background(), newAPIClient(), "v9.9.9")
			if got.status != tt.wantStatus {
				t.Fatalf("status = %q, want %q (detail=%q)", got.status, tt.wantStatus, got.detail)
			}
			if !strings.Contains(got.detail, tt.wantDetail) {
				t.Errorf("detail %q does not contain %q", got.detail, tt.wantDetail)
			}
		})
	}
}

// ─── config source attribution ────────────────────────────────────────

func TestServerSourceLabel(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		source  cli.ServerSource
		profile string
		want    string
	}{
		{name: "flag", source: cli.ServerSourceFlag, want: "--server flag"},
		{name: "profile names itself", source: cli.ServerSourceProfile, profile: "dev2", want: `profile "dev2"`},
		{name: "env", source: cli.ServerSourceEnv, want: "CREWSHIP_SERVER"},
		{name: "config file", source: cli.ServerSourceConfig, want: "config file"},
		{name: "default", source: cli.ServerSourceDefault, want: "default"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := serverSourceLabel(tt.source, tt.profile); !strings.Contains(got, tt.want) {
				t.Errorf("serverSourceLabel(%q, %q) = %q, want it to contain %q", tt.source, tt.profile, got, tt.want)
			}
		})
	}
}

// The attribution helper must agree with EffectiveServer on the URL for
// every precedence layer — a source string that describes a different
// resolution than the one the CLI actually dials is worse than none.
func TestEffectiveServerWithSource(t *testing.T) {
	tests := []struct {
		name       string
		flagServer string
		flagProf   string
		env        string
		cfg        *cli.CLIConfig
		wantURL    string
		wantSource cli.ServerSource
	}{
		{
			name:       "flag wins",
			flagServer: "http://flag.example",
			env:        "http://env.example",
			cfg:        &cli.CLIConfig{Server: "http://cfg.example"},
			wantURL:    "http://flag.example",
			wantSource: cli.ServerSourceFlag,
		},
		{
			name:       "profile beats env",
			flagProf:   "dev2",
			env:        "http://env.example",
			cfg:        &cli.CLIConfig{Servers: map[string]*cli.ServerProfile{"dev2": {Server: "http://dev2.example"}}},
			wantURL:    "http://dev2.example",
			wantSource: cli.ServerSourceProfile,
		},
		{
			name:       "selected profile without a server fails closed",
			flagProf:   "empty",
			env:        "http://env.example",
			cfg:        &cli.CLIConfig{Servers: map[string]*cli.ServerProfile{"empty": {}}},
			wantURL:    "",
			wantSource: cli.ServerSourceProfile,
		},
		{
			name:       "env beats config file",
			env:        "http://env.example",
			cfg:        &cli.CLIConfig{Server: "http://cfg.example"},
			wantURL:    "http://env.example",
			wantSource: cli.ServerSourceEnv,
		},
		{
			name:       "config file beats the built-in default",
			cfg:        &cli.CLIConfig{Server: "http://cfg.example"},
			wantURL:    "http://cfg.example",
			wantSource: cli.ServerSourceConfig,
		},
		{
			name:       "nothing configured falls back to the default",
			cfg:        &cli.CLIConfig{},
			wantURL:    "http://localhost:8080",
			wantSource: cli.ServerSourceDefault,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("CREWSHIP_SERVER", tt.env)
			t.Setenv("CREWSHIP_PROFILE", "")
			url, src := cli.EffectiveServerWithSource(tt.flagServer, tt.flagProf, tt.cfg)
			if url != tt.wantURL {
				t.Errorf("url = %q, want %q", url, tt.wantURL)
			}
			if src != tt.wantSource {
				t.Errorf("source = %q, want %q", src, tt.wantSource)
			}
			if plain := cli.EffectiveServer(tt.flagServer, tt.flagProf, tt.cfg); plain != url {
				t.Errorf("attribution helper disagrees with EffectiveServer: %q vs %q", url, plain)
			}
		})
	}
}

// ─── provider selection parity with `crewship start` ──────────────────

func TestContainerProviderSetting(t *testing.T) {
	tests := []struct {
		name string
		env  string
		want string
	}{
		{name: "default matches internal/config", want: "docker"},
		{name: "env override", env: "auto", want: "auto"},
		{name: "whitespace and case are normalised", env: "  Apple ", want: "apple"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("CREWSHIP_CONTAINER_PROVIDER", tt.env)
			if got := containerProviderSetting(); got != tt.want {
				t.Errorf("containerProviderSetting() = %q, want %q", got, tt.want)
			}
		})
	}
}

// The verdict table encodes the selection `crewship start` performs in
// initProviders (cmd_start.go): explicit docker/apple, and auto trying
// APPLE FIRST. Doctor previously hand-rolled docker-then-apple, so it
// could report a provider start would never pick.
func TestContainerRuntimeVerdict(t *testing.T) {
	t.Parallel()
	const dockerDesc = "docker 27.0 (/var/run/docker.sock)"
	const appleDesc = "apple 0.4 (host_ip=192.168.64.1)"
	tests := []struct {
		name       string
		provider   string
		docker     string
		apple      string
		installed  []string
		wantStatus string
		wantDetail []string
	}{
		{
			name:       "docker configured, docker present",
			provider:   "docker",
			docker:     dockerDesc,
			wantStatus: "PASS",
			wantDetail: []string{"docker 27.0", "provider=docker"},
		},
		{
			name:       "docker configured, only apple present → FAIL (start would not use apple)",
			provider:   "docker",
			apple:      appleDesc,
			wantStatus: "FAIL",
			wantDetail: []string{"provider=docker"},
		},
		{
			name:       "apple configured and present",
			provider:   "apple",
			apple:      appleDesc,
			docker:     dockerDesc,
			wantStatus: "PASS",
			wantDetail: []string{"apple 0.4", "provider=apple"},
		},
		{
			name:       "auto prefers apple over docker (matches initProviders)",
			provider:   "auto",
			docker:     dockerDesc,
			apple:      appleDesc,
			wantStatus: "PASS",
			wantDetail: []string{"apple 0.4", "provider=auto"},
		},
		{
			name:       "auto falls back to docker",
			provider:   "auto",
			docker:     dockerDesc,
			wantStatus: "PASS",
			wantDetail: []string{"docker 27.0"},
		},
		{
			name:       "auto with nothing running but something installed",
			provider:   "auto",
			installed:  []string{"Docker Desktop"},
			wantStatus: "FAIL",
			wantDetail: []string{"installed but not running"},
		},
		{
			name:       "auto with nothing installed",
			provider:   "auto",
			wantStatus: "FAIL",
			wantDetail: []string{"no container runtime installed"},
		},
		{
			name:       "unknown provider is a configuration FAIL",
			provider:   "k8s",
			docker:     dockerDesc,
			wantStatus: "FAIL",
			wantDetail: []string{"k8s"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := containerRuntimeVerdict(tt.provider, tt.docker, tt.apple, tt.installed, "start it")
			if got.status != tt.wantStatus {
				t.Fatalf("status = %q, want %q (detail=%q)", got.status, tt.wantStatus, got.detail)
			}
			for _, want := range tt.wantDetail {
				if !strings.Contains(got.detail, want) {
					t.Errorf("detail %q does not contain %q", got.detail, want)
				}
			}
		})
	}
}

// On macOS the Apple Containers row must appear even when Docker
// answered first — before #1305 doctor was silent about Apple whenever
// Docker was up, hiding the alternative provider entirely.
func TestAppleContainersVerdict(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		desc       string
		wantStatus string
		wantDetail string
	}{
		{name: "available", desc: "apple 0.4 (host_ip=192.168.64.1)", wantStatus: "PASS", wantDetail: "apple 0.4"},
		{name: "unavailable is informational, never a failure", desc: "", wantStatus: "INFO", wantDetail: "not available"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := appleContainersVerdict(tt.desc)
			if got.status != tt.wantStatus {
				t.Fatalf("status = %q, want %q", got.status, tt.wantStatus)
			}
			if !strings.Contains(got.detail, tt.wantDetail) {
				t.Errorf("detail %q does not contain %q", got.detail, tt.wantDetail)
			}
		})
	}
}

// The command's Long text and the port-binding remediation must keep
// quoting the same env var the resolver reads, or the hint sends
// operators to a knob that does nothing.
func TestDoctorLongDocumentsPortKnobs(t *testing.T) {
	t.Parallel()
	for _, want := range []string{"CREWSHIP_PORT", "CREWSHIP_HOST", "Exit code"} {
		if !strings.Contains(doctorCmd.Long, want) {
			t.Errorf("doctor Long missing %q — contract drift", want)
		}
	}
}

// doctor is a PRE-flight tool, so "the local daemon isn't up yet" must not trip
// the exit-1 gate — otherwise `crewship doctor && crewship start`, the sequence
// the docs recommend, could never succeed. A remote target still FAILs.
func TestUnreachableServerVerdict(t *testing.T) {
	dialErr := errors.New("connection refused")

	tests := []struct {
		name       string
		host       string
		wantStatus string
	}{
		{"loopback name warns", "localhost:8080", "WARN"},
		{"loopback v4 warns", "127.0.0.1:8080", "WARN"},
		{"loopback v6 warns", "[::1]:8080", "WARN"},
		{"remote host fails", "dev2.example.com:8080", "FAIL"},
		{"remote ip fails", "10.0.0.5:8080", "FAIL"},
		{"hostless value falls back to whole string", "localhost", "WARN"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := unreachableServerVerdict(tc.host, "config", dialErr)
			if got.status != tc.wantStatus {
				t.Errorf("status = %q, want %q (detail %q)", got.status, tc.wantStatus, got.detail)
			}
			if got.name != "server reachable" {
				t.Errorf("name = %q, want %q", got.name, "server reachable")
			}
			if got.hint == "" {
				t.Error("an unreachable server must carry a remediation hint")
			}
		})
	}
}
