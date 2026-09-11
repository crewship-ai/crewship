// Package dispatch owns execution.
//
// It exists to close review finding R3. Acceptance and execution had different
// owners: a webhook committed durable work and then started an agent directly,
// without claiming that work. So `queued` could mean "an agent is running",
// a finished agent might never finish its work item, a restart resumed nothing,
// and cancel and the metrics read a different reality than the runtime.
//
// The rule this package enforces is that there is exactly one way work runs:
//
//	Claim -> MarkStarting -> create the runtime -> StartRunning ->
//	heartbeat -> completion, or reconciliation
//
// and that the same run id ties the attempt, the agent_runs row, the runtime
// and the journal together. Adding this loop while leaving the direct path in
// place would be worse than either alone — two owners of one piece of work is
// how the same delivery runs twice — so the direct path goes in the same change.
package dispatch

import (
	"context"
	"time"

	"github.com/crewship-ai/crewship/internal/work"
)

// Outcome is what a finished attempt actually means.
//
// The four are separate because they lead to four different actions, and the
// old code collapsed three of them into "retry". An agent that failed after
// making an external change is not the same as one that never started, and
// treating them alike is how a webhook delivery gets acted on twice.
type Outcome int

const (
	// OutcomeUnclear is the zero value on purpose. An outcome nobody
	// classified is one nobody can vouch for, and the safe reading of that is
	// "somebody has to look" rather than "try it again".
	OutcomeUnclear Outcome = iota
	// OutcomeSucceeded: the work is done.
	OutcomeSucceeded
	// OutcomeFailed: it failed, it will fail again, and no retry is warranted.
	OutcomeFailed
	// OutcomeRetryable: the failure is PROVABLY before any external effect —
	// the container would not start, the run record could not be written, the
	// agent never ran. Retrying repeats nothing because nothing happened.
	OutcomeRetryable
)

func (o Outcome) String() string {
	switch o {
	case OutcomeSucceeded:
		return "succeeded"
	case OutcomeFailed:
		return "failed"
	case OutcomeRetryable:
		return "retryable"
	default:
		return "unclear"
	}
}

// Assignment is one claimed attempt, handed to a Runtime to execute.
//
// It carries the identity three systems have to agree on. The run id is the
// same value in work_attempts, in agent_runs, in the runtime's own naming and
// in the journal's trace id — one identifier, not four that happen to be
// related.
type Assignment struct {
	Item       *work.Item
	RunID      string
	Attempt    int
	Generation int64
}

// Runtime is everything the dispatcher needs from the thing that actually runs
// an agent. Production wires this to the orchestrator; tests wire it to
// something they can crash on purpose, which is the only way the crash
// guarantees can be tested at all.
type Runtime interface {
	// Locator returns the identity the runtime WILL have, derived from the
	// assignment alone and stable across processes.
	//
	// It must be computable before anything exists. That is what lets the
	// dispatcher write down where a runtime is going to be BEFORE creating it,
	// so a crash in between leaves recovery an identity to search for rather
	// than silence to interpret. A locator invented at start time would be
	// written after the danger had passed.
	Locator(a Assignment) string

	// Classify says what a failure from Run means.
	//
	// It exists because the dispatcher cannot know, and must not guess. An
	// agent turn can make external changes and then fail, so "it returned an
	// error" is not evidence that nothing happened — and retrying on that
	// basis repeats whatever did. Only the runtime knows which of its own
	// failures happened before anything left the machine.
	//
	// The default for anything unrecognised is OutcomeUnclear, not
	// OutcomeRetryable: reconciliation is the safe direction, and a retry that
	// turns out to be unsafe is not recoverable after the fact.
	Classify(a Assignment, err error) Outcome

	// Run creates the runtime and blocks until it finishes.
	//
	// It calls started() exactly once, as soon as the process exists — before
	// any result is known. That callback is what separates "a runtime is out
	// there" from "the work succeeded", and the dispatcher needs the first fact
	// long before the second.
	//
	// A non-nil error means the attempt failed. Whether that failure is worth
	// retrying is the dispatcher's decision, not the Runtime's.
	Run(ctx context.Context, a Assignment, started func()) error

	// Stop signals the runtime at locator and reports whether it is now
	// stopped. Returning (false, nil) means "I asked and it is still there" —
	// which is not a failure, and must not be reported to a user as a stop.
	Stop(ctx context.Context, locator string) (stopped bool, err error)

	// Alive reports whether a runtime still exists at locator. Recovery uses it
	// to decide between "that process is gone, the work can be retried" and
	// "something is still running under this identity and must not be joined by
	// a second one".
	//
	// An error is not a "no". A reconciler that treats an unreachable container
	// as an absent process is the same bug as an empty locator meaning nothing
	// started.
	Alive(ctx context.Context, locator string) (bool, error)
}

// Authorizer re-checks, at dispatch time, what acceptance checked at acceptance
// time.
//
// The gap between them can be long — work waits for capacity — and in that gap
// a permission can be revoked, a budget exhausted, a target deleted or an agent
// disabled. §3's I8 requires the check at BOTH ends for exactly that reason,
// and the contract is explicit that the runtime must not be handed an
// unverified snapshot taken at acceptance.
//
// Refusing here is not an error: it is the system declining to run work it is
// no longer allowed to run, and the reason is what the user will be shown.
type Authorizer interface {
	// Authorize returns a refusal reason, or "" to proceed. An error means the
	// check could not be made, which is not permission — the dispatcher parks
	// the work rather than assuming either answer.
	Authorize(ctx context.Context, a Assignment) (refusal string, err error)
}

// AuthorizerFunc adapts a function to Authorizer.
type AuthorizerFunc func(ctx context.Context, a Assignment) (string, error)

func (f AuthorizerFunc) Authorize(ctx context.Context, a Assignment) (string, error) {
	return f(ctx, a)
}

// Config tunes one dispatcher.
type Config struct {
	// Owner identifies this dispatcher in the lease. Recovery uses it to tell
	// its own abandoned attempts from another process's live ones.
	Owner string
	// Limits is the admission contract. Every producer shares it; a dispatcher
	// that narrowed it privately would be a second admission policy.
	Limits work.Limits
	// PollInterval is the floor on how often the loop looks for work when no
	// hint arrives.
	//
	// The hint is an optimisation and the poll is the guarantee. If losing a
	// wake-up could strand work, then a crash between the acceptance commit and
	// the hint would strand it — which is precisely the window the durable
	// ledger exists to survive.
	PollInterval time.Duration
	// HeartbeatInterval is how often a live attempt renews its lease. It must
	// stay well under work.LeaseDuration or a slow tick looks like a dead
	// worker.
	HeartbeatInterval time.Duration
	// CancelPollInterval is how often a running attempt checks whether someone
	// asked it to stop. A cancel recorded by another process — or before this
	// one restarted — is only visible in the ledger.
	CancelPollInterval time.Duration
	// StopGrace is how long a signalled runtime has to stop before the
	// dispatcher escalates. §4: after the grace, an unconfirmed stop is
	// reconciliation, not a cancellation.
	StopGrace time.Duration
	// RecoveryInterval is how often lease recovery runs.
	//
	// Once at boot is not enough: a server that restarts BEFORE an old lease
	// expires skips it on the way up, and if nothing runs again the work hangs
	// forever. §4 wants a scan at least every few seconds.
	RecoveryInterval time.Duration
}

func (c Config) withDefaults() Config {
	if c.Owner == "" {
		c.Owner = "dispatcher"
	}
	if c.Limits == (work.Limits{}) {
		c.Limits = work.DefaultLimits()
	}
	if c.PollInterval <= 0 {
		c.PollInterval = 2 * time.Second
	}
	if c.HeartbeatInterval <= 0 {
		c.HeartbeatInterval = work.HeartbeatInterval
	}
	if c.CancelPollInterval <= 0 {
		c.CancelPollInterval = 2 * time.Second
	}
	if c.StopGrace <= 0 {
		c.StopGrace = work.CancelGrace
	}
	if c.RecoveryInterval <= 0 {
		c.RecoveryInterval = work.RecoveryScanMax
	}
	return c
}
