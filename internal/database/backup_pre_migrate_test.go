package database

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
}

// listSnapshots returns just the snapshot files matching the pre-migrate
// prefix for `dbPath`, so tests don't accidentally count the live DB +
// its WAL/SHM sidecars.
func listSnapshots(t *testing.T, dbPath string) []string {
	t.Helper()
	dir := filepath.Dir(dbPath)
	base := filepath.Base(dbPath)
	prefix := base + ".pre-migrate-"
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), prefix) && strings.HasSuffix(e.Name(), ".bak") {
			out = append(out, e.Name())
		}
	}
	return out
}

// TestSnapshotBeforeMigrate_FreshInstall covers the "no DB file yet" path:
// nothing to back up, no error, no snapshot file.
func TestSnapshotBeforeMigrate_FreshInstall(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "nope.db")

	// Open creates the file, so close + delete to simulate a truly fresh boot
	// before the first Open. Easier than constructing a *DB manually.
	db, err := Open("file:" + dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	db.Close()
	if err := os.Remove(dbPath); err != nil {
		t.Fatalf("remove db file: %v", err)
	}
	// Reopen for the snapshot call — DB struct needs a valid handle, but the
	// underlying file is gone again because SQLite doesn't materialize until a
	// write. The snapshot path must short-circuit on Stat-not-exist.
	db, err = Open("file:" + dbPath)
	if err != nil {
		t.Fatalf("Open 2: %v", err)
	}
	defer db.Close()
	// Force the file to NOT exist at the moment of the call: close, delete, then
	// invoke the snapshot helper directly.
	db.Close()
	_ = os.Remove(dbPath)
	_ = os.Remove(dbPath + "-wal")
	_ = os.Remove(dbPath + "-shm")

	// We need a live handle for the helper to even attempt VACUUM INTO; rebuild
	// one against the now-missing file. Open will recreate it, so re-delete.
	db, err = Open("file:" + dbPath)
	if err != nil {
		t.Fatalf("Open 3: %v", err)
	}
	defer db.Close()
	_ = os.Remove(dbPath)

	if err := SnapshotBeforeMigrate(context.Background(), db, newTestLogger()); err != nil {
		t.Fatalf("expected nil on missing file, got: %v", err)
	}
	if got := listSnapshots(t, dbPath); len(got) != 0 {
		t.Errorf("unexpected snapshots: %v", got)
	}
}

// TestSnapshotBeforeMigrate_NoPending covers the "DB is already at latest"
// path: Migrate has been run, _migrations.max == migrations[last].version,
// so the helper should noop.
func TestSnapshotBeforeMigrate_NoPending(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "uptodate.db")
	db, err := Open("file:" + dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	logger := newTestLogger()
	if err := Migrate(context.Background(), db.DB, logger); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	if err := SnapshotBeforeMigrate(context.Background(), db, logger); err != nil {
		t.Fatalf("SnapshotBeforeMigrate: %v", err)
	}
	if got := listSnapshots(t, dbPath); len(got) != 0 {
		t.Errorf("expected no snapshot when nothing pending, got %v", got)
	}
}

// TestSnapshotBeforeMigrate_CreatesBackup is the core happy-path test. We
// run Migrate so the schema is current, then delete a tail of _migrations
// rows to fake "pending migrations" without actually rolling back any
// schema. SnapshotBeforeMigrate must produce one .bak file.
func TestSnapshotBeforeMigrate_CreatesBackup(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "pending.db")
	db, err := Open("file:" + dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	logger := newTestLogger()
	if err := Migrate(context.Background(), db.DB, logger); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	// Drop the last two migration rows to fake pending state. The schema
	// columns those migrations added still exist — Migrate's re-run will
	// re-INSERT into _migrations without re-applying SQL because... well,
	// it WILL re-apply the SQL. We're not testing Migrate here, just the
	// snapshot helper, so that's fine: we tear down after the snapshot call.
	res, err := db.Exec(`DELETE FROM _migrations WHERE version IN
		(SELECT version FROM _migrations ORDER BY version DESC LIMIT 2)`)
	if err != nil {
		t.Fatalf("tamper _migrations: %v", err)
	}
	n, _ := res.RowsAffected()
	if n != 2 {
		t.Fatalf("expected to delete 2 rows, got %d", n)
	}

	if err := SnapshotBeforeMigrate(context.Background(), db, logger); err != nil {
		t.Fatalf("SnapshotBeforeMigrate: %v", err)
	}

	snaps := listSnapshots(t, dbPath)
	if len(snaps) != 1 {
		t.Fatalf("expected 1 snapshot, got %d: %v", len(snaps), snaps)
	}
	// Name shape: "<base>.pre-migrate-v<from>-to-v<to>-<timestamp>.bak"
	if !strings.Contains(snaps[0], ".pre-migrate-v") || !strings.Contains(snaps[0], "-to-v") {
		t.Errorf("unexpected snapshot name: %q", snaps[0])
	}
	// File must be non-empty and chmod 0600.
	info, err := os.Stat(filepath.Join(dir, snaps[0]))
	if err != nil {
		t.Fatalf("stat snapshot: %v", err)
	}
	if info.Size() == 0 {
		t.Error("snapshot is empty")
	}
	// Chmod is POSIX-only; skip the mode assertion on Windows in case this
	// test runs in a cross-platform matrix later.
	if info.Mode().Perm() != 0o600 && info.Mode().Perm() != 0 {
		// 0 only happens on Windows where Mode().Perm() is meaningless.
		if info.Mode().Perm() != 0o600 {
			t.Errorf("snapshot mode = %o, want 0600", info.Mode().Perm())
		}
	}
}

// TestSnapshotBeforeMigrate_EnvOptOut verifies CREWSHIP_SKIP_MIGRATION_BACKUP=1
// disables the snapshot even when migrations are pending.
func TestSnapshotBeforeMigrate_EnvOptOut(t *testing.T) {
	t.Setenv("CREWSHIP_SKIP_MIGRATION_BACKUP", "1")

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "optout.db")
	db, err := Open("file:" + dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	logger := newTestLogger()
	if err := Migrate(context.Background(), db.DB, logger); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if _, err := db.Exec(`DELETE FROM _migrations WHERE version =
		(SELECT MAX(version) FROM _migrations)`); err != nil {
		t.Fatalf("tamper: %v", err)
	}

	if err := SnapshotBeforeMigrate(context.Background(), db, logger); err != nil {
		t.Fatalf("SnapshotBeforeMigrate: %v", err)
	}
	if got := listSnapshots(t, dbPath); len(got) != 0 {
		t.Errorf("opt-out failed, got snapshots: %v", got)
	}
}

// TestPruneOldSnapshots feeds 12 fake snapshot files with monotonically
// older mtimes and confirms the helper keeps the 10 newest.
func TestPruneOldSnapshots(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "rotate.db")
	base := filepath.Base(dbPath)

	now := time.Now()
	for i := 0; i < 12; i++ {
		name := filepath.Join(dir, base+".pre-migrate-v1-to-v2-"+
			now.Add(-time.Duration(i)*time.Hour).UTC().Format("20060102T150405Z")+".bak")
		if err := os.WriteFile(name, []byte("dummy"), 0o600); err != nil {
			t.Fatalf("write fake snapshot: %v", err)
		}
		// Force mtime to match the index so prune has deterministic ordering.
		mtime := now.Add(-time.Duration(i) * time.Hour)
		if err := os.Chtimes(name, mtime, mtime); err != nil {
			t.Fatalf("chtimes: %v", err)
		}
	}

	if err := pruneOldSnapshots(dbPath, 10, newTestLogger()); err != nil {
		t.Fatalf("pruneOldSnapshots: %v", err)
	}
	if got := listSnapshots(t, dbPath); len(got) != 10 {
		t.Errorf("expected 10 snapshots after prune, got %d", len(got))
	}
}
