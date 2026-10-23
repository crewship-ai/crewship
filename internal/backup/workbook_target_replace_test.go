package backup_test

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"

	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/database"
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
	replacementExec(t, f.db, `INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json,private_audience) VALUES('wb-page-7','ws_e2e_1','quarterly-plan','Target book','u_admin','{}',1)`)
	replacementExec(t, f.db, `INSERT INTO gridcraft_heads(page_id,workspace_id,schema_version,head_rev,generation,snapshot) VALUES('wb-page-7','ws_e2e_1',1,1,1,x'7b7d')`)
	restore := func(discard bool) error {
		_, err := backup.RestoreBackup(t.Context(), f.db, backup.RestoreOptions{
			Path: bundle.Path, Identities: []age.Identity{f.identity}, Replace: true, DiscardWorkbooks: discard,
			Actor: backup.Actor{UserID: "u_admin", Role: "OWNER"}, ConversationRoot: f.paths.Output,
		})
		return err
	}
	workbooks := func() int {
		var n int
		if err := f.db.QueryRow(`SELECT count(*) FROM gridcraft_heads WHERE page_id='wb-page-7'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	err = restore(false)
	// The refusal names opaque page IDs, never slugs: a slug is usually
	// derived from a private workbook's title.
	if !errors.Is(err, backup.ErrWorkbookTargetReplacement) || !strings.Contains(err.Error(), "wb-page-7") || strings.Contains(err.Error(), "quarterly-plan") {
		t.Fatalf("replace over a workbook = %v, want refusal naming page ID wb-page-7 and no slug", err)
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

// Offline recovery with --force replaces the whole database, and no bundle
// contains a workbook, so it refuses exactly like a replace restore unless the
// operator acknowledges the loss with DiscardWorkbooks (--discard-workbooks).
func TestForceRecoverRefusesTargetWorkbooks(t *testing.T) {
	f := newInstanceFixture(t)
	bundle := f.create(t, true)
	dir := filepath.Join(t.TempDir(), "installation")
	opts := backup.RecoverOptions{BundlePath: bundle.Path, Identities: []age.Identity{f.identity}, DataDir: dir, Actor: "u_admin"}
	report, err := backup.RecoverInstance(t.Context(), opts)
	if err != nil {
		t.Fatalf("prepare earlier installation: %v", err)
	}
	workbooks := func() int {
		target, err := database.Open("file:" + report.DatabasePath)
		if err != nil {
			t.Fatal(err)
		}
		defer target.Close()
		var n int
		if err := target.DB.QueryRow(`SELECT count(*) FROM gridcraft_heads WHERE page_id='wb-page-7'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	target, err := database.Open("file:" + report.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	replacementExec(t, target.DB, `INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json,private_audience) VALUES('wb-page-7','ws_e2e_1','quarterly-plan','Target book','u_admin','{}',1)`)
	replacementExec(t, target.DB, `INSERT INTO gridcraft_heads(page_id,workspace_id,schema_version,head_rev,generation,snapshot) VALUES('wb-page-7','ws_e2e_1',1,1,1,x'7b7d')`)
	if err := target.Close(); err != nil {
		t.Fatal(err)
	}
	opts.Force = true
	_, err = backup.RecoverInstance(t.Context(), opts)
	if !errors.Is(err, backup.ErrWorkbookTargetReplacement) || !strings.Contains(err.Error(), "wb-page-7") || strings.Contains(err.Error(), "quarterly-plan") {
		t.Fatalf("forced recover over a workbook = %v, want refusal naming page ID wb-page-7", err)
	}
	if workbooks() != 1 {
		t.Fatal("refused forced recover destroyed the workbook")
	}
	opts.DiscardWorkbooks = true
	if _, err := backup.RecoverInstance(t.Context(), opts); err != nil {
		t.Fatalf("acknowledged forced recover: %v", err)
	}
	if workbooks() != 0 {
		t.Fatal("acknowledged forced recover kept a workbook the bundle does not contain")
	}
}
