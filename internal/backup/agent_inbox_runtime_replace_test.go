package backup

import (
	"context"
	"database/sql"
	"testing"
)

func seedAgentInboxRuntimeEvidence(t *testing.T, db *sql.DB, workspace, agent, marker string) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO agent_inbox_receipts(workspace_id,agent_id,source_kind,source_id,run_id,read_at,source_sha256) VALUES(?,?,'chat_message',?,?,'2026-11-18T00:00:00Z','synthetic-source-digest')`, workspace, agent, "trigger-"+marker, "run-"+marker); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO agent_inbox_run_sources(run_id,workspace_id,agent_id,source_kind,source_id,source_sha256) VALUES(?,?,?,'chat_message',?,'synthetic-source-digest')`, "run-"+marker, workspace, agent, "trigger-"+marker); err != nil {
		t.Fatal(err)
	}
}
func assertAgentInboxRuntimeRows(t *testing.T, db *sql.DB, workspace string, want int) {
	t.Helper()
	for _, table := range []string{"agent_inbox_receipts", "agent_inbox_run_sources"} {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE workspace_id=?`, workspace).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != want {
			t.Errorf("%s runtime rows for %s=%d, want %d", table, workspace, n, want)
		}
	}
}

// Exercise the canonical dump + replacing restore transaction, not a mocked
// DELETE. Runtime evidence must not reattach when old parent IDs are restored.
func TestAgentInboxRuntimeEvidenceDoesNotSurviveReplacingRestore(t *testing.T) {
	db := openMigratedDBCov(t)
	workspace, crew := seedCovWorkspace(t, db, "inbox-runtime-replace")
	agent := "a_cov_inbox-runtime-replace"
	keep, _ := seedCovWorkspace(t, db, "inbox-runtime-keep")
	seedAgentInboxRuntimeEvidence(t, db, workspace, agent, "replace")
	seedAgentInboxRuntimeEvidence(t, db, keep, "a_cov_inbox-runtime-keep", "keep")
	dump, err := DumpWorkspace(t.Context(), db, workspace)
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"agent_inbox_receipts", "agent_inbox_run_sources"} {
		if len(dump.Tables[table]) != 0 {
			t.Fatalf("runtime capability %s leaked into portable dump", table)
		}
	}
	if _, err = db.Exec(`UPDATE agents SET name='Changed after backup' WHERE id=?`, agent); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`UPDATE workspaces SET name='Changed after backup' WHERE id=?`, workspace); err != nil {
		t.Fatal(err)
	}
	hooks := &RestoreDumpHooks{PreInsert: func(ctx context.Context, tx *sql.Tx) error {
		_, err := ReplaceWorkspaceContentsWithOptions(ctx, tx, workspace, "cov-inbox-runtime-replace", ReplaceOptions{})
		return err
	}}
	if _, err = RestoreDumpTxHooks(t.Context(), db, dump, hooks); err != nil {
		t.Fatalf("real replacing restore failed with populated runtime evidence: %v", err)
	}
	var restoredWorkspace, restoredAgent, restoredCrew string
	if err = db.QueryRow(`SELECT w.name,a.name,a.crew_id FROM workspaces w JOIN agents a ON a.workspace_id=w.id WHERE w.id=? AND a.id=?`, workspace, agent).Scan(&restoredWorkspace, &restoredAgent, &restoredCrew); err != nil {
		t.Fatal(err)
	}
	if restoredWorkspace != "Cov inbox-runtime-replace" || restoredAgent != "Agent" || restoredCrew != crew {
		t.Fatalf("parents were not actually restored: %q %q %q", restoredWorkspace, restoredAgent, restoredCrew)
	}
	assertAgentInboxRuntimeRows(t, db, workspace, 0)
	assertAgentInboxRuntimeRows(t, db, keep, 1)
}

func TestAgentInboxRuntimeEvidenceIsRemovedWithAgent(t *testing.T) {
	db := openMigratedDBCov(t)
	workspace, _ := seedCovWorkspace(t, db, "inbox-runtime-delete")
	agent := "a_cov_inbox-runtime-delete"
	seedAgentInboxRuntimeEvidence(t, db, workspace, agent, "delete")
	if _, err := db.Exec(`DELETE FROM agents WHERE id=?`, agent); err != nil {
		t.Fatalf("agent removal blocked by runtime evidence: %v", err)
	}
	assertAgentInboxRuntimeRows(t, db, workspace, 0)
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM workspaces WHERE id=?`, workspace).Scan(&n); err != nil || n != 1 {
		t.Fatalf("unrelated parent workspace lost: %d %v", n, err)
	}
}
