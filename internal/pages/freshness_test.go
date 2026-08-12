package pages

import (
	"testing"
	"time"
)

// The freshness contract (§4) is the reason Pages is not a Pushgateway. A
// pushed metric that sits there forever, rendered as if it were current, is
// worse than no dashboard at all — so the server computes one of three states
// on every read, from the timestamp IT wrote, and the producer has no say.
//
// The clock is injected for the obvious reason: a boundary that can only be
// tested by sleeping is a boundary nobody tests.

// fixedClock is the injected clock. Advance() moves it so a single test can
// walk a panel across the SLA boundary without sleeping.
type fixedClock struct{ now time.Time }

func (c *fixedClock) Now() time.Time          { return c.now }
func (c *fixedClock) Advance(d time.Duration) { c.now = c.now.Add(d) }

var epoch = time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)

func TestFreshness_StatesAcrossTheSLABoundary(t *testing.T) {
	t.Parallel()

	const sla = 30 * time.Second
	produced := epoch

	cases := []struct {
		name  string
		now   time.Time
		state State
		why   string
	}{
		{
			name: "the moment it was pushed", now: produced, state: StateFresh,
			why: "age 0 is inside every SLA",
		},
		{
			name: "one nanosecond before the deadline", now: produced.Add(sla - time.Nanosecond), state: StateFresh,
			why: "§4: fresh means within SLA, and this is still within it",
		},
		{
			name: "exactly at the deadline", now: produced.Add(sla), state: StateStale,
			why: "the SLA is when the next push was DUE. At that instant the data is no longer within it, " +
				"and a panel that waits one more tick before dimming is a panel that shows an old number as current",
		},
		{
			name: "well past the deadline", now: produced.Add(10 * sla), state: StateStale,
			why: "stale has no upper bound; it renders dimmed with an absolute age (§4 rule 3)",
		},
		{
			name: "a clock that went backwards", now: produced.Add(-time.Hour), state: StateFresh,
			why: "a negative age is a clock skew, not a stale panel. Treating it as stale would flap " +
				"every panel in the workspace on an NTP correction",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clock := &fixedClock{now: tc.now}
			got := NewEvaluator(clock).Evaluate(PanelState{
				Last: &Observation{ProducedAt: produced, Push: PushOK},
				SLA:  sla,
			})
			if got.State != tc.state {
				t.Errorf("state = %q, want %q — %s", got.State, tc.state, tc.why)
			}
		})
	}
}

// The state machine as a walk, with one panel and one clock. This is the shape
// an operator actually sees: a panel that was fine, then was not, then a
// producer run failed, then a push repaired it.
func TestFreshness_TransitionsInSequence(t *testing.T) {
	t.Parallel()

	const sla = time.Minute
	clock := &fixedClock{now: epoch}
	eval := NewEvaluator(clock)

	panel := PanelState{SLA: sla}

	if v := eval.Evaluate(panel); v.State != StateNeverProduced {
		t.Fatalf("a panel with no payload is %q, want %q — §9b.4 renders it as an em dash plus the "+
			"empty-state sentence, not as a stale value", v.State, StateNeverProduced)
	}

	panel.Last = &Observation{ProducedAt: clock.Now(), Push: PushOK}
	if v := eval.Evaluate(panel); v.State != StateFresh {
		t.Fatalf("after a push the panel is %q, want %q", v.State, StateFresh)
	}

	clock.Advance(sla)
	v := eval.Evaluate(panel)
	if v.State != StateStale {
		t.Fatalf("at the SLA boundary the panel is %q, want %q", v.State, StateStale)
	}
	if v.Age != sla {
		t.Errorf("age = %s, want %s — stale renders the age in absolute terms, so it has to be exact", v.Age, sla)
	}

	// An explicit failure push. It outranks the age: a producer that ran and
	// failed is a different fact from a producer that has gone quiet, and the
	// second one is not an excuse to stop reporting the first.
	clock.Advance(time.Second)
	panel.Last = &Observation{ProducedAt: clock.Now(), Push: PushFailed}
	if v := eval.Evaluate(panel); v.State != StateFailed {
		t.Errorf("a failure push inside the SLA reads as %q, want %q", v.State, StateFailed)
	}
	clock.Advance(10 * sla)
	if v := eval.Evaluate(panel); v.State != StateFailed {
		t.Errorf("a failure push that then went stale reads as %q, want %q — failed is the more "+
			"actionable fact and must not be overwritten by the clock", v.State, StateFailed)
	}

	// A good push repairs it.
	panel.Last = &Observation{ProducedAt: clock.Now(), Push: PushOK}
	if v := eval.Evaluate(panel); v.State != StateFresh {
		t.Errorf("a good push after a failure reads as %q, want %q", v.State, StateFresh)
	}
}

// §10b.4: when the ground moves — the producer routine is deleted, the owning
// crew is removed, the agent holding `produce` is dismissed — the panel goes to
// failed WITH A STATED REASON and stays on the page. A panel that quietly
// disappears makes the page lie about what it is supposed to show.
func TestFreshness_AFaultOutranksEverything(t *testing.T) {
	t.Parallel()

	clock := &fixedClock{now: epoch}
	eval := NewEvaluator(clock)

	const reason = "producer routine `incident-rozbor` no longer exists"
	v := eval.Evaluate(PanelState{
		Last:  &Observation{ProducedAt: clock.Now(), Push: PushOK},
		SLA:   time.Hour,
		Fault: reason,
	})
	if v.State != StateFailed {
		t.Errorf("state = %q, want %q — a panel whose producer is gone is not fresh, however recent "+
			"its last payload is", v.State, StateFailed)
	}
	if v.Reason != reason {
		t.Errorf("reason = %q, want %q — §10b.4 requires the reason to be stated, not inferred", v.Reason, reason)
	}

	// It applies to a panel that never produced, too: "waiting for first data"
	// and "its producer was deleted" are different sentences.
	v = eval.Evaluate(PanelState{SLA: time.Hour, Fault: reason})
	if v.State != StateFailed {
		t.Errorf("a never-produced panel with a fault reads as %q, want %q", v.State, StateFailed)
	}
}

// A panel with no SLA does not validate (§4 rule 1). If one reaches the
// evaluator anyway — a legacy row, a hand-edited database — it must not be
// reported as permanently fresh, which is the exact Pushgateway behaviour the
// contract exists to reject.
func TestFreshness_AMissingSLAIsNotAnExcuseToLookCurrent(t *testing.T) {
	t.Parallel()

	clock := &fixedClock{now: epoch.Add(365 * 24 * time.Hour)}
	v := NewEvaluator(clock).Evaluate(PanelState{
		Last: &Observation{ProducedAt: epoch, Push: PushOK},
		SLA:  0,
	})
	if v.State == StateFresh {
		t.Error("a panel with sla=0 reported fresh a year after its last push; there is no default " +
			"that means 'never mind' (§4 rule 1)")
	}
}

// The evaluator reads the STORED produced_at. There is no argument, field or
// setter through which a producer's own timestamp could reach it — this test
// exists so that a future convenience overload has to delete it on purpose.
func TestFreshness_UsesTheStoredTimestamp(t *testing.T) {
	t.Parallel()

	clock := &fixedClock{now: epoch.Add(2 * time.Hour)}
	stored := epoch
	v := NewEvaluator(clock).Evaluate(PanelState{
		Last: &Observation{ProducedAt: stored, Push: PushOK},
		SLA:  time.Minute,
	})
	if v.ProducedAt != stored {
		t.Errorf("ProducedAt = %s, want the stored %s", v.ProducedAt, stored)
	}
	if v.Age != 2*time.Hour {
		t.Errorf("age = %s, want 2h computed from the server clock and the stored timestamp", v.Age)
	}
}

// SystemClock is what production passes. It is trivial, and it is exactly the
// kind of trivial that gets wired up wrong once and drifts by a timezone.
func TestSystemClock_IsUTCAndMoving(t *testing.T) {
	t.Parallel()

	now := SystemClock{}.Now()
	if now.Location() != time.UTC {
		t.Errorf("SystemClock.Now() is in %s, want UTC — panel ages are compared against timestamps "+
			"stored by datetime('now'), which is UTC", now.Location())
	}
	if time.Since(now) > time.Minute {
		t.Errorf("SystemClock.Now() returned %s, which is not now", now)
	}
}
