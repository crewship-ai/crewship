package api

import "testing"

func TestArtifactDataIdentityErasurePreservesValuesAndOtherWorkspace(t *testing.T) {
	db, ws, user := artifactIdentityDataFixture(t)
	for _, scope := range []struct{ ws, page string }{{ws, "identity-page"}, {"identity-other-ws", "identity-other-page"}} {
		mustExec(t, db, `INSERT INTO artifact_data_collections(workspace_id,page_id,name) VALUES(?,?,'sales')`, scope.ws, scope.page)
		mustExec(t, db, `INSERT INTO artifact_data_documents(workspace_id,page_id,collection,id,version,value_json,updated_by,updated_at) VALUES(?,?,'sales','row',1,'{"n":42}',?,datetime('now'))`, scope.ws, scope.page, "user:"+user)
		mustExec(t, db, `INSERT INTO artifact_data_runs(workspace_id,page_id,collection,id,idempotency_key,state,started_by,started_at,code_version) VALUES(?,?,'sales','run_1','k','running',?,datetime('now'),1)`, scope.ws, scope.page, user)
		mustExec(t, db, `INSERT INTO artifact_data_runs(workspace_id,page_id,collection,id,idempotency_key,state,started_by,started_at,finished_at,finished_by,code_version,start_generation,end_generation) VALUES(?,?,'sales','run_2','k2','failed','someone-else',datetime('now'),datetime('now'),?,1,0,0)`, scope.ws, scope.page, "user:"+user)
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
		want := scope.actor
		if want != "erased" {
			want = user
		}
		if err := db.QueryRow(`SELECT started_by FROM artifact_data_runs WHERE workspace_id=? AND id='run_1'`, scope.ws).Scan(&actor); err != nil || actor != want {
			t.Fatalf("run producer identity: %q %v", actor, err)
		}
		var starter string
		if err := db.QueryRow(`SELECT started_by,finished_by FROM artifact_data_runs WHERE workspace_id=? AND id='run_2'`, scope.ws).Scan(&starter, &actor); err != nil || starter != "someone-else" || actor != scope.actor {
			t.Fatalf("run finisher identity: %q %q %v", starter, actor, err)
		}
	}
}
