package api

import "testing"

func TestArtifactDataIdentityErasurePreservesValuesAndOtherWorkspace(t *testing.T) {
	db, ws, user := artifactIdentityDataFixture(t)
	for _, scope := range []struct{ ws, page string }{{ws, "identity-page"}, {"identity-other-ws", "identity-other-page"}} {
		mustExec(t, db, `INSERT INTO artifact_data_collections(workspace_id,page_id,name) VALUES(?,?,'sales')`, scope.ws, scope.page)
		mustExec(t, db, `INSERT INTO artifact_data_documents(workspace_id,page_id,collection,id,version,value_json,updated_by,updated_at) VALUES(?,?,'sales','row',1,'{"n":42}',?,datetime('now'))`, scope.ws, scope.page, "user:"+user)
	}
	tx, err := db.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if err := anonymiseArtifactProjectIdentity(t.Context(), tx, ws, user); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	for _, scope := range []struct{ ws, actor string }{{ws, "erased"}, {"identity-other-ws", "user:" + user}} {
		var actor, value string
		var version int64
		if err := db.QueryRow(`SELECT updated_by,value_json,version FROM artifact_data_documents WHERE workspace_id=?`, scope.ws).Scan(&actor, &value, &version); err != nil || actor != scope.actor || value != `{"n":42}` || version != 1 {
			t.Fatalf("identity/value changed: %q %q %d %v", actor, value, version, err)
		}
	}
}
