package backup_test

import (
	"context"
	"testing"

	"github.com/crewship-ai/crewship/internal/backup"
)

// TestJournalChainCheckpoints_RoundTrips proves the #1487-audit fix: the
// signed compaction checkpoints ride the bundle and survive a restore. Before
// the fix journal_chain_checkpoints was in neither BackupTableIntent nor
// BackupTables, so it was silently dropped — and a restored journal_entries
// chain with a compaction gap but no checkpoint reads that gap as tampering,
// turning `journal verify` red for a benign reason on every restored instance.
func TestJournalChainCheckpoints_RoundTrips(t *testing.T) {
	ctx := context.Background()

	source := openMigratedDB(t)
	workspaceID := seedWorkspace(t, source)

	const (
		ckptID      = "jck_e2e_1"
		removedJSON = `[{"seq":42,"entry_hash":"deadbeef"}]`
		mac         = "signed-mac-value-abc123"
	)
	if _, err := source.ExecContext(ctx,
		`INSERT INTO journal_chain_checkpoints (id, workspace_id, removed_json, mac) VALUES (?, ?, ?, ?)`,
		ckptID, workspaceID, removedJSON, mac); err != nil {
		t.Fatalf("seed checkpoint: %v", err)
	}

	const passphrase = "checkpoint-roundtrip-pass-123"
	bundleDir := t.TempDir()
	actor := backup.Actor{UserID: "u_admin", Email: "admin@e2e.test", Role: "ADMIN"}
	created, err := backup.CreateBackup(ctx, source, backup.CreateOptions{
		Scope:       backup.ScopeWorkspace,
		WorkspaceID: workspaceID,
		OutputDir:   bundleDir,
		Actor:       actor,
		Passphrase:  passphrase,
	})
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}

	target := openMigratedDB(t)
	if _, err := backup.RestoreBackup(ctx, target, backup.RestoreOptions{
		Path:       created.Path,
		Passphrase: passphrase,
		Actor:      actor,
	}); err != nil {
		t.Fatalf("RestoreBackup: %v", err)
	}

	var gotRemoved, gotMac string
	err = target.QueryRowContext(ctx,
		`SELECT removed_json, mac FROM journal_chain_checkpoints WHERE id = ? AND workspace_id = ?`,
		ckptID, workspaceID).Scan(&gotRemoved, &gotMac)
	if err != nil {
		t.Fatalf("checkpoint missing from restored target (silent data loss): %v", err)
	}
	if gotRemoved != removedJSON || gotMac != mac {
		t.Errorf("checkpoint round-trip corrupted:\n  removed_json got=%q want=%q\n  mac got=%q want=%q",
			gotRemoved, removedJSON, gotMac, mac)
	}
}
