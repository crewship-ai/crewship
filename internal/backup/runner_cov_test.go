package backup

// Coverage tests for runner.go — ListBackups, Verify, Delete, Rotate,
// ForceReleaseLock, ensureAgentsIdle, columnExists, and the small
// version/name helpers.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

var runnerCovMemCounter atomic.Uint64

func newRunnerCovDB(t *testing.T, schema string) *sql.DB {
	t.Helper()
	name := fmt.Sprintf("crewship-runnercov-%d", runnerCovMemCounter.Add(1))
	db, err := sql.Open("sqlite", fmt.Sprintf("file:%s?mode=memory&cache=shared", name))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if schema != "" {
		if _, err := db.Exec(schema); err != nil {
			t.Fatalf("schema: %v", err)
		}
	}
	return db
}

// writeBundleFile creates a real bundle on disk and returns its path.
func writeBundleFile(t *testing.T, dir string, m *Manifest, payload string) string {
	t.Helper()
	name := BundleFileName(m.Scope, "covslug", m.CreatedAt)
	p := filepath.Join(dir, name)
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteBundle(f, m, strings.NewReader(payload), WriteBundleOptions{NoEncrypt: true}); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return p
}

func covManifestForWorkspace(wsID string, createdAt time.Time) *Manifest {
	return &Manifest{
		FormatVersion:     FormatVersion,
		Scope:             ScopeWorkspace,
		CompatibleTargets: []Target{TargetAnyInstance},
		CreatedAt:         createdAt,
		CreatedBy:         Actor{UserID: "u_cov"},
		Contents: Contents{Workspace: &WorkspaceSummary{
			ID: wsID, Slug: "covslug", Name: "Cov",
		}},
	}
}

func TestListBackups_MixedDirectory(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	older := covManifestForWorkspace("ws_list", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	newer := covManifestForWorkspace("ws_list", time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC))
	pOld := writeBundleFile(t, dir, older, "old")
	pNew := writeBundleFile(t, dir, newer, "new")

	// Distractors: a non-.zst file, a subdirectory, and a corrupt .zst
	// whose entry must still be listed with mtime fallback.
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "subdir"), 0o700); err != nil {
		t.Fatal(err)
	}
	corrupt := filepath.Join(dir, "broken.tar.zst")
	if err := os.WriteFile(corrupt, []byte("not zstd"), 0o600); err != nil {
		t.Fatal(err)
	}

	entries, err := ListBackups(ctx, dir)
	if err != nil {
		t.Fatalf("ListBackups: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("got %d entries (%+v), want 3", len(entries), entries)
	}
	// Newest-first contract; the corrupt file has today's mtime so it
	// sorts first, then the two manifest-dated bundles.
	if entries[0].Path != corrupt {
		t.Errorf("entries[0] = %s, want corrupt-with-recent-mtime first", entries[0].Path)
	}
	if entries[1].Path != pNew || entries[2].Path != pOld {
		t.Errorf("manifest ordering wrong: %s, %s", entries[1].Path, entries[2].Path)
	}
	if entries[1].WorkspaceID != "ws_list" || entries[1].Scope != ScopeWorkspace {
		t.Errorf("manifest fields not populated: %+v", entries[1])
	}
	if entries[1].ScopeLevel != DefaultScopeLevel {
		t.Errorf("empty scope level must default to %s, got %s", DefaultScopeLevel, entries[1].ScopeLevel)
	}
	// Corrupt entry carries no manifest data.
	if entries[0].WorkspaceID != "" || entries[0].FormatVersion != 0 {
		t.Errorf("corrupt entry should be metadata-free: %+v", entries[0])
	}

	// Missing directory is not an error.
	got, err := ListBackups(ctx, filepath.Join(dir, "missing-subdir"))
	if got != nil || err != nil {
		t.Errorf("missing dir = (%v, %v), want (nil, nil)", got, err)
	}
}

func TestVerify_Branches(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	t.Run("valid bundle", func(t *testing.T) {
		p := writeBundleFile(t, dir, covManifestForWorkspace("ws_v", time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)), "payload")
		res, err := Verify(ctx, p)
		if err != nil {
			t.Fatalf("Verify: %v", err)
		}
		if !res.Valid || res.Err != nil || res.Size <= 0 {
			t.Errorf("res = %+v", res)
		}
	})
	t.Run("checksum mismatch", func(t *testing.T) {
		// Assemble with WriteBundleStream and a deliberately wrong sha.
		m := covManifestForWorkspace("ws_v2", time.Date(2026, 3, 2, 0, 0, 0, 0, time.UTC))
		m.Checksums.PayloadSHA256 = "sha256:" + strings.Repeat("0", 64)
		p := filepath.Join(dir, "mismatch.tar.zst")
		f, err := os.Create(p)
		if err != nil {
			t.Fatal(err)
		}
		if err := WriteBundleStream(f, m, strings.NewReader("sealed"), 6); err != nil {
			t.Fatal(err)
		}
		_ = f.Close()
		res, err := Verify(ctx, p)
		if err != nil {
			t.Fatalf("Verify: %v", err)
		}
		if res.Valid || !errors.Is(res.Err, ErrInvalidChecksum) {
			t.Errorf("res = %+v, want checksum failure", res)
		}
	})
	t.Run("unreadable bundle", func(t *testing.T) {
		p := filepath.Join(dir, "junk.tar.zst")
		if err := os.WriteFile(p, []byte("junk"), 0o600); err != nil {
			t.Fatal(err)
		}
		res, err := Verify(ctx, p)
		if err != nil {
			t.Fatalf("Verify: %v", err)
		}
		if res.Valid || res.Err == nil {
			t.Errorf("res = %+v, want structural failure", res)
		}
	})
	t.Run("missing file", func(t *testing.T) {
		_, err := Verify(ctx, filepath.Join(dir, "ghost.tar.zst"))
		if err == nil || !strings.Contains(err.Error(), "stat") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestDelete_Branches(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	t.Run("refuses non-bundle suffix", func(t *testing.T) {
		err := Delete(ctx, filepath.Join(dir, "data.db"))
		if err == nil || !strings.Contains(err.Error(), "not a .tar.zst bundle") {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("refuses files that fail inspect", func(t *testing.T) {
		p := filepath.Join(dir, "fake.tar.zst")
		if err := os.WriteFile(p, []byte("not a bundle"), 0o600); err != nil {
			t.Fatal(err)
		}
		err := Delete(ctx, p)
		if err == nil || !strings.Contains(err.Error(), "failed inspect") {
			t.Fatalf("err = %v", err)
		}
		if _, statErr := os.Stat(p); statErr != nil {
			t.Errorf("refused delete must leave the file: %v", statErr)
		}
	})
	t.Run("deletes a real bundle", func(t *testing.T) {
		p := writeBundleFile(t, dir, covManifestForWorkspace("ws_d", time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)), "x")
		if err := Delete(ctx, p); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("bundle survived delete")
		}
	})
}

func TestRotate_CountAndAgeRules(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	now := time.Now().UTC()

	mk := func(ws string, age time.Duration) string {
		return writeBundleFile(t, dir, covManifestForWorkspace(ws, now.Add(-age)), "p")
	}
	newest := mk("ws_rot", 1*time.Hour)
	middle := mk("ws_rot", 48*time.Hour)
	oldest := mk("ws_rot", 30*24*time.Hour)
	other := mk("ws_other", 90*24*time.Hour) // different workspace — untouchable

	t.Run("dry-run keepLast", func(t *testing.T) {
		got, err := Rotate(ctx, dir, "ws_rot", 1, 0, true)
		if err != nil {
			t.Fatalf("Rotate: %v", err)
		}
		want := map[string]bool{middle: true, oldest: true}
		if len(got) != 2 || !want[got[0]] || !want[got[1]] {
			t.Errorf("dry-run = %v, want middle+oldest", got)
		}
		// Dry run must not delete.
		for _, p := range []string{newest, middle, oldest, other} {
			if _, err := os.Stat(p); err != nil {
				t.Errorf("dry-run deleted %s", p)
			}
		}
	})
	t.Run("keepDays cutoff", func(t *testing.T) {
		got, err := Rotate(ctx, dir, "ws_rot", 0, 7, true)
		if err != nil {
			t.Fatalf("Rotate: %v", err)
		}
		if len(got) != 1 || got[0] != oldest {
			t.Errorf("keepDays dry-run = %v, want only the 30-day bundle", got)
		}
	})
	t.Run("live delete", func(t *testing.T) {
		got, err := Rotate(ctx, dir, "ws_rot", 2, 0, false)
		if err != nil {
			t.Fatalf("Rotate: %v", err)
		}
		if len(got) != 1 || got[0] != oldest {
			t.Fatalf("deleted = %v, want oldest only", got)
		}
		if _, err := os.Stat(oldest); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("oldest survived live rotate")
		}
		for _, p := range []string{newest, middle, other} {
			if _, err := os.Stat(p); err != nil {
				t.Errorf("rotate over-deleted %s", p)
			}
		}
	})
}

func TestForceReleaseLock(t *testing.T) {
	ctx := context.Background()

	if err := ForceReleaseLock(ctx, nil, "ws"); err == nil {
		t.Error("nil db must error")
	}
	db := newRunnerCovDB(t, `
		CREATE TABLE backup_locks (
			workspace_id TEXT PRIMARY KEY,
			acquired_at TEXT,
			acquired_by TEXT,
			expires_at TEXT
		);`)
	if err := ForceReleaseLock(ctx, db, ""); err == nil {
		t.Error("empty workspace must error")
	}
	if _, err := db.Exec(
		`INSERT INTO backup_locks (workspace_id, acquired_at, acquired_by, expires_at) VALUES ('ws_f', 'now', 'u1', 'later')`,
	); err != nil {
		t.Fatal(err)
	}
	if err := ForceReleaseLock(ctx, db, "ws_f"); err != nil {
		t.Fatalf("ForceReleaseLock: %v", err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM backup_locks WHERE workspace_id='ws_f'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("lock row survived force release")
	}
}

func TestEnsureAgentsIdle_Branches(t *testing.T) {
	ctx := context.Background()
	target := &WorkspaceTarget{ID: "ws1", CrewTargets: []CrewTarget{{ID: "c1"}, {ID: "c2"}}}

	t.Run("no agents table", func(t *testing.T) {
		db := newRunnerCovDB(t, ``)
		if err := ensureAgentsIdle(ctx, db, target); err != nil {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("agents table without status column", func(t *testing.T) {
		db := newRunnerCovDB(t, `CREATE TABLE agents (id TEXT PRIMARY KEY, crew_id TEXT, slug TEXT);`)
		if err := ensureAgentsIdle(ctx, db, target); err != nil {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("no crews in scope", func(t *testing.T) {
		db := newRunnerCovDB(t, `CREATE TABLE agents (id TEXT PRIMARY KEY, crew_id TEXT, slug TEXT, status TEXT);`)
		if err := ensureAgentsIdle(ctx, db, &WorkspaceTarget{ID: "ws1"}); err != nil {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("all idle", func(t *testing.T) {
		db := newRunnerCovDB(t, `CREATE TABLE agents (id TEXT PRIMARY KEY, crew_id TEXT, slug TEXT, status TEXT);
			INSERT INTO agents VALUES ('a1','c1','alice','IDLE');`)
		if err := ensureAgentsIdle(ctx, db, target); err != nil {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("running agent blocks", func(t *testing.T) {
		db := newRunnerCovDB(t, `CREATE TABLE agents (id TEXT PRIMARY KEY, crew_id TEXT, slug TEXT, status TEXT);
			INSERT INTO agents VALUES ('a1','c1','alice','running');
			INSERT INTO agents VALUES ('a2','c2','bob','IDLE');`)
		err := ensureAgentsIdle(ctx, db, target)
		if !errors.Is(err, ErrAgentRunning) {
			t.Fatalf("err = %v, want ErrAgentRunning", err)
		}
		if !strings.Contains(err.Error(), "alice") {
			t.Errorf("error should name the running agent: %v", err)
		}
	})
	t.Run("agent in unrelated crew does not block", func(t *testing.T) {
		db := newRunnerCovDB(t, `CREATE TABLE agents (id TEXT PRIMARY KEY, crew_id TEXT, slug TEXT, status TEXT);
			INSERT INTO agents VALUES ('a1','c_elsewhere','zed','busy');`)
		if err := ensureAgentsIdle(ctx, db, target); err != nil {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestColumnExists(t *testing.T) {
	ctx := context.Background()
	db := newRunnerCovDB(t, `CREATE TABLE things (id TEXT PRIMARY KEY, label TEXT);`)

	if _, err := columnExists(ctx, db, "things; DROP TABLE things", "id"); err == nil {
		t.Error("injection-shaped table name must be rejected")
	}
	ok, err := columnExists(ctx, db, "things", "label")
	if err != nil || !ok {
		t.Errorf("existing column = (%v, %v)", ok, err)
	}
	ok, err = columnExists(ctx, db, "things", "ghost")
	if err != nil || ok {
		t.Errorf("missing column = (%v, %v)", ok, err)
	}
	if _, err := columnExists(ctx, db, "missing_table", "id"); err != nil {
		// PRAGMA on a missing table yields zero rows, not an error, on
		// modernc; either way must not panic. Accept both shapes.
		t.Logf("missing table: %v", err)
	}
}

func TestSlugFromPartialName_Branches(t *testing.T) {
	cases := []struct {
		in     string
		want   string
		wantOK bool
	}{
		{"crewship-workspace-my-ws-20260101T000000Z.tar.zst.partial", "my-ws", true},
		{"crewship-crew-alpha-beta-20260101T000000Z.tar.zst.partial", "alpha-beta", true},
		{"crewship-instance-host-20260101T000000Z.tar.zst.partial", "host", true},
		{"unrelated.partial", "", false},
		{"crewship-workspace-noslugtimestamp.tar.zst.partial", "", false},
		{"crewship-unknownscope-x-20260101T000000Z.tar.zst.partial", "", false},
	}
	for _, tc := range cases {
		got, ok := slugFromPartialName(tc.in)
		if got != tc.want || ok != tc.wantOK {
			t.Errorf("slugFromPartialName(%q) = (%q, %v), want (%q, %v)", tc.in, got, ok, tc.want, tc.wantOK)
		}
	}
}

func TestDetectCrewshipVersion(t *testing.T) {
	t.Run("override wins", func(t *testing.T) {
		if got := DetectCrewshipVersion("v9.9.9"); got != "v9.9.9" {
			t.Errorf("got %q", got)
		}
	})
	t.Run("env var second", func(t *testing.T) {
		t.Setenv("CREWSHIP_VERSION", "v1.2.3-env")
		if got := DetectCrewshipVersion(""); got != "v1.2.3-env" {
			t.Errorf("got %q", got)
		}
	})
	t.Run("fallback never empty", func(t *testing.T) {
		t.Setenv("CREWSHIP_VERSION", "")
		if got := DetectCrewshipVersion(""); got == "" {
			t.Error("version must never be empty")
		}
	})
}

func TestAppliedMigrationVersions_Fallbacks(t *testing.T) {
	ctx := context.Background()
	if got := AppliedMigrationVersions(ctx, nil); got != nil {
		t.Errorf("nil db = %v, want nil", got)
	}
	db := newRunnerCovDB(t, ``)
	if got := AppliedMigrationVersions(ctx, db); got != nil {
		t.Errorf("missing table = %v, want nil", got)
	}
	db2 := newRunnerCovDB(t, `CREATE TABLE _migrations (version INTEGER PRIMARY KEY);
		INSERT INTO _migrations VALUES (3),(1),(2);`)
	got := AppliedMigrationVersions(ctx, db2)
	if len(got) != 3 || got[0] != 1 || got[2] != 3 {
		t.Errorf("got %v, want sorted [1 2 3]", got)
	}
}

func TestForceReleaseLock_ClosedDB(t *testing.T) {
	db := newRunnerCovDB(t, `CREATE TABLE backup_locks (workspace_id TEXT PRIMARY KEY);`)
	_ = db.Close()
	err := ForceReleaseLock(context.Background(), db, "ws")
	if err == nil || !strings.Contains(err.Error(), "force release lock") {
		t.Fatalf("err = %v", err)
	}
}

func TestInspect_OpenError(t *testing.T) {
	_, err := Inspect(context.Background(), filepath.Join(t.TempDir(), "absent.tar.zst"))
	if err == nil || !strings.Contains(err.Error(), "open") {
		t.Fatalf("err = %v", err)
	}
}
