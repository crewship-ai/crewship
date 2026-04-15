package backup

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// DefaultLockTTL is how long an acquired backup lock stays valid before
// another caller can reclaim it. One hour is ample for the largest
// bundles we expect; a crashed backup self-heals after this window
// without operator intervention.
const DefaultLockTTL = 1 * time.Hour

// ReleaseFunc releases a previously acquired workspace lock. It is
// idempotent — calling it multiple times is safe. If the lock expired
// and was reclaimed by another caller before Release was invoked, the
// function still returns nil (the broken lock is no longer ours to
// release, and there is nothing useful we can do about it from here).
//
// The caller's context is respected for cancellation and deadlines; pass
// context.Background() if none is available. A nil context is treated
// as context.Background() so old callers (and defensive deferred
// releases) do not panic.
type ReleaseFunc func(ctx context.Context) error

// LockManager provides the advisory-lock operations the backup flow
// needs. It is satisfied by *sql.DB in production and by in-memory
// fakes in tests.
type LockManager interface {
	// AcquireWorkspaceLock inserts a row into backup_locks for the
	// given workspace. Returns ErrLockHeld if another backup is
	// already in progress and the prior lock has not expired.
	AcquireWorkspaceLock(ctx context.Context, workspaceID, userID string, ttl time.Duration) (ReleaseFunc, error)
}

// SQLLockManager implements LockManager against the main Crewship DB.
type SQLLockManager struct {
	DB *sql.DB
	// Now lets tests inject a deterministic clock.
	Now func() time.Time
}

// NewSQLLockManager returns a LockManager backed by db using wall-clock time.
func NewSQLLockManager(db *sql.DB) *SQLLockManager {
	return &SQLLockManager{DB: db, Now: time.Now}
}

// AcquireWorkspaceLock implements LockManager. It runs in a single
// transaction so the read-then-write sequence is atomic even under
// SQLite's default isolation. We first evict any row whose expires_at
// is in the past (that backup crashed or was abandoned), then attempt
// to insert our own row. PK conflict = another live backup is holding
// the lock.
func (m *SQLLockManager) AcquireWorkspaceLock(ctx context.Context, workspaceID, userID string, ttl time.Duration) (ReleaseFunc, error) {
	if workspaceID == "" {
		return nil, fmt.Errorf("backup: AcquireWorkspaceLock: workspace_id must be set")
	}
	if ttl <= 0 {
		ttl = DefaultLockTTL
	}
	now := m.Now().UTC()
	expires := now.Add(ttl)

	tx, err := m.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return nil, fmt.Errorf("backup: begin lock tx: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	// Evict a stale lock (expired by now). We also reap rows whose
	// expires_at is non-parseable — otherwise a corrupted write leaves
	// a ghost row that IsLockHeld treats as "not held" but PK collision
	// in the INSERT below surfaces as ErrLockHeld forever. datetime()
	// returns NULL on input that is not a recognisable SQLite datetime,
	// which lets us spot and drop the bad row in the same statement.
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM backup_locks
		 WHERE workspace_id = ?
		   AND (expires_at < ? OR datetime(expires_at) IS NULL)`,
		workspaceID, now.Format(time.RFC3339),
	); err != nil {
		return nil, fmt.Errorf("backup: evict stale lock: %w", err)
	}

	// Attempt to claim the lock. PK collision = held by someone else.
	_, err = tx.ExecContext(ctx,
		`INSERT INTO backup_locks (workspace_id, acquired_at, acquired_by, expires_at)
		 VALUES (?, ?, ?, ?)`,
		workspaceID,
		now.Format(time.RFC3339),
		userID,
		expires.Format(time.RFC3339),
	)
	if err != nil {
		// Distinguish primary-key collision (lock held by another backup)
		// from any other DB error (connection drop, FK violation on a
		// non-existent workspace_id, etc.). modernc.org/sqlite surfaces
		// the collision as a message containing "UNIQUE constraint failed"
		// or "PRIMARY KEY constraint failed"; fall back to wrapping the
		// raw error otherwise so the caller can see the root cause.
		msg := err.Error()
		if strings.Contains(msg, "UNIQUE constraint failed") ||
			strings.Contains(msg, "PRIMARY KEY constraint failed") {
			return nil, ErrLockHeld
		}
		return nil, fmt.Errorf("backup: insert lock: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("backup: commit lock: %w", err)
	}
	committed = true

	release := func(ctx context.Context) error {
		if ctx == nil {
			ctx = context.Background()
		}
		// Only release the lock if the row we see still belongs to us
		// (same acquired_at). If another caller reclaimed after TTL
		// expiry we silently succeed — their lock is legitimate.
		_, err := m.DB.ExecContext(ctx,
			`DELETE FROM backup_locks
			 WHERE workspace_id = ?
			   AND acquired_at = ?
			   AND acquired_by = ?`,
			workspaceID, now.Format(time.RFC3339), userID,
		)
		if err != nil {
			return fmt.Errorf("backup: release lock: %w", err)
		}
		return nil
	}
	return release, nil
}

// IsLockHeld reports whether a workspace currently has a live backup
// lock. Stale locks (past expires_at) are treated as not held — this
// matches the auto-eviction behaviour of AcquireWorkspaceLock.
//
// Callers in the orchestrator use this to refuse starting new agent
// runs while a backup is in flight.
func IsLockHeld(ctx context.Context, db *sql.DB, workspaceID string, now time.Time) (bool, error) {
	var expiresAt string
	err := db.QueryRowContext(ctx,
		`SELECT expires_at FROM backup_locks WHERE workspace_id = ?`,
		workspaceID,
	).Scan(&expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("backup: check lock: %w", err)
	}
	exp, err := time.Parse(time.RFC3339, expiresAt)
	if err != nil {
		// Corrupt row — treat as not held so backups can proceed; the
		// eviction path in AcquireWorkspaceLock will clean up next time.
		return false, nil
	}
	return exp.After(now.UTC()), nil
}
