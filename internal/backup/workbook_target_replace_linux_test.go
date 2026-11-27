//go:build linux

package backup_test

import (
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"

	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/database"
)

// The instance bundle predates the target workbook. Forced recovery refuses
// unless the operator acknowledges the loss with DiscardWorkbooks.
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

func TestForceRecoverRefusesTargetNativeWorkbookHistory(t *testing.T) {
	f := newInstanceFixture(t)
	f.db = reopenDurableBackupDB(t, f.db)
	scope, original := seedWorkbookBackupRestore(t, f.db)
	older := f.create(t, true)
	newer := convertAndEditWorkbookBackup(t, f.db, scope)
	f.outputDir = t.TempDir()
	current := f.create(t, true)
	opts := backup.RecoverOptions{
		BundlePath: current.Path, Identities: []age.Identity{f.identity},
		DataDir: filepath.Join(t.TempDir(), "installation"), Actor: "u_admin",
	}
	report, err := backup.RecoverInstance(t.Context(), opts)
	if err != nil {
		t.Fatalf("recover current portable bundle: %v", err)
	}
	readTarget := func(check func(*sql.DB)) {
		t.Helper()
		db, err := database.Open("file:" + report.DatabasePath)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		check(db.DB)
	}
	readTarget(func(db *sql.DB) {
		assertNoWorkbookBackupRuntime(t, db)
		assertWorkbookBackupUnchanged(t, db, scope, original, newer)
	})
	opts.BundlePath, opts.Force = older.Path, true
	_, err = backup.RecoverInstance(t.Context(), opts)
	if !errors.Is(err, backup.ErrWorkbookTargetReplacement) || strings.Count(err.Error(), scope.ArtifactID) != 1 {
		t.Fatalf("force-recover older bundle: %v, want one opaque Page ID", err)
	}
	readTarget(func(db *sql.DB) { assertWorkbookBackupUnchanged(t, db, scope, original, newer) })

	opts.DiscardWorkbooks = true
	if _, err := backup.RecoverInstance(t.Context(), opts); err != nil {
		t.Fatalf("acknowledged force recovery: %v", err)
	}
	readTarget(func(db *sql.DB) { assertLegacyWorkbookBackupRestored(t, db, scope, original) })
}
