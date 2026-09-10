// Package work is the single owner of durable dispatch: which accepted piece
// of work runs next, on whose lease, and what happens when that lease is lost.
//
// It exists because six independent pumps can start agent work today — the
// assignment pump, the routine scheduler, the pending-run dispatcher, the agent
// cron, the recurring-issue dispatcher and the automation registry — each with
// its own idea of claiming, retrying and recovering. The contract in
// docs/prd/WEBHOOKS-AGENT-PARALLELISM-IMPLEMENTATION-1-0.md §3 requires one.
//
// It is deliberately NOT a second queue beside those. The domain tables keep
// their rows; this package owns the dispatch decision about them.
//
// # The three identities, which are routinely conflated
//
//   - work_id    one accepted unit of work. Stable across every retry. A manual
//     replay of terminal work mints a NEW work_id carrying replay_of,
//     because replaying is a new authorization, not a continuation.
//   - run_id     one attempt at that work. This is the same namespace as
//     agent_runs.id, so the journal's existing run_id index keeps
//     meaning one thing. Every retry mints a new one.
//   - session_id the conversation. At most one active turn per session (I3);
//     further messages wait in the durable mailbox rather than
//     being bounced back to the sender to retype.
//
// A fourth value does the fencing: generation, bumped on every claim. A
// transition carrying a stale generation is refused. The River spike measured a
// queue whose completion predicate was (id, state='running') with no attempt
// term, and watched a superseded worker overwrite the live attempt's result —
// see docs/prd/ADR-QUEUE-RIVER-SQLITE-2026-09-10.md. Whichever queue is
// underneath, this term is ours to carry.
package work

import (
	"errors"
	"time"
)

// State is the lifecycle of a work item. Contract §4.
type State string

const (
	// StateQueued is accepted and durable, waiting for capacity.
	StateQueued State = "queued"
	// StateStarting is claimed: capacity reserved, runtime not yet confirmed.
	StateStarting State = "starting"
	// StateRunning is a confirmed live runtime.
	StateRunning State = "running"
	// StateWaiting is parked on a durable waitpoint or on a child. It releases
	// its execution slot only once the executing process is confirmed parked or
	// finished — but the session stays logically occupied, so a later turn
	// cannot overtake it.
	StateWaiting State = "waiting"
	// StateRetryWait is a safely repeatable failure, eligible again at
	// EligibleAt.
	StateRetryWait State = "retry_wait"

	// StateSucceeded, StateFailed, StateExpired and StateCancelled are terminal.
	StateSucceeded State = "succeeded"
	StateFailed    State = "failed"
	// StateExpired means an explicit deadline passed before the work started.
	// Work without a deadline never expires on its own.
	StateExpired State = "expired"
	// StateCancelled is a CONFIRMED stop. Requesting a cancel does not reach it;
	// an unstoppable or unclear process goes to needs_reconciliation instead.
	StateCancelled State = "cancelled"

	// StateNeedsReconciliation is an unclear external effect, or a live runtime
	// whose ownership cannot be safely recovered. It is not a success and not an
	// automatic retry: it holds its conflicting capacity until a human or a
	// reconciler resolves it.
	StateNeedsReconciliation State = "needs_reconciliation"
)

// Terminal reports whether s admits no further transition. Terminal history is
// never rewritten; a rerun is a new work item.
func (s State) Terminal() bool {
	switch s {
	case StateSucceeded, StateFailed, StateExpired, StateCancelled:
		return true
	}
	return false
}

// Live reports whether s holds an execution slot.
func (s State) Live() bool { return s == StateStarting || s == StateRunning }

func (s State) valid() bool {
	switch s {
	case StateQueued, StateStarting, StateRunning, StateWaiting, StateRetryWait,
		StateSucceeded, StateFailed, StateExpired, StateCancelled, StateNeedsReconciliation:
		return true
	}
	return false
}

// allowed is the transition table. Everything absent from it is refused, which
// is the point: a state machine that accepts any edge is a status column.
//
// needs_reconciliation is reachable from every live state and from retry_wait,
// and leaves only by an explicit resolution — never by a timer.
var allowed = map[State][]State{
	StateQueued: {
		StateStarting, StateCancelled, StateExpired, StateFailed, StateNeedsReconciliation,
	},
	StateStarting: {
		StateRunning, StateRetryWait, StateFailed, StateCancelled, StateNeedsReconciliation,
		// A claim that never reached a runtime returns to the queue rather than
		// burning the work; recovery decides which, having looked at the locator.
		StateQueued,
	},
	StateRunning: {
		StateSucceeded, StateFailed, StateWaiting, StateRetryWait, StateCancelled,
		StateNeedsReconciliation,
	},
	StateWaiting: {
		StateRunning, StateQueued, StateFailed, StateCancelled, StateExpired,
		StateNeedsReconciliation,
	},
	StateRetryWait: {
		StateQueued, StateStarting, StateFailed, StateCancelled, StateExpired,
		StateNeedsReconciliation,
	},
	StateNeedsReconciliation: {
		// Resolution is explicit and authorized. It may land anywhere a human or
		// reconciler determines the truth to be, including back into the queue.
		StateQueued, StateSucceeded, StateFailed, StateCancelled,
	},
}

// CanTransition reports whether from -> to is a legal edge.
func CanTransition(from, to State) bool {
	if from == to {
		return false
	}
	for _, s := range allowed[from] {
		if s == to {
			return true
		}
	}
	return false
}

// Class splits reserved capacity. §6 gives chat a reservation that background
// cannot borrow in 1.0, so a busy background queue can never starve a person
// out of their own conversation.
type Class string

const (
	ClassChat       Class = "chat"
	ClassBackground Class = "background"
)

func (c Class) valid() bool { return c == ClassChat || c == ClassBackground }

// Source names the producer. Every one of these must go through this package;
// I7 forbids a producer with its own private path to a runtime.
type Source string

const (
	SourceWebhook      Source = "webhook"
	SourceChat         Source = "chat"
	SourceAssignment   Source = "assignment"
	SourceSchedule     Source = "schedule"
	SourcePipelineStep Source = "pipeline_step"
	SourceManual       Source = "manual"
)

// Lease and attempt geometry, from §4. These are the contract's numbers, not
// tuning knobs: recovery correctness depends on scan < lease and on heartbeat
// being well under lease.
const (
	HeartbeatInterval = 10 * time.Second
	LeaseDuration     = 60 * time.Second
	RecoveryScanMax   = 5 * time.Second

	// MaxAttempts counts attempts in total, not retries after the first.
	MaxAttempts = 5
	// Exponential backoff with FULL jitter, base 2s, capped at 300s.
	BackoffBase = 2 * time.Second
	BackoffCap  = 300 * time.Second

	// CancelGrace is how long a signalled process has before escalation to its
	// own process group. Escalation targets the run, never the agent slug.
	CancelGrace = 10 * time.Second
)

var (
	// ErrNotFound is returned when a work item, attempt or delivery is absent.
	ErrNotFound = errors.New("work: not found")
	// ErrIllegalTransition means the edge is not in the state machine.
	ErrIllegalTransition = errors.New("work: illegal transition")
	// ErrStaleGeneration means the caller's attempt has been superseded. This is
	// the fencing refusal: the caller must not retry it as if it were a
	// transient error, because a newer attempt owns the work now.
	ErrStaleGeneration = errors.New("work: stale generation")
	// ErrTerminal means the work already reached a terminal state. A cancel that
	// loses the race to a completion gets this, and the API must answer with the
	// real finished state rather than claiming a cancel it did not perform.
	ErrTerminal = errors.New("work: already terminal")
	// ErrDuplicateDelivery means this (workspace, endpoint, source id) or content
	// key was already accepted. The caller returns the ORIGINAL receipt.
	ErrDuplicateDelivery = errors.New("work: duplicate delivery")
	// ErrDeliveryConflict means the same source delivery id arrived with a
	// different body. §5 answers 409 and keeps the original record.
	ErrDeliveryConflict = errors.New("work: delivery conflict")
)

// Backoff returns the delay before attempt n (1-based) may run again: full
// jitter over an exponentially growing window, capped. Randomness comes from
// the store's injected source so a test can pin it.
func Backoff(attempt int, rnd func(int64) int64) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	window := BackoffBase
	for i := 1; i < attempt; i++ {
		window *= 2
		if window >= BackoffCap {
			window = BackoffCap
			break
		}
	}
	if window > BackoffCap {
		window = BackoffCap
	}
	// Full jitter: uniform over [0, window). A thundering herd of retries after
	// a shared outage is the failure this prevents.
	return time.Duration(rnd(int64(window)))
}
