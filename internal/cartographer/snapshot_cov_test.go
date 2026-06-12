package cartographer

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// openPartialDB builds an in-memory DB with only the listed tables so we
// can hit each SQL error branch in Capture deterministically.
func openPartialDB(t *testing.T, tables ...string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	db.SetMaxOpenConns(1)

	ddl := map[string]string{
		"journal_entries": `CREATE TABLE journal_entries (
			id TEXT PRIMARY KEY, mission_id TEXT, ts TEXT NOT NULL DEFAULT (datetime('now')))`,
		"mission_tasks": `CREATE TABLE mission_tasks (
			id TEXT PRIMARY KEY, mission_id TEXT, status TEXT, assignment_id TEXT)`,
		"assignments": `CREATE TABLE assignments (id TEXT PRIMARY KEY, status TEXT)`,
	}
	for _, tbl := range tables {
		if _, err := db.Exec(ddl[tbl]); err != nil {
			t.Fatalf("create %s: %v", tbl, err)
		}
	}
	return db
}

// TestCapture_RequiresMissionID pins the validation guard.
func TestCapture_RequiresMissionID(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	_, _, err := Capture(context.Background(), db, "")
	if err == nil || !strings.Contains(err.Error(), "mission_id") {
		t.Errorf("expected mission_id error, got %v", err)
	}
}

// TestCapture_SQLErrorBranches forces each of the three queries inside
// Capture to fail independently by leaving its table out of the schema,
// proving every failure surfaces as a wrapped error instead of a partial
// snapshot.
func TestCapture_SQLErrorBranches(t *testing.T) {
	ctx := context.Background()

	t.Run("cursor lookup fails", func(t *testing.T) {
		db := openPartialDB(t, "mission_tasks", "assignments") // no journal_entries
		_, _, err := Capture(ctx, db, "mis_1")
		if err == nil || !strings.Contains(err.Error(), "cursor") {
			t.Errorf("expected cursor lookup error, got %v", err)
		}
	})

	t.Run("pending tasks fails", func(t *testing.T) {
		db := openPartialDB(t, "journal_entries", "assignments") // no mission_tasks
		_, _, err := Capture(ctx, db, "mis_1")
		if err == nil || !strings.Contains(err.Error(), "pending tasks") {
			t.Errorf("expected pending tasks error, got %v", err)
		}
	})

	t.Run("open assignments fails", func(t *testing.T) {
		db := openPartialDB(t, "journal_entries", "mission_tasks") // no assignments
		_, _, err := Capture(ctx, db, "mis_1")
		if err == nil || !strings.Contains(err.Error(), "open assignments") {
			t.Errorf("expected open assignments error, got %v", err)
		}
	})
}

// TestCapture_EmptyMissionYieldsEmptyCursor pins the "snapshot at moment
// of creation" contract: no journal entries → empty cursor, no error,
// and initialized (non-nil) snapshot containers.
func TestCapture_EmptyMissionYieldsEmptyCursor(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	snap, cursor, err := Capture(context.Background(), db, "mis_1")
	if err != nil {
		t.Fatalf("capture: %v", err)
	}
	if cursor != "" {
		t.Errorf("cursor = %q, want empty for journal-less mission", cursor)
	}
	if snap.AgentMemory == nil || snap.PendingTasks == nil || snap.OpenAssignments == nil {
		t.Errorf("snapshot containers not initialized: %+v", snap)
	}
	if len(snap.PendingTasks) != 0 || len(snap.OpenAssignments) != 0 {
		t.Errorf("expected empty snapshot, got %+v", snap)
	}
}

// TestCaptureMemoryDir_Validation pins the guard clauses: nil snapshot
// and empty agent key are errors; a path that is a FILE (not a dir) is
// rejected by hashDir and propagates out.
func TestCaptureMemoryDir_Validation(t *testing.T) {
	if err := CaptureMemoryDir(nil, "agent", t.TempDir()); err == nil {
		t.Error("nil snapshot accepted")
	}
	snap := &StateSnapshot{}
	if err := CaptureMemoryDir(snap, "", t.TempDir()); err == nil {
		t.Error("empty agent key accepted")
	}

	// dir is actually a file → "not a directory" error.
	f := filepath.Join(t.TempDir(), "plain.txt")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	err := CaptureMemoryDir(snap, "agent", f)
	if err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Errorf("expected not-a-directory error, got %v", err)
	}
}

// TestHashDir_UnreadableFileStillHashes pins the permission-blip branch:
// an unreadable file contributes its path (but no bytes) to the digest,
// so hashing stays deterministic and error-free.
func TestHashDir_UnreadableFileStillHashes(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root; chmod 000 still readable")
	}
	dir := t.TempDir()
	readable := filepath.Join(dir, "a.md")
	locked := filepath.Join(dir, "b.md")
	if err := os.WriteFile(readable, []byte("hello"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(locked, []byte("secret"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o644) })

	h1, err := hashDir(dir)
	if err != nil {
		t.Fatalf("hashDir with unreadable file errored: %v", err)
	}
	if h1 == "" {
		t.Fatal("empty digest")
	}
	// Deterministic across calls even with the unreadable file present.
	h2, err := hashDir(dir)
	if err != nil {
		t.Fatalf("hashDir second pass: %v", err)
	}
	if h1 != h2 {
		t.Errorf("digest unstable with unreadable file: %q vs %q", h1, h2)
	}

	// The unreadable file's CONTENT must not be part of the digest:
	// changing bytes behind the 000 mask cannot be observed, but making
	// it readable again must change the digest (bytes now included).
	if err := os.Chmod(locked, 0o644); err != nil {
		t.Fatalf("chmod back: %v", err)
	}
	h3, err := hashDir(dir)
	if err != nil {
		t.Fatalf("hashDir readable pass: %v", err)
	}
	if h3 == h1 {
		t.Errorf("digest identical with and without file bytes — content not hashed?")
	}
}
