package database

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
)

// The in-memory snapshot: a point-in-time copy that reads like the source,
// serializes to a database file image, and never creates a file.
func TestSnapshotToMemory(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	db := newHotJournalDB(t, filepath.Join(dir, "live.db"))
	defer db.Close()
	before, _ := os.ReadDir(dir)

	snap, err := SnapshotToMemory(ctx, db.DB)
	if err != nil {
		t.Fatal(err)
	}
	// A write after the snapshot is not in it.
	if _, err := db.ExecContext(ctx, `INSERT INTO journal_entries (id, workspace_id, entry_type, summary) VALUES ('after', 'w', 't', 's')`); err != nil {
		t.Fatal(err)
	}
	var n, live int
	if err := snap.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM journal_entries`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM journal_entries`).Scan(&live)
	if n != journalRowsSeeded || live != journalRowsSeeded+1 {
		t.Fatalf("snapshot %d rows, live %d", n, live)
	}
	img, err := snap.Serialize(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(img, []byte("SQLite format 3\x00")) {
		t.Fatalf("image does not start with the SQLite header: %q", img[:16])
	}
	// The image opens as a database with the snapshot's rows.
	p := filepath.Join(t.TempDir(), "img.db")
	if err := os.WriteFile(p, img, 0o600); err != nil {
		t.Fatal(err)
	}
	re, err := Open("file:" + p)
	if err != nil {
		t.Fatal(err)
	}
	defer re.Close()
	if err := re.QueryRowContext(ctx, `SELECT COUNT(*) FROM journal_entries`).Scan(&n); err != nil || n != journalRowsSeeded {
		t.Fatalf("image rows = %d, %v", n, err)
	}
	if err := snap.Close(); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadDir(dir)
	if len(after) != len(before) {
		t.Fatalf("the snapshot created files beside the database: %v -> %v", before, after)
	}
	if _, err := snap.Serialize(ctx); err == nil {
		t.Fatal("a closed snapshot serialized")
	}
}
