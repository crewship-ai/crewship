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
	"strings"
	"time"
)

// deliveryPriorityNormal is §11.5's baseline priority. B2's only producer
// (mentionRecorder.record) always writes this — 'stop' and 'correction' are
// reserved for the interrupt-as-event producers §11.5 describes and no
// B2 code path emits either.
// Delivery priority is §11.5's interruption-as-an-event model, stored in
// mission_comment_mentions.priority (CHECK stop|correction|normal, the
// deliveries_widen migration). §9.3's ordering is stop > correction >
// normal: a correction — a comment that arrived while the session already
// had a run in flight (B3b, #2350) — is consumed at the next safe boundary
// AHEAD of ordinary follow-ups, so the resumed step reflects the correction
// first. stop is reserved for the §10.3 stop path and is not written here.
const (
	deliveryPriorityStop       = "stop"
	deliveryPriorityCorrection = "correction"
	deliveryPriorityNormal     = "normal"
)

// The fold's priority ordering (stop > correction > normal) lives in one
// place: the ORDER BY CASE in pendingFollowUpsFor (issue_session_followups.go).
// It is not duplicated as a Go comparator because nothing re-sorts the rows
// after that query — keeping a second copy in sync would be pure risk.

// markDeliveryPriority raises a still-pending delivery's priority. Used
// when a comment turns out to have arrived during an active run (B3b): the
// delivery was created 'normal' before the session-busy result was known,
// so this reclassifies it to 'correction' while it waits to be folded into
// the next step. Scoped to state='pending' so it can never rewrite a
// delivery a run has already claimed or consumed.
func markDeliveryPriority(ctx context.Context, db *sql.DB, deliveryID, priority string) error {
	_, err := db.ExecContext(ctx,
		`UPDATE mission_comment_mentions SET priority = ? WHERE id = ? AND state = 'pending'`,
		priority, deliveryID)
	return err
}

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

// attachDeliveryRun stamps claimed_by_run_id directly, bypassing persist's
// usual ON CONFLICT path — mentionRecorder.record does NOT call this: it
// lets persist's INSERT ... ON CONFLICT(event_id, agent_id) DO UPDATE set
// claimed_by_run_id = excluded.assignment_id in the SAME statement that
// records dispatch_state/assignment_id, so there is exactly one write
// (and one failure window, not two) between "a run was dispatched" and
// "the delivery knows which run claimed it" — a real gap an earlier
// revision of this file had, caught in review: a failed second UPDATE left
// claimed_by_run_id NULL on a delivery whose run really was executing, and
// consumeDeliveriesForRun's `WHERE claimed_by_run_id = ?` then never
// matched it. Kept as a standalone primitive for direct use (tests, and any
// future caller that attaches a run outside persist's own write).
func attachDeliveryRun(ctx context.Context, db *sql.DB, deliveryID, runID string) error {
	_, err := db.ExecContext(ctx,
		`UPDATE mission_comment_mentions SET claimed_by_run_id = ? WHERE id = ?`,
		runID, deliveryID)
	return err
}

// consumeDelivery is the consume CAS — UPDATE ... WHERE state='claimed',
// same F57 shape as claimDelivery. Called once a run that claimed a delivery
// has actually finished with it (see consumeDeliveriesForRun, its bulk
// sibling) — a claimed delivery that never gets a run at all is
// failClaimedDelivery's job, not this one; see that function's doc comment
// for why the distinction matters.
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

// failClaimedDelivery is the CAS that resolves a delivery whose claim will
// never get a run — a self-mention, a capacity refusal, no dispatcher wired,
// or a dispatch error. Same F57 shape as claimDelivery/consumeDelivery.
//
// This is 'failed', not 'consumed': §9.3's state column answers "did a RUN
// consume this", and none did here — the pre-B2 fallback path (the
// issue_deliveries flag off, or createDelivery itself erroring) reaches the
// identical outcome through persist's resolvedState mapping
// (issue_mentions.go) and also writes 'failed'. Using 'consumed' here would
// make the column's meaning depend on which code path a given delivery took
// to reach the same fact, which defeats the point of having the column.
func failClaimedDelivery(ctx context.Context, db *sql.DB, deliveryID string) (bool, error) {
	res, err := db.ExecContext(ctx, `
		UPDATE mission_comment_mentions SET state = 'failed'
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

// releaseClaimedDelivery reverts a delivery from 'claimed' back to
// 'pending' — the CAS deliverAndDispatch (issue_mentions.go) uses when
// dispatch turned out to be a B3 session-busy result
// (mentionDispatchQueued, §9.4, #2339).
//
// claimDelivery has to run BEFORE the dispatch attempt (it is what
// collapses concurrent identical redeliveries of the SAME event to one
// winner — see its own doc comment), so by the time a session-busy result
// comes back the delivery is already 'claimed'. But the run that result
// names is not going to consume it — it is the run already in flight, and
// it cannot see a comment that arrived after its own exec started.
// Review on #2342 caught an earlier revision claiming it under that run
// anyway: consumeDeliveriesForRun would then mark it 'consumed' the moment
// that run finished, regardless of whether anything had actually read the
// text. 'pending', not 'failed': the work still needs doing.
// dispatchQueuedFollowUpsForSession (assignments_run.go) picks 'pending'
// rows for the (mission, agent) pair back up once the session's active run
// actually finishes and folds them into one new, real dispatch.
func releaseClaimedDelivery(ctx context.Context, db *sql.DB, deliveryID string) (bool, error) {
	res, err := db.ExecContext(ctx, `
		UPDATE mission_comment_mentions SET state = 'pending'
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

// abandonPendingDelivery resolves a delivery that never got claimed because
// the claim CAS itself errored (SQLITE_BUSY or similar) — as distinct from
// LOSING the claim race (claimDelivery's ordinary (false, nil), handled by
// its caller without ever reaching this function). Without this, an errored
// claim attempt left the row at 'pending' forever with a dispatch_state that
// reads 'failed' — a permanent, silently-stuck row nothing will ever revisit,
// since nothing scans for 'pending' rows and B4's eventual lease sweep only
// reaps 'claimed' ones.
func abandonPendingDelivery(ctx context.Context, db *sql.DB, deliveryID string) (bool, error) {
	res, err := db.ExecContext(ctx, `
		UPDATE mission_comment_mentions SET state = 'failed'
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

// claimPendingDeliveriesByID claims a specific, pre-selected set of
// 'pending' deliveries under runID in ONE statement — state and
// claimed_by_run_id together, so a failure partway through never leaves a
// delivery 'claimed' with no run that will ever consume it (the same class
// of gap attachDeliveryRun's doc comment names for the two-write shape an
// earlier revision of THIS path had).
//
// Used by dispatchQueuedFollowUpsForSession (assignments_run.go) with the
// EXACT ids its own SELECT (pendingFollowUpsFor) just read — not a fresh
// `WHERE mission_id=? AND agent_id=? AND state='pending'` predicate at
// claim time, which would also sweep up a delivery from a brand-new
// mention that arrived in the split second between that SELECT and this
// UPDATE: a comment whose text never made it into the brief this run was
// just given, claimed under it anyway, and later marked 'consumed' without
// ever being read — precisely the lie review caught elsewhere on this
// path. Passing exact ids closes that window: a delivery not in the list
// stays 'pending' for the next run to finish on this session to pick up.
func claimPendingDeliveriesByID(ctx context.Context, db *sql.DB, deliveryIDs []string, runID string) (int64, error) {
	if len(deliveryIDs) == 0 || runID == "" {
		return 0, nil
	}
	placeholders := make([]string, len(deliveryIDs))
	args := make([]any, 0, len(deliveryIDs)+1)
	args = append(args, runID)
	for i, id := range deliveryIDs {
		placeholders[i] = "?"
		args = append(args, id)
	}
	res, err := db.ExecContext(ctx,
		`UPDATE mission_comment_mentions SET state = 'claimed', claimed_by_run_id = ?
		  WHERE state = 'pending' AND id IN (`+strings.Join(placeholders, ",")+`)`,
		args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
