package backup_test

import (
	"context"
	"testing"

	"github.com/crewship-ai/crewship/internal/backup"
)

// journal_entries.run_id is a VIRTUAL generated column. The dump's SELECT *
// returns it, PRAGMA table_info does not list it, and SQLite recomputes it on
// insert, so nothing is lost. The restore used to report it as a column the
// target lacks ("1 bundle column(s) did NOT land"), sending an operator after
// schema skew that does not exist.
func TestRestore_GeneratedColumnIsNotReportedDropped(t *testing.T) {
	ctx := context.Background()
	db := openMigratedDB(t)
	workspaceID := seedWorkspace(t, db)
	seedJournalChain(t, db, workspaceID, 3)

	const passphrase = "generated-column-pass-123"
	actor := backup.Actor{UserID: "u_admin", Email: "admin@e2e.test", Role: "ADMIN"}
	created, err := backup.CreateBackup(ctx, db, backup.CreateOptions{
		Scope: backup.ScopeWorkspace, WorkspaceID: workspaceID, OutputDir: t.TempDir(),
		Actor: actor, Passphrase: passphrase,
	})
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}
	for _, dry := range []bool{true, false} {
		res, err := backup.RestoreBackup(ctx, db, backup.RestoreOptions{
			Path: created.Path, Passphrase: passphrase, Actor: actor,
			AsWorkspace: "generated-column-fork", DryRun: dry,
		})
		if err != nil {
			t.Fatalf("RestoreBackup dry=%v: %v", dry, err)
		}
		for _, d := range res.DroppedColumns {
			if d.Table == "journal_entries" && d.Column == "run_id" {
				t.Errorf("dry=%v: generated column journal_entries.run_id reported as dropped (%d rows)", dry, d.Rows)
			}
		}
	}
}
