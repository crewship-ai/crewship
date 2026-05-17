package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

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
