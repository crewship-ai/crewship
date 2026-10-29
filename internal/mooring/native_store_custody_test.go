package mooring

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

// These fixtures model persisted custody, not qualification of a C1 engine.
func nativeCustodyCollaborationInsert(t *testing.T, db *sql.DB, scope EditLeaseScope, command bool) {
	t.Helper()
	profile, revision, hash := "mooring-causal-v1", "", ""
	if command {
		profile = "wordcraft-c1-v2"
		revision = "7ef2633a7e84956522d1ca47b99fe6feef16c1db"
		hash = "sha256:" + strings.Repeat("a", 64)
	}
	if _, err := db.Exec(`INSERT INTO mooring_collab_documents(page_id,workspace_id,application_id,revision,checkpoint_base64,format_profile,engine_revision,engine_sha256) VALUES(?,?,'wordcraft','0','e30=',?,?,?)`, scope.ArtifactID, scope.WorkspaceID, profile, revision, hash); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO mooring_collab_checkpoints(page_id,workspace_id,revision,checkpoint_base64,decoded_bytes) VALUES(?,?,'0','e30=',2)`, scope.ArtifactID, scope.WorkspaceID); err != nil {
		t.Fatal(err)
	}
}
func nativeCustodyNoNativeRows(t *testing.T, db *sql.DB, scope EditLeaseScope) {
	t.Helper()
	for _, table := range []string{"craft_native_documents", "mooring_edit_leases", "artifact_version_heads", "artifact_versions"} {
		var count int
		if err := db.QueryRow("SELECT count(*) FROM "+table+" WHERE page_id=?", scope.ArtifactID).Scan(&count); err != nil || count != 0 {
			t.Errorf("custody failure left partial %s rows=%d error=%v", table, count, err)
		}
	}
}
func TestNativeStoreCustodyFenceRejectsExistingCollaboration(t *testing.T) {
	for _, command := range []bool{false, true} {
		t.Run(map[bool]string{false: "causal", true: "command"}[command], func(t *testing.T) {
			store, _, db, _, scope, p := nativeStoreFixture(t)
			nativeCustodyCollaborationInsert(t, db, scope, command)
			if _, _, err := store.Initialize(t.Context(), scope, p, []byte(`{"v":1}`)); err == nil {
				t.Error("existing collaboration accepted a second native custody")
			}
			nativeCustodyNoNativeRows(t, db, scope)
			var checkpoint, revision string
			if err := db.QueryRow(`SELECT checkpoint_base64,revision FROM mooring_collab_documents WHERE page_id=?`, scope.ArtifactID).Scan(&checkpoint, &revision); err != nil || checkpoint != "e30=" || revision != "0" {
				t.Fatalf("existing collaboration changed: %s %s %v", checkpoint, revision, err)
			}
			var rows int
			if err := db.QueryRow(`SELECT count(*) FROM mooring_collab_checkpoints WHERE page_id=? AND checkpoint_base64='e30=' AND revision='0' AND decoded_bytes=2`, scope.ArtifactID).Scan(&rows); err != nil || rows != 1 {
				t.Fatalf("existing checkpoint changed: rows=%d error=%v", rows, err)
			}
		})
	}
}
func TestNativeStoreCustodyFenceRejectsCollaborationAfterNative(t *testing.T) {
	store, _, db, _, scope, p := nativeStoreFixture(t)
	if _, _, err := store.Initialize(t.Context(), scope, p, []byte(`{"v":1}`)); err != nil {
		t.Fatal(err)
	}
	_, err := db.Exec(`INSERT INTO mooring_collab_documents(page_id,workspace_id,application_id,revision,checkpoint_base64) VALUES(?,?,'wordcraft','0','e30=')`, scope.ArtifactID, scope.WorkspaceID)
	if err == nil {
		t.Error("native custody accepted a second collaboration custody")
	}
	var count int
	if err = db.QueryRow(`SELECT count(*) FROM mooring_collab_documents WHERE page_id=?`, scope.ArtifactID).Scan(&count); err != nil || count != 0 {
		t.Errorf("refused collaboration left rows=%d error=%v", count, err)
	}
	state, err := store.Read(t.Context(), scope)
	if err != nil || state.LatestVersion != 1 || string(state.Manifest.Files["document.json"].Content) != `{"v":1}` {
		t.Errorf("native head changed: version=%d error=%v", state.LatestVersion, err)
	}
	// Calibration: the same persisted collaboration format remains valid on a
	// different empty personal Page; the new fence is native-specific.
	if _, err = db.Exec(`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) VALUES('p-empty','w','p-empty','Empty','u','{}')`); err != nil {
		t.Fatal(err)
	}
	nativeCustodyCollaborationInsert(t, db, EditLeaseScope{WorkspaceID: scope.WorkspaceID, ArtifactID: "p-empty"}, false)
}
func TestNativeStoreCustodyFenceRollsBackLateCollaborationInsert(t *testing.T) {
	store, _, db, _, scope, p := nativeStoreFixture(t)
	store.config.Authorize = func(ctx context.Context, tx *sql.Tx, k EditLeaseScope, intent string) (EditLeasePrincipal, error) {
		if intent == "write" {
			if _, err := tx.ExecContext(ctx, `INSERT INTO mooring_collab_documents(page_id,workspace_id,application_id,revision,checkpoint_base64) VALUES(?,?,'wordcraft','0','e30=')`, k.ArtifactID, k.WorkspaceID); err != nil {
				return EditLeasePrincipal{}, err
			}
		}
		return p, nil
	}
	if _, _, err := store.Initialize(t.Context(), scope, p, []byte(`{"v":1}`)); err == nil {
		t.Error("late collaboration insertion did not abort native publication")
	}
	nativeCustodyNoNativeRows(t, db, scope)
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM mooring_collab_documents WHERE page_id=?`, scope.ArtifactID).Scan(&count); err != nil || count != 0 {
		t.Errorf("late collision survived rollback: rows=%d error=%v", count, err)
	}
}
