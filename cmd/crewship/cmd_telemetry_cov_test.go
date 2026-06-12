package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/crashreport"
)

func TestDsnEndpointHostCov(t *testing.T) {
	cases := []struct {
		name string
		dsn  string
		want string
	}{
		{"full dsn", "https://abc123@o450.ingest.sentry.io/123456", "o450.ingest.sentry.io"},
		{"no project path", "https://abc123@sentry.example.com", "sentry.example.com"},
		{"no at sign", "https://sentry.example.com/1", "unknown"},
		{"empty", "", "unknown"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := dsnEndpointHost(tc.dsn); got != tc.want {
				t.Errorf("dsnEndpointHost(%q) = %q, want %q", tc.dsn, got, tc.want)
			}
		})
	}
}

// TestSetTelemetry_RoundTrip drives setTelemetry against a throwaway
// data dir (CREWSHIP_DATA_DIR override) — migrations run on the fresh
// SQLite file, then the consent flag is written and read back via
// crashreport.Status, proving the on/off path actually persists.
func TestSetTelemetry_RoundTrip(t *testing.T) {
	t.Setenv("CREWSHIP_DATA_DIR", t.TempDir())
	t.Setenv("CREWSHIP_SENTRY_DSN", "https://key@host.example/1")
	ctx := context.Background()

	// Enable — prints success + endpoint host.
	stderr, err := captureStderrCov(t, func() error {
		_, e := captureStdoutCovCli10(t, func() error {
			return setTelemetry(ctx, true)
		})
		return e
	})
	if err != nil {
		t.Fatalf("setTelemetry(true): %v", err)
	}
	if !strings.Contains(stderr, "Telemetry enabled") {
		t.Errorf("enable message missing: %q", stderr)
	}

	// State must persist in the same data dir.
	db, err := openLocalDB(ctx)
	if err != nil {
		t.Fatalf("openLocalDB: %v", err)
	}
	enabled, asked, installID, err := crashreport.Status(ctx, db.DB)
	db.Close()
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !enabled || !asked {
		t.Errorf("after enable: enabled=%v asked=%v, want true/true", enabled, asked)
	}
	if installID == "" {
		t.Error("install id should be generated on opt-in")
	}

	// Disable flips the flag.
	stderr, err = captureStderrCov(t, func() error {
		return setTelemetry(ctx, false)
	})
	if err != nil {
		t.Fatalf("setTelemetry(false): %v", err)
	}
	if !strings.Contains(stderr, "Telemetry disabled") {
		t.Errorf("disable message missing: %q", stderr)
	}
	db2, err := openLocalDB(ctx)
	if err != nil {
		t.Fatalf("openLocalDB: %v", err)
	}
	enabled, _, _, err = crashreport.Status(ctx, db2.DB)
	db2.Close()
	if err != nil {
		t.Fatalf("Status after disable: %v", err)
	}
	if enabled {
		t.Error("telemetry should be disabled after setTelemetry(false)")
	}
}

// TestTelemetryStatusRunE covers the status subcommand's three display
// branches against the same temp data dir.
func TestTelemetryStatusRunE(t *testing.T) {
	t.Setenv("CREWSHIP_DATA_DIR", t.TempDir())
	t.Setenv("CREWSHIP_SENTRY_DSN", "https://key@host.example/1")
	ctx := context.Background()
	// RunE consults cmd.Context(), which is nil unless Execute() ran.
	telemetryStatusCmd.SetContext(ctx)

	// Fresh DB → "not yet configured" branch.
	out, err := captureStdoutCovCli10(t, func() error {
		return telemetryStatusCmd.RunE(telemetryStatusCmd, nil)
	})
	if err != nil {
		t.Fatalf("status (unconfigured): %v", err)
	}
	if !strings.Contains(out, "not yet configured") {
		t.Errorf("unconfigured branch missing: %q", out)
	}

	// Opt in → ENABLED branch with endpoint host + env-override source.
	if err := setTelemetry(ctx, true); err != nil {
		t.Fatalf("setTelemetry: %v", err)
	}
	out, err = captureStdoutCovCli10(t, func() error {
		return telemetryStatusCmd.RunE(telemetryStatusCmd, nil)
	})
	if err != nil {
		t.Fatalf("status (enabled): %v", err)
	}
	if !strings.Contains(out, "Telemetry: ENABLED") {
		t.Errorf("enabled branch missing: %q", out)
	}
	if !strings.Contains(out, "host.example") || !strings.Contains(out, "CREWSHIP_SENTRY_DSN env override") {
		t.Errorf("endpoint/source line missing: %q", out)
	}
	if !strings.Contains(out, "install_id:") {
		t.Errorf("install id missing: %q", out)
	}

	// Opt out → DISABLED branch.
	if err := setTelemetry(ctx, false); err != nil {
		t.Fatalf("setTelemetry off: %v", err)
	}
	out, err = captureStdoutCovCli10(t, func() error {
		return telemetryStatusCmd.RunE(telemetryStatusCmd, nil)
	})
	if err != nil {
		t.Fatalf("status (disabled): %v", err)
	}
	if !strings.Contains(out, "Telemetry: DISABLED") {
		t.Errorf("disabled branch missing: %q", out)
	}
}

// TestOpenLocalDB_UnwritableDataDir drives the openLocalDB error path:
// CREWSHIP_DATA_DIR points below a read-only directory so the data-dir
// bootstrap (MkdirAll) fails. Both setTelemetry and the status RunE
// must surface that instead of panicking.
func TestOpenLocalDB_UnwritableDataDir(t *testing.T) {
	parent := t.TempDir()
	if err := os.Chmod(parent, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(parent, 0o700) })
	t.Setenv("CREWSHIP_DATA_DIR", filepath.Join(parent, "nested", "data"))

	if _, err := openLocalDB(context.Background()); err == nil {
		t.Error("openLocalDB: expected error for unwritable data dir")
	}
	if err := setTelemetry(context.Background(), true); err == nil {
		t.Error("setTelemetry: expected error for unwritable data dir")
	}
	telemetryStatusCmd.SetContext(context.Background())
	if err := telemetryStatusCmd.RunE(telemetryStatusCmd, nil); err == nil {
		t.Error("status: expected error for unwritable data dir")
	}
}

// TestTelemetryStatusRunE_StableBuildUnconfiguredMessage flips the
// build version to a stable tag so the "DISABLED until you opt in"
// variant of the unconfigured branch renders.
func TestTelemetryStatusRunE_StableBuildUnconfiguredMessage(t *testing.T) {
	t.Setenv("CREWSHIP_DATA_DIR", t.TempDir())
	origVersion := version
	version = "1.2.3"
	t.Cleanup(func() { version = origVersion })
	telemetryStatusCmd.SetContext(context.Background())

	out, err := captureStdoutCovCli10(t, func() error {
		return telemetryStatusCmd.RunE(telemetryStatusCmd, nil)
	})
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !strings.Contains(out, "stable build keeps telemetry DISABLED") {
		t.Errorf("stable-build message missing: %q", out)
	}
}

// TestTelemetryStatusRunE_EnabledWithoutDSNWarns covers the enabled
// branch when no DSN is available (test builds compile none in and the
// env override is empty) — consent recorded, nothing would be sent.
func TestTelemetryStatusRunE_EnabledWithoutDSNWarns(t *testing.T) {
	t.Setenv("CREWSHIP_DATA_DIR", t.TempDir())
	t.Setenv("CREWSHIP_SENTRY_DSN", "")
	ctx := context.Background()
	telemetryStatusCmd.SetContext(ctx)
	if _, err := captureStderrCov(t, func() error {
		return setTelemetry(ctx, true)
	}); err != nil {
		t.Fatalf("setTelemetry: %v", err)
	}

	var stderr string
	stdout, err := captureStdoutCovCli10(t, func() error {
		var runErr error
		stderr, runErr = captureStderrCov(t, func() error {
			return telemetryStatusCmd.RunE(telemetryStatusCmd, nil)
		})
		return runErr
	})
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !strings.Contains(stdout, "Telemetry: ENABLED") {
		t.Errorf("enabled banner missing: %q", stdout)
	}
	if !strings.Contains(stderr, "no events are sent") {
		t.Errorf("no-DSN warning missing: %q", stderr)
	}
}

// TestTelemetryOnOffCmds drives the `on` / `off` RunE wrappers.
func TestTelemetryOnOffCmds(t *testing.T) {
	t.Setenv("CREWSHIP_DATA_DIR", t.TempDir())
	t.Setenv("CREWSHIP_SENTRY_DSN", "")
	telemetryOnCmd.SetContext(context.Background())
	telemetryOffCmd.SetContext(context.Background())
	stderr, err := captureStderrCov(t, func() error {
		_, e := captureStdoutCovCli10(t, func() error {
			return telemetryOnCmd.RunE(telemetryOnCmd, nil)
		})
		return e
	})
	if err != nil {
		t.Fatalf("telemetry on: %v", err)
	}
	// No DSN compiled into test builds and env empty → warning branch.
	if !strings.Contains(stderr, "Telemetry enabled") {
		t.Errorf("enable message missing: %q", stderr)
	}
	if err := telemetryOffCmd.RunE(telemetryOffCmd, nil); err != nil {
		t.Fatalf("telemetry off: %v", err)
	}
}
