package database

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

// Restoring a pre-migration snapshot rewinds document content, so it must
// rotate the database identity the public Docs epoch is derived from (#5163),
// or open browsers would replay edits onto the older content.
func TestRestoreSnapshotRotatesDocumentDatabaseIdentity(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "crewship.db")
	ctx := context.Background()
	db := openMigratedTestDBAt(t, dbPath)
	identity := func(db *sql.DB) string {
		t.Helper()
		// First use creates the identity (documents.DatabaseIdentity).
		if _, err := db.ExecContext(ctx, `INSERT OR IGNORE INTO document_database_identity(id,value) VALUES(1,lower(hex(randomblob(16))))`); err != nil {
			t.Fatal(err)
		}
		var value string
		if err := db.QueryRowContext(ctx, `SELECT value FROM document_database_identity WHERE id=1`).Scan(&value); err != nil || len(value) != 32 {
			t.Fatalf("document database identity %q: %v", value, err)
		}
		return value
	}
	before := identity(db.DB)
	maxV := maxKnownMigrationVersion()
	snapPath := filepath.Join(dir, "crewship.db.pre-migrate-v"+itoa(maxV)+"-to-v"+itoa(maxV+1)+"-20261109T000000Z.bak")
	if err := snapshotDatabase(ctx, db.DB, snapPath); err != nil {
		t.Fatalf("stage snapshot: %v", err)
	}
	db.Close()
	if err := RestoreSnapshot(dbPath, snapPath); err != nil {
		t.Fatalf("RestoreSnapshot: %v", err)
	}
	restored, err := Open("file:" + dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if after := identity(restored.DB); after == before {
		t.Fatal("snapshot restore kept the document database identity")
	}
	if matches, _ := filepath.Glob(filepath.Join(dir, "crewship.db.restore-tmp-*")); len(matches) != 0 {
		t.Fatalf("staged restore files left behind: %v", matches)
	}
}
