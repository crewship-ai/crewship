package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/crewship-ai/crewship/internal/chatbridge"
	"github.com/crewship-ai/crewship/internal/dispatch"
)

// WebhookAuthorizer re-checks, at dispatch, what acceptance checked at
// acceptance, and it is required in production.
//
// The gap between the two can be long — work waits for capacity — and in that
// gap the answer is allowed to change. §3's I8 asks for the check at both ends
// for exactly that reason, and handing the runtime what acceptance saw would be
// checking one end of the gap twice.
//
// WHAT THIS METHOD ACTUALLY CHECKS, and nothing more:
//
//   - the agent still exists, and has not been deleted;
//   - the agent still belongs to the workspace the delivery was accepted for;
//   - the agent's crew still exists, and has not been deleted;
//   - the agent is not HELD — agents.status == PENDING_REVIEW, the one status
//     in this system that means "created, but inert until an operator says
//     otherwise" (see refuseHeldAgent in assignments.go, which owns the rule).
//     That is a deferral, not a refusal: approval is expected to arrive.
//
// WHAT IT DOES NOT CHECK, and where those live instead. An earlier version of
// this comment claimed a disabled agent and an exhausted budget among its
// concerns; the code checked neither, and a comment that promises more than the
// implementation is worse than no comment, because the next reader stops
// looking.
//
//   - There is no agent "disabled" flag in 1.0. agents.status is a LIFECYCLE
//     column — IDLE, RUNNING, ERROR — and refusing on any of those would mean
//     an agent could never be given a second task, or that one failed run
//     bricked it permanently. PENDING_REVIEW above is the only status that is a
//     decision rather than a state.
//   - Concurrency admission (per-server, per-agent, per-class) is enforced in
//     work.Claim, inside the transaction that takes the item — before this
//     method is called at all, and it has to be there: two dispatchers must not
//     both see a free slot.
//   - Per-agent ingress rate and in-flight caps are enforced at ACCEPTANCE
//     (arrival rate gate and the durable store's ingress limits), because their job
//     is to refuse a flood at the door rather than to queue it.
//   - A workspace backup holding the write lock is enforced inside the run
//     itself (refuseIfBackupInProgress in runWebhookAgent), where the lock has
//     to be held for the duration rather than sampled here.
//   - Egress policy is applied when the run request is built: an externally
//     triggered run is forced to restricted network mode regardless of the
//     crew's own setting.
//   - There is NO SPEND BUDGET for agent runs in 1.0. §6's ingress capacity
//     check (work.CheckIngressTx) is a queue-depth and byte limit, not money,
//     and nothing calls it on this path yet. When a spend gate exists it
//     belongs here, because it is exactly the kind of answer that changes while
//     work waits.
type WebhookAuthorizer struct {
	db *sql.DB
	// heldRetry is how long a held agent's work waits before being looked at
	// again. Long, because it is waiting on a person.
	heldRetry time.Duration
	// beforeDecide, when set, runs after the agent row is read and before the
	// decision is returned. It is nil in production and exists so a test can
	// hold the real authorizer open mid-decision — the window in which a
	// cancel arriving against the live attempt used to be lost.
	beforeDecide func(ctx context.Context)
}

func NewWebhookAuthorizer(db *sql.DB) *WebhookAuthorizer {
	return &WebhookAuthorizer{db: db, heldRetry: 30 * time.Second}
}

// Authorize answers with a Decision.
//
// An error means the question could not be answered, which is NOT permission.
// The dispatcher parks the work rather than guessing in either direction — and
// the distinction matters in both: reading an unavailable database as "allowed"
// runs work nobody authorised, and reading it as "refused" fails work that was
// perfectly fine.
func (a *WebhookAuthorizer) Authorize(ctx context.Context, as dispatch.Assignment) (dispatch.Decision, error) {
	if as.Item.AgentID == "" {
		return dispatch.Refuse("the work names no agent"), nil
	}

	var (
		deletedAt   sql.NullString
		crewID      sql.NullString
		status      string
		workspaceID string
	)
	err := a.db.QueryRowContext(ctx,
		`SELECT deleted_at, crew_id, status, workspace_id FROM agents WHERE id = ?`, as.Item.AgentID).
		Scan(&deletedAt, &crewID, &status, &workspaceID)
	if errors.Is(err, sql.ErrNoRows) {
		return dispatch.Refuse(fmt.Sprintf("agent %s no longer exists", as.Item.AgentID)), nil
	}
	if err != nil {
		return dispatch.Decision{}, fmt.Errorf("read agent %s at dispatch: %w", as.Item.AgentID, err)
	}
	if deletedAt.Valid && deletedAt.String != "" {
		return dispatch.Refuse(fmt.Sprintf("agent %s was deleted while this work waited",
			as.Item.AgentID)), nil
	}

	// The work was accepted for a workspace. An agent that has since moved to
	// another one must not run it: the delivery's authorization belonged to the
	// workspace it arrived in.
	if workspaceID != as.Item.WorkspaceID {
		return dispatch.Refuse(fmt.Sprintf(
			"agent %s now belongs to a different workspace than the work that named it",
			as.Item.AgentID)), nil
	}

	if a.beforeDecide != nil {
		a.beforeDecide(ctx)
	}

	// Held, not refused. An agent created or hired by another agent is staged
	// PENDING_REVIEW and is inert until an operator approves it — and approval
	// is the expected outcome, so the work waits for it rather than dying of it.
	if status == chatbridge.AgentStatusPendingReview {
		return dispatch.NotYet(fmt.Sprintf(
			"agent %s is PENDING_REVIEW: it is held until an operator approves it",
			as.Item.AgentID), a.heldRetry), nil
	}

	if crewID.Valid && crewID.String != "" {
		var crewDeleted sql.NullString
		err := a.db.QueryRowContext(ctx,
			`SELECT deleted_at FROM crews WHERE id = ?`, crewID.String).Scan(&crewDeleted)
		if errors.Is(err, sql.ErrNoRows) {
			return dispatch.Refuse(fmt.Sprintf("the crew agent %s belongs to no longer exists",
				as.Item.AgentID)), nil
		}
		if err != nil {
			return dispatch.Decision{}, fmt.Errorf("read crew %s at dispatch: %w", crewID.String, err)
		}
		if crewDeleted.Valid && crewDeleted.String != "" {
			return dispatch.Refuse(fmt.Sprintf(
				"the crew agent %s belongs to was deleted while this work waited",
				as.Item.AgentID)), nil
		}
	}

	return dispatch.Allow(), nil
}

var _ dispatch.Authorizer = (*WebhookAuthorizer)(nil)
