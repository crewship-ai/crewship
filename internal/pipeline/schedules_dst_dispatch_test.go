package pipeline

// DST on the DISPATCH path (PRD §9 "Jednorázový start + recurrence + DST").
//
// The calendar preview and the dispatcher are two different pieces of code
// answering the same question, and only one of them starts runs. A preview
// that lists 25 Oct 02:30 twice proves the projection is right; it says
// nothing about whether fireOne actually fires twice, once, or stalls. These
// tests drive fireOne itself with a pinned clock across both Prague
// transitions and assert what the dispatcher does with the occurrences that
// are duplicated and the one that does not exist.
//
// Pinned clocks throughout, and never the host's: the whole question is
// where `now` sits relative to a cron grid expressed in a zone that is not
// the machine's, so a fixture that reads time.Now() would be asking a
// different question on every build agent. The host clock is never touched.

import (
	"context"
	"testing"
	"time"
	// The zone IS the fixture. Embedding tzdata means a runner without a
	// system zoneinfo database fails these tests rather than skipping them
	// — a DST test that quietly does not run is worse than no DST test.
	_ "time/tzdata"
)

// prague is the zone under test. The two transitions the fixtures below
// straddle:
//
//	autumn 2026-10-25 — 03:00 CEST steps back to 02:00 CET, so every local
//	                    time in [02:00, 03:00) happens TWICE: 02:30 CEST is
//	                    00:30Z and 02:30 CET is 01:30Z.
//	spring 2027-03-28 — 02:00 CET jumps to 03:00 CEST, so no local time in
//	                    [02:00, 03:00) exists at all: 02:30 never happens.
func pragueLoc(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("Europe/Prague")
	if err != nil {
		t.Fatalf("Europe/Prague must load — tzdata is embedded above: %v", err)
	}
	return loc
}

// dstRig saves one "30 2 * * *" Europe/Prague schedule and lets a test fire
// it repeatedly with the clock pinned wherever the fixture says.
type dstRig struct {
	rig   *pinningRig
	sched *Schedule
}

func newDSTRig(t *testing.T, ctx context.Context, cronExpr string) *dstRig {
	t.Helper()
	r := newPinningRig(t)
	seedPipelineDef(t, r.db, "pipe_dst", "dst-main", transformPipelineDef("dst-main", "fired"))
	sched, err := r.store.Save(ctx, SaveScheduleInput{
		WorkspaceID:      "ws_test",
		Name:             "dst-sched",
		TargetPipelineID: "pipe_dst",
		CronExpr:         cronExpr,
		Timezone:         "Europe/Prague",
		Enabled:          true,
	})
	if err != nil {
		t.Fatalf("save schedule: %v", err)
	}
	return &dstRig{rig: r, sched: sched}
}

// fireAt pins the clock at `now`, sets the due bar to `dueAt`, fires once,
// and returns the next_run_at the dispatcher committed (in UTC) plus how
// many runs the schedule has produced in total.
func (d *dstRig) fireAt(t *testing.T, ctx context.Context, dueAt, now time.Time) (nextRun time.Time, totalRuns int) {
	t.Helper()
	d.rig.pinClock(now)
	got, err := d.rig.store.GetByID(ctx, d.sched.ID)
	if err != nil {
		t.Fatalf("get schedule: %v", err)
	}
	due := dueAt.UTC()
	got.NextRunAt = &due
	d.rig.scheduler.fireOne(ctx, got)

	after, err := d.rig.store.GetByID(ctx, d.sched.ID)
	if err != nil {
		t.Fatalf("reload schedule: %v", err)
	}
	if after.NextRunAt == nil {
		t.Fatalf("dispatcher left next_run_at unset — the schedule would stall")
	}
	if err := d.rig.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pipeline_runs WHERE pipeline_id = 'pipe_dst'`).Scan(&totalRuns); err != nil {
		t.Fatalf("count runs: %v", err)
	}
	return after.NextRunAt.UTC(), totalRuns
}

// TestDSTDispatch_AutumnRepeatedLocalTime_FiresBothOccurrences walks the
// dispatcher through the hour Prague repeats. Cron grids are local-time
// grids, so "02:30 every day" has two instants on 25 Oct 2026 — and the
// dispatcher must produce a run at each, an hour apart, rather than
// collapsing them or skipping the day.
func TestDSTDispatch_AutumnRepeatedLocalTime_FiresBothOccurrences(t *testing.T) {
	loc := pragueLoc(t)
	ctx := t.Context()
	d := newDSTRig(t, ctx, "30 2 * * *")

	// The day before: 24 Oct 02:30 CEST == 23 Oct 00:30Z.
	prev := time.Date(2026, 10, 24, 0, 30, 0, 0, time.UTC)
	next, runs := d.fireAt(t, ctx, prev, prev)
	firstAutumn := time.Date(2026, 10, 25, 0, 30, 0, 0, time.UTC) // 02:30 CEST
	if !next.Equal(firstAutumn) {
		t.Fatalf("after the 24 Oct fire, next = %s, want %s (02:30 CEST)", next.Format(time.RFC3339), firstAutumn.Format(time.RFC3339))
	}
	if runs != 1 {
		t.Fatalf("runs after the first fire = %d, want 1", runs)
	}

	// First 02:30 of the doubled day.
	next, runs = d.fireAt(t, ctx, firstAutumn, firstAutumn)
	secondAutumn := time.Date(2026, 10, 25, 1, 30, 0, 0, time.UTC) // 02:30 CET, the repeat
	if !next.Equal(secondAutumn) {
		t.Errorf("after 02:30 CEST, next = %s, want %s (the SECOND 02:30, one hour later in UTC)",
			next.Format(time.RFC3339), secondAutumn.Format(time.RFC3339))
	}
	if runs != 2 {
		t.Errorf("runs after the 02:30 CEST fire = %d, want 2", runs)
	}

	// Second 02:30 of the doubled day — a distinct dispatch, not a dedupe.
	next, runs = d.fireAt(t, ctx, secondAutumn, secondAutumn)
	dayAfter := time.Date(2026, 10, 26, 1, 30, 0, 0, time.UTC) // 02:30 CET
	if !next.Equal(dayAfter) {
		t.Errorf("after 02:30 CET, next = %s, want %s", next.Format(time.RFC3339), dayAfter.Format(time.RFC3339))
	}
	if runs != 3 {
		t.Errorf("runs after the 02:30 CET fire = %d, want 3 — the repeated local time must dispatch twice", runs)
	}

	// Sanity on the fixture itself: the two instants really are the same
	// local wall-clock reading in different offsets.
	if a, b := firstAutumn.In(loc).Format("15:04"), secondAutumn.In(loc).Format("15:04"); a != b || a != "02:30" {
		t.Fatalf("fixture is wrong: %s and %s read %s / %s locally", firstAutumn, secondAutumn, a, b)
	}
}

// TestDSTDispatch_SpringMissingLocalTime_SkipsTheDayWithoutStalling walks the
// dispatcher through the hour Prague deletes. 02:30 does not exist on
// 28 Mar 2027, so the honest behaviour is no run that day and a due bar that
// moves on to the 29th — the failure mode to rule out is a schedule that
// parks on an instant that will never arrive.
func TestDSTDispatch_SpringMissingLocalTime_SkipsTheDayWithoutStalling(t *testing.T) {
	loc := pragueLoc(t)
	ctx := t.Context()
	d := newDSTRig(t, ctx, "30 2 * * *")

	// 27 Mar 2027 02:30 CET == 01:30Z, the last occurrence before the jump.
	beforeJump := time.Date(2027, 3, 27, 1, 30, 0, 0, time.UTC)
	next, runs := d.fireAt(t, ctx, beforeJump, beforeJump)
	if runs != 1 {
		t.Fatalf("runs after the 27 Mar fire = %d, want 1", runs)
	}
	afterJump := time.Date(2027, 3, 29, 0, 30, 0, 0, time.UTC) // 02:30 CEST on the 29th
	if !next.Equal(afterJump) {
		t.Errorf("next after 27 Mar = %s, want %s — 28 Mar has no 02:30 local, so the next real occurrence is the 29th",
			next.Format(time.RFC3339), afterJump.Format(time.RFC3339))
	}
	if next.In(loc).Day() == 28 {
		t.Errorf("the dispatcher scheduled an occurrence on 28 Mar (%s local), a wall-clock reading that never happens",
			next.In(loc).Format(time.RFC3339))
	}

	// And the 29th really does fire, so the skipped day cost one run, not
	// the schedule.
	next, runs = d.fireAt(t, ctx, afterJump, afterJump)
	if runs != 2 {
		t.Errorf("runs after the 29 Mar fire = %d, want 2", runs)
	}
	if !next.After(afterJump) {
		t.Errorf("next = %s did not advance past %s", next, afterJump)
	}
}

// TestDSTDispatch_HourlyScheduleKeepsItsCadenceAcrossBothTransitions is the
// control: an hourly cron has an occurrence on every side of both jumps, so
// neither transition may drop or duplicate a fire beyond the one hour the
// clock itself gains or loses.
func TestDSTDispatch_HourlyScheduleKeepsItsCadenceAcrossBothTransitions(t *testing.T) {
	for _, tc := range []struct {
		name  string
		start time.Time
		want  []time.Time
	}{
		{
			// 25 Oct 2026: the 02:00 local hour happens twice, so UTC sees
			// 00:00Z, 01:00Z, 02:00Z at one-hour spacing throughout.
			name:  "autumn fall-back",
			start: time.Date(2026, 10, 24, 23, 0, 0, 0, time.UTC),
			want: []time.Time{
				time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC),
				time.Date(2026, 10, 25, 1, 0, 0, 0, time.UTC),
				time.Date(2026, 10, 25, 2, 0, 0, 0, time.UTC),
			},
		},
		{
			// 28 Mar 2027: 02:00 local never happens, but UTC spacing is
			// still one hour — the local label jumps 01:00 → 03:00.
			name:  "spring forward",
			start: time.Date(2027, 3, 28, 0, 0, 0, 0, time.UTC),
			want: []time.Time{
				time.Date(2027, 3, 28, 1, 0, 0, 0, time.UTC),
				time.Date(2027, 3, 28, 2, 0, 0, 0, time.UTC),
				time.Date(2027, 3, 28, 3, 0, 0, 0, time.UTC),
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			d := newDSTRig(t, ctx, "0 * * * *")
			at := tc.start
			for i, want := range tc.want {
				next, runs := d.fireAt(t, ctx, at, at)
				if !next.Equal(want) {
					t.Fatalf("fire %d: next = %s, want %s", i, next.Format(time.RFC3339), want.Format(time.RFC3339))
				}
				if runs != i+1 {
					t.Fatalf("fire %d: runs = %d, want %d", i, runs, i+1)
				}
				at = next
			}
		})
	}
}
