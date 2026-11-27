package backup_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"filippo.io/age"

	"github.com/crewship-ai/crewship/internal/artifacts"
	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/mooring"
)

// This bundle predates the target workbook. Replacement refuses and names the
// workbook Page unless the operator explicitly acknowledges the loss.
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

// The older encrypted bundle carries the genuine legacy profile and JSON v1.
// Rebuilding the newer target from its portable dump drops runtime rows, so
// neither refusal can be attributed to lease or delivery metadata.
func TestReplaceRestoreRefusesTargetNativeWorkbookHistory(t *testing.T) {
	ctx := t.Context()
	f := newInstanceFixture(t)
	source := reopenDurableBackupDB(t, f.db)
	scope, original := seedWorkbookBackupRestore(t, source)
	bundle, err := backup.CreateBackup(ctx, source, backup.CreateOptions{
		Scope: backup.ScopeWorkspace, WorkspaceID: scope.WorkspaceID, OutputDir: t.TempDir(),
		Actor: backup.Actor{UserID: "u_admin", Role: "OWNER"}, Recipients: []age.Recipient{f.identity.Recipient()},
		ConversationRoot: f.paths.Output,
	})
	if err != nil {
		t.Fatal(err)
	}
	newer := convertAndEditWorkbookBackup(t, source, scope)
	dump, err := backup.DumpWorkspace(ctx, source, scope.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"converted workbook", "native head without receipt", "receipt with a legacy head", "operations with a legacy head", "undo with a legacy head", "signature warning with a legacy head"} {
		t.Run(mode, func(t *testing.T) {
			target := openDurableArtifactBackupDB(t)
			if err := backup.RestoreDump(t.Context(), target, dump); err != nil {
				t.Fatal(err)
			}
			assertNoWorkbookBackupRuntime(t, target)
			switch mode {
			case "native head without receipt":
				replacementExec(t, target, "DELETE FROM craft_workbook_migrations WHERE page_id=?", scope.ArtifactID)
			case "receipt with a legacy head":
				replacementExec(t, target, "UPDATE artifact_version_heads SET latest_version=1 WHERE page_id=?", scope.ArtifactID)
			case "operations with a legacy head", "undo with a legacy head", "signature warning with a legacy head":
				replacementExec(t, target, "DELETE FROM craft_workbook_migrations WHERE page_id=?", scope.ArtifactID)
				replacementExec(t, target, "UPDATE artifact_version_heads SET latest_version=1 WHERE page_id=?", scope.ArtifactID)
				switch mode {
				case "operations with a legacy head":
					replacementExec(t, target, `INSERT INTO craft_version_ops
						(workspace_id,page_id,version,base_version,actor_kind,actor_id,operation_id,engine_pin,calculate,replayable,ops,created_at)
						VALUES(?,?,3,2,'agent','agent-test','test-op','test-pin','saved',0,'[]',CURRENT_TIMESTAMP)`, scope.WorkspaceID, scope.ArtifactID)
				case "undo with a legacy head":
					replacementExec(t, target, `INSERT INTO craft_version_undos
						(workspace_id,page_id,operation_id,version,skipped,created_at)
						VALUES(?,?,'test-op',3,0,CURRENT_TIMESTAMP)`, scope.WorkspaceID, scope.ArtifactID)
				case "signature warning with a legacy head":
					replacementExec(t, target, `INSERT INTO craft_version_signature_breaks
						(workspace_id,page_id,version,created_at)
						VALUES(?,?,3,CURRENT_TIMESTAMP)`, scope.WorkspaceID, scope.ArtifactID)
				}
			}
			opts := backup.RestoreOptions{
				Path: bundle.Path, Identities: []age.Identity{f.identity}, Replace: true,
				Actor: backup.Actor{UserID: "u_admin", Role: "OWNER"}, ConversationRoot: f.paths.Output,
			}
			for _, dryRun := range []bool{true, false} {
				opts.DryRun = dryRun
				_, err := backup.RestoreBackup(t.Context(), target, opts)
				if !errors.Is(err, backup.ErrWorkbookTargetReplacement) || strings.Count(err.Error(), scope.ArtifactID) != 1 {
					t.Fatalf("replace (dry run %t): %v, want one opaque Page ID", dryRun, err)
				}
			}
			tx, err := target.BeginTx(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			_, err = backup.ReplaceWorkspaceContents(t.Context(), tx, scope.WorkspaceID, "e2e-ws")
			_ = tx.Rollback()
			if !errors.Is(err, backup.ErrWorkbookTargetReplacement) {
				t.Fatalf("replace in the writer transaction: %v", err)
			}
			if mode == "converted workbook" {
				assertWorkbookBackupUnchanged(t, target, scope, original, newer)
				opts.DiscardWorkbooks = true
				if _, err := backup.RestoreBackup(t.Context(), target, opts); err != nil {
					t.Fatalf("acknowledged replace: %v", err)
				}
				assertLegacyWorkbookBackupRestored(t, target, scope, original)
			}
		})
	}
}

func seedWorkbookBackupRestore(t *testing.T, db *sql.DB) (mooring.EditLeaseScope, []byte) {
	t.Helper()
	scope := mooring.EditLeaseScope{WorkspaceID: "ws_e2e_1", ArtifactID: "native-wb-restore"}
	replacementExec(t, db, `INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES(?,?,'legacy-sheet','Legacy Sheet','u_admin','{}')`, scope.ArtifactID, scope.WorkspaceID)
	replacementExec(t, db, `INSERT INTO craft_native_documents(page_id,workspace_id,application_id,engine_revision,engine_sha256,editor_bundle)
		VALUES(?,?,'gridcraft','e68d192282499bed81a9800d742cb9b239d367e5',?,?)`, scope.ArtifactID, scope.WorkspaceID, strings.Repeat("a", 64), strings.Repeat("b", 64))
	original := []byte("{\n  \"sheets\": [{\"name\": \"Original\"}]\n}\n")
	publishWorkbookBackupVersion(t, db, scope, 0, "document.json", "application/json", original)
	return scope, original
}

func convertAndEditWorkbookBackup(t *testing.T, db *sql.DB, scope mooring.EditLeaseScope) []byte {
	t.Helper()
	store, _ := workbookBackupStore(db, true)
	legacy, err := store.LegacyWorkbook(t.Context(), scope, true)
	if err != nil {
		t.Fatal(err)
	}
	if version, err := store.MigrateLegacyWorkbook(t.Context(), scope, legacy, workbookBackupFixture(t, "converted")); err != nil || version != 2 {
		t.Fatalf("migrate: version %d, %v", version, err)
	}
	newer := workbookBackupFixture(t, "edited after conversion")
	publishWorkbookBackupVersion(t, db, scope, 2, "workbook.xlsx", mooring.WorkbookMediaTypeXLSX, newer)
	return newer
}

func publishWorkbookBackupVersion(t *testing.T, db *sql.DB, scope mooring.EditLeaseScope, base int64, name, media string, content []byte) {
	t.Helper()
	history := artifacts.NewVersionStore(db, artifacts.VersionOptions{Enabled: true, ContentType: "gridcraft"})
	key := artifacts.VersionKey{WorkspaceID: scope.WorkspaceID, PageID: scope.ArtifactID}
	req := artifacts.VersionRequest{BaseVersion: base, EntryPoint: name,
		Files: map[string]artifacts.VersionFile{name: {Content: content, MediaType: media}}}
	version, err := history.Apply(t.Context(), key, req, func(context.Context, *sql.Tx) (string, error) {
		return workbookBackupPrincipal.ActorID, nil
	})
	if err != nil || version.Version != base+1 {
		t.Fatalf("publish %s: version %+v, %v", name, version, err)
	}
}

func assertNoWorkbookBackupRuntime(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, table := range []string{"gridcraft_heads", "mooring_delivery_gates", "mooring_delivery_barriers", "mooring_edit_leases", "mooring_edit_lease_workspaces"} {
		var count int
		if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM "+table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("target has %d %s rows or lookup failed: %v", count, table, err)
		}
	}
}

func assertWorkbookBackupUnchanged(t *testing.T, db *sql.DB, scope mooring.EditLeaseScope, original, newer []byte) {
	t.Helper()
	store, _ := workbookBackupStore(db, true)
	state, err := store.Read(t.Context(), scope)
	if err != nil || state.LatestVersion != 3 || sha256.Sum256(state.Manifest.Files["workbook.xlsx"].Content) != sha256.Sum256(newer) {
		t.Fatalf("refused restoration changed workbook v3: head %d, %v", state.LatestVersion, err)
	}
	legacy, err := store.LegacyWorkbook(t.Context(), scope, true)
	if err != nil || legacy.Version != 1 || legacy.WorkbookVersion != 2 || sha256.Sum256(legacy.Bytes) != sha256.Sum256(original) {
		t.Fatalf("refused restoration changed receipt or source: versions %d/%d, %v", legacy.Version, legacy.WorkbookVersion, err)
	}
}

func assertLegacyWorkbookBackupRestored(t *testing.T, db *sql.DB, scope mooring.EditLeaseScope, original []byte) {
	t.Helper()
	store, _ := workbookBackupStore(db, false)
	state, err := store.Read(t.Context(), scope)
	if err != nil || state.LatestVersion != 1 || sha256.Sum256(state.Manifest.Files["document.json"].Content) != sha256.Sum256(original) {
		t.Fatalf("acknowledged restoration did not restore the legacy JSON: head %d, %v", state.LatestVersion, err)
	}
	var receipts int
	if err := db.QueryRowContext(t.Context(), "SELECT count(*) FROM craft_workbook_migrations WHERE page_id=?", scope.ArtifactID).Scan(&receipts); err != nil || receipts != 0 {
		t.Fatalf("acknowledged restoration retained %d migration receipts, %v", receipts, err)
	}
}
