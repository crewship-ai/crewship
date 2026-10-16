package database

import (
	"context"
	"path/filepath"
	"testing"
)

func TestApprovalAnswerOperationMigrationAndSnapshot(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "crewship.db")
	db := openMigratedTestDBAt(t, path)
	mustExec(t, db.DB, `INSERT INTO workspaces(id,name,slug) VALUES('approval-ws','Approval','approval-ws')`)
	mustExec(t, db.DB, `INSERT INTO approvals_queue(id,workspace_id,requested_by,kind,reason,payload,status,created_at) VALUES('legacy-approval','approval-ws','agent','custom','legacy','{}','pending','2026-10-16T13:00:00Z')`)
	var nulls int
	if err := db.QueryRow(`SELECT answer_operation_key IS NULL AND answer_operation_hash IS NULL FROM approvals_queue WHERE id='legacy-approval'`).Scan(&nulls); err != nil || nulls != 1 {
		t.Fatalf("legacy retry metadata %d %v", nulls, err)
	}
	mustExec(t, db.DB, `UPDATE approvals_queue SET status='approved',answer_operation_key='synthetic-key',answer_operation_hash='synthetic-hash' WHERE id='legacy-approval'`)
	snapshot := path + ".pre-migrate-v1-to-v2-20261016T130000Z.bak"
	if err := snapshotDatabase(ctx, db.DB, snapshot); err != nil {
		t.Fatal(err)
	}
	mustExec(t, db.DB, `UPDATE approvals_queue SET answer_operation_key='later-key' WHERE id='legacy-approval'`)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if err := RestoreSnapshot(path, snapshot); err != nil {
		t.Fatal(err)
	}
	restored, err := Open("file:" + path)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if err = Migrate(ctx, restored.DB, newTestLogger()); err != nil {
		t.Fatal(err)
	}
	var status, key, hash string
	if err = restored.QueryRow(`SELECT status,answer_operation_key,answer_operation_hash FROM approvals_queue WHERE id='legacy-approval'`).Scan(&status, &key, &hash); err != nil {
		t.Fatal(err)
	}
	if status != "approved" || key != "synthetic-key" || hash != "synthetic-hash" {
		t.Fatalf("restored source differs %q %q %q", status, key, hash)
	}
}
