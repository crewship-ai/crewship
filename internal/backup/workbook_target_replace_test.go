package backup_test

import (
	"errors"
	"strings"
	"testing"

	"filippo.io/age"

	"github.com/crewship-ai/crewship/internal/backup"
)

// Workbooks are never in a bundle, so a replace restore that wipes the target
// workspace would destroy them for good. It refuses, naming the workbook
// Pages, unless the operator explicitly acknowledges the loss.
func TestReplaceRestoreRefusesTargetWorkbooks(t *testing.T) {
	f := newInstanceFixture(t)
	bundle, err := backup.CreateBackup(t.Context(), f.db, backup.CreateOptions{
		Scope: backup.ScopeWorkspace, WorkspaceID: "ws_e2e_1", OutputDir: t.TempDir(),
		Actor:      backup.Actor{UserID: "u_admin", Role: "OWNER"},
		Recipients: []age.Recipient{f.identity.Recipient()}, ConversationRoot: f.paths.Output,
	})
	if err != nil {
		t.Fatal(err)
	}
	replacementExec(t, f.db, `INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json,private_audience) VALUES('target-book','ws_e2e_1','target-book','Target book','u_admin','{}',1)`)
	replacementExec(t, f.db, `INSERT INTO gridcraft_heads(page_id,workspace_id,schema_version,head_rev,generation,snapshot) VALUES('target-book','ws_e2e_1',1,1,1,x'7b7d')`)
	restore := func(discard bool) error {
		_, err := backup.RestoreBackup(t.Context(), f.db, backup.RestoreOptions{
			Path: bundle.Path, Identities: []age.Identity{f.identity}, Replace: true, DiscardWorkbooks: discard,
			Actor: backup.Actor{UserID: "u_admin", Role: "OWNER"}, ConversationRoot: f.paths.Output,
		})
		return err
	}
	workbooks := func() int {
		var n int
		if err := f.db.QueryRow(`SELECT count(*) FROM gridcraft_heads WHERE page_id='target-book'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	err = restore(false)
	if !errors.Is(err, backup.ErrWorkbookTargetReplacement) || !strings.Contains(err.Error(), "target-book") {
		t.Fatalf("replace over a workbook = %v, want refusal naming target-book", err)
	}
	if workbooks() != 1 {
		t.Fatal("refused replace destroyed the workbook")
	}
	// The deleting transaction repeats the check (admission races).
	tx, err := f.db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := backup.ReplaceWorkspaceContents(t.Context(), tx, "ws_e2e_1", "e2e"); !errors.Is(err, backup.ErrWorkbookTargetReplacement) {
		t.Fatalf("in-transaction replace over a workbook = %v", err)
	}
	_ = tx.Rollback()
	if err := restore(true); err != nil {
		t.Fatalf("acknowledged replace: %v", err)
	}
	if workbooks() != 0 {
		t.Fatal("acknowledged replace kept a workbook the bundle does not contain")
	}
}
