package backup_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/runauthority"
)

func TestRestoreFencesWebReceiptsWithoutGrantingAutonomousWork(t *testing.T) {
	source := openMigratedDB(t)
	workspace := seedChatEntityHistory(t, source)
	if _, err := source.Exec(`INSERT INTO workspace_members(id,workspace_id,user_id,role) VALUES('restore-web-member',?,'u_admin','ADMIN')`, workspace); err != nil {
		t.Fatal(err)
	}
	tx, err := source.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	origin, err := runauthority.CaptureWebOrigin(t.Context(), tx, "web_issue", "history-live", "u_admin", workspace)
	if err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	dump, err := backup.DumpWorkspace(t.Context(), source, workspace)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = source.Exec(`UPDATE workspace_members SET web_execution_revision=25 WHERE id='restore-web-member'`); err != nil {
		t.Fatal(err)
	}
	for _, replace := range []bool{false, true} {
		t.Run(map[bool]string{false: "merge", true: "replace"}[replace], func(t *testing.T) {
			hooks := &backup.RestoreDumpHooks{}
			if replace {
				hooks.PreInsert = func(ctx context.Context, tx *sql.Tx) error {
					_, e := tx.ExecContext(ctx, `DELETE FROM workspace_members WHERE id='restore-web-member'`)
					return e
				}
			}
			if _, err := backup.RestoreDumpTxHooks(t.Context(), source, dump, hooks); err != nil {
				t.Fatal(err)
			}
			if _, err := runauthority.ValidateOriginReceipt(t.Context(), source, origin.ID, workspace); err == nil {
				t.Fatal("restored old receipt regained authority")
			}
			var epoch int64
			if err := source.QueryRow(`SELECT web_execution_revision FROM workspace_members WHERE id='restore-web-member'`).Scan(&epoch); err != nil || epoch <= 25 {
				t.Fatalf("restore reused old epoch %d: %v", epoch, err)
			}
			// History remains immutable, and an explicit fresh authenticated action works.
			var old string
			if err := source.QueryRow(`SELECT human_authority FROM execution_origins WHERE id=?`, origin.ID).Scan(&old); err != nil || old == "{}" {
				t.Fatalf("lost historical receipt: %s %v", old, err)
			}
			tx, err := source.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			fresh, err := runauthority.CaptureWebOrigin(t.Context(), tx, "web_issue", "history-live", "u_admin", workspace)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := runauthority.ValidateOriginReceipt(t.Context(), tx, fresh.ID, workspace); err != nil {
				t.Fatalf("fresh admission rejected: %v", err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
