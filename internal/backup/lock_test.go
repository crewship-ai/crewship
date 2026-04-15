package backup

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// newLockTestDB returns an in-memory SQLite DB with just the tables
// needed for lock tests. We intentionally bypass the full migration
// framework so the tests stay scoped to this package.
func newLockTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	// Minimal workspaces table to satisfy the FK in backup_locks.
	_, err = db.Exec(`
		CREATE TABLE workspaces (id TEXT PRIMARY KEY);
		CREATE TABLE backup_locks (
			workspace_id TEXT PRIMARY KEY REFERENCES workspaces(id) ON DELETE CASCADE,
			acquired_at TEXT NOT NULL,
			acquired_by TEXT NOT NULL,
			expires_at TEXT NOT NULL
		);
		INSERT INTO workspaces (id) VALUES ('ws_test');
		INSERT INTO workspaces (id) VALUES ('ws_other');
	`)
	if err != nil {
		t.Fatalf("schema: %v", err)
	}
	return db
}

func TestAcquireWorkspaceLock_OK(t *testing.T) {
	db := newLockTestDB(t)
	mgr := NewSQLLockManager(db)
	release, err := mgr.AcquireWorkspaceLock(context.Background(), "ws_test", "user_1", 0)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if release == nil {
		t.Fatal("release func is nil")
	}
	// The row should exist.
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM backup_locks WHERE workspace_id = 'ws_test'`).Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 lock row, got %d", count)
	}
	if err := release(); err != nil {
		t.Errorf("release: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM backup_locks WHERE workspace_id = 'ws_test'`).Scan(&count); err != nil {
		t.Fatalf("count after release: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 lock rows after release, got %d", count)
	}
}

func TestAcquireWorkspaceLock_ConflictSameWorkspace(t *testing.T) {
	db := newLockTestDB(t)
	mgr := NewSQLLockManager(db)
	release, err := mgr.AcquireWorkspaceLock(context.Background(), "ws_test", "user_1", 0)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	defer func() { _ = release() }()

	_, err = mgr.AcquireWorkspaceLock(context.Background(), "ws_test", "user_2", 0)
	if !errors.Is(err, ErrLockHeld) {
		t.Errorf("second acquire should return ErrLockHeld, got %v", err)
	}
}

func TestAcquireWorkspaceLock_DifferentWorkspacesConcurrent(t *testing.T) {
	// Two workspaces can each hold their own lock simultaneously — the
	// lock is per-workspace, not global.
	db := newLockTestDB(t)
	mgr := NewSQLLockManager(db)

	rel1, err := mgr.AcquireWorkspaceLock(context.Background(), "ws_test", "u", 0)
	if err != nil {
		t.Fatalf("ws_test acquire: %v", err)
	}
	defer func() { _ = rel1() }()

	rel2, err := mgr.AcquireWorkspaceLock(context.Background(), "ws_other", "u", 0)
	if err != nil {
		t.Fatalf("ws_other acquire: %v", err)
	}
	defer func() { _ = rel2() }()
}

func TestAcquireWorkspaceLock_ExpiredReclaimable(t *testing.T) {
	db := newLockTestDB(t)
	// Manually insert a stale lock (expired one minute ago).
	past := time.Now().UTC().Add(-10 * time.Minute).Format(time.RFC3339)
	expiredAt := time.Now().UTC().Add(-1 * time.Minute).Format(time.RFC3339)
	if _, err := db.Exec(
		`INSERT INTO backup_locks (workspace_id, acquired_at, acquired_by, expires_at)
		 VALUES ('ws_test', ?, 'ghost_user', ?)`,
		past, expiredAt,
	); err != nil {
		t.Fatalf("seed stale lock: %v", err)
	}

	mgr := NewSQLLockManager(db)
	release, err := mgr.AcquireWorkspaceLock(context.Background(), "ws_test", "new_user", 0)
	if err != nil {
		t.Fatalf("reclaim expired: %v", err)
	}
	defer func() { _ = release() }()

	var user string
	if err := db.QueryRow(`SELECT acquired_by FROM backup_locks WHERE workspace_id = 'ws_test'`).Scan(&user); err != nil {
		t.Fatalf("read lock: %v", err)
	}
	if user != "new_user" {
		t.Errorf("stale lock not reclaimed; acquired_by = %q, want %q", user, "new_user")
	}
}

func TestRelease_Idempotent(t *testing.T) {
	db := newLockTestDB(t)
	mgr := NewSQLLockManager(db)
	release, err := mgr.AcquireWorkspaceLock(context.Background(), "ws_test", "u", 0)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if err := release(); err != nil {
		t.Fatalf("first release: %v", err)
	}
	if err := release(); err != nil {
		t.Errorf("second release should be idempotent, got %v", err)
	}
}

func TestIsLockHeld(t *testing.T) {
	db := newLockTestDB(t)
	ctx := context.Background()
	now := time.Now().UTC()

	// No row yet.
	held, err := IsLockHeld(ctx, db, "ws_test", now)
	if err != nil {
		t.Fatalf("IsLockHeld: %v", err)
	}
	if held {
		t.Error("expected not held before acquire")
	}

	mgr := NewSQLLockManager(db)
	release, err := mgr.AcquireWorkspaceLock(ctx, "ws_test", "u", time.Hour)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer func() { _ = release() }()

	held, err = IsLockHeld(ctx, db, "ws_test", now)
	if err != nil {
		t.Fatalf("IsLockHeld after acquire: %v", err)
	}
	if !held {
		t.Error("expected held after acquire")
	}

	// Stale row appears unheld.
	heldFuture, err := IsLockHeld(ctx, db, "ws_test", now.Add(2*time.Hour))
	if err != nil {
		t.Fatalf("IsLockHeld future: %v", err)
	}
	if heldFuture {
		t.Error("expected not held when querying after TTL")
	}
}
