package pipeline

// Missed-occurrence visibility on recovery (#1409 item 2).
//
// fireOne always computes next_run_at from the scheduler clock, so
// downtime spanning N cron occurrences yields at most one fire — the
// rest are silently absorbed with no trace. This is explicitly NOT a
// backfill fix (the routine still only fires once for the current
// occurrence); it's an observability fix: emit ONE journal event per
// schedule reporting how many occurrences were skipped and the time
// window they fell in, so an operator reviewing an incident can see
// "this schedule was dark for 3 hours and missed 11 fires" instead of
// nothing at all.
//
// Every test here PINS the scheduler clock (#1740). fireOne's whole
// decision is "did a cron occurrence pass between next_run_at and now",
// so a fixture expressed as time.Now()-1s silently smuggles in the
// question "where does the wall clock currently sit relative to the cron
// grid?". That made
// TestPipelineScheduler_FireOne_NoMissedOccurrences_NoEvent red for
// roughly one second in every 3600 — rare enough to look like -shuffle
// order dependence and to go green on a re-run. The fixtures below name
// the instant instead of inheriting it.

import (
	"context"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
)

// pinClock freezes the scheduler's clock at `at`. Tests state the instant
// a fire happens rather than inheriting whatever the wall clock says, so
// the position of `at` relative to the cron grid — the only thing fireOne
// actually reasons about — is part of the fixture.
func (r *pinningRig) pinClock(at time.Time) {
	r.scheduler.nowFn = func() time.Time { return at }
}

// missedFixtureBase is an arbitrary instant that sits exactly on an hourly
// and a minutely cron occurrence, so a case can place `now` a named offset
// away from a boundary. Deliberately not "now" — see the file comment.
var missedFixtureBase = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

func missedEvents(e *captureEmitter) []journal.Entry {
	var out []journal.Entry
	for _, entry := range e.entries {
		if entry.Type == journal.EntryPipelineScheduleMissedOccurrences {
			out = append(out, entry)
		}
	}
	return out
}

// fireWithPinnedClock builds a fresh rig (its own in-memory DB, so cases
// never share idempotency or schedule rows), saves a schedule on cronExpr,
// pins the clock at `now`, forces the due bar to `dueAt`, fires once and
// returns whatever landed on the scheduler's emitter.
func fireWithPinnedClock(t *testing.T, name, cronExpr string, dueAt, now time.Time) (*Schedule, []journal.Entry) {
	t.Helper()
	r := newPinningRig(t)
	seedPipelineDef(t, r.db, "pipe_main", "main", transformPipelineDef("main", "ran"))

	sched, err := r.store.Save(context.Background(), SaveScheduleInput{
		WorkspaceID:      "ws_test",
		Name:             name,
		TargetPipelineID: "pipe_main",
		CronExpr:         cronExpr,
		Timezone:         "UTC",
		Enabled:          true,
	})
	if err != nil {
		t.Fatalf("save schedule: %v", err)
	}

	emitter := &captureEmitter{}
	r.scheduler.SetEmitter(emitter)
	r.pinClock(now)

	got, err := r.store.GetByID(context.Background(), sched.ID)
	if err != nil {
		t.Fatalf("get schedule: %v", err)
	}
	due := dueAt.UTC()
	got.NextRunAt = &due

	r.scheduler.fireOne(context.Background(), got)
	return sched, missedEvents(emitter)
}

func TestPipelineScheduler_FireOne_EmitsMissedOccurrences_AfterDowntime(t *testing.T) {
	tests := []struct {
		name       string
		cronExpr   string
		dueAt      time.Time
		now        time.Time
		wantMissed int
	}{
		{
			// The original #1409 case: the process was down for ten
			// minutes, so a minutely cron lost ten fires.
			name:       "ten minutes of downtime on a minutely cron",
			cronExpr:   "* * * * *",
			dueAt:      missedFixtureBase.Add(-10 * time.Minute),
			now:        missedFixtureBase,
			wantMissed: 10,
		},
		{
			// The counterpart to the healthy case below, and the exact
			// shape the #1740 fixture accidentally produced: an OFF-GRID
			// due bar half a second before an occurrence really does have
			// an occurrence behind it, so the breadcrumb is correct here.
			// Production never writes an off-grid next_run_at — recordRun
			// always stores a cron occurrence — which is why the old
			// time.Now()-1s fixture only misbehaved near a boundary.
			name:       "off-grid due bar with one occurrence behind it",
			cronExpr:   "* * * * *",
			dueAt:      missedFixtureBase.Add(-500 * time.Millisecond),
			now:        missedFixtureBase.Add(500 * time.Millisecond),
			wantMissed: 1,
		},
		{
			name:       "three hours of downtime on an hourly cron",
			cronExpr:   "0 * * * *",
			dueAt:      missedFixtureBase.Add(-3 * time.Hour),
			now:        missedFixtureBase,
			wantMissed: 3,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sched, missed := fireWithPinnedClock(t, "downtime", tc.cronExpr, tc.dueAt, tc.now)
			if len(missed) != 1 {
				t.Fatalf("expected exactly 1 missed-occurrences journal event, got %d", len(missed))
			}
			if got, _ := missed[0].Payload["missed_count"].(int); got != tc.wantMissed {
				t.Errorf("missed_count = %v, want %d", missed[0].Payload["missed_count"], tc.wantMissed)
			}
			if missed[0].Payload["schedule_id"] != sched.ID {
				t.Errorf("schedule_id payload = %v, want %q", missed[0].Payload["schedule_id"], sched.ID)
			}
			if missed[0].Severity != journal.SeverityWarn {
				t.Errorf("severity = %v, want warn", missed[0].Severity)
			}
			if got, want := missed[0].Payload["window_start"], tc.dueAt.UTC().Format(time.RFC3339); got != want {
				t.Errorf("window_start = %v, want %q", got, want)
			}
			if got, want := missed[0].Payload["window_end"], tc.now.UTC().Format(time.RFC3339); got != want {
				t.Errorf("window_end = %v, want %q", got, want)
			}
		})
	}
}

// TestPipelineScheduler_FireOne_NoMissedOccurrences_NoEvent is the #1740
// regression guard. A healthy on-time fire must not emit the breadcrumb —
// and "healthy" must not depend on where in the cron interval the fire
// happens to land. Each case pins the clock at a different position
// within the interval, including the boundary instant that used to make
// this test a one-in-3600 coin flip on CI, with next_run_at on the cron
// grid exactly as recordRun writes it in production. All must be silent.
func TestPipelineScheduler_FireOne_NoMissedOccurrences_NoEvent(t *testing.T) {
	tests := []struct {
		name     string
		cronExpr string
		dueAt    time.Time
		now      time.Time
	}{
		{
			name:     "hourly fired exactly on its occurrence",
			cronExpr: "0 * * * *",
			dueAt:    missedFixtureBase,
			now:      missedFixtureBase,
		},
		{
			// The #1740 instant. Under the old ambient-clock fixture this
			// is the ~1s window per hour in which the test went red.
			name:     "hourly fired half a second after its occurrence",
			cronExpr: "0 * * * *",
			dueAt:    missedFixtureBase,
			now:      missedFixtureBase.Add(500 * time.Millisecond),
		},
		{
			name:     "hourly fired mid-interval",
			cronExpr: "0 * * * *",
			dueAt:    missedFixtureBase,
			now:      missedFixtureBase.Add(30 * time.Minute),
		},
		{
			name:     "hourly fired one second before the next occurrence",
			cronExpr: "0 * * * *",
			dueAt:    missedFixtureBase,
			now:      missedFixtureBase.Add(time.Hour - time.Second),
		},
		{
			// Same boundary hazard, 60x tighter — a minutely cron makes
			// the old fixture fail once a minute rather than once an hour.
			name:     "minutely fired half a second after its occurrence",
			cronExpr: "* * * * *",
			dueAt:    missedFixtureBase,
			now:      missedFixtureBase.Add(500 * time.Millisecond),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if _, missed := fireWithPinnedClock(t, "healthy", tc.cronExpr, tc.dueAt, tc.now); len(missed) != 0 {
				t.Fatalf("unexpected missed-occurrences event on a healthy on-time fire: %+v", missed)
			}
		})
	}
}

// TestPipelineScheduler_FireOne_ClockIsDeclared pins the isolation itself:
// fireOne must read the scheduler's clock and nothing else. Pinning it to
// a fixed instant makes the same fixture produce the same verdict on every
// run, no matter what the wall clock says — the property whose absence
// made #1740 look like -shuffle order dependence. Firing the identical
// fixture twice under two different pinned clocks, one boundary-adjacent
// and one mid-interval, must give the identical (silent) result.
func TestPipelineScheduler_FireOne_ClockIsDeclared(t *testing.T) {
	// A due bar far from any wall clock this test could ever run under:
	// if fireOne still read time.Now(), a 2026-03-01 due bar would be
	// hours or years stale and the breadcrumb would fire.
	dueAt := missedFixtureBase

	for _, now := range []time.Time{
		missedFixtureBase.Add(1 * time.Millisecond), // boundary-adjacent
		missedFixtureBase.Add(17 * time.Minute),     // mid-interval
	} {
		if _, missed := fireWithPinnedClock(t, "declared", "0 * * * *", dueAt, now); len(missed) != 0 {
			t.Fatalf("pinned clock %s: fireOne consulted something other than its own clock: %+v",
				now.Format(time.RFC3339Nano), missed)
		}
	}

	// And the pinned clock is genuinely load-bearing, not ignored: move it
	// forward past three occurrences and the breadcrumb must appear with a
	// count derived from the PINNED instant, not from the wall clock.
	_, missed := fireWithPinnedClock(t, "declared", "0 * * * *", dueAt, missedFixtureBase.Add(3*time.Hour))
	if len(missed) != 1 {
		t.Fatalf("expected 1 missed-occurrences event once the pinned clock advances, got %d", len(missed))
	}
	if got, _ := missed[0].Payload["missed_count"].(int); got != 3 {
		t.Errorf("missed_count = %v, want 3 — the count must follow the pinned clock", missed[0].Payload["missed_count"])
	}
}
