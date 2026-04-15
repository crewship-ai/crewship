package api

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/crewship-ai/crewship/internal/backup"
)

// newGuardTestDB builds a minimal DB with just the backup_locks +
// workspaces tables so refuseIfBackupInProgress can probe a real
// IsLockHeld call. Kept local so test pollution stays bounded.
func newGuardTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dbPath := t.TempDir() + "/guard.db"
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`
		CREATE TABLE workspaces (id TEXT PRIMARY KEY);
		CREATE TABLE backup_locks (
			workspace_id TEXT PRIMARY KEY REFERENCES workspaces(id) ON DELETE CASCADE,
			acquired_at TEXT NOT NULL,
			acquired_by TEXT NOT NULL,
			expires_at TEXT NOT NULL
		);
		INSERT INTO workspaces (id) VALUES ('ws_test');
	`); err != nil {
		t.Fatalf("schema: %v", err)
	}
	return db
}

func TestRefuseIfBackupInProgress_NilDB(t *testing.T) {
	// Nil db no longer short-circuits (we still claim the in-process
	// guard) but a nil db + any workspace MUST still return a usable
	// release func and a nil error when no backup is active.
	rel, err := refuseIfBackupInProgress(context.Background(), nil, "ws_test_nil_db")
	if err != nil {
		t.Errorf("nil db should not fail, got %v", err)
	}
	if rel == nil {
		t.Fatal("release func must be non-nil on success")
	}
	rel()
}

func TestRefuseIfBackupInProgress_EmptyWorkspace(t *testing.T) {
	db := newGuardTestDB(t)
	rel, err := refuseIfBackupInProgress(context.Background(), db, "")
	if err != nil {
		t.Errorf("empty workspace should short-circuit, got %v", err)
	}
	if rel == nil {
		t.Fatal("release must be non-nil even on short-circuit")
	}
	rel()
}

func TestRefuseIfBackupInProgress_NoLockHeld(t *testing.T) {
	db := newGuardTestDB(t)
	rel, err := refuseIfBackupInProgress(context.Background(), db, "ws_test")
	if err != nil {
		t.Errorf("no lock held: should return nil, got %v", err)
	}
	if rel == nil {
		t.Fatal("release must be non-nil")
	}
	rel()
}

func TestRefuseIfBackupInProgress_LockHeld(t *testing.T) {
	db := newGuardTestDB(t)
	mgr := backup.NewSQLLockManager(db)
	release, err := mgr.AcquireWorkspaceLock(context.Background(), "ws_test", "user_1", time.Hour)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	defer func() { _ = release(context.Background()) }()

	rel, err := refuseIfBackupInProgress(context.Background(), db, "ws_test")
	if err == nil {
		if rel != nil {
			rel()
		}
		t.Fatal("expected error when lock is held")
	}
	// Message must mention something the admin can act on.
	if got := err.Error(); got == "" {
		t.Error("error message should be non-empty")
	}
}

func TestRefuseIfBackupInProgress_LockExpired(t *testing.T) {
	// A stale lock (past expires_at) should NOT refuse the run — the
	// TTL auto-reclaim happens on the next acquire attempt, so until
	// then IsLockHeld returns false.
	db := newGuardTestDB(t)
	past := time.Now().UTC().Add(-1 * time.Hour).Format(time.RFC3339)
	expiredAt := time.Now().UTC().Add(-30 * time.Minute).Format(time.RFC3339)
	if _, err := db.Exec(
		`INSERT INTO backup_locks (workspace_id, acquired_at, acquired_by, expires_at)
		 VALUES ('ws_test', ?, 'ghost', ?)`,
		past, expiredAt,
	); err != nil {
		t.Fatalf("seed: %v", err)
	}
	rel, err := refuseIfBackupInProgress(context.Background(), db, "ws_test")
	if err != nil {
		t.Errorf("stale lock should not refuse, got %v", err)
	}
	if rel != nil {
		rel()
	}
}
