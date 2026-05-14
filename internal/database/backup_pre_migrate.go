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

	// VACUUM INTO writes a consistent snapshot using SQLite's own copy
	// machinery — safer than a plain file copy because it serializes against
	// concurrent writers and produces a defragmented, WAL-checkpointed file.
	// The destination must not exist; the timestamped name guarantees that.
	if _, err := db.ExecContext(ctx, "VACUUM INTO ?", backupPath); err != nil {
		return fmt.Errorf("VACUUM INTO snapshot: %w", err)
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
