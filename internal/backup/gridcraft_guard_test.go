package backup

import (
	"strings"
	"testing"
)

func TestWorkspaceDumpRefusesUnsupportedWorkbookPortability(t *testing.T) {
	db := openMigratedDBCov(t)
	ws, _ := seedCovWorkspace(t, db, "workbook-portability")
	if _, err := db.Exec(`INSERT INTO pages(id,workspace_id,slug,name,owner_user_id,spec_json) SELECT 'workbook-guard',?,'workbook-guard','Workbook',id,'{}' FROM users LIMIT 1`, ws); err != nil {
		t.Fatal(err)
	}
	// Use the actual Page owner created by the fixture; no new authority model.
	if _, err := db.Exec(`UPDATE pages SET owner_user_id=(SELECT id FROM users LIMIT 1),private_audience=1 WHERE id='workbook-guard'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO gridcraft_heads(page_id,workspace_id,schema_version,head_rev,generation,snapshot) VALUES('workbook-guard',?,1,1,1,'{"sheets":[]}')`, ws); err != nil {
		t.Fatal(err)
	}
	if _, err := DumpWorkspace(t.Context(), db, ws); err == nil || !strings.Contains(err.Error(), "portable backup of experimental workbooks is unavailable") {
		t.Fatalf("silent content loss: %v", err)
	}
}

func TestRestoreRefusesWorkbookBundleBeforeAnyDatabaseWork(t *testing.T) {
	dump := &DBDump{Tables: map[string][]map[string]any{"gridcraft_heads": {{"page_id": "synthetic"}}}}
	if _, err := RestoreDumpTxHooks(t.Context(), nil, dump, nil); err == nil || !strings.Contains(err.Error(), "portable restore of experimental workbooks is unavailable") {
		t.Fatalf("unsupported restore accepted: %v", err)
	}
}
