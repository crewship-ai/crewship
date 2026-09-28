package backup

import (
	"context"
	"testing"
)

func TestServiceIntents_BackupScopesAndRestoresDesiredState(t *testing.T) {
	db := newDumpTestDB(t)
	_, err := db.Exec(`CREATE TABLE service_runtime_intents(id TEXT PRIMARY KEY,crew_id TEXT REFERENCES crews(id),service_name TEXT,desired_state TEXT,version INTEGER);
 INSERT INTO service_runtime_intents VALUES ('one','c_1_a','db','stopped',3),('two','c_1_b','cache','running',2),('foreign','c_2_a','secret','running',1);`)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	workspace, err := DumpWorkspace(ctx, db, "ws_1")
	if err != nil {
		t.Fatal(err)
	}
	if len(workspace.Tables["service_runtime_intents"]) != 2 {
		t.Fatal("workspace backup lost intent or included foreign crew")
	}
	crew, err := DumpCrew(ctx, db, "c_1_a")
	if err != nil {
		t.Fatal(err)
	}
	rows := crew.Tables["service_runtime_intents"]
	if len(rows) != 1 || rows[0]["id"] != "one" {
		t.Fatalf("crew backup has wrong scope: %+v", rows)
	}
	if _, err = db.Exec(`DELETE FROM service_runtime_intents WHERE id='one'`); err != nil {
		t.Fatal(err)
	}
	if err = RestoreDump(ctx, db, crew); err != nil {
		t.Fatal(err)
	}
	var state string
	var version int
	if err = db.QueryRow(`SELECT desired_state,version FROM service_runtime_intents WHERE id='one'`).Scan(&state, &version); err != nil {
		t.Fatal(err)
	}
	if state != "stopped" || version != 3 {
		t.Fatalf("manual stop lost on restore: %s %d", state, version)
	}
}
