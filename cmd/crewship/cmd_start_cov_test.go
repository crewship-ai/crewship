//go:build !clionly

package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/config"
	"github.com/crewship-ai/crewship/internal/testutil"
)

func covLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestStartCmdFlags(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"config", "db", "no-docker"} {
		if f := startCmd.Flags().Lookup(name); f == nil {
			t.Errorf("start missing --%s flag", name)
		}
	}
	if startCmd.Use != "start" {
		t.Errorf("start Use: %q", startCmd.Use)
	}
}

func TestCfgBoltPathFromEnv(t *testing.T) {
	t.Setenv("CREWSHIP_BOLT_PATH", "")
	if cfgBoltPathFromEnv() {
		t.Error("empty env must report false")
	}
	t.Setenv("CREWSHIP_BOLT_PATH", "   ")
	if cfgBoltPathFromEnv() {
		t.Error("whitespace-only env must report false")
	}
	t.Setenv("CREWSHIP_BOLT_PATH", "/tmp/custom-state.db")
	if !cfgBoltPathFromEnv() {
		t.Error("set env must report true")
	}
}

func TestPipelineResumeEnabled(t *testing.T) {
	cases := []struct {
		value string
		want  bool
	}{
		{"", true},
		{"1", true},
		{"on", true},
		{"yes", true},
		{"0", false},
		{"false", false},
		{"OFF", false},
		{"  No  ", false},
		{"n", false},
		{"F", false},
	}
	for _, tc := range cases {
		t.Setenv("CREWSHIP_PIPELINE_RESUME", tc.value)
		if got := pipelineResumeEnabled(); got != tc.want {
			t.Errorf("CREWSHIP_PIPELINE_RESUME=%q: got %v want %v", tc.value, got, tc.want)
		}
	}
}

func TestInitProviders_SkipContainerProviders(t *testing.T) {
	for _, provider := range []string{"docker", "apple", "auto"} {
		t.Run(provider, func(t *testing.T) {
			cfg := &config.Config{}
			cfg.Container.Provider = provider

			deps, err := initProviders(context.Background(), cfg, covLogger(), true)
			if err != nil {
				t.Fatalf("initProviders: %v", err)
			}
			t.Cleanup(deps.Close)
			if deps.Container != nil {
				t.Errorf("--no-docker must leave Container nil for provider %q", provider)
			}
		})
	}
}

func TestInitProviders_StorageAndState(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.Config{}
	cfg.Container.Provider = "k8s" // unknown-but-tolerated: no warning branch
	cfg.Storage.Provider = "localfs"
	cfg.Storage.BasePath = filepath.Join(dir, "storage")
	cfg.State.Provider = "bbolt"
	cfg.State.BoltPath = filepath.Join(dir, "state.db")

	deps, err := initProviders(context.Background(), cfg, covLogger(), true)
	if err != nil {
		t.Fatalf("initProviders: %v", err)
	}
	t.Cleanup(deps.Close)

	if deps.Storage == nil {
		t.Error("localfs storage provider should be wired")
	}
	if deps.State == nil {
		t.Error("bbolt state provider should be wired")
	}
	if _, err := os.Stat(cfg.Storage.BasePath); err != nil {
		t.Errorf("localfs base dir not created: %v", err)
	}
	if _, err := os.Stat(cfg.State.BoltPath); err != nil {
		t.Errorf("bbolt file not created: %v", err)
	}
}

func TestInitProviders_UnknownProvidersTolerated(t *testing.T) {
	cfg := &config.Config{}
	cfg.Container.Provider = "warp-drive"
	cfg.Storage.Provider = "s3" // recognised in config validation but not wired here
	cfg.State.Provider = "postgres"

	deps, err := initProviders(context.Background(), cfg, covLogger(), true)
	if err != nil {
		t.Fatalf("unknown providers must not error: %v", err)
	}
	t.Cleanup(deps.Close)
	if deps.Container != nil || deps.Storage != nil || deps.State != nil {
		t.Errorf("nothing should be wired: %+v", deps)
	}
}

func TestInitProviders_LocalfsError(t *testing.T) {
	// A regular file in the way of MkdirAll forces the localfs error path.
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{}
	cfg.Storage.Provider = "localfs"
	cfg.Storage.BasePath = filepath.Join(blocker, "sub")

	if _, err := initProviders(context.Background(), cfg, covLogger(), true); err == nil {
		t.Fatal("expected init localfs provider error")
	}
}

func TestInitProviders_BboltError(t *testing.T) {
	// Pointing bbolt at a directory makes Open fail.
	cfg := &config.Config{}
	cfg.State.Provider = "bbolt"
	cfg.State.BoltPath = t.TempDir()

	if _, err := initProviders(context.Background(), cfg, covLogger(), true); err == nil {
		t.Fatal("expected init bbolt provider error")
	}
}

// TestPrintFirstRunWelcome_NonTTY pins the suppress-when-redirected
// contract: with stdout being a pipe the banner must not print and the
// DB must not be queried (the fixture has no users table, so reaching
// the query would log a warning — and printing would corrupt the pipe
// expectations of CI log scrapers, which is the documented rationale).
func TestPrintFirstRunWelcome_NonTTY(t *testing.T) {
	db := testutil.NewMemDB(t) // deliberately schema-free

	out, _ := captureStdoutCov(t, func() error {
		printFirstRunWelcome(db, covLogger())
		return nil
	})
	if out != "" {
		t.Errorf("non-TTY stdout must stay silent; got %q", out)
	}
}

// withDevNullStdoutCov swaps os.Stdout for /dev/null — a character
// device, so printFirstRunWelcome's "is stdout a TTY-ish device" gate
// passes while writes stay harmless. Restores the original at cleanup.
func withDevNullStdoutCov(t *testing.T) {
	t.Helper()
	f, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Skipf("cannot open %s: %v", os.DevNull, err)
	}
	fi, err := f.Stat()
	if err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		_ = f.Close()
		t.Skipf("%s is not a char device on this platform", os.DevNull)
	}
	orig := os.Stdout
	os.Stdout = f
	t.Cleanup(func() {
		os.Stdout = orig
		_ = f.Close()
	})
}

// covLogBuffer returns a logger writing into the returned buffer so the
// warn-path of printFirstRunWelcome is observable.
func covLogBuffer() (*slog.Logger, *strings.Builder) {
	var sb strings.Builder
	return slog.New(slog.NewTextHandler(&sb, nil)), &sb
}

func TestPrintFirstRunWelcome_DBPaths(t *testing.T) {
	t.Run("query failure logs warning", func(t *testing.T) {
		withDevNullStdoutCov(t)
		db := testutil.NewMemDB(t) // no users table → query fails
		logger, logs := covLogBuffer()

		printFirstRunWelcome(db, logger)
		if !strings.Contains(logs.String(), "count(users) failed") {
			t.Errorf("expected warn log about users count; got %q", logs.String())
		}
	})

	t.Run("existing users suppress banner", func(t *testing.T) {
		withDevNullStdoutCov(t)
		db := testutil.NewMemDBWithSchema(t, `CREATE TABLE users (id TEXT PRIMARY KEY)`)
		testutil.SeedRow(t, db, `INSERT INTO users (id) VALUES ('u1')`)
		logger, logs := covLogBuffer()

		printFirstRunWelcome(db, logger)
		if logs.String() != "" {
			t.Errorf("no logs expected for populated install; got %q", logs.String())
		}
	})

	t.Run("fresh install prints banner without error", func(t *testing.T) {
		withDevNullStdoutCov(t)
		t.Setenv("CREWSHIP_PORT", "9999")
		db := testutil.NewMemDBWithSchema(t, `CREATE TABLE users (id TEXT PRIMARY KEY)`)
		logger, logs := covLogBuffer()

		printFirstRunWelcome(db, logger) // banner goes to /dev/null
		if logs.String() != "" {
			t.Errorf("no warn logs expected on fresh install; got %q", logs.String())
		}
	})
}
