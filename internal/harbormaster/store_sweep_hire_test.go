package harbormaster

// #1304: a lapsed approval window has to be a decision, not a pause.
// The queue row going terminal is invisible to approve-hire (a terminal
// row can belong to a previous, rehired cycle — #1272), so the sweeper
// must also write the per-cycle marker the deny path writes:
// agents.expired_at.

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

// seedStagedHire inserts the agent row shape a guided hire leaves
// behind plus a pending ephemeral_hire approval whose window has
// already lapsed.
func seedStagedHire(t *testing.T, db *sql.DB, agentID, status string) string {
	t.Helper()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `INSERT INTO agents (id, status, ephemeral, updated_at)
		VALUES (?, ?, 1, '2026-01-01T00:00:00Z')`, agentID, status); err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	req := newReq()
	req.AgentID = agentID
	req.Kind = KindEphemeralHire
	req.Reason = "hire ephemeral agent " + agentID
	id, err := Enqueue(ctx, db, nil, req)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	past := time.Now().UTC().Add(-time.Minute).Format(timeFmt)
	if _, err := db.ExecContext(ctx, `UPDATE approvals_queue SET timeout_at = ? WHERE id = ?`, past, id); err != nil {
		t.Fatalf("rewrite timeout: %v", err)
	}
	return id
}

func agentExpiredAt(t *testing.T, db *sql.DB, agentID string) sql.NullString {
	t.Helper()
	var expired sql.NullString
	if err := db.QueryRow(`SELECT expired_at FROM agents WHERE id = ?`, agentID).Scan(&expired); err != nil {
		t.Fatalf("read agent: %v", err)
	}
	return expired
}

func TestSweepTimeouts_EphemeralHire_GhostsStagedAgent(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	id := seedStagedHire(t, db, "agent_staged", "PENDING_REVIEW")

	n, err := SweepTimeouts(context.Background(), db, nil)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if n != 1 {
		t.Fatalf("swept %d rows, want 1", n)
	}

	got, err := Get(context.Background(), db, "ws_test", id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != StatusTimeout {
		t.Errorf("approval status = %q, want timeout", got.Status)
	}
	if expired := agentExpiredAt(t, db, "agent_staged"); !expired.Valid {
		t.Error("agents.expired_at is NULL — the lapsed window left the hire approvable")
	}
}

// A hire the operator already approved (agent is IDLE) must not be
// ghosted by a sweep that loses the race on the queue row's own guard —
// same shape as the deny path's status='PENDING_REVIEW' guard.
func TestSweepTimeouts_EphemeralHire_LeavesActivatedAgentAlone(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	seedStagedHire(t, db, "agent_live", "IDLE")

	if _, err := SweepTimeouts(context.Background(), db, nil); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if expired := agentExpiredAt(t, db, "agent_live"); expired.Valid {
		t.Errorf("sweeper ghosted an already-activated agent (expired_at = %q)", expired.String)
	}
}

// Non-hire kinds keep the old behaviour: queue row only, no agent
// write, even though the row carries an agent_id.
func TestSweepTimeouts_ToolCall_DoesNotTouchAgent(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `INSERT INTO agents (id, status, ephemeral, updated_at)
		VALUES ('agent_1', 'PENDING_REVIEW', 1, '2026-01-01T00:00:00Z')`); err != nil {
		t.Fatalf("seed agent: %v", err)
	}
	req := newReq() // KindToolCall, AgentID agent_1
	id, err := Enqueue(ctx, db, nil, req)
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	past := time.Now().UTC().Add(-time.Minute).Format(timeFmt)
	if _, err := db.ExecContext(ctx, `UPDATE approvals_queue SET timeout_at = ? WHERE id = ?`, past, id); err != nil {
		t.Fatalf("rewrite timeout: %v", err)
	}

	if _, err := SweepTimeouts(ctx, db, nil); err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if expired := agentExpiredAt(t, db, "agent_1"); expired.Valid {
		t.Errorf("a timed-out tool-call approval ghosted its agent (expired_at = %q)", expired.String)
	}
}
