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

// TestForkedRestore_InboxItemReads_FollowTheirItem: since #2274 scoped
// inbox_items' UNIQUE(kind, source_id) per workspace, the item — and
// therefore its read marker — LANDS on a fork. The marker attaches to
// the fork's remapped item id, never to the source's, and no shortfall
// is reported because nothing was skipped. (The orphanGuardedChildren
// insert guard remains as a safety net for bundles that carry a marker
// without its item; this test is the proof it is a no-op on a fork.)
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
		t.Fatalf("RestoreBackup --as-workspace: %v", err)
	}
	forkID := res.RestoredWorkspaceID
	if forkID == "" || forkID == workspaceID {
		t.Fatalf("--as-workspace did not fork (got %q)", forkID)
	}
	// The original marker is untouched, still on the source's item.
	if got := countReadsForItem(t, source, itemID); got != 1 {
		t.Errorf("read markers for the original item = %d, want 1", got)
	}
	// The fork's marker landed on the fork's remapped item, so the
	// instance holds exactly two markers — one per workspace.
	var total int
	if err := source.QueryRow(`SELECT COUNT(*) FROM inbox_item_reads`).Scan(&total); err != nil {
		t.Fatal(err)
	}
	if total != 2 {
		t.Errorf("inbox_item_reads rows after the fork = %d, want 2 (source + fork)", total)
	}
	var forkMarkers int
	if err := source.QueryRow(`
		SELECT COUNT(*) FROM inbox_item_reads r
		JOIN inbox_items i ON i.id = r.inbox_item_id
		WHERE i.workspace_id = ?`, forkID).Scan(&forkMarkers); err != nil {
		t.Fatal(err)
	}
	if forkMarkers != 1 {
		t.Errorf("markers attached to the fork's item = %d, want 1", forkMarkers)
	}
	for _, m := range res.RowsInsertedShortfalls {
		if m.Table == "inbox_item_reads" {
			t.Errorf("fork reported an inbox_item_reads shortfall — the row should land: %+v", m)
		}
	}
}
