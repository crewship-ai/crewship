package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"modernc.org/sqlite"
)

// MigrationBackupRetention is the number of pre-migration snapshots to keep
// per database file. Older snapshots are pruned after each successful backup.
const MigrationBackupRetention = 10

// SnapshotBeforeMigrate takes a hot-copy snapshot of the SQLite database if
// any migrations are pending, so the operator has a one-step rollback if a
// migration corrupts data or the new binary refuses to start. Snapshots are
// written next to the database file as
// "<db>.pre-migrate-v<from>-to-v<to>-<UTC-RFC3339>.bak".
//
// Skips silently when:
//   - CREWSHIP_SKIP_MIGRATION_BACKUP=1 (operator opt-out)
//   - db is in-memory (path empty or ":memory:")
//   - no migrations are pending (nothing to roll back from)
//   - the DB file does not yet exist (fresh install)
//
// Errors are returned to the caller — a backup failure must abort the boot.
// Silently continuing would leave the operator without a rollback point
// exactly when they need one most.
func SnapshotBeforeMigrate(ctx context.Context, db *DB, logger *slog.Logger) error {
	if os.Getenv("CREWSHIP_SKIP_MIGRATION_BACKUP") == "1" {
		logger.Info("skipping pre-migration backup (CREWSHIP_SKIP_MIGRATION_BACKUP=1)")
		return nil
	}

	path := db.Path()
	if path == "" || path == ":memory:" || strings.HasPrefix(path, "file::memory:") {
		return nil
	}
	// Strip query string from DSN-style paths.
	if i := strings.IndexByte(path, '?'); i >= 0 {
		path = path[:i]
	}
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("stat database file: %w", err)
	}

	fromVersion, toVersion, pending, err := pendingMigrationRange(ctx, db.DB)
	if err != nil {
		return fmt.Errorf("inspect migration state: %w", err)
	}
	if pending == 0 {
		return nil
	}

	backupPath := fmt.Sprintf(
		"%s.pre-migrate-v%d-to-v%d-%s.bak",
		path,
		fromVersion,
		toVersion,
		time.Now().UTC().Format("20060102T150405Z"),
	)

	logger.Info(
		"creating pre-migration snapshot",
		"from_version", fromVersion,
		"to_version", toVersion,
		"pending", pending,
		"backup", backupPath,
	)

	if err := snapshotDatabase(ctx, db.DB, backupPath); err != nil {
		return fmt.Errorf("snapshot database: %w", err)
	}
	if err := os.Chmod(backupPath, 0600); err != nil {
		logger.Warn("chmod snapshot failed", "path", backupPath, "error", err)
	}

	if err := pruneOldSnapshots(path, MigrationBackupRetention, logger); err != nil {
		// Non-fatal: the snapshot we just took is the one that matters.
		logger.Warn("prune old snapshots failed", "error", err)
	}
	return nil
}

// sqliteOnlineBackup is the sliver of modernc.org/sqlite's driver connection
// we need: the constructor for SQLite's online backup API
// (sqlite3_backup_init/step/finish). The concrete driver type is unexported,
// so the only way at it is to declare the method set we want here and type-
// assert the raw driver connection handed to us by (*sql.Conn).Raw.
type sqliteOnlineBackup interface {
	NewBackup(dstURI string) (*sqlite.Backup, error)
}

// onlineBackup is the part of *sqlite.Backup that copyAllPages drives. It
// exists as an interface for one reason: the order in which these four methods
// are called is load-bearing (Finish destroys the object — see copyAllPages),
// and an ordering invariant that cannot be observed cannot be regression-
// tested. *sqlite.Backup satisfies it as-is.
type onlineBackup interface {
	Step(n int32) (bool, error)
	Finish() error
	Remaining() int
	PageCount() int
}

// snapshotDatabase copies the whole database into dstPath using SQLite's
// online backup API — a page-level copy of the source file, made through a
// connection that already has the database open.
//
// It replaces "VACUUM INTO ?", which cannot be trusted against a database
// that anything else is writing. VACUUM INTO does not copy pages: it
// re-creates the schema in the destination and then replays every table
// through "INSERT INTO vacuum_db.<t> SELECT * FROM main.<t>", i.e. through
// the SQL layer, where a table's column list has to line up on both sides.
// journal_entries carries a VIRTUAL generated column (run_id, added by v120
// and hardened by v121), and against the LIVE production database that copy
// failed with
//
//	table vacuum_db.journal_entries has 17 columns but 18 values were supplied
//
// — the destination counted the generated column as not-insertable while the
// source's "SELECT *" still produced a value for it. The same file at rest,
// same DDL and same WAL, vacuumed cleanly, so this is about the database
// being live, not about the schema being wrong. The pre-migration snapshot
// has gotten away with it because it runs at boot, before the server serves
// anything; any hot backup on this path — the nightly "snapshot production
// and replay the migrations against it" rehearsal we want next — hits it.
//
// The online backup API sidesteps the whole class: it copies b-tree pages,
// never compiles a statement against the source schema, and so is indifferent
// to generated columns, CHECK constraints, triggers and whatever else a
// future migration bolts onto a table.
func snapshotDatabase(ctx context.Context, db *sql.DB, dstPath string) error {
	// One pooled connection is taken for the whole copy and handed back
	// afterwards. Open() caps the pool at 5, so on a live instance this costs
	// the server one of its four readers for the duration — the price of a
	// hot backup, and the reason the nightly rehearsal should run off-peak.
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire connection for snapshot: %w", err)
	}
	defer conn.Close()
	return snapshotDatabaseConn(ctx, conn, dstPath)
}

// snapshotDatabaseConn is snapshotDatabase against a connection the caller
// already holds. Split out because the copy has to be drivable on a specific
// connection: the online backup API reads through the connection's own
// database handle, and a test (or a future caller that wants to pin a
// consistent view with an explicit read transaction) needs to say which
// connection that is. VACUUM INTO could not be used that way at all — it
// refuses to run on a connection with a transaction open.
func snapshotDatabaseConn(ctx context.Context, conn *sql.Conn, dstPath string) error {
	// VACUUM INTO refuses to write to a destination that already exists, and
	// callers (the timestamped .bak name above, `crewship db snapshot`) lean
	// on that: a snapshot must never be a merge into someone else's file.
	// The backup API has no such rule — it would happily copy pages over an
	// existing database — so keep the guarantee ourselves.
	//
	// Lstat, not Stat: Stat resolves symlinks, so a DANGLING link planted at
	// the destination reports ErrNotExist and passes this refusal, after
	// which NewBackup opens the link and SQLite writes a page-for-page copy
	// of the whole database — credentials, tokens, session material — to
	// wherever the link points. SnapshotBeforeMigrate's chmod 0600 then lands
	// on the target rather than the link, and removeSnapshotArtifacts only
	// unlinks the link, so the copy survives the cleanup. The pre-migration
	// destination is guessable ("<db>.pre-migrate-v<from>-to-v<to>-<UTC>.bak"
	// beside the database), which means anyone who can create an entry in the
	// data directory can choose where the snapshot goes. Any existing entry
	// is refused, symlink included — a snapshot writes a new file or nothing.
	if _, err := os.Lstat(dstPath); err == nil {
		return fmt.Errorf("snapshot destination already exists: %s", dstPath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat snapshot destination: %w", err)
	}

	// The copy itself cannot be interrupted (see the Step(-1) note below), so
	// an already-cancelled context is worth catching before a multi-GB file
	// starts being written.
	if err := ctx.Err(); err != nil {
		return err
	}

	err := conn.Raw(func(driverConn any) error {
		backupper, ok := driverConn.(sqliteOnlineBackup)
		if !ok {
			// Only reachable if the sqlite driver is swapped or downgraded
			// below modernc.org/sqlite v1.56.0, which is where NewBackup
			// appeared. Fail loudly rather than fall back to VACUUM INTO:
			// a snapshot that silently uses the broken mechanism is worse
			// than no snapshot, because boot continues on into the
			// migrations believing it has a rollback point.
			return fmt.Errorf("sqlite driver connection %T has no NewBackup method "+
				"(modernc.org/sqlite v1.56.0+ required for the online backup API)", driverConn)
		}

		// NewBackup opens the destination, which creates the file — so from
		// here on every error path has to delete it again (see below).
		backup, err := backupper.NewBackup(dstPath)
		if err != nil {
			return fmt.Errorf("open snapshot destination: %w", err)
		}

		return copyAllPages(backup)
	})
	if err != nil {
		// A half-written destination must never survive: it is a valid-looking
		// SQLite file sitting at the exact path (and, for the pre-migration
		// case, matching the exact name pattern) that `crewship db
		// restore-snapshot` offers as a rollback target. Silence from the
		// removal is deliberate — the copy error is what the caller needs to
		// see, and os.Remove failing on a file we just created is not a
		// separate story worth burying it under.
		removeSnapshotArtifacts(dstPath)
		return err
	}
	return nil
}

// copyAllPages runs the copy on an opened backup handle and always finishes
// it. Split out of snapshotDatabaseConn so the call order can be pinned by a
// test: the handle's lifetime rules are the subtle part of this function, and
// getting them wrong is a use-after-free rather than a visible failure.
func copyAllPages(backup onlineBackup) error {
	// Step(-1) copies every remaining page in a single call, holding a
	// read lock on the source for the whole copy, instead of stepping in
	// batches and letting writers in between steps.
	//
	// That is the right trade for this snapshot. When the source is
	// written to between steps by a *different* connection, SQLite
	// silently restarts the backup from page one — so a batched loop on
	// a busy production database is not "friendlier to writers", it is a
	// livelock risk that gets worse the bigger the database and the
	// busier the server, and it is precisely the nightly-rehearsal case
	// where the copy is largest and the instance is still serving. One
	// Step(-1) either produces a point-in-time image of the database or
	// fails; it cannot spin. In WAL mode (Open() sets it) the read lock
	// does not block writers anyway — they append to the WAL while we
	// read the pages behind them — so the cost is bounded to holding one
	// pooled connection and delaying WAL checkpointing for the duration.
	//
	// The flip side, stated plainly: a single Step is not interruptible,
	// so ctx cancellation cannot cut a copy that has already started. The
	// callers here (boot-time snapshot, nightly rehearsal) want the copy
	// to finish more than they want to abandon it midway.
	more, stepErr := backup.Step(-1)

	// Read the progress counters HERE, while the handle is still alive. They
	// are only used by the incomplete-copy branch below, but that branch runs
	// after Finish, and Finish does not merely release the object — it frees
	// it. modernc's Backup.Finish calls sqlite3_backup_finish, which ends in
	// sqlite3_free(p), and Remaining/PageCount dereference that same pointer
	// with no validity check. Reading them after Finish would therefore build
	// the "snapshot incomplete" message out of freed memory: nonsense page
	// counts if the block happens to still be mapped, a segfault if it is
	// not — and this runs at boot, before the migrations, so the failure mode
	// is a crash from the very code whose job is to leave a rollback point.
	// Two cheap reads on the happy path are the price of that not happening.
	remaining, pageCount := backup.Remaining(), backup.PageCount()

	// Finish releases the sqlite3_backup object AND closes the
	// destination connection; it must run on every path, including the
	// error paths, or the destination handle leaks for the life of the
	// process. Its own return value matters too: sqlite3_backup_finish
	// is where an I/O error from the last step surfaces, so a copy is
	// only good if BOTH calls came back clean. Prefer the step error
	// when both fail — it is the closer cause.
	finishErr := backup.Finish()

	switch {
	case stepErr != nil:
		return fmt.Errorf("copy pages into snapshot: %w", stepErr)
	case finishErr != nil:
		return fmt.Errorf("finalize snapshot: %w", finishErr)
	case more:
		// Step(-1) returns false ("SQLITE_DONE") when it has copied
		// everything. A true here means pages remain despite asking for
		// all of them; treat the file as partial rather than hand the
		// operator a truncated database that opens fine.
		return fmt.Errorf("snapshot incomplete: %d of %d pages left after a full step",
			remaining, pageCount)
	}
	return nil
}

// removeSnapshotArtifacts deletes a failed snapshot and any sidecar files
// SQLite may have left beside it. The destination is opened as an ordinary
// database, so a crash mid-copy can leave a rollback journal (and, if a
// future driver default changes, WAL/SHM) next to it; leaving those behind
// would make a later snapshot at the same path recover into a Frankenstein
// database.
func removeSnapshotArtifacts(dstPath string) {
	for _, suffix := range []string{"", "-journal", "-wal", "-shm"} {
		_ = os.Remove(dstPath + suffix)
	}
}

// pendingMigrationRange returns (currentMaxAppliedVersion, targetVersion,
// pendingCount). If the _migrations table does not exist yet, fromVersion is
// 0 — caller treats this as "fresh install, nothing to back up" via the
// file-existence check above, but we still report it accurately here for
// callers that may want the info.
func pendingMigrationRange(ctx context.Context, db *sql.DB) (from, to, pending int, err error) {
	var hasTable int
	err = db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='_migrations'`,
	).Scan(&hasTable)
	if err != nil {
		return 0, 0, 0, err
	}
	if hasTable == 0 {
		// No migrations table → all migrations are pending, but there's also
		// no existing schema to protect. Return 0/0/0 to signal "skip backup".
		return 0, 0, 0, nil
	}

	var maxApplied sql.NullInt64
	if err = db.QueryRowContext(ctx, `SELECT MAX(version) FROM _migrations`).Scan(&maxApplied); err != nil {
		return 0, 0, 0, err
	}
	from = int(maxApplied.Int64)

	if len(migrations) == 0 {
		return from, from, 0, nil
	}
	to = migrations[len(migrations)-1].version

	for _, m := range migrations {
		if m.version <= from {
			continue
		}
		pending++
	}
	return from, to, pending, nil
}

// pruneOldSnapshots keeps the most recent `keep` snapshots and deletes the
// rest. Snapshots are matched by the prefix "<db>.pre-migrate-" so we never
// touch unrelated files even if they happen to live in the same directory.
func pruneOldSnapshots(dbPath string, keep int, logger *slog.Logger) error {
	dir := filepath.Dir(dbPath)
	base := filepath.Base(dbPath)
	prefix := base + ".pre-migrate-"

	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}

	type snap struct {
		path  string
		mtime time.Time
	}
	var snaps []snap
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), prefix) || !strings.HasSuffix(e.Name(), ".bak") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		snaps = append(snaps, snap{path: filepath.Join(dir, e.Name()), mtime: info.ModTime()})
	}
	if len(snaps) <= keep {
		return nil
	}
	sort.Slice(snaps, func(i, j int) bool { return snaps[i].mtime.After(snaps[j].mtime) })
	for _, s := range snaps[keep:] {
		if err := os.Remove(s.path); err != nil {
			logger.Warn("remove old snapshot failed", "path", s.path, "error", err)
		}
	}
	return nil
}

// PendingMigrations reports the schema upgrade a Migrate call would perform:
// the highest applied version, the highest known version, and how many would
// be applied. On a database with no _migrations table it returns 0/0/0 —
// "fresh install", which callers must distinguish from "up to date" (also
// 0 pending) by the fromVersion.
//
// Exported so a caller can decide NOT to migrate. A diagnostic or read-only
// command that opens the local database should not silently apply a schema
// upgrade: the snapshot and the secrets bootstrap that make an upgrade safe
// live on the server's startup path, and an upgrade taken without them is one
// that cannot be rolled back — and, for the migrations that key off
// ENCRYPTION_KEY, one that produces permanently wrong data.
func PendingMigrations(ctx context.Context, db *sql.DB) (from, to, pending int, err error) {
	return pendingMigrationRange(ctx, db)
}
