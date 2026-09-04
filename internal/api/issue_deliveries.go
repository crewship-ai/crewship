package api

// issue_deliveries.go — the write path B2 asks for (PRD-ISSUES-AND-ROUTINES-
// 2026 §9.3, work package B2, #2337): create a pending delivery for one
// (event, agent) pair, claim it before dispatch, and mark it consumed once a
// run has finished with it — or, when no run is ever going to consume it, in
// the same call.
//
// mission_comment_mentions (widened by 20260904145200_deliveries_widen.sql)
// is the table. Three functions here mirror the shape
// PendingRunStore.MarkFired already proves out (F57):
//
//	createDelivery  idempotent create — INSERT OR IGNORE against
//	                UNIQUE(event_id, agent_id), so N concurrent callers
//	                naming the same (event, agent) produce exactly one row.
//	claimDelivery   the claim CAS — UPDATE ... WHERE state='pending', with
//	                err handled separately from RowsAffected()==0 (the
//	                distinction that makes SQLITE_BUSY surface as an error
//	                rather than a false "someone else won" — F57's whole
//	                point).
//	consumeDelivery /
//	consumeDeliveriesForRun
//	                the consume CAS — UPDATE ... WHERE state='claimed'.
//
// What is deliberately NOT here: a lease/reap mechanism (B4 — §9.4's
// lease_owner/lease_expires_at), the exclusivity index that makes
// assignments.session_id a hard admission gate (B3), and anything about
// context assembly or checkpoints (B5). A delivery that is claimed and never
// consumed (a crash mid-run, a restore that lost the in-flight run — see
// intent.go's F37 comment on this table) simply stays 'claimed' until B4
// ships a sweeper; that gap is named, not hidden.

import (
	"context"
	"database/sql"
	"time"
)

// deliveryPriorityNormal is §11.5's baseline priority. B2's only producer
// (mentionRecorder.record) always writes this — 'stop' and 'correction' are
// reserved for the interrupt-as-event producers §11.5 describes and no
// B2 code path emits either.
const deliveryPriorityNormal = "normal"

// deliveryParams is what createDelivery needs to write one pending row.
type deliveryParams struct {
	WorkspaceID string
	MissionID   string
	EventID     string // mission_activity.id — nullable in the schema, but every B2 caller sets it.
	CommentID   string // nullable; empty means NULL, matching persist's existing convention.
	AgentID     string
	Position    int
	Priority    string // deliveryPriorityNormal unless the caller has a reason otherwise.
}

// deliveryResult is what createDelivery hands back: the row's id, and
// whether THIS call is the one that created it — Created is what
// mentionRecorder.record uses to decide whether to broadcast
// issue.delivery.acked (once per delivery, not once per redelivery attempt).
type deliveryResult struct {
	ID      string
	Created bool
}

// createDelivery idempotently creates the pending delivery row for one
// (event_id, agent_id) pair.
//
// INSERT OR IGNORE against UNIQUE(event_id, agent_id)
// (20260904145200_deliveries_widen.sql) rather than a SELECT-then-INSERT:
// the whole point of the constraint is that N concurrent callers naming the
// same (event, agent) cannot each mint their own row — exactly the shape
// resolveOrCreateIssueAgentSession already uses for UNIQUE(mission_id,
// agent_id) (issue_sessions.go). The differences here: an UPSERT with a
// no-op DO UPDATE would work equally well, but IGNORE-then-SELECT is used
// instead because a delivery row's columns (comment_id, position, priority)
// are meaningful WRITE-ONCE facts about how the delivery was raised — an
// ON CONFLICT clause that silently rewrote them on a redelivery would make
// "which comment actually raised this" ambiguous the moment a second
// redelivery carried different values (a bug elsewhere, but one this shape
// cannot itself introduce).
func createDelivery(ctx context.Context, db *sql.DB, p deliveryParams) (deliveryResult, error) {
	if p.WorkspaceID == "" || p.MissionID == "" || p.EventID == "" || p.AgentID == "" {
		return deliveryResult{}, errDeliveryParams
	}
	priority := p.Priority
	if priority == "" {
		priority = deliveryPriorityNormal
	}

	newID := generateCUID()
	var commentVal any
	if p.CommentID != "" {
		commentVal = p.CommentID
	}
	now := time.Now().UTC().Format(time.RFC3339)

	res, err := db.ExecContext(ctx, `
		INSERT OR IGNORE INTO mission_comment_mentions
		    (id, workspace_id, mission_id, comment_id, event_id, agent_id,
		     position, state, priority, dispatch_state, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, 'pending', ?, 'skipped', ?)`,
		newID, p.WorkspaceID, p.MissionID, commentVal, p.EventID, p.AgentID,
		p.Position, priority, now)
	if err != nil {
		return deliveryResult{}, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return deliveryResult{}, err
	}
	if n > 0 {
		return deliveryResult{ID: newID, Created: true}, nil
	}

	// Lost the INSERT race (or this event/agent pair was already delivered
	// earlier) — the row exists under someone else's id. Read it back.
	var existingID string
	if err := db.QueryRowContext(ctx,
		`SELECT id FROM mission_comment_mentions WHERE event_id = ? AND agent_id = ?`,
		p.EventID, p.AgentID,
	).Scan(&existingID); err != nil {
		return deliveryResult{}, err
	}
	return deliveryResult{ID: existingID, Created: false}, nil
}

// errDeliveryParams is returned by createDelivery when a caller is missing a
// required field — a programmer error, not a runtime condition, so it is a
// sentinel rather than a formatted string per call site.
var errDeliveryParams = deliveryParamsError{}

type deliveryParamsError struct{}

func (deliveryParamsError) Error() string {
	return "create delivery: workspace_id, mission_id, event_id and agent_id are required"
}

// claimDelivery is the claim CAS — F57's shape, copied exactly:
// UPDATE ... WHERE id=? AND state='pending', then err handled SEPARATELY
// from RowsAffected()==0. That separation is what makes SQLITE_BUSY surface
// as an error the caller can retry/log rather than as a false "someone else
// already claimed it" — the exact bug F57 calls out PendingRunStore.MarkFired
// as having gotten right.
//
// Returns (true, nil) when THIS call won the claim. A caller that loses
// (false, nil) must not proceed to dispatch — see
// mentionRecorder.record's use of this.
func claimDelivery(ctx context.Context, db *sql.DB, deliveryID string) (bool, error) {
	res, err := db.ExecContext(ctx, `
		UPDATE mission_comment_mentions SET state = 'claimed'
		WHERE id = ? AND state = 'pending'`, deliveryID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// attachDeliveryRun stamps claimed_by_run_id once the winning claimant has
// an assignment id to record. No CAS guard needed: only the caller that won
// claimDelivery ever reaches this, so there is no second writer to race
// against — the same reasoning PendingRunStore.SetFiredRunID's doc comment
// gives for skipping a status guard on its own post-claim backfill.
func attachDeliveryRun(ctx context.Context, db *sql.DB, deliveryID, runID string) error {
	_, err := db.ExecContext(ctx,
		`UPDATE mission_comment_mentions SET claimed_by_run_id = ? WHERE id = ?`,
		runID, deliveryID)
	return err
}

// consumeDelivery is the consume CAS — UPDATE ... WHERE state='claimed',
// same F57 shape as claimDelivery. Used directly when a claimed delivery is
// never going to have a run consume it (dispatch was refused, skipped, or
// failed before an assignment existed) — there the winning claimant resolves
// its own delivery inline rather than waiting on a completion callback that
// will never fire.
func consumeDelivery(ctx context.Context, db *sql.DB, deliveryID string) (bool, error) {
	res, err := db.ExecContext(ctx, `
		UPDATE mission_comment_mentions SET state = 'consumed'
		WHERE id = ? AND state = 'claimed'`, deliveryID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// consumeDeliveriesForRun marks every delivery a run claimed as consumed,
// once that run has finished. Called from finishAssignment
// (assignments_run.go) — the one place an assignment's terminal status is
// decided — regardless of whether it finished COMPLETED, FAILED or
// CANCELLED: "did a run consume this" is a fact about whether the run's turn
// processed the delivery, not about whether the turn succeeded (§9.3's own
// distinction between dispatch_state and state).
//
// A bulk UPDATE rather than a per-row CAS loop: claimed_by_run_id is set by
// attachDeliveryRun ONLY on the delivery's own winning claimant, so every row
// this statement touches is already uniquely owned by this run — there is no
// second writer to lose a race against, the same reasoning attachDeliveryRun
// itself relies on.
func consumeDeliveriesForRun(ctx context.Context, db *sql.DB, runID string) (int64, error) {
	if runID == "" {
		return 0, nil
	}
	res, err := db.ExecContext(ctx, `
		UPDATE mission_comment_mentions SET state = 'consumed'
		WHERE claimed_by_run_id = ? AND state = 'claimed'`, runID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
