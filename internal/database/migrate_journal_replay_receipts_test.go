package database

import "testing"

func TestMigrateJournalReplayReceiptsOutliveVisibleRows(t *testing.T) {
	db := openMigratedTestDB(t)
	mustExec(t, db.DB, `INSERT INTO workspaces(id,name,slug) VALUES('receipt-ws','Receipt','receipt-ws')`)
	mustExec(t, db.DB, `INSERT INTO journal_entries(id,workspace_id,ts,entry_type,actor_type,summary)
 VALUES('projected','receipt-ws','2026-10-12T00:00:00.000Z','exec.output.chunk','agent','Projection')`)
	mustExec(t, db.DB, `INSERT INTO journal_replay_receipts(id,fingerprint,workspace_id,agent_id,trace_id,entry_type) VALUES('projected','immutable-content','replay-ws','agent','run','run.failed')`)
	mustExec(t, db.DB, `DELETE FROM journal_entries WHERE id='projected'`)
	var fingerprint string
	if err := db.DB.QueryRow(`SELECT fingerprint FROM journal_replay_receipts WHERE id='projected'`).Scan(&fingerprint); err != nil || fingerprint != "immutable-content" {
		t.Fatalf("receipt did not survive retention: fingerprint=%q err=%v", fingerprint, err)
	}
	if _, err := db.DB.Exec(`INSERT INTO journal_replay_receipts(id,fingerprint,workspace_id,agent_id,trace_id,entry_type) VALUES('projected','changed','replay-ws','agent','run','run.failed')`); err == nil {
		t.Fatal("duplicate replay receipt identity accepted")
	}
}
