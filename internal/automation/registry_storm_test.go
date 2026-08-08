package automation

import (
	"context"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
)

// ---------------------------------------------------------------------------
// The sustained storm — the exact condition max_per_hour exists for.
//
// TestSustainedStormCostsOneBudgetUnit pins the SHORT storm: five seconds of
// activity inside one 10s debounce window is one run and must cost one unit.
// These two pin the LONG one, which is a different question with a different
// answer: a storm that outlives the debounce ceiling produces run after run,
// and every one of those runs must be paid for.
//
// Both properties hinge on the same field. `intent.debounceMaxAt` is described
// as "the ceiling on how long a never-ending storm may keep pushing the run
// out" — but the intent is recreated from scratch on the first match after
// every Flush, so the ceiling is recomputed relative to `now` several times a
// second and never actually arrives. `charged[key]` is then pruned against a
// fireAt that slides with it, so the budget unit is spent once and never
// again.
// ---------------------------------------------------------------------------

// storm drives one matching entry every `every` for `over`, flushing on the
// registry's real 250ms cadence, and returns the enqueues it produced.
func storm(t *testing.T, reg *Registry, clock *time.Time, ws, eventType, mission string, every, over time.Duration) {
	t.Helper()
	const flushEvery = 250 * time.Millisecond
	deadline := clock.Add(over)
	next := *clock
	for clock.Before(deadline) {
		if !clock.Before(next) {
			reg.Observer([]journal.Entry{entry(ws, eventType, mission)})
			next = clock.Add(every)
		}
		reg.Flush(context.Background())
		*clock = clock.Add(flushEvery)
	}
}

// One budget unit per RUN is the stated meaning of max_per_hour. A sustained
// storm does not produce one run — the parked row fires when it reaches the
// debounce_max_at pending_runs stored for it, and the next match inserts a
// fresh row — so over half an hour of a 10s debounce it produces roughly
// 1800/100 = 18 of them. Eighteen runs must cost eighteen units.
//
// This is the accounting behind TestSustainedStormPastTheCeilingPaysAgain,
// asserted directly on the window so the number is visible rather than
// inferred from whether a throttle notice appeared. The cap is set far out of
// reach here precisely so the count, and not the refusal, is what is measured.
func TestSustainedStormChargesOneUnitPerRun(t *testing.T) {
	start := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	now := start
	reg := NewRegistry(nil, &recordingEnqueuer{}, Options{Now: func() time.Time { return now }})

	r := rule("a1", "ws_1", "mission.status_change")
	r.DebounceSeconds = 10
	r.MaxPerHour = 10000 // out of reach: measure the charge, not the refusal
	reg.Load([]Resolved{r})

	const over = 30 * time.Minute
	ceilingWidth := time.Duration(r.DebounceSeconds*10) * time.Second
	wantRuns := int(over / ceilingWidth)

	storm(t, reg, &now, "ws_1", "mission.status_change", "m_1", 5*time.Second, over)

	reg.mu.Lock()
	charged := 0
	if w := reg.rate[r.ID]; w != nil {
		charged = w.count
	}
	reg.mu.Unlock()

	// Allow one either way for where the storm's start and end land relative
	// to a ceiling boundary; the defect is off by a factor of eighteen, not by
	// one.
	if charged < wantRuns-1 || charged > wantRuns+1 {
		t.Fatalf("budget units charged = %d over %s, want ~%d (one per run: a run fires "+
			"every debounce_seconds×10 = %s and the next match starts a new one). "+
			"charged[key] is pruned against a fireAt the storm keeps pushing forward, so "+
			"one unit covers an unbounded number of runs",
			charged, over, wantRuns, ceilingWidth)
	}
}

// max_per_hour caps how many RUNS an automation may cause per rolling hour.
// A storm lasting half an hour produces a run every ceiling-width (100s for a
// 10s debounce) because pending_runs fires the parked row at its
// debounce_max_at and the next match inserts a fresh one — so roughly eighteen
// runs. With max_per_hour=1 every run after the first must be refused, and the
// refusal recorded once for the window.
//
// The budget is instead charged exactly once: charged[key] is pruned only when
// `now` reaches the fireAt the Observer keeps pushing forward, which under a
// sustained storm is never. A cap that cannot be reached by a storm is a cap
// that does not exist.
func TestSustainedStormPastTheCeilingPaysAgain(t *testing.T) {
	start := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	now := start
	enq := &recordingEnqueuer{}
	jrn := &recordingJournal{}
	reg := NewRegistry(nil, enq, Options{Journal: jrn, Now: func() time.Time { return now }})

	r := rule("a1", "ws_1", "mission.status_change")
	r.DebounceSeconds = 10
	r.MaxPerHour = 1
	reg.Load([]Resolved{r})

	// Half an hour of one matching event every 5s — inside the debounce, so
	// the key never goes quiet, and far past the 100s ceiling.
	storm(t, reg, &now, "ws_1", "mission.status_change", "m_1", 5*time.Second, 30*time.Minute)

	if got := jrn.count(journal.EntryAutomationThrottled); got == 0 {
		t.Fatalf("throttle notices = 0 after 30 minutes of sustained matches against a "+
			"max_per_hour=1 automation: the budget was charged once and never again, so "+
			"the cap is unreachable by the storm it exists to bound (%d enqueues)", enq.n())
	}
}
