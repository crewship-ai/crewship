package api

import (
	"bytes"
	"testing"
)

func TestDesignIdentityErasurePreservesLiveStateAndOtherWorkspace(t *testing.T) {
	db, workspace, user := artifactIdentityDataFixture(t)
	state := []byte{0, 255, 128}
	for _, scope := range []struct{ workspace, page string }{{workspace, "design-identity-page"}, {"identity-other-ws", "design-identity-other-page"}} {
		mustExec(t, db, `INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json,private_audience) VALUES(?,?,?,?,?,'{}',1)`, scope.page, scope.workspace, scope.page, "Retained Design", user)
		mustExec(t, db, `INSERT INTO artifact_version_heads(workspace_id,page_id,latest_version,content_type) VALUES(?,?,1,'design')`, scope.workspace, scope.page)
		mustExec(t, db, `INSERT INTO artifact_design_documents(workspace_id,page_id,state,revision,saved_revision,updated_by,updated_at) VALUES(?,?,?,7,4,?,'2026-10-28T00:00:00Z')`, scope.workspace, scope.page, state, user)
	}
	eraser := NewAdminGDPRHandler(db, newTestLogger(), t.TempDir())
	if _, err := eraser.eraseSubjectIdentity(t.Context(), "design-erasure-action", user, workspace, user); err != nil {
		t.Fatal(err)
	}
	for _, scope := range []struct{ workspace, actor string }{{workspace, "erased"}, {"identity-other-ws", user}} {
		var actor, stamp string
		var actual []byte
		var revision, saved int
		if err := db.QueryRow(`SELECT updated_by,state,revision,saved_revision,updated_at FROM artifact_design_documents WHERE workspace_id=?`, scope.workspace).Scan(&actor, &actual, &revision, &saved, &stamp); err != nil {
			t.Fatal(err)
		}
		if actor != scope.actor || !bytes.Equal(actual, state) || revision != 7 || saved != 4 || stamp != "2026-10-28T00:00:00Z" {
			t.Fatalf("identity/state changed outside scope: actor=%q state=%x revision=%d/%d stamp=%q", actor, actual, revision, saved, stamp)
		}
	}
}
