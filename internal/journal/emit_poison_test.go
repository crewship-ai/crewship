package journal

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

// poisonSchema builds a journal_entries table whose mission_id carries a
// FOREIGN KEY into a missions table with foreign_keys ON — so an entry that
// references a missing mission fails with the exact "FOREIGN KEY constraint
// failed" error that wedged dev3 (an orphaned mission.status_change retried
// forever at 10/s, filling the disk).
func openPoisonDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	// One shared conn so PRAGMA foreign_keys + the in-memory schema are
	// visible to every query in the pool.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`
		PRAGMA foreign_keys = ON;
		CREATE TABLE missions (id TEXT PRIMARY KEY);
		INSERT INTO missions (id) VALUES ('m_ok');
		CREATE TABLE journal_entries (
			id TEXT PRIMARY KEY,
			workspace_id TEXT NOT NULL,
			crew_id TEXT, agent_id TEXT,
			mission_id TEXT REFERENCES missions(id),
			ts TEXT NOT NULL,
			entry_type TEXT NOT NULL,
			severity TEXT NOT NULL DEFAULT 'info',
			priority TEXT NOT NULL DEFAULT 'normal',
			actor_type TEXT NOT NULL, actor_id TEXT,
			summary TEXT NOT NULL,
			payload TEXT NOT NULL DEFAULT '{}',
			refs TEXT NOT NULL DEFAULT '{}',
			trace_id TEXT, span_id TEXT, expires_at TEXT
		);
	`); err != nil {
		t.Fatalf("schema: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// TestWriter_PoisonEntry_DroppedNotRetriedForever is the regression guard for
// the dev3 disk-fill: a batch with one FK-violating entry must NOT wedge the
// stream. The good entries commit; the poison is dropped (once), and the
// writer keeps accepting new entries afterward.
func TestWriter_PoisonEntry_DroppedNotRetriedForever(t *testing.T) {
	db := openPoisonDB(t)
	w := NewWriter(db, quietLogger(), WriterOptions{
		QueueSize:     16,
		FlushSize:     3,
		FlushInterval: 1 * time.Hour,
	})
	ctx := context.Background()

	emit := func(mission, summary string) {
		// mission_id is nullable→FK: a real id ('m_ok') passes, a missing
		// one ('m_ghost') violates the FOREIGN KEY exactly like dev3.
		_, _ = w.Emit(ctx, Entry{
			WorkspaceID: "ws1",
			MissionID:   mission,
			Type:        EntryRunStarted,
			ActorType:   ActorAgent,
			Summary:     summary,
		})
	}
	emit("m_ok", "valid-1")
	emit("m_ghost", "POISON") // FK violation: m_ghost doesn't exist
	emit("m_ok", "valid-2")

	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// The two valid entries must be persisted; the poison must be gone.
	var n int64
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM journal_entries").Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 2 {
		t.Fatalf("committed rows = %d, want 2 (both valid entries, poison dropped)", n)
	}
	// The poison row specifically must not have sneaked in.
	var poison int64
	if err := db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM journal_entries WHERE summary = 'POISON'").Scan(&poison); err != nil {
		t.Fatalf("count poison: %v", err)
	}
	if poison != 0 {
		t.Fatalf("poison entry was committed (%d) — FK should have rejected it", poison)
	}
}

func TestIsPermanentDBError(t *testing.T) {
	permanent := []error{
		errors.New("journal: insert mission.status_change: constraint failed: FOREIGN KEY constraint failed (787)"),
		errors.New("journal: insert x: constraint failed: UNIQUE constraint failed: journal_entries.id"),
		errors.New("journal: marshal payload: unsupported type"),
	}
	for _, e := range permanent {
		if !isPermanentDBError(e) {
			t.Errorf("isPermanentDBError(%q) = false, want true", e)
		}
	}
	transient := []error{
		nil,
		errors.New("journal: begin tx: database is locked"),
		errors.New("journal: prepare: database table is locked"),
		context.DeadlineExceeded,
		errors.New("dial tcp: connection refused"),
	}
	for _, e := range transient {
		if isPermanentDBError(e) {
			t.Errorf("isPermanentDBError(%v) = true, want false", e)
		}
	}
}
