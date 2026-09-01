package database

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
)

// approvalsRetentionBackfillVersion is the companion migration that pins a
// pre-existing workspace's approvals_retention_days to 0 (keep forever). See
// its file for the full rationale; named here so the marker-clearing
// re-apply in the test below cannot drift from the filename.
const approvalsRetentionBackfillVersion = 20260901140000

func approvalsRetentionTestDB(t *testing.T) (*DB, context.Context, *slog.Logger) {
	t.Helper()
	silent := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()
	db, err := Open("file:" + filepath.Join(t.TempDir(), "approvals-retention.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	if err := Migrate(ctx, db.DB, silent); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	return db, ctx, silent
}

// TestApprovalsRetentionMigrationPinsExistingWorkspacesToKeepForever covers
// the destructive-upgrade case (#2233 CodeRabbit review): the approvals_queue
// retention sweeper performs one immediate sweep at boot
// (StartApprovalsRetentionSweeper). Without a backfill, every workspace that
// predates 20260901134904_approvals_retention_days resolves its NULL
// override to the 90-day harbormaster.DefaultApprovalsRetentionDays and every
// terminal approval older than 90 days is DELETEd on the very first restart
// after the upgrade — before the API that sets the override is even
// listening, so the operator has no chance to say "keep it" and nothing to
// recover from short of a pre-upgrade backup.
//
// The companion migration pins existing workspaces to an explicit 0 ("keep
// forever", per the 0-means-keep-forever semantics this PR gives the
// column — see retention.go's package comment) and leaves the column NULL
// for workspaces created afterwards, which get the 90-day default. New
// installs are bounded; existing installs are asked. Same technique as
// TestAuditRetentionMigrationPinsExistingWorkspacesToKeepForever for the
// credential_audit / audit_log pair this column was modelled on.
func TestApprovalsRetentionMigrationPinsExistingWorkspacesToKeepForever(t *testing.T) {
	t.Parallel()
	db, ctx, silent := approvalsRetentionTestDB(t)

	execMigrationFixture(t, db, `INSERT INTO workspaces (id, name, slug) VALUES ('ws_pre_appr', 'Pre', 'ws-pre-appr')`)
	// Reconstruct the pre-migration state for that row (a workspace that
	// existed before 20260901134904 ran gets NULL from ADD COLUMN, same as
	// this INSERT already produced — this line documents that explicitly and
	// keeps the test robust if a default ever gets added upstream), then
	// clear the backfill migration's ledger marker and re-apply.
	execMigrationFixture(t, db, `UPDATE workspaces SET approvals_retention_days = NULL WHERE id = 'ws_pre_appr'`)
	if _, err := db.Exec(`DELETE FROM _migrations WHERE version = ?`, approvalsRetentionBackfillVersion); err != nil {
		t.Fatalf("clear marker: %v", err)
	}
	if err := Migrate(ctx, db.DB, silent); err != nil {
		t.Fatalf("re-Migrate: %v", err)
	}

	var got *int
	if err := db.QueryRow(
		`SELECT approvals_retention_days FROM workspaces WHERE id = 'ws_pre_appr'`).Scan(&got); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got == nil {
		t.Fatal("approvals_retention_days is NULL for a pre-existing workspace — it will resolve to the 90-day default and the first boot sweep will delete its decided approvals history")
	}
	if *got != 0 {
		t.Errorf("approvals_retention_days = %d, want 0 (keep forever) for a workspace that predates the retention window", *got)
	}
}

// TestApprovalsRetentionMigrationLeavesNewWorkspacesNull pins the other half
// of the asymmetry: a workspace created AFTER the backfill migration ran
// must stay NULL (no opinion recorded, resolves to the 90-day default) — the
// backfill's WHERE clause must not become an unconditional UPDATE that also
// zeroes out fresh workspaces, which would silently turn off retention for
// every new install.
func TestApprovalsRetentionMigrationLeavesNewWorkspacesNull(t *testing.T) {
	t.Parallel()
	db, _, _ := approvalsRetentionTestDB(t)

	if _, err := db.Exec(
		`INSERT INTO workspaces (id, name, slug) VALUES ('ws_post_appr', 'Post', 'ws-post-appr')`); err != nil {
		t.Fatalf("insert: %v", err)
	}

	var got *int
	if err := db.QueryRow(
		`SELECT approvals_retention_days FROM workspaces WHERE id = 'ws_post_appr'`).Scan(&got); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if got != nil {
		t.Errorf("approvals_retention_days = %d, want NULL for a workspace created after the backfill — it should get the 90-day default via NULL resolution, not an explicit override", *got)
	}
}
