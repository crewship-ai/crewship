package backupplan

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

func busyFixtureDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "busy.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, query := range []string{
		`CREATE TABLE crews(id TEXT PRIMARY KEY,workspace_id TEXT)`,
		`CREATE TABLE agents(crew_id TEXT,status TEXT)`,
		`CREATE TABLE pipeline_runs(workspace_id TEXT,status TEXT)`,
		`INSERT INTO crews VALUES('a','ws_a'),('b','ws_b'),('c','ws_c')`,
		`INSERT INTO agents VALUES('a','running'),('a','busy'),('b','idle')`,
		`INSERT INTO pipeline_runs VALUES('ws_b','running'),('ws_a','completed')`,
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func TestBackupBusyGuardScopesAgentAndRoutineActivity(t *testing.T) {
	db := busyFixtureDB(t)
	guard := DBBusy{DB: db}
	for _, tc := range []struct {
		name   string
		ids    []string
		busy   bool
		detail string
	}{
		{"all", nil, true, "2 agent(s) working, 1 routine run(s) in progress"},
		{"agents", []string{"ws_a"}, true, "2 agent(s) working"},
		{"routines", []string{"ws_b"}, true, "1 routine run(s) in progress"},
		{"both scopes", []string{"ws_a", "ws_b"}, true, "2 agent(s) working, 1 routine run(s) in progress"},
		{"idle scope", []string{"ws_c"}, false, ""},
		{"unknown scope", []string{"unknown"}, false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			busy, detail, err := guard.Busy(t.Context(), tc.ids)
			if err != nil || busy != tc.busy || detail != tc.detail {
				t.Fatalf("busy=%v detail=%q error=%v", busy, detail, err)
			}
		})
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := guard.Busy(t.Context(), nil); err == nil {
		t.Fatal("unreadable activity treated as idle")
	}
}

func TestBackupBusyGuardDistinguishesLegacySchemaFromReadFailure(t *testing.T) {
	db := busyFixtureDB(t)
	guard := DBBusy{DB: db}
	if _, err := db.Exec(`DROP TABLE pipeline_runs`); err != nil {
		t.Fatal(err)
	}
	if busy, detail, err := guard.Busy(t.Context(), []string{"ws_c"}); err != nil || busy || detail != "" {
		t.Fatalf("legacy schema: %v %q %v", busy, detail, err)
	}
	if _, err := db.Exec(`CREATE VIEW pipeline_runs AS SELECT 'ws_c' AS workspace_id, json_extract('not-json', '$') AS status`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := guard.Busy(t.Context(), []string{"ws_c"}); err == nil || !strings.Contains(err.Error(), "malformed JSON") {
		t.Fatalf("broken activity read treated as idle: %v", err)
	}
}
