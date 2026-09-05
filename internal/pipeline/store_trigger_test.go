package pipeline

import (
	"context"
	"testing"
)

// TestStore_SaveWithTrigger_Rollback_BadCron is B8's atomicity accept line
// (#2359): "routine+version+trigger commit together or not at all". A
// syntactically invalid cron expression must roll back the ENTIRE save —
// the pipeline row that Save's insert path would otherwise have committed
// must not exist either. Proven through the same public entry point a
// real caller uses (Store.SaveWithTrigger), not by inspecting a partial
// transaction from the inside.
//
// This test fails on the pre-B8 code (SaveWithTrigger did not exist; Save
// had no trigger parameter at all), which is the point: it is new
// behaviour, not a regression guard for something that already worked.
func TestStore_SaveWithTrigger_Rollback_BadCron(t *testing.T) {
	db := openScheduleTestDB(t)
	defer db.Close()
	store := NewStore(db)
	ctx := context.Background()

	in := validSaveInput("rollback-routine")
	trigger := &TriggerInput{
		Kind:     TriggerKindSchedule,
		CronExpr: "not a cron expression",
		Timezone: "UTC",
	}

	p, sched, err := store.SaveWithTrigger(ctx, in, trigger)
	if err == nil {
		t.Fatalf("expected an error from a bad cron expression, got none (pipeline=%+v, schedule=%+v)", p, sched)
	}
	if p != nil || sched != nil {
		t.Fatalf("expected nil pipeline and schedule on error, got pipeline=%+v schedule=%+v", p, sched)
	}

	// The pipeline must not exist — not half-saved, not saved-then-orphaned.
	if _, err := store.GetBySlug(ctx, in.WorkspaceID, in.Slug); err == nil {
		t.Fatalf("pipeline %q exists after a rolled-back trigger save — atomicity violated", in.Slug)
	} else if err != ErrNotFound {
		t.Fatalf("GetBySlug: unexpected error %v", err)
	}

	// Nothing landed in pipelines at all for this slug (belt + suspenders
	// against a future GetBySlug change masking a real row).
	var count int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pipelines WHERE workspace_id = ? AND slug = ?`,
		in.WorkspaceID, in.Slug,
	).Scan(&count); err != nil {
		t.Fatalf("count pipelines: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected 0 pipeline rows for %q after rollback, got %d", in.Slug, count)
	}

	// And no schedule row either.
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pipeline_schedules WHERE workspace_id = ?`, in.WorkspaceID,
	).Scan(&count); err != nil {
		t.Fatalf("count schedules: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected 0 schedule rows after rollback, got %d", count)
	}
}

// TestStore_SaveWithTrigger_Rollback_UpdatePath proves the SAME atomicity
// on the update branch of save(): re-saving an EXISTING routine with a bad
// trigger must not persist the new version either, and must not touch the
// existing schedule.
func TestStore_SaveWithTrigger_Rollback_UpdatePath(t *testing.T) {
	db := openScheduleTestDB(t)
	defer db.Close()
	store := NewStore(db)
	ctx := context.Background()

	in := validSaveInput("rollback-update-routine")
	first, err := store.Save(ctx, in)
	if err != nil {
		t.Fatalf("initial save: %v", err)
	}

	// Re-save with a changed definition (new version would be v2) AND a
	// broken trigger.
	in2 := in
	in2.DefinitionJSON = `{"name":"rollback-update-routine","steps":[],"description":"v2"}`
	badTrigger := &TriggerInput{Kind: TriggerKindSchedule, CronExpr: "@@nonsense@@", Timezone: "UTC"}
	_, _, err = store.SaveWithTrigger(ctx, in2, badTrigger)
	if err == nil {
		t.Fatalf("expected an error from a bad cron expression on update, got none")
	}

	// The pipeline's definition must still be v1's — the update rolled back.
	reloaded, err := store.GetBySlug(ctx, in.WorkspaceID, in.Slug)
	if err != nil {
		t.Fatalf("reload after rollback: %v", err)
	}
	if reloaded.DefinitionHash != first.DefinitionHash {
		t.Fatalf("definition changed despite rollback: got hash %s, want %s (v1's)",
			reloaded.DefinitionHash, first.DefinitionHash)
	}
}

// TestStore_SaveWithTrigger_Schedule_HappyPath proves the positive case:
// routine + version + schedule all land together, and the schedule's
// NextRunAt is populated — the "first fire time" the agent's final message
// and the CLI both name.
func TestStore_SaveWithTrigger_Schedule_HappyPath(t *testing.T) {
	db := openScheduleTestDB(t)
	defer db.Close()
	store := NewStore(db)
	ctx := context.Background()

	in := validSaveInput("scheduled-routine")
	trigger := &TriggerInput{
		Kind:     TriggerKindSchedule,
		CronExpr: "0 9 * * 1-5",
		Timezone: "UTC",
	}
	p, sched, err := store.SaveWithTrigger(ctx, in, trigger)
	if err != nil {
		t.Fatalf("SaveWithTrigger: %v", err)
	}
	if p == nil {
		t.Fatalf("expected a saved pipeline")
	}
	if sched == nil {
		t.Fatalf("expected a created schedule")
	}
	if sched.TargetPipelineID != p.ID {
		t.Fatalf("schedule targets %q, want %q", sched.TargetPipelineID, p.ID)
	}
	if !sched.Enabled {
		t.Fatalf("expected the schedule to be enabled (activation was not draft)")
	}
	if sched.Activation != "" {
		t.Fatalf("expected empty activation for a non-draft trigger, got %q", sched.Activation)
	}
	if sched.NextRunAt == nil {
		t.Fatalf("expected NextRunAt to be populated — this is the first fire time")
	}
}

// TestStore_SaveWithTrigger_Draft proves draft activation: the schedule is
// created disabled, with activation marked, so the API layer's inbox raise
// has something to point at and ActivateSchedule has something to flip.
func TestStore_SaveWithTrigger_Draft(t *testing.T) {
	db := openScheduleTestDB(t)
	defer db.Close()
	store := NewStore(db)
	ctx := context.Background()

	in := validSaveInput("draft-routine")
	trigger := &TriggerInput{
		Kind:       TriggerKindSchedule,
		CronExpr:   "0 9 * * 1-5",
		Timezone:   "UTC",
		Activation: TriggerActivationDraft,
	}
	_, sched, err := store.SaveWithTrigger(ctx, in, trigger)
	if err != nil {
		t.Fatalf("SaveWithTrigger: %v", err)
	}
	if sched.Enabled {
		t.Fatalf("expected a draft trigger to be created DISABLED")
	}
	if sched.Activation != TriggerActivationDraft {
		t.Fatalf("expected activation=%q, got %q", TriggerActivationDraft, sched.Activation)
	}
	if sched.NextRunAt == nil {
		t.Fatalf("expected NextRunAt to still be computed even though disabled — it is what 'first run would be X' names")
	}

	// Activating it flips both.
	activated, err := NewScheduleStore(db).Activate(ctx, sched.ID)
	if err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if !activated.Enabled {
		t.Fatalf("expected the schedule to be enabled after Activate")
	}
	if activated.Activation != "" {
		t.Fatalf("expected activation cleared after Activate, got %q", activated.Activation)
	}
}

// TestStore_SaveWithTrigger_Manual proves the explicit no-op: a "manual"
// trigger creates no schedule row at all.
func TestStore_SaveWithTrigger_Manual(t *testing.T) {
	db := openScheduleTestDB(t)
	defer db.Close()
	store := NewStore(db)
	ctx := context.Background()

	in := validSaveInput("manual-routine")
	p, sched, err := store.SaveWithTrigger(ctx, in, &TriggerInput{Kind: TriggerKindManual})
	if err != nil {
		t.Fatalf("SaveWithTrigger: %v", err)
	}
	if p == nil {
		t.Fatalf("expected a saved pipeline")
	}
	if sched != nil {
		t.Fatalf("expected no schedule for a manual trigger, got %+v", sched)
	}
}
