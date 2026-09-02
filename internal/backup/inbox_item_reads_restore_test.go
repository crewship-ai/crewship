package backup_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/crewship-ai/crewship/internal/backup"
)

// seedInboxItemWithRead puts one inbox item in the workspace and marks it
// read by u_admin (the user seedWorkspace creates), returning the item id.
func seedInboxItemWithRead(t *testing.T, db *sql.DB, workspaceID string) string {
	t.Helper()
	const itemID = "inb_read_roundtrip"
	if _, err := db.Exec(`INSERT INTO inbox_items (id, workspace_id, kind, source_id, title)
		VALUES (?, ?, 'message', 'src_read_roundtrip', 'read me')`, itemID, workspaceID); err != nil {
		t.Fatalf("seed inbox item: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO inbox_item_reads (inbox_item_id, user_id) VALUES (?, 'u_admin')`, itemID); err != nil {
		t.Fatalf("seed read marker: %v", err)
	}
	return itemID
}

func countReadsForItem(t *testing.T, db *sql.DB, itemID string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM inbox_item_reads WHERE inbox_item_id = ?`, itemID).Scan(&n); err != nil {
		t.Fatalf("count read markers: %v", err)
	}
	return n
}

// TestRestore_InboxItemReads_LandWithTheirItem: a plain restore into an empty
// instance brings the item AND the per-user read marker back, so the guard
// that protects a fork (below) is not over-eager on the ordinary path.
func TestRestore_InboxItemReads_LandWithTheirItem(t *testing.T) {
	ctx := context.Background()
	source := openMigratedDB(t)
	workspaceID := seedWorkspace(t, source)
	itemID := seedInboxItemWithRead(t, source, workspaceID)

	const passphrase = "inbox-reads-pass-123"
	actor := backup.Actor{UserID: "u_admin", Email: "admin@e2e.test", Role: "ADMIN"}
	created, err := backup.CreateBackup(ctx, source, backup.CreateOptions{
		Scope: backup.ScopeWorkspace, WorkspaceID: workspaceID,
		OutputDir: t.TempDir(), Actor: actor, Passphrase: passphrase,
	})
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}

	target := openMigratedDB(t)
	res, err := backup.RestoreBackup(ctx, target, backup.RestoreOptions{
		Path: created.Path, Passphrase: passphrase, Actor: actor,
	})
	if err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}
	if got := countReadsForItem(t, target, itemID); got != 1 {
		t.Errorf("read markers for %s on the target = %d, want 1", itemID, got)
	}
	for _, m := range res.RowsInsertedShortfalls {
		if m.Table == "inbox_item_reads" {
			t.Errorf("plain restore reported an inbox_item_reads shortfall: %+v", m)
		}
	}
}

// TestForkedRestore_InboxItemReads_SkippedAndReported: on a fork the item
// itself does not land (inbox_items is UNIQUE(kind, source_id) instance-wide,
// #2274), so its read marker has no parent. The restore must neither abort on
// the deferred FK check nor pretend the marker landed: it is skipped, and the
// skip shows up in rows_inserted_shortfalls.
func TestForkedRestore_InboxItemReads_SkippedAndReported(t *testing.T) {
	ctx := context.Background()
	source := openMigratedDB(t)
	workspaceID := seedWorkspace(t, source)
	itemID := seedInboxItemWithRead(t, source, workspaceID)

	const passphrase = "inbox-reads-fork-pass-123"
	actor := backup.Actor{UserID: "u_admin", Email: "admin@e2e.test", Role: "ADMIN"}
	created, err := backup.CreateBackup(ctx, source, backup.CreateOptions{
		Scope: backup.ScopeWorkspace, WorkspaceID: workspaceID,
		OutputDir: t.TempDir(), Actor: actor, Passphrase: passphrase,
	})
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}

	res, err := backup.RestoreBackup(ctx, source, backup.RestoreOptions{
		Path: created.Path, Passphrase: passphrase, Actor: actor,
		AsWorkspace: "inbox-reads-fork",
	})
	if err != nil {
		t.Fatalf("RestoreBackup --as-workspace: %v (the read marker must be skipped, not abort the restore)", err)
	}
	if res.RestoredWorkspaceID == "" || res.RestoredWorkspaceID == workspaceID {
		t.Fatalf("--as-workspace did not fork (got %q)", res.RestoredWorkspaceID)
	}
	// The original marker is untouched; nothing new attached to anything.
	if got := countReadsForItem(t, source, itemID); got != 1 {
		t.Errorf("read markers for the original item = %d, want 1", got)
	}
	var total int
	if err := source.QueryRow(`SELECT COUNT(*) FROM inbox_item_reads`).Scan(&total); err != nil {
		t.Fatal(err)
	}
	if total != 1 {
		t.Errorf("inbox_item_reads rows after the fork = %d, want 1 (the fork's marker had no item to attach to)", total)
	}
	reported := false
	for _, m := range res.RowsInsertedShortfalls {
		if m.Table == "inbox_item_reads" {
			reported = true
		}
	}
	if !reported {
		t.Errorf("the skipped read marker was not reported in rows_inserted_shortfalls: %+v", res.RowsInsertedShortfalls)
	}
}
