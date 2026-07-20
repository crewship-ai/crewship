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
// already lapsed. expiresAt is the agent's own deadline: a hire writes
// it from the same instant + TTL as the queue row's timeout_at, so the
// two only disagree after a `crewship rehire` pushed the agent's
// forward.
func seedStagedHire(t *testing.T, db *sql.DB, agentID, status, expiresAt string) string {
	t.Helper()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `INSERT INTO agents (id, status, ephemeral, expires_at, updated_at)
		VALUES (?, ?, 1, ?, '2026-01-01T00:00:00Z')`, agentID, status, expiresAt); err != nil {
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

// lapsed / extended are the agent-side deadlines the two directions of
// the #1316 review hinge on: the hire's own TTL has run out, or a
// rehire pushed it forward while the queue row's timeout_at stayed put.
func lapsed() string   { return time.Now().UTC().Add(-time.Minute).Format(time.RFC3339) }
func extended() string { return time.Now().UTC().Add(time.Hour).Format(time.RFC3339) }

func TestSweepTimeouts_EphemeralHire_GhostsStagedAgent(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	id := seedStagedHire(t, db, "agent_staged", "PENDING_REVIEW", lapsed())

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

// A rehire extends agents.expires_at but leaves the queue row's
// timeout_at where it was — rehire reopens a hire cycle without
// enqueuing a new approval. Ghosting off the stale queue deadline would
// silently undo the operator's extension and 409 the approve that
// follows, so the agent UPDATE carries the agent's OWN deadline as a
// guard. The queue row still goes terminal: its window really did lapse,
// and approve-hire skips a terminal row anyway.
func TestSweepTimeouts_EphemeralHire_RehiredAgentNotGhosted(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()

	id := seedStagedHire(t, db, "agent_rehired", "PENDING_REVIEW", extended())

	n, err := SweepTimeouts(context.Background(), db, nil)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if n != 1 {
		t.Fatalf("swept %d rows, want 1 — the queue row's own window lapsed", n)
	}
	got, err := Get(context.Background(), db, "ws_test", id)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status != StatusTimeout {
		t.Errorf("approval status = %q, want timeout", got.Status)
	}
	if expired := agentExpiredAt(t, db, "agent_rehired"); expired.Valid {
		t.Errorf("sweeper ghosted a rehired hire off the stale queue deadline (expired_at = %q) — "+
			"the operator's TTL extension was silently undone", expired.String)
	}
}

// A hire the operator already approved must not be ghosted by a sweep
// that loses the race on the queue row's own guard — same shape as the
// deny path's status='PENDING_REVIEW' guard. Both live statuses are
// pinned: an IDLE agent waiting for work and a BUSY one mid-mission are
// the worst-case regressions here.
func TestSweepTimeouts_EphemeralHire_LeavesActivatedAgentAlone(t *testing.T) {
	for _, status := range []string{"IDLE", "BUSY"} {
		t.Run(status, func(t *testing.T) {
			db := openTestDB(t)
			defer db.Close()

			// Deliberately lapsed on BOTH deadlines: neither the queue
			// row nor the agent TTL may override status='PENDING_REVIEW'.
			seedStagedHire(t, db, "agent_live", status, lapsed())

			if _, err := SweepTimeouts(context.Background(), db, nil); err != nil {
				t.Fatalf("sweep: %v", err)
			}
			if expired := agentExpiredAt(t, db, "agent_live"); expired.Valid {
				t.Errorf("sweeper ghosted an already-activated %s agent (expired_at = %q)", status, expired.String)
			}
		})
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
