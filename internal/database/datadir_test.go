package database

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewDataDir_CreatesAllSubdirs(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	target := filepath.Join(root, "crewship-data")

	d, err := NewDataDir(target)
	if err != nil {
		t.Fatalf("NewDataDir: %v", err)
	}
	if d.Root != target {
		t.Errorf("Root = %q, want %q", d.Root, target)
	}

	for _, sub := range []string{"output", "chats", "logs", "skills"} {
		info, err := os.Stat(filepath.Join(target, sub))
		if err != nil {
			t.Errorf("expected subdir %q created: %v", sub, err)
			continue
		}
		if !info.IsDir() {
			t.Errorf("%s is not a directory", sub)
		}
	}
}

func TestNewDataDir_Idempotent(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "data")
	if _, err := NewDataDir(root); err != nil {
		t.Fatalf("first NewDataDir: %v", err)
	}
	probe := filepath.Join(root, "output", "probe.txt")
	if err := os.WriteFile(probe, []byte("keep"), 0o600); err != nil {
		t.Fatalf("write probe: %v", err)
	}
	if _, err := NewDataDir(root); err != nil {
		t.Fatalf("second NewDataDir: %v", err)
	}
	data, err := os.ReadFile(probe)
	if err != nil {
		t.Fatalf("probe missing after second call: %v", err)
	}
	if string(data) != "keep" {
		t.Errorf("probe content changed: %q", string(data))
	}
}

func TestDataDir_PathHelpers(t *testing.T) {
	t.Parallel()
	d, err := NewDataDir(filepath.Join(t.TempDir(), "x"))
	if err != nil {
		t.Fatalf("NewDataDir: %v", err)
	}

	cases := []struct {
		name string
		got  string
		tail string
	}{
		{"DatabasePath", d.DatabasePath(), "crewship.db"},
		{"DatabaseURL", d.DatabaseURL(), "crewship.db"},
		{"OutputDir", d.OutputDir(), "output"},
		{"ChatsDir", d.ChatsDir(), "chats"},
		{"LogsDir", d.LogsDir(), "logs"},
		{"SkillsDir", d.SkillsDir(), "skills"},
		{"WorkspaceMemoryDir", d.WorkspaceMemoryDir("ws-1"), filepath.Join("memory", "ws-1")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.HasSuffix(tc.got, tc.tail) {
				t.Errorf("%s = %q, want suffix %q", tc.name, tc.got, tc.tail)
			}
			if !strings.Contains(tc.got, d.Root) {
				t.Errorf("%s = %q, want to contain %q", tc.name, tc.got, d.Root)
			}
		})
	}

	if !strings.HasPrefix(d.DatabaseURL(), "file:") {
		t.Errorf("DatabaseURL must start with file:, got %q", d.DatabaseURL())
	}
}

func TestDefaultDataDir_UsesHomeDir(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	d, err := DefaultDataDir()
	if err != nil {
		t.Fatalf("DefaultDataDir: %v", err)
	}
	if !strings.HasPrefix(d.Root, tmp) {
		t.Errorf("Root %q should be inside HOME %q", d.Root, tmp)
	}
	if filepath.Base(d.Root) != ".crewship" {
		t.Errorf("expected .crewship, got %q", filepath.Base(d.Root))
	}
}

func TestSeedBundledSkills_Idempotent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	db, err := Open("file:" + filepath.Join(dir, "seed.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	logger := newSilentLogger()
	if err := Migrate(context.Background(), db.DB, logger); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	if err := SeedBundledSkills(context.Background(), db.DB, logger); err != nil {
		t.Fatalf("seed first run: %v", err)
	}
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM skills WHERE source='BUNDLED'").Scan(&count); err != nil {
		t.Fatalf("count skills: %v", err)
	}
	if count < 3 {
		t.Errorf("expected ≥3 bundled skills, got %d", count)
	}

	if err := SeedBundledSkills(context.Background(), db.DB, logger); err != nil {
		t.Fatalf("seed second run: %v", err)
	}
	var count2 int
	if err := db.QueryRow("SELECT COUNT(*) FROM skills WHERE source='BUNDLED'").Scan(&count2); err != nil {
		t.Fatalf("count2: %v", err)
	}
	if count != count2 {
		t.Errorf("seed not idempotent: %d -> %d", count, count2)
	}
}

func TestSeedBuiltinTemplates_Idempotent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	db, err := Open("file:" + filepath.Join(dir, "tpl.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	logger := newSilentLogger()
	if err := Migrate(context.Background(), db.DB, logger); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES ('ws1', 'WS', 'ws')`); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}

	if err := SeedBuiltinTemplates(context.Background(), db.DB, "ws1", logger); err != nil {
		t.Fatalf("seed first: %v", err)
	}
	var n1 int
	if err := db.QueryRow("SELECT COUNT(*) FROM workflow_templates WHERE workspace_id='ws1' AND is_builtin=1").Scan(&n1); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n1 < 4 {
		t.Errorf("expected ≥4 builtin templates, got %d", n1)
	}

	if err := SeedBuiltinTemplates(context.Background(), db.DB, "ws1", logger); err != nil {
		t.Fatalf("seed second: %v", err)
	}
	var n2 int
	_ = db.QueryRow("SELECT COUNT(*) FROM workflow_templates WHERE workspace_id='ws1' AND is_builtin=1").Scan(&n2)
	if n2 != n1 {
		t.Errorf("templates duplicated: %d -> %d", n1, n2)
	}
}

func TestSeedBuiltinCrewTemplates_Idempotent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	db, err := Open("file:" + filepath.Join(dir, "ct.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	logger := newSilentLogger()
	if err := Migrate(context.Background(), db.DB, logger); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	if err := SeedBuiltinCrewTemplates(context.Background(), db.DB, logger); err != nil {
		t.Fatalf("seed first: %v", err)
	}
	var n1 int
	if err := db.QueryRow("SELECT COUNT(*) FROM crew_templates WHERE is_builtin=1").Scan(&n1); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n1 < 4 {
		t.Errorf("expected ≥4 crew templates, got %d", n1)
	}

	if err := SeedBuiltinCrewTemplates(context.Background(), db.DB, logger); err != nil {
		t.Fatalf("seed second: %v", err)
	}
	var n2 int
	_ = db.QueryRow("SELECT COUNT(*) FROM crew_templates WHERE is_builtin=1").Scan(&n2)
	if n2 != n1 {
		t.Errorf("crew templates duplicated: %d -> %d", n1, n2)
	}
}

func TestGenerateSeedID_FormatAndUniqueness(t *testing.T) {
	t.Parallel()
	a := generateSeedID("ct")
	b := generateSeedID("ct")
	if !strings.HasPrefix(a, "ct_") {
		t.Errorf("missing prefix: %q", a)
	}
	if a == b {
		t.Error("expected unique IDs")
	}
}

func TestRollbackV47_Idempotent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	db, err := Open("file:" + filepath.Join(dir, "rb.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	logger := newSilentLogger()
	if err := Migrate(context.Background(), db.DB, logger); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	if err := RollbackV47(context.Background(), db.DB, logger); err != nil {
		t.Fatalf("rollback first: %v", err)
	}
	var has int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM pragma_table_info('crews') WHERE name='cached_requirements'`,
	).Scan(&has); err != nil {
		t.Fatalf("probe column: %v", err)
	}
	if has != 0 {
		t.Errorf("expected column dropped, got %d", has)
	}
	var rows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM _migrations WHERE version=47`).Scan(&rows); err != nil {
		t.Fatalf("probe migration: %v", err)
	}
	if rows != 0 {
		t.Errorf("expected migration row removed, got %d", rows)
	}

	if err := RollbackV47(context.Background(), db.DB, logger); err != nil {
		t.Fatalf("rollback second: %v", err)
	}
}

func TestRegisterRestoreBackfill_Override(t *testing.T) {
	// Cannot run in parallel — global state.
	called := false
	hook := func(_ context.Context, _ *sql.Tx, _ *slog.Logger) error {
		called = true
		return nil
	}
	unreg := RegisterRestoreBackfill(9999, hook)
	t.Cleanup(unreg)

	got := RestoreBackfillFor(9999)
	if got == nil {
		t.Fatal("expected hook registered")
	}
	if err := got(context.Background(), nil, newSilentLogger()); err != nil {
		t.Fatalf("hook returned: %v", err)
	}
	if !called {
		t.Error("hook was not invoked")
	}

	// Replace with a different hook for the same version.
	unreg2 := RegisterRestoreBackfill(9999, func(_ context.Context, _ *sql.Tx, _ *slog.Logger) error {
		return nil
	})
	t.Cleanup(unreg2)
	if RestoreBackfillFor(9999) == nil {
		t.Error("override should remain registered")
	}
}

func TestRestoreBackfillFor_UnknownVersion(t *testing.T) {
	t.Parallel()
	if RestoreBackfillFor(-1) != nil {
		t.Error("expected nil for unknown version")
	}
}

func TestParseDSN(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"file:/tmp/x.db", "/tmp/x.db", false},
		{"file:///tmp/x.db", "/tmp/x.db", false},
		{"/tmp/y.db", "/tmp/y.db", false},
		{"", "", true},
		{"file:", "", true},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got, err := parseDSN(c.in)
			if c.wantErr {
				if err == nil {
					t.Errorf("expected error for %q", c.in)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestIsTextLikeType(t *testing.T) {
	t.Parallel()
	yes := []string{"TEXT", "VARCHAR(50)", "CHAR(10)", "CLOB", ""}
	no := []string{"INTEGER", "REAL", "BLOB", "NUMERIC"}
	for _, s := range yes {
		if !isTextLikeType(s) {
			t.Errorf("isTextLikeType(%q) = false, want true", s)
		}
	}
	for _, s := range no {
		if isTextLikeType(s) {
			t.Errorf("isTextLikeType(%q) = true, want false", s)
		}
	}
}

// newSilentLogger returns a logger that drops all output.
func newSilentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: slog.LevelError}))
}
