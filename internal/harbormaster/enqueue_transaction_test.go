package harbormaster

import "testing"

func TestEnqueueTxSharesCallerAuditRollbackAndCommit(t *testing.T) {
	db := openTestDB(t)
	t.Cleanup(func() { _ = db.Close() })
	ctx := t.Context()
	if _, err := db.ExecContext(ctx, `CREATE TABLE approval_audit_sentinel(value INTEGER CHECK(value=0))`); err != nil {
		t.Fatal(err)
	}
	request := Request{WorkspaceID: "ws_test", RequestedBy: "human-editor", Kind: KindCustom, Reason: "Review the exact shared draft", Status: StatusApproved, Payload: map[string]any{"draft_id": "draft-1", "draft_revision": 3}}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	id, err := EnqueueTx(ctx, tx, request)
	if err != nil {
		t.Fatal(err)
	}
	var status string
	var unpublished bool
	if err = tx.QueryRowContext(ctx, `SELECT status,routine_version IS NULL FROM approvals_queue WHERE id=?`, id).Scan(&status, &unpublished); err != nil || status != string(StatusPending) || !unpublished {
		t.Fatal("submission acquired approval or a fabricated published version", status, unpublished, err)
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO approval_audit_sentinel VALUES(1)`); err == nil {
		t.Fatal("synthetic caller audit failure did not fire")
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if rolledBack, readErr := Get(ctx, db, "ws_test", id); readErr != nil || rolledBack != nil {
		t.Fatalf("request survived caller audit rollback: %+v %v", rolledBack, readErr)
	}
	tx, err = db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	id, err = EnqueueTx(ctx, tx, request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO approval_audit_sentinel VALUES(0)`); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal("enqueue took ownership of caller transaction", err)
	}
	saved, err := Get(ctx, db, "ws_test", id)
	if err != nil || saved.Status != StatusPending || saved.RequestedBy != request.RequestedBy || saved.Payload["draft_revision"] != float64(3) || saved.TimeoutAt == nil || saved.CreatedAt.IsZero() {
		t.Fatal("caller commit lost normalized request or provenance", saved, err)
	}
}
