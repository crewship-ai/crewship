//go:build !clionly

package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/crashreport"
	"github.com/crewship-ai/crewship/internal/database"
)

// tempDataDir points CREWSHIP_DATA_DIR at a fresh temp dir and returns
// the resolved DataDir (the override also creates the structure).
func tempDataDir(t *testing.T) *database.DataDir {
	t.Helper()
	t.Setenv("CREWSHIP_DATA_DIR", t.TempDir())
	dd, err := database.DefaultDataDir()
	if err != nil {
		t.Fatalf("DefaultDataDir: %v", err)
	}
	return dd
}

// brokenDataDir makes database.DefaultDataDir fail by nesting the
// override under a regular file.
func brokenDataDir(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CREWSHIP_DATA_DIR", filepath.Join(blocker, "sub"))
}

func TestCheckResultPrint_AllStatuses(t *testing.T) {
	out := captureStdoutCovCli2(t, func() {
		checkResult{name: "alpha", status: "PASS", detail: "ok"}.print()
		checkResult{name: "beta", status: "WARN", detail: "meh", hint: "try harder"}.print()
		checkResult{name: "gamma", status: "FAIL", detail: "broken"}.print()
		checkResult{name: "delta", status: "INFO", detail: "fyi"}.print()
		checkResult{name: "epsilon", status: "????", detail: "odd"}.print()
	})
	for _, want := range []string{"[PASS", "[WARN", "[FAIL", "[INFO", "alpha", "broken", "try harder", "odd"} {
		if !strings.Contains(out, want) {
			t.Errorf("print output missing %q:\n%s", want, out)
		}
	}
}

func TestCheckContainerRuntime_FailsFastWithoutRuntimes(t *testing.T) {
	// Force both probes to fail deterministically: a dead DOCKER_HOST
	// socket and a PATH without the apple `container` CLI.
	t.Setenv("DOCKER_HOST", "unix:///nonexistent/crewship-doctor-test.sock")
	t.Setenv("PATH", "/nonexistent")

	r := checkContainerRuntime(context.Background())
	if r.name != "container runtime" {
		t.Errorf("name = %q", r.name)
	}
	if r.status != "FAIL" {
		t.Fatalf("status = %q, want FAIL (detail=%q)", r.status, r.detail)
	}
	if !strings.Contains(r.detail, "no Docker-compatible runtime") {
		t.Errorf("detail = %q", r.detail)
	}
	if !strings.Contains(r.hint, "docker") {
		t.Errorf("hint = %q", r.hint)
	}
}

func TestCheckDataDir_PassAndResolveError(t *testing.T) {
	dd := tempDataDir(t)
	r := checkDataDir(false)
	if r.status != "PASS" || r.detail != dd.Root {
		t.Errorf("got %+v, want PASS at %s", r, dd.Root)
	}

	brokenDataDir(t)
	r2 := checkDataDir(false)
	if r2.status != "FAIL" {
		t.Errorf("broken dir: got %+v", r2)
	}
}

func TestCheckDataDirWritable(t *testing.T) {
	dd := tempDataDir(t)
	if r := checkDataDirWritable(); r.status != "PASS" || r.detail != "touch test ok" {
		t.Errorf("writable dir: got %+v", r)
	}

	// Read-only data dir → CreateTemp fails → FAIL with chmod hint.
	if err := os.Chmod(dd.Root, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dd.Root, 0o700) })
	r := checkDataDirWritable()
	if r.status != "FAIL" || !strings.Contains(r.detail, "create test file failed") {
		t.Errorf("read-only dir: got %+v", r)
	}

	brokenDataDir(t)
	if r := checkDataDirWritable(); r.status != "FAIL" {
		t.Errorf("broken dir: got %+v", r)
	}
}

func TestCheckDBMigrationVersion(t *testing.T) {
	dd := tempDataDir(t)
	ctx := context.Background()

	// No DB file yet.
	if r := checkDBMigrationVersion(ctx); r.status != "WARN" || !strings.Contains(r.detail, "does not exist") {
		t.Errorf("missing db: got %+v", r)
	}

	// DB exists but has no _migrations table.
	db, err := sql.Open("sqlite", dd.DatabasePath())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE placeholder (x INTEGER)"); err != nil {
		t.Fatal(err)
	}
	if r := checkDBMigrationVersion(ctx); r.status != "WARN" || !strings.Contains(r.detail, "_migrations") {
		t.Errorf("no migrations table: got %+v", r)
	}

	if _, err := db.Exec("CREATE TABLE _migrations (version INTEGER)"); err != nil {
		t.Fatal(err)
	}
	setVersion := func(v int) {
		t.Helper()
		if _, err := db.Exec("DELETE FROM _migrations"); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec("INSERT INTO _migrations (version) VALUES (?)", v); err != nil {
			t.Fatal(err)
		}
	}

	setVersion(85) // expectedLatest in cmd_doctor.go
	if r := checkDBMigrationVersion(ctx); r.status != "PASS" || !strings.Contains(r.detail, "v85 (latest)") {
		t.Errorf("latest: got %+v", r)
	}
	setVersion(99)
	if r := checkDBMigrationVersion(ctx); r.status != "WARN" || !strings.Contains(r.detail, "newer than CLI knows about") {
		t.Errorf("newer: got %+v", r)
	}
	setVersion(3)
	r := checkDBMigrationVersion(ctx)
	if r.status != "WARN" || !strings.Contains(r.detail, "v3 (CLI expects v85)") {
		t.Errorf("older: got %+v", r)
	}
	if !strings.Contains(r.hint, "pending migrations") {
		t.Errorf("older hint: got %+v", r)
	}
	db.Close()

	brokenDataDir(t)
	if r := checkDBMigrationVersion(ctx); r.status != "FAIL" {
		t.Errorf("broken dir: got %+v", r)
	}
}

func TestCheckSidecarBinary(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	r := checkSidecarBinary()
	if r.status != "WARN" || !strings.Contains(r.detail, "not found on disk") {
		t.Errorf("absent: got %+v", r)
	}
	if !strings.Contains(r.hint, filepath.Join(home, ".crewship", "bin", "crewship-sidecar")) {
		t.Errorf("hint must list searched paths: %q", r.hint)
	}

	binDir := filepath.Join(home, ".crewship", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sidecar := filepath.Join(binDir, "crewship-sidecar")
	if err := os.WriteFile(sidecar, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	r2 := checkSidecarBinary()
	if r2.status != "PASS" || r2.detail != sidecar {
		t.Errorf("present: got %+v", r2)
	}
}

func TestCheckNextAuthSecret(t *testing.T) {
	long := strings.Repeat("s", 40)

	t.Run("env long", func(t *testing.T) {
		t.Setenv("NEXTAUTH_SECRET", long)
		r := checkNextAuthSecret()
		if r.status != "PASS" || !strings.Contains(r.detail, "env-provided (40 chars)") {
			t.Errorf("got %+v", r)
		}
	})
	t.Run("env short", func(t *testing.T) {
		t.Setenv("NEXTAUTH_SECRET", "short")
		r := checkNextAuthSecret()
		if r.status != "WARN" || !strings.Contains(r.detail, "short (5 chars)") {
			t.Errorf("got %+v", r)
		}
	})
	t.Run("not bootstrapped", func(t *testing.T) {
		t.Setenv("NEXTAUTH_SECRET", "")
		tempDataDir(t)
		r := checkNextAuthSecret()
		if r.status != "INFO" || r.detail != "not yet bootstrapped" {
			t.Errorf("got %+v", r)
		}
	})
	t.Run("persisted valid", func(t *testing.T) {
		t.Setenv("NEXTAUTH_SECRET", "")
		dd := tempDataDir(t)
		path := filepath.Join(dd.Root, "secrets.env")
		if err := os.WriteFile(path, []byte("NEXTAUTH_SECRET="+long+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		r := checkNextAuthSecret()
		if r.status != "PASS" || !strings.Contains(r.detail, "auto-managed in "+path) {
			t.Errorf("got %+v", r)
		}
	})
	t.Run("persisted missing key", func(t *testing.T) {
		t.Setenv("NEXTAUTH_SECRET", "")
		dd := tempDataDir(t)
		if err := os.WriteFile(filepath.Join(dd.Root, "secrets.env"), []byte("OTHER=x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		r := checkNextAuthSecret()
		if r.status != "WARN" || !strings.Contains(r.detail, "entry is missing") {
			t.Errorf("got %+v", r)
		}
	})
	t.Run("persisted too short", func(t *testing.T) {
		t.Setenv("NEXTAUTH_SECRET", "")
		dd := tempDataDir(t)
		if err := os.WriteFile(filepath.Join(dd.Root, "secrets.env"), []byte("NEXTAUTH_SECRET=tiny\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		r := checkNextAuthSecret()
		if r.status != "WARN" || !strings.Contains(r.detail, "persisted value invalid") {
			t.Errorf("got %+v", r)
		}
	})
	t.Run("data dir resolution error", func(t *testing.T) {
		t.Setenv("NEXTAUTH_SECRET", "")
		brokenDataDir(t)
		r := checkNextAuthSecret()
		if r.status != "WARN" || !strings.Contains(r.detail, "could not locate data dir") {
			t.Errorf("got %+v", r)
		}
	})
}

func TestCheckServerReachable(t *testing.T) {
	saveCLIState(t)
	t.Setenv("CREWSHIP_SERVER", "")
	flagServer = ""
	ctx := context.Background()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer srv.Close()

	cliCfg = &cli.CLIConfig{Server: srv.URL}
	if r := checkServerReachable(ctx); r.status != "PASS" || !strings.HasPrefix(r.detail, "TCP ") {
		t.Errorf("up server: got %+v", r)
	}

	cliCfg = &cli.CLIConfig{Server: "://bad"}
	if r := checkServerReachable(ctx); r.status != "FAIL" || !strings.Contains(r.detail, "invalid server URL") {
		t.Errorf("bad url: got %+v", r)
	}

	cliCfg = &cli.CLIConfig{Server: "http://"}
	if r := checkServerReachable(ctx); r.status != "FAIL" || !strings.Contains(r.detail, "could not parse host") {
		t.Errorf("empty host: got %+v", r)
	}

	// Grab a port that was just released — dialing it must fail.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	cliCfg = &cli.CLIConfig{Server: "http://" + addr}
	if r := checkServerReachable(ctx); r.status != "FAIL" || !strings.Contains(r.detail, "dial") {
		t.Errorf("closed port: got %+v", r)
	}
}

func TestRunCheckTelemetryWrappers_FreshDataDir(t *testing.T) {
	tempDataDir(t)
	t.Setenv("CREWSHIP_SENTRY_DSN", "")
	ctx := context.Background()

	// openLocalDB migrates the fresh DB; consent is unset → "not asked".
	r := runCheckTelemetryStatus(ctx)
	if r.status != "WARN" || !strings.Contains(r.detail, "telemetry not yet configured") {
		t.Errorf("telemetry status: got %+v", r)
	}
	r2 := runCheckSentryDSNWiring(ctx)
	if r2.status != "INFO" || !strings.Contains(r2.detail, "not yet configured") {
		t.Errorf("dsn wiring: got %+v", r2)
	}
	r3 := runCheckDsnReachability(ctx)
	if r3.status != "INFO" || !strings.Contains(r3.detail, "skipped") {
		t.Errorf("dsn reachability: got %+v", r3)
	}
}

func TestRunCheckTelemetryWrappers_BrokenDataDir(t *testing.T) {
	brokenDataDir(t)
	ctx := context.Background()

	if r := runCheckTelemetryStatus(ctx); r.status != "WARN" || !strings.Contains(r.detail, "could not open local DB") {
		t.Errorf("telemetry status: got %+v", r)
	}
	if r := runCheckSentryDSNWiring(ctx); r.status != "INFO" || !strings.Contains(r.detail, "skipped") {
		t.Errorf("dsn wiring: got %+v", r)
	}
	if r := runCheckDsnReachability(ctx); r.status != "INFO" || !strings.Contains(r.detail, "skipped (local DB unavailable)") {
		t.Errorf("dsn reachability: got %+v", r)
	}
}

func TestCheckDsnReachability_EnabledBranches(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	if _, _, err := crashreport.SetOptIn(ctx, db.DB, true); err != nil {
		t.Fatalf("SetOptIn: %v", err)
	}

	// DSN without an @ → host parse failure.
	r := checkDsnReachability(ctx, db.DB, "https://no-at-sign.example/1")
	if r.status != "WARN" || !strings.Contains(r.detail, "could not parse DSN host") {
		t.Errorf("unparseable: got %+v", r)
	}

	// Loopback host: either nothing listens on :443 (WARN dial) or some
	// local proxy answers (PASS). Both are real outcomes of the probe;
	// assert the row stays consistent with whichever happened.
	r2 := checkDsnReachability(ctx, db.DB, "https://key@127.0.0.1/42")
	switch r2.status {
	case "WARN":
		if !strings.Contains(r2.detail, "dial 127.0.0.1:443") {
			t.Errorf("dial WARN detail = %q", r2.detail)
		}
	case "PASS":
		if r2.detail != "TCP 127.0.0.1:443 ok" {
			t.Errorf("dial PASS detail = %q", r2.detail)
		}
	default:
		t.Errorf("unexpected status %+v", r2)
	}
}

func TestRunCheckDataDirPerms_Wrapper(t *testing.T) {
	dd := tempDataDir(t)

	// DefaultDataDir creates the root 0755 → WARN drift.
	r := runCheckDataDirPerms()
	if r.status != "WARN" || !strings.Contains(r.detail, "want 0700") {
		t.Errorf("0755 root: got %+v", r)
	}

	if err := os.Chmod(dd.Root, 0o700); err != nil {
		t.Fatal(err)
	}
	r2 := runCheckDataDirPerms()
	if r2.status != "PASS" || !strings.Contains(r2.detail, "db file not yet created") {
		t.Errorf("0700 no db: got %+v", r2)
	}

	if err := os.WriteFile(dd.DatabasePath(), []byte("db"), 0o644); err != nil {
		t.Fatal(err)
	}
	r3 := runCheckDataDirPerms()
	if r3.status != "WARN" || !strings.Contains(r3.detail, "want 0600") {
		t.Errorf("0644 db: got %+v", r3)
	}

	if err := os.Chmod(dd.DatabasePath(), 0o600); err != nil {
		t.Fatal(err)
	}
	r4 := runCheckDataDirPerms()
	if r4.status != "PASS" || !strings.Contains(r4.detail, "dir 0700, db file 0600") {
		t.Errorf("all good: got %+v", r4)
	}

	brokenDataDir(t)
	if r := runCheckDataDirPerms(); r.status != "FAIL" {
		t.Errorf("broken dir: got %+v", r)
	}
}

func TestRunCheckUpdateAvailable_DevBuildSkips(t *testing.T) {
	// The test binary's version global is "dev" → no network call.
	if version != "dev" {
		t.Skipf("version = %q, expected dev in test builds", version)
	}
	r := runCheckUpdateAvailable(context.Background())
	if r.status != "INFO" || r.detail != "skipped (development build)" {
		t.Errorf("got %+v", r)
	}
}

func TestEmitDoctorJSON_WriteError(t *testing.T) {
	t.Parallel()
	err := emitDoctorJSON(failWriter{}, []checkResult{{name: "x", status: "PASS"}}, 0, 0)
	if err == nil || !strings.Contains(err.Error(), "emit doctor JSON") {
		t.Errorf("got %v", err)
	}
}

func TestDoctorRunE_JSONBattery(t *testing.T) {
	// Deterministic environment: dead docker socket, no apple CLI on
	// PATH, fresh data dir, long env secret, stub server, no DSN.
	tempDataDir(t)
	t.Setenv("DOCKER_HOST", "unix:///nonexistent/crewship-doctor-test.sock")
	t.Setenv("PATH", "/nonexistent")
	t.Setenv("NEXTAUTH_SECRET", strings.Repeat("s", 40))
	t.Setenv("CREWSHIP_SENTRY_DSN", "")
	t.Setenv("CREWSHIP_CONFIG", filepath.Join(t.TempDir(), "cli-config.yaml"))
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CREWSHIP_SERVER", "")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	saveCLIState(t)
	flagServer = ""
	cliCfg = &cli.CLIConfig{Server: srv.URL}

	buf := new(bytes.Buffer)
	doctorCmd.SetOut(buf)
	t.Cleanup(func() {
		doctorCmd.SetOut(nil)
		_ = doctorCmd.Flags().Set("json", "false")
	})
	if err := doctorCmd.Flags().Set("json", "true"); err != nil {
		t.Fatal(err)
	}

	if err := doctorCmd.RunE(doctorCmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}

	var payload struct {
		Checks []struct {
			Name   string `json:"name"`
			Status string `json:"status"`
		} `json:"checks"`
		Failed  int    `json:"failed"`
		Warned  int    `json:"warned"`
		Version string `json:"version"`
		OS      string `json:"os"`
	}
	if err := json.Unmarshal(buf.Bytes(), &payload); err != nil {
		t.Fatalf("parse doctor JSON: %v\n%s", err, buf.String())
	}
	if payload.Version != version || payload.OS != runtime.GOOS {
		t.Errorf("version/os = %q/%q", payload.Version, payload.OS)
	}
	if len(payload.Checks) < 12 {
		t.Fatalf("checks = %d, want the full battery (≥12)", len(payload.Checks))
	}
	byName := map[string]string{}
	failed, warned := 0, 0
	for _, c := range payload.Checks {
		byName[c.Name] = c.Status
		switch c.Status {
		case "FAIL":
			failed++
		case "WARN":
			warned++
		}
	}
	if failed != payload.Failed || warned != payload.Warned {
		t.Errorf("counters failed=%d/%d warned=%d/%d", payload.Failed, failed, payload.Warned, warned)
	}
	if byName["container runtime"] != "FAIL" {
		t.Errorf("container runtime = %q (env was rigged to fail)", byName["container runtime"])
	}
	if byName["server reachable"] != "PASS" {
		t.Errorf("server reachable = %q against live stub", byName["server reachable"])
	}
	if byName["NEXTAUTH_SECRET"] != "PASS" {
		t.Errorf("NEXTAUTH_SECRET = %q with a 40-char env value", byName["NEXTAUTH_SECRET"])
	}
}

func TestDoctorRunE_HumanOutputFooter(t *testing.T) {
	tempDataDir(t)
	t.Setenv("DOCKER_HOST", "unix:///nonexistent/crewship-doctor-test.sock")
	t.Setenv("PATH", "/nonexistent")
	t.Setenv("NEXTAUTH_SECRET", strings.Repeat("s", 40))
	t.Setenv("CREWSHIP_SENTRY_DSN", "")
	t.Setenv("CREWSHIP_CONFIG", filepath.Join(t.TempDir(), "cli-config.yaml"))
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CREWSHIP_SERVER", "")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	saveCLIState(t)
	flagServer = ""
	cliCfg = &cli.CLIConfig{Server: srv.URL}
	t.Cleanup(func() { _ = doctorCmd.Flags().Set("json", "false") })
	_ = doctorCmd.Flags().Set("json", "false")

	out := captureStdoutCovCli2(t, func() {
		if err := doctorCmd.RunE(doctorCmd, nil); err != nil {
			t.Errorf("RunE: %v", err)
		}
	})
	if !strings.Contains(out, "Crewship doctor") {
		t.Errorf("banner missing:\n%s", out)
	}
	// Container runtime is rigged to FAIL → red footer with the docs link.
	if !strings.Contains(out, "fix the FAILs and re-run") || !strings.Contains(out, "docs.crewship.ai/troubleshooting") {
		t.Errorf("FAIL footer missing:\n%s", out)
	}
}

func TestCheckDataDirPerms_StatErrorBranches(t *testing.T) {
	// Root missing entirely → WARN skip.
	r := checkDataDirPerms(filepath.Join(t.TempDir(), "never-created"), "irrelevant")
	if r.status != "WARN" || !strings.Contains(r.detail, "does not exist") {
		t.Errorf("missing root: got %+v", r)
	}

	// Root stat fails with EACCES (parent has no search permission).
	blocked := filepath.Join(t.TempDir(), "blocked")
	if err := os.MkdirAll(filepath.Join(blocked, "inner"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(blocked, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(blocked, 0o755) })
	r2 := checkDataDirPerms(filepath.Join(blocked, "inner"), "irrelevant")
	if r2.status != "FAIL" {
		t.Errorf("unreadable root: got %+v", r2)
	}

	// Good root, db stat fails with EACCES.
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	blocked2 := filepath.Join(t.TempDir(), "blocked2")
	if err := os.MkdirAll(blocked2, 0o755); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(blocked2, "crewship.db")
	if err := os.WriteFile(dbPath, []byte("db"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(blocked2, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(blocked2, 0o755) })
	r3 := checkDataDirPerms(root, dbPath)
	if r3.status != "FAIL" {
		t.Errorf("unreadable db: got %+v", r3)
	}
}

func TestCheckServerReachable_DefaultPortAppended(t *testing.T) {
	saveCLIState(t)
	t.Setenv("CREWSHIP_SERVER", "")
	flagServer = ""

	// URL without a port → the prober must dial the scheme default. The
	// dial outcome depends on whether anything local answers on :443,
	// so assert the port-defaulting itself via the detail string.
	cliCfg = &cli.CLIConfig{Server: "https://127.0.0.1"}
	r := checkServerReachable(context.Background())
	if !strings.Contains(r.detail, "127.0.0.1:443") {
		t.Errorf("expected :443 default in detail, got %+v", r)
	}
	if r.status != "PASS" && r.status != "FAIL" {
		t.Errorf("unexpected status %+v", r)
	}
}

func TestCheckServerReachable_HTTPDefaultPort(t *testing.T) {
	saveCLIState(t)
	t.Setenv("CREWSHIP_SERVER", "")
	flagServer = ""

	cliCfg = &cli.CLIConfig{Server: "http://127.0.0.1"}
	r := checkServerReachable(context.Background())
	if !strings.Contains(r.detail, "127.0.0.1:80") {
		t.Errorf("expected :80 default in detail, got %+v", r)
	}
	if r.status != "PASS" && r.status != "FAIL" {
		t.Errorf("unexpected status %+v", r)
	}
}

func TestTelemetryChecks_ClosedDBSurfaceReadErrors(t *testing.T) {
	db := openTestDB(t)
	sqlDB := db.DB
	db.Close() // force "read consent" errors
	ctx := context.Background()

	r := checkTelemetryStatus(ctx, sqlDB, "")
	if r.status != "WARN" || !strings.Contains(r.detail, "read consent") {
		t.Errorf("telemetry status: got %+v", r)
	}
	r2 := checkSentryDSNWiring(ctx, sqlDB, "")
	if r2.status != "INFO" || !strings.Contains(r2.detail, "could not read consent") {
		t.Errorf("dsn wiring: got %+v", r2)
	}
	r3 := checkDsnReachability(ctx, sqlDB, "")
	if r3.status != "INFO" || !strings.Contains(r3.detail, "skipped (telemetry off)") {
		t.Errorf("dsn reachability: got %+v", r3)
	}
}
