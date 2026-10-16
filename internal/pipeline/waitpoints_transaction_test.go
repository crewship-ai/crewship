package pipeline

import (
	"context"
	"errors"
	"testing"
)

func TestWaitpointDecisionTxRollbackKeepsCanonicalDoorAvailable(t *testing.T) {
	store, cleanup := openWaitpointsTestDB(t)
	defer cleanup()
	ctx := context.Background()
	token, err := store.CreateApproval(ctx, WaitpointApprovalRequest{WorkspaceID: "ws_test", PipelineRunID: "run_tx", StepID: "review", TimeoutSec: 3600})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := store.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		t.Fatal(err)
	}
	result, err := store.DecideTx(ctx, conn, "ws_test", token, true, WaitpointDecider{Kind: DeciderUser, ID: "chat-user"}, "draft")
	if err != nil || result == nil || !result.Applied {
		t.Fatalf("transaction decision: %+v %v", result, err)
	}
	if _, err = conn.ExecContext(ctx, "ROLLBACK"); err != nil {
		t.Fatal(err)
	}
	if err = conn.Close(); err != nil {
		t.Fatal(err)
	}
	var status, actor, payload string
	if err = store.db.QueryRowContext(ctx, `SELECT status,COALESCE(decided_by_user_id,''),COALESCE(decision_payload,'') FROM pipeline_waitpoints WHERE token=?`, token).Scan(&status, &actor, &payload); err != nil {
		t.Fatal(err)
	}
	if status != "pending" || actor != "" || payload != "" {
		t.Fatalf("rolled-back answer escaped: %q %q %q", status, actor, payload)
	}
	if err = store.CompleteApproval(ctx, "ws_test", token, false, "inbox-user", "final"); err != nil {
		t.Fatal(err)
	}
	if err = store.CompleteApproval(ctx, "ws_test", token, true, "chat-user", "retry"); !errors.Is(err, ErrAlreadyDecided) {
		t.Fatalf("losing source overwrote winner: %v", err)
	}
	if err = store.db.QueryRowContext(ctx, `SELECT status,decided_by_user_id,decision_payload FROM pipeline_waitpoints WHERE token=?`, token).Scan(&status, &actor, &payload); err != nil {
		t.Fatal(err)
	}
	if status != "denied" || actor != "inbox-user" || payload != "final" {
		t.Fatalf("canonical winner changed: %q %q %q", status, actor, payload)
	}
}
