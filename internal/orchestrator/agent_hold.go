package orchestrator

// A HELD agent, and the third answer a dispatch error can have.
//
// The mission engine classifies every dispatch outcome into one of two
// buckets: "this task failed" (updateTaskStatus FAILED — terminal) or "the
// dispatch broke, try again next tick" (dispatchLeadPlanning's
// planningDispatched reset). Neither is right for an agent an operator has
// staged for approval:
//
//   - FAILED is wrong because the answer changes the moment a human clicks
//     approve, and a terminally failed task retries nothing. Under guided
//     autonomy an ephemeral hire is legitimately PENDING_REVIEW *while
//     waiting* — that IS the flow — so failing it turns the supported path
//     into a dead mission.
//   - retry-now is wrong because the answer does NOT change until a human
//     acts, so the retry loop spins at the tick rate, writing a fresh
//     assignment row and an ERROR line every three seconds for as long as
//     the mission lives.
//
// The third answer is DEFER: leave the work in the state it was in before
// the dispatch was attempted, write nothing, and let the next tick ask
// again. That is a wait, and a wait is what a hold means.
//
// Two halves make it hold:
//
//  1. ADMISSION. scheduleTask and dispatchLeadPlanning read agents.status in
//     the row lookup they already do, and return a *heldAgentDeferral BEFORE
//     flipping a task to IN_PROGRESS and BEFORE inserting an assignment row.
//     A held agent therefore costs zero rows per tick, however long the hold
//     stands.
//  2. THE DOOR. api's DispatchAssignment still refuses a held target — the
//     admission check above is a read followed by a write, so a hire staged
//     in between would slip past it, and a door that trusts its caller's
//     check is not a door. That refusal carries DispatchDeferred(), so the
//     goroutine that receives it unwinds the row it wrote instead of failing
//     the task.

import "errors"

// AgentStatusPendingReview is the agents.status sentinel meaning "created,
// but inert until an operator decides".
//
// It MIRRORS chatbridge.AgentStatusPendingReview, which is the definition —
// this package cannot import chatbridge (chatbridge imports orchestrator, so
// that edge is a cycle). The two are pinned equal by
// TestOrchestratorHoldSentinelMatchesChatbridge in internal/api, which
// imports both; a drift in either spelling fails there rather than silently
// giving the mission engine a predicate that never fires.
const AgentStatusPendingReview = "PENDING_REVIEW"

// agentHeldForDispatch reports whether an agents.status value means the agent
// must not be given work yet.
//
// EXACTLY ONE status qualifies, for the reasons api's refuseHeldAgent spells
// out: every other value (IDLE, RUNNING, ERROR, …) is a lifecycle state, not
// a decision, and an unknown or empty status stays permissive because a
// status nobody has decided about must not silently become a deny.
func agentHeldForDispatch(status string) bool {
	return status == AgentStatusPendingReview
}

// heldAgentDeferral is "not now, ask again after a human acts".
//
// Deliberately NOT an error the mission loop treats as a failure and NOT one
// it treats as a broken dispatch: see this file's header. It is recognised
// structurally (DispatchDeferred) rather than by type, because the same
// answer arrives from the api package — which this package cannot import —
// when the door catches a hold the admission read missed.
type heldAgentDeferral struct{ msg string }

func (e *heldAgentDeferral) Error() string { return e.msg }

// DispatchDeferred marks this error as "permanent until a human acts".
// Exported because the api package's *agentHeldError implements the same
// method and this package classifies it across the package boundary.
func (e *heldAgentDeferral) DispatchDeferred() {}

// isDeferredDispatch reports whether err means "wait for an operator" rather
// than "this failed" or "retry immediately". Matches any error in the chain
// carrying DispatchDeferred, wherever it was constructed.
func isDeferredDispatch(err error) bool {
	var d interface{ DispatchDeferred() }
	return errors.As(err, &d)
}
