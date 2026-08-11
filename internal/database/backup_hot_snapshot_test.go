package database

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// This file covers the snapshot COPY MECHANISM — the step that turns a live
// database file into a .bak — as opposed to backup_pre_migrate_test.go, which
// covers when SnapshotBeforeMigrate decides to take one at all.
//
// Background (B-05): the pre-migration snapshot used to run
// "VACUUM INTO ?". VACUUM INTO is not a file copy. It re-creates the schema
// in the destination and then replays every table through
// "INSERT INTO vacuum_db.<t> SELECT * FROM main.<t>" — the SQL layer, with
// all its column-list, constraint and expression machinery. On the live
// production database that failed on journal_entries, which carries the
// VIRTUAL generated column run_id from v120/v121:
//
//	table vacuum_db.journal_entries has 17 columns but 18 values were supplied
//
// The same file at rest copied fine, so it is the liveness that breaks it,
// not the schema. SQLite's online backup API copies b-tree pages instead and
// never compiles anything against the source schema, which is why the fix is
// a mechanism swap rather than a schema change.
//
// The tests below pin the mechanism swap by running BOTH copies over the same
// table of hot-database conditions.

const journalRowsSeeded = 500

// newHotJournalDB builds a database shaped like the one that broke: a
// journal_entries table carrying the v121 form of the run_id VIRTUAL
// generated column (json_valid-guarded) plus the partial index that made the
// column necessary in the first place.
func newHotJournalDB(t *testing.T, dbPath string) *DB {
	t.Helper()
	ctx := context.Background()
	db, err := Open("file:" + dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	stmts := []string{
		`CREATE TABLE journal_entries (
			id TEXT PRIMARY KEY,
			workspace_id TEXT NOT NULL,
			entry_type TEXT NOT NULL,
			summary TEXT NOT NULL,
			payload TEXT NOT NULL DEFAULT '{}'
		)`,
		`ALTER TABLE journal_entries ADD COLUMN run_id TEXT
			GENERATED ALWAYS AS (
				CASE WHEN json_valid(payload) THEN json_extract(payload, '$.run_id') END
			) VIRTUAL`,
		`CREATE INDEX idx_journal_ws_run ON journal_entries(workspace_id, run_id)
			WHERE run_id IS NOT NULL`,
	}
	for _, s := range stmts {
		if _, err := db.ExecContext(ctx, s); err != nil {
			t.Fatalf("setup %q: %v", s, err)
		}
	}
	for i := 0; i < journalRowsSeeded; i++ {
		if _, err := db.ExecContext(ctx,
			`INSERT INTO journal_entries (id, workspace_id, entry_type, summary, payload)
			 VALUES (?,?,?,?,?)`,
			fmt.Sprintf("seed-%d", i), "ws-1", "test", "seeded",
			fmt.Sprintf(`{"run_id":"run-%d"}`, i)); err != nil {
			t.Fatalf("seed row %d: %v", i, err)
		}
	}
	return db
}

// legacyVacuumIntoSnapshot is the copy this change replaces, kept verbatim so
// the table below can show what each hot-database condition does to it.
func legacyVacuumIntoSnapshot(ctx context.Context, conn *sql.Conn, dstPath string) error {
	_, err := conn.ExecContext(ctx, "VACUUM INTO ?", dstPath)
	return err
}

// assertUsableSnapshot opens a produced snapshot and checks it is a real,
// readable database and not just a plausible-looking file: the rows are
// there, the generated column still resolves, and SQLite's own integrity
// check passes. A snapshot exists to be restored from, so "it was written"
// is not the assertion that matters.
func assertUsableSnapshot(t *testing.T, snapPath string, wantAtLeast int) {
	t.Helper()
	ctx := context.Background()
	snap, err := Open("file:" + snapPath)
	if err != nil {
		t.Fatalf("open snapshot: %v", err)
	}
	defer snap.Close()

	var integrity string
	if err := snap.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&integrity); err != nil {
		t.Fatalf("integrity_check: %v", err)
	}
	if integrity != "ok" {
		t.Errorf("snapshot integrity_check = %q, want %q", integrity, "ok")
	}

	var rows int
	if err := snap.QueryRowContext(ctx, `SELECT COUNT(*) FROM journal_entries`).Scan(&rows); err != nil {
		t.Fatalf("count journal_entries in snapshot: %v", err)
	}
	if rows < wantAtLeast {
		t.Errorf("snapshot has %d journal_entries, want at least %d", rows, wantAtLeast)
	}

	// The generated column is the whole reason this bug exists — confirm it
	// survived the copy as a generated column and still indexes.
	var withRunID int
	if err := snap.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM journal_entries WHERE workspace_id = 'ws-1' AND run_id IS NOT NULL`,
	).Scan(&withRunID); err != nil {
		t.Fatalf("query generated column in snapshot: %v", err)
	}
	if withRunID < wantAtLeast {
		t.Errorf("snapshot has %d rows with run_id, want at least %d", withRunID, wantAtLeast)
	}
}

// TestSnapshotMechanisms_HotDatabase runs both copy mechanisms over the
// states a database is in while a server is using it. The online backup must
// produce a usable snapshot in every one of them; VACUUM INTO's column
// records what it does instead, which is the case for the swap.
func TestSnapshotMechanisms_HotDatabase(t *testing.T) {
	mechanisms := []struct {
		name string
		snap func(ctx context.Context, conn *sql.Conn, dstPath string) error
	}{
		{name: "vacuum_into", snap: legacyVacuumIntoSnapshot},
		{name: "online_backup", snap: snapshotDatabaseConn},
	}

	tests := []struct {
		name string
		// prepare puts the database (and the connection the snapshot will be
		// taken on) into the state under test, and returns a teardown.
		prepare func(t *testing.T, dbPath string, conn *sql.Conn) func()
		// wantVacuumErr, when non-empty, is the substring VACUUM INTO fails
		// with in this state. Empty means it is expected to succeed.
		wantVacuumErr string
	}{
		{
			name:    "idle database",
			prepare: func(t *testing.T, dbPath string, conn *sql.Conn) func() { return func() {} },
		},
		{
			name: "server writing journal entries throughout the copy",
			// The production condition: a second connection appending to
			// journal_entries — every INSERT re-evaluating the run_id
			// generated expression and maintaining its partial index — for
			// the whole duration of the snapshot.
			prepare: func(t *testing.T, dbPath string, conn *sql.Conn) func() {
				writer, err := Open("file:" + dbPath)
				if err != nil {
					t.Fatalf("open writer: %v", err)
				}
				stop := make(chan struct{})
				var wg sync.WaitGroup
				wg.Add(1)
				go func() {
					defer wg.Done()
					for i := 0; ; i++ {
						select {
						case <-stop:
							return
						default:
						}
						if _, err := writer.Exec(
							`INSERT INTO journal_entries (id, workspace_id, entry_type, summary, payload)
							 VALUES (?,?,?,?,?)`,
							fmt.Sprintf("live-%d", i), "ws-1", "test", "written during snapshot",
							fmt.Sprintf(`{"run_id":"run-live-%d"}`, i)); err != nil {
							// Lock contention is expected and not the point of
							// the test; keep the pressure on and move along.
							time.Sleep(time.Millisecond)
						}
					}
				}()
				// Let the writer get ahead of the snapshot so the copy really
				// does run against a moving file.
				time.Sleep(50 * time.Millisecond)
				return func() {
					close(stop)
					wg.Wait()
					writer.Close()
				}
			},
		},
		{
			name: "snapshot pinned to a consistent read view",
			// What a hot backup wants: hold the read transaction open so the
			// copy is of one point in time even while writers commit. The
			// online backup reads through the connection and is fine with it;
			// VACUUM INTO is a statement that cannot run inside a
			// transaction, so on this path it cannot produce a snapshot at
			// all — the pre-fix code had no way to take a pinned hot backup.
			prepare: func(t *testing.T, dbPath string, conn *sql.Conn) func() {
				ctx := context.Background()
				// BEGIN via raw SQL rather than sql.Tx: the transaction has to
				// belong to this connection while we still hold the *sql.Conn
				// to run the snapshot on.
				if _, err := conn.ExecContext(ctx, "BEGIN"); err != nil {
					t.Fatalf("BEGIN: %v", err)
				}
				var n int
				if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM journal_entries`).Scan(&n); err != nil {
					t.Fatalf("pin read view: %v", err)
				}
				return func() {
					if _, err := conn.ExecContext(ctx, "ROLLBACK"); err != nil {
						t.Errorf("ROLLBACK: %v", err)
					}
				}
			},
			wantVacuumErr: "cannot VACUUM from within a transaction",
		},
	}

	for _, tc := range tests {
		for _, mech := range mechanisms {
			t.Run(tc.name+"/"+mech.name, func(t *testing.T) {
				ctx := context.Background()
				dir := t.TempDir()
				dbPath := filepath.Join(dir, "crewship.db")
				db := newHotJournalDB(t, dbPath)
				defer db.Close()

				conn, err := db.Conn(ctx)
				if err != nil {
					t.Fatalf("acquire connection: %v", err)
				}
				defer conn.Close()

				teardown := tc.prepare(t, dbPath, conn)
				defer teardown()

				snapPath := filepath.Join(dir, "crewship.db.snap.bak")
				err = mech.snap(ctx, conn, snapPath)

				if mech.name == "vacuum_into" && tc.wantVacuumErr != "" {
					if err == nil {
						t.Fatalf("VACUUM INTO unexpectedly produced a snapshot; this state used to "+
							"break it with %q — if SQLite changed, revisit the mechanism note in "+
							"backup_pre_migrate.go", tc.wantVacuumErr)
					}
					if !strings.Contains(err.Error(), tc.wantVacuumErr) {
						t.Fatalf("VACUUM INTO error = %v, want it to contain %q", err, tc.wantVacuumErr)
					}
					if _, statErr := os.Stat(snapPath); statErr == nil {
						t.Errorf("failed VACUUM INTO left a snapshot file behind at %s", snapPath)
					}
					return
				}

				if err != nil {
					t.Fatalf("%s snapshot failed: %v", mech.name, err)
				}
				assertUsableSnapshot(t, snapPath, journalRowsSeeded)
			})
		}
	}
}

// TestSnapshotDatabase_RefusesExistingDestination pins the one guarantee
// VACUUM INTO gave us for free and the backup API does not: a snapshot is a
// new file, never a copy laid over an existing database. Without the guard,
// re-running a snapshot onto an existing path would copy pages into it and
// produce a file that looks restorable but is a blend of two databases.
func TestSnapshotDatabase_RefusesExistingDestination(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db := newHotJournalDB(t, filepath.Join(dir, "crewship.db"))
	defer db.Close()

	occupied := filepath.Join(dir, "already-there.bak")
	if err := os.WriteFile(occupied, []byte("not a snapshot"), 0o600); err != nil {
		t.Fatalf("write existing file: %v", err)
	}

	err := snapshotDatabase(ctx, db.DB, occupied)
	if err == nil {
		t.Fatal("expected snapshot to refuse an existing destination")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("error = %v, want it to mention the destination already exists", err)
	}
	got, err := os.ReadFile(occupied)
	if err != nil {
		t.Fatalf("read existing file: %v", err)
	}
	if string(got) != "not a snapshot" {
		t.Errorf("existing file was modified: %q", got)
	}
}

// TestSnapshotDatabase_NoFileLeftOnFailure covers the other half of the
// cleanup contract: when the copy cannot be completed, nothing may be left at
// the destination path. A half-written file there is worse than no file, as
// it matches the .bak name pattern `crewship db restore-snapshot` offers as a
// rollback target.
func TestSnapshotDatabase_NoFileLeftOnFailure(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db := newHotJournalDB(t, filepath.Join(dir, "crewship.db"))
	defer db.Close()

	// A destination inside a directory that does not exist: SQLite cannot
	// open it, so the copy fails after the destination path was chosen.
	dstPath := filepath.Join(dir, "no-such-dir", "crewship.db.bak")
	if err := snapshotDatabase(ctx, db.DB, dstPath); err == nil {
		t.Fatal("expected snapshot to fail for an unopenable destination")
	}
	for _, suffix := range []string{"", "-journal", "-wal", "-shm"} {
		if _, err := os.Stat(dstPath + suffix); err == nil {
			t.Errorf("failed snapshot left %s behind", dstPath+suffix)
		}
	}
}

// TestSnapshotBeforeMigrate_WhileServerWrites is the end-to-end shape of the
// thing B-05 blocks: take the pre-migration snapshot on a real migrated
// schema while a second connection keeps writing journal entries, and get a
// restorable file out. It exercises the production entry point, so the
// filename, the 0600 mode and the usability of the result are all pinned
// together.
func TestSnapshotBeforeMigrate_WhileServerWrites(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "crewship.db")
	logger := newTestLogger()

	db, err := Open("file:" + dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()
	if err := Migrate(ctx, db.DB, logger); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	// journal_entries needs a workspace to hang off; the real schema has the
	// FK that the standalone table in newHotJournalDB does not.
	if _, err := db.ExecContext(ctx,
		`INSERT INTO workspaces (id, name, slug) VALUES ('ws-1','Snapshot','snapshot')`); err != nil {
		t.Fatalf("seed workspace: %v", err)
	}

	// Pretend two migrations are pending so the snapshot is taken; the schema
	// itself stays at HEAD, which is what we want to copy.
	if _, err := db.ExecContext(ctx, `DELETE FROM _migrations WHERE version IN
		(SELECT version FROM _migrations ORDER BY version DESC LIMIT 2)`); err != nil {
		t.Fatalf("tamper _migrations: %v", err)
	}

	writer, err := Open("file:" + dbPath)
	if err != nil {
		t.Fatalf("open writer: %v", err)
	}
	defer writer.Close()
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			if _, err := writer.Exec(
				`INSERT INTO journal_entries (id, workspace_id, entry_type, actor_type, summary, payload)
				 VALUES (?,?,?,?,?,?)`,
				fmt.Sprintf("live-%d", i), "ws-1", "test", "system", "written during snapshot",
				fmt.Sprintf(`{"run_id":"run-%d"}`, i)); err != nil {
				time.Sleep(time.Millisecond)
			}
		}
	}()
	time.Sleep(50 * time.Millisecond)

	snapErr := SnapshotBeforeMigrate(ctx, db, logger)
	close(stop)
	wg.Wait()
	if snapErr != nil {
		t.Fatalf("SnapshotBeforeMigrate under concurrent writes: %v", snapErr)
	}

	snaps := listSnapshots(t, dbPath)
	if len(snaps) != 1 {
		t.Fatalf("expected 1 snapshot, got %d: %v", len(snaps), snaps)
	}
	snapPath := filepath.Join(dir, snaps[0])
	info, err := os.Stat(snapPath)
	if err != nil {
		t.Fatalf("stat snapshot: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("snapshot mode = %o, want 0600", info.Mode().Perm())
	}

	snap, err := Open("file:" + snapPath)
	if err != nil {
		t.Fatalf("open snapshot: %v", err)
	}
	defer snap.Close()
	var integrity string
	if err := snap.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&integrity); err != nil {
		t.Fatalf("integrity_check: %v", err)
	}
	if integrity != "ok" {
		t.Errorf("snapshot integrity_check = %q, want %q", integrity, "ok")
	}
	// The snapshot has to carry the schema AND the rows written before it was
	// taken, including through the run_id generated column that VACUUM INTO
	// choked on.
	var withRunID int
	if err := snap.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM journal_entries WHERE run_id IS NOT NULL`).Scan(&withRunID); err != nil {
		t.Fatalf("query run_id in snapshot: %v", err)
	}
	if withRunID == 0 {
		t.Error("snapshot contains no journal entries with a run_id — the copy raced the writer to empty")
	}
	var maxApplied int
	if err := snap.QueryRowContext(ctx, `SELECT MAX(version) FROM _migrations`).Scan(&maxApplied); err != nil {
		t.Fatalf("read _migrations from snapshot: %v", err)
	}
	if maxApplied == 0 {
		t.Error("snapshot has no applied migrations recorded")
	}
}
