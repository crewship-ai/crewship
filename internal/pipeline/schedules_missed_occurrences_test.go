package pipeline

// Missed-occurrence visibility on recovery (#1409 item 2).
//
// fireOne always computes next_run_at from time.Now(), so downtime
// spanning N cron occurrences yields at most one fire — the rest are
// silently absorbed with no trace. This is explicitly NOT a backfill
// fix (the routine still only fires once for the current occurrence);
// it's an observability fix: emit ONE journal event per schedule
// reporting how many occurrences were skipped and the time window they
// fell in, so an operator reviewing an incident can see "this schedule
// was dark for 3 hours and missed 11 fires" instead of nothing at all.

import (
	"context"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
)

func TestPipelineScheduler_FireOne_EmitsMissedOccurrences_AfterDowntime(t *testing.T) {
	r := newPinningRig(t)
	seedPipelineDef(t, r.db, "pipe_main", "main", transformPipelineDef("main", "ran"))

	sched, err := r.store.Save(context.Background(), SaveScheduleInput{
		WorkspaceID:      "ws_test",
		Name:             "every-minute",
		TargetPipelineID: "pipe_main",
		CronExpr:         "* * * * *", // every minute
		Timezone:         "UTC",
		Enabled:          true,
	})
	if err != nil {
		t.Fatalf("save schedule: %v", err)
	}

	emitter := &captureEmitter{}
	r.scheduler.SetEmitter(emitter)

	// Simulate downtime: the schedule was due 10 minutes ago (as if the
	// process had been down since then) — a live process would have
	// fired ~9 times in that window; all of them were silently missed.
	stale := time.Now().Add(-10 * time.Minute).UTC()
	got, err := r.store.GetByID(context.Background(), sched.ID)
	if err != nil {
		t.Fatalf("get schedule: %v", err)
	}
	got.NextRunAt = &stale

	r.scheduler.fireOne(context.Background(), got)

	var missedEntries []journal.Entry
	for _, e := range emitter.entries {
		if e.Type == journal.EntryPipelineScheduleMissedOccurrences {
			missedEntries = append(missedEntries, e)
		}
	}
	if len(missedEntries) != 1 {
		t.Fatalf("expected exactly 1 missed-occurrences journal event, got %d", len(missedEntries))
	}
	missed, _ := missedEntries[0].Payload["missed_count"].(int)
	if missed < 5 {
		t.Errorf("missed_count = %v, want at least ~9 for a 10-minute gap on a minutely cron", missed)
	}
	if missedEntries[0].Payload["schedule_id"] != sched.ID {
		t.Errorf("schedule_id payload = %v, want %q", missedEntries[0].Payload["schedule_id"], sched.ID)
	}
	if missedEntries[0].Severity != journal.SeverityWarn {
		t.Errorf("severity = %v, want warn", missedEntries[0].Severity)
	}
}

func TestPipelineScheduler_FireOne_NoMissedOccurrences_NoEvent(t *testing.T) {
	r := newPinningRig(t)
	seedPipelineDef(t, r.db, "pipe_main", "main", transformPipelineDef("main", "ran"))

	sched, err := r.store.Save(context.Background(), SaveScheduleInput{
		WorkspaceID:      "ws_test",
		Name:             "hourly",
		TargetPipelineID: "pipe_main",
		CronExpr:         "0 * * * *", // hourly
		Timezone:         "UTC",
		Enabled:          true,
	})
	if err != nil {
		t.Fatalf("save schedule: %v", err)
	}

	emitter := &captureEmitter{}
	r.scheduler.SetEmitter(emitter)

	// A due row that just barely crossed its boundary — no downtime, no
	// missed occurrences. Must NOT emit the breadcrumb on ordinary,
	// healthy on-time fires.
	justDue := time.Now().Add(-1 * time.Second).UTC()
	got, err := r.store.GetByID(context.Background(), sched.ID)
	if err != nil {
		t.Fatalf("get schedule: %v", err)
	}
	got.NextRunAt = &justDue

	r.scheduler.fireOne(context.Background(), got)

	for _, e := range emitter.entries {
		if e.Type == journal.EntryPipelineScheduleMissedOccurrences {
			t.Fatalf("unexpected missed-occurrences event on a healthy on-time fire: %+v", e)
		}
	}
}
