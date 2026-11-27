package api

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// The audit row names the revision and its source, keeps only the size and a
// digest prefix of the document, and refuses an incomplete or repeated record.
func TestCraftSheetSavedAuditRow(t *testing.T) {
	db := setupTestDB(t)
	mustExec(t, db, `INSERT INTO users(id,email) VALUES('u1','u1@example.test')`)
	mustExec(t, db, `INSERT INTO workspaces(id,name,slug) VALUES('w1','W','w1')`)
	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	doc := []byte(`{"cells":"secret-cell-text"}`)
	ok := craftSheetSaved{WorkspaceID: "w1", PageID: "p1", Version: 2, Source: craftSheetSavedByAgent, UserID: "u1", AgentID: "a1", RunID: "r1", Native: doc}
	if err := recordCraftSheetSaved(context.Background(), tx, ok); err != nil {
		t.Fatal(err)
	}
	var meta string
	if err := tx.QueryRow(`SELECT metadata FROM audit_logs WHERE action='artifact.sheet_saved' AND entity_id='p1' AND user_id='u1'`).Scan(&meta); err != nil {
		t.Fatal(err)
	}
	var f map[string]any
	_ = json.Unmarshal([]byte(meta), &f)
	if f["version"] != "2" || f["source"] != "agent" || f["agent_id"] != "a1" || f["run_id"] != "r1" || f["bytes"] != float64(len(doc)) || len(f["sha256"].(string)) != 16 {
		t.Fatalf("metadata: %s", meta)
	}
	if strings.Contains(meta, "secret") {
		t.Fatal("the audit row carries content")
	}
	if recordCraftSheetSaved(context.Background(), tx, ok) == nil {
		t.Fatal("the same revision was audited twice")
	}
	for name, bad := range map[string]craftSheetSaved{
		"unknown source":       {WorkspaceID: "w1", PageID: "p1", Version: 3, Source: "cli", Native: doc},
		"agent without run":    {WorkspaceID: "w1", PageID: "p1", Version: 3, Source: craftSheetSavedByAgent, AgentID: "a1", Native: doc},
		"editor with an agent": {WorkspaceID: "w1", PageID: "p1", Version: 3, Source: craftSheetSavedByEditor, AgentID: "a1", RunID: "r1", Native: doc},
		"no version":           {WorkspaceID: "w1", PageID: "p1", Source: craftSheetSavedByEditor, Native: doc},
		"no document":          {WorkspaceID: "w1", PageID: "p1", Version: 3, Source: craftSheetSavedByEditor},
	} {
		if recordCraftSheetSaved(context.Background(), tx, bad) == nil {
			t.Fatalf("%s: recorded", name)
		}
	}
}
