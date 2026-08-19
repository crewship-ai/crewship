package pages

import "time"

// The freshness contract (PRD §4).
//
// A dashboard that silently shows old numbers is worse than no dashboard. This
// is exactly where push models fail quietly — Prometheus Pushgateway keeps a
// pushed metric forever unless the job deletes it — and where the push-native
// monitors get it right: Uptime Kuma, Healthchecks.io, Gatus and Better Stack
// all flip state when an expected ping does not arrive. Pages takes the monitor
// behaviour.
//
// Two properties make it hold:
//
//   - The state is computed, never stored. `fresh` and `stale` are a function
//     of the clock, so a stored state would still read fresh a year later.
//   - It is computed from the timestamp the SERVER wrote. There is no argument
//     to anything here through which a producer's own timestamp could arrive.

// Clock is the time source. Injected so the SLA boundary can be tested at the
// nanosecond instead of by sleeping.
type Clock interface {
	Now() time.Time
}

// SystemClock is the production clock. UTC, because panel timestamps are stored
// by datetime('now'), which is UTC — comparing them against a local time would
// make every panel in a non-UTC deployment wrong by the offset.
type SystemClock struct{}

// Now implements Clock.
func (SystemClock) Now() time.Time { return time.Now().UTC() }

// State is what the reader is told about a panel.
type State string

const (
	// StateFresh — a payload arrived within the panel's SLA. Rendered at full
	// contrast.
	StateFresh State = "fresh"
	// StateStale — the SLA has passed with no new payload. Rendered dimmed,
	// with the age in absolute terms ("last value 12:40"), never as "a while
	// ago" and never as if it were current.
	StateStale State = "stale"
	// StateFailed — the producer's last run failed, an explicit failure was
	// pushed, or the ground moved under the panel (§10b.4: its producer was
	// deleted, its crew removed, the agent holding `produce` dismissed).
	// Rendered as an em dash plus the failure, in the destructive tone.
	StateFailed State = "failed"
	// StateNeverProduced — nothing has ever been pushed. Rendered as an em dash
	// plus the empty-state sentence that says how to make data arrive.
	//
	// §4 names three states; §9b.4's render table has four rows, and this is
	// the fourth. It is not a freshness state — it is the absence of anything
	// to be fresh about — but the renderer has to tell it apart from stale, and
	// a panel restored by a rollback lands here on purpose (§10b.1: a rollback
	// restores structure, never numbers).
	StateNeverProduced State = "never_produced"
)

// PushState is what the producer said about its own push, and it is the only
// part of the verdict a producer influences. `fresh` and `stale` are not
// members: they are the server's arithmetic, not the producer's claim.
type PushState string

const (
	PushOK     PushState = "ok"
	PushFailed PushState = "failed"
)

// Observation is one stored payload's metadata — the row the ring holds, minus
// the payload itself.
type Observation struct {
	// ProducedAt is the timestamp the SERVER wrote when it accepted the push.
	ProducedAt time.Time
	// Push is the producer's own verdict on that push.
	Push PushState
}

// PanelState is everything the evaluator needs about one panel.
type PanelState struct {
	// Last is the newest stored payload, or nil when nothing has ever been
	// pushed.
	Last *Observation
	// SLA is how often data is expected. A panel without one does not validate
	// (§4 rule 1); if a zero reaches here anyway it is treated as a fault
	// rather than as "never mind", because "never mind" is the Pushgateway
	// behaviour this whole contract exists to reject.
	SLA time.Duration
	// Fault, when set, is a stated reason the panel is broken regardless of its
	// age — "producer routine `x` no longer exists". §10b.4: a panel never
	// disappears quietly; it switches to failed with a reason and stays on the
	// page, because silently shrinking the page would make it lie about what it
	// is supposed to show.
	Fault string
}

// Verdict is the computed answer for one panel.
type Verdict struct {
	State State
	// Age is now minus ProducedAt. Zero when nothing was ever produced.
	Age time.Duration
	// ProducedAt is echoed back so the renderer shows an absolute time without
	// a second lookup.
	ProducedAt time.Time
	// Reason is set for StateFailed and is safe to show internally. It is NOT
	// safe to show on a public page: failure text is internal vocabulary —
	// container names, routine slugs, crew names — and §7.3.2b is explicit that
	// a public panel shows the age and never the reason.
	Reason string
}

// Evaluator computes freshness against an injected clock.
type Evaluator struct {
	clock Clock
}

// NewEvaluator returns an Evaluator reading time from clock. Pass SystemClock{}
// in production.
func NewEvaluator(clock Clock) *Evaluator {
	if clock == nil {
		clock = SystemClock{}
	}
	return &Evaluator{clock: clock}
}

// Now exposes the evaluator's clock, so a caller stamping produced_at uses the
// same time source the verdict will be computed against.
func (e *Evaluator) Now() time.Time { return e.clock.Now() }

// Evaluate returns the panel's current state.
//
// Precedence, most actionable first:
//
//  1. a fault — the ground moved, and no amount of recent data changes that;
//  2. an explicit failure push — a producer that ran and failed is a different
//     fact from one that went quiet, and going quiet afterwards is not a reason
//     to stop reporting the first;
//  3. nothing ever pushed;
//  4. the arithmetic: age against the SLA.
func (e *Evaluator) Evaluate(p PanelState) Verdict {
	v := Verdict{}
	if p.Last != nil {
		v.ProducedAt = p.Last.ProducedAt
		v.Age = e.clock.Now().Sub(p.Last.ProducedAt)
	}

	if p.Fault != "" {
		v.State = StateFailed
		v.Reason = p.Fault
		return v
	}
	if p.Last == nil {
		v.State = StateNeverProduced
		v.Age = 0
		return v
	}
	if p.Last.Push == PushFailed {
		v.State = StateFailed
		v.Reason = "the producer's last push reported a failure"
		return v
	}
	if p.SLA <= 0 {
		// Unreachable through the authoring gate, and deliberately not
		// reported as fresh: a panel that cannot go stale is the failure mode
		// the contract exists to prevent, so it is reported as broken instead.
		v.State = StateFailed
		v.Reason = "the panel declares no SLA, so its data cannot be shown as current"
		return v
	}

	// A negative age is clock skew, not freshness. Treating it as stale would
	// flap every panel in the workspace on an NTP correction.
	if v.Age >= p.SLA {
		v.State = StateStale
		return v
	}
	v.State = StateFresh
	return v
}
