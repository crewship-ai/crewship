package pipeline

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"testing"
	"time"
)

func TestScheduleSpreadOffset(t *testing.T) {
	tests := []struct {
		name   string
		id     string
		spread int
	}{
		{"typical", "psched_0123456789abcdef", 300},
		{"one second window", "psched_x", 1},
		{"one hour window", "psched_y", 3600},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			first := ScheduleSpreadOffset(tc.id, tc.spread)
			if first < 0 || first >= tc.spread {
				t.Fatalf("offset %d outside [0,%d)", first, tc.spread)
			}
			if again := ScheduleSpreadOffset(tc.id, tc.spread); again != first {
				t.Fatalf("offset is not stable: %d then %d", first, again)
			}
		})
	}
	for _, spread := range []int{0, -5} {
		if got := ScheduleSpreadOffset("psched_x", spread); got != 0 {
			t.Errorf("spread %d: offset = %d, want 0", spread, got)
		}
	}
	// Different ids actually spread out.
	seen := map[int]bool{}
	for i := 0; i < 50; i++ {
		seen[ScheduleSpreadOffset("psched_"+string(rune('a'+i%26))+string(rune('A'+i/26)), 600)] = true
	}
	if len(seen) < 10 {
		t.Errorf("only %d distinct offsets over 50 ids; the hash is not spreading", len(seen))
	}
}

func TestValidateSpread(t *testing.T) {
	tests := []struct {
		name    string
		cron    string
		spread  int
		wantErr bool
	}{
		{"off", "* * * * *", 0, false},
		{"negative", "0 * * * *", -1, true},
		{"just under a minute", "* * * * *", 30, false},
		{"equal to a minute", "* * * * *", 60, true},
		{"over a minute", "* * * * *", 61, true},
		{"just under an hour", "0 * * * *", 3570, false},
		{"equal to an hour", "0 * * * *", 3600, true},
		{"daily", "0 9 * * *", 3600, false},
		{"irregular cron uses the shortest gap", "0 9,10 * * *", 3600, true},
		{"irregular cron under the shortest gap", "0 9,10 * * *", 3570, false},
		{"rare cron", "0 0 1 1 *", 86400, false},
		{"bad cron", "not a cron", 5, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateSpread(tc.cron, "UTC", tc.spread)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateSpread(%q,%d) err = %v, wantErr %v", tc.cron, tc.spread, err, tc.wantErr)
			}
			if err != nil && !errors.Is(err, ErrInvalidTrigger) {
				t.Fatalf("error must wrap ErrInvalidTrigger so the API answers 4xx: %v", err)
			}
		})
	}
}

func TestScheduleStore_Spread_SaveAndDisplay(t *testing.T) {
	db := openScheduleTestDB(t)
	defer db.Close()
	seedPipeline(t, db, "pipe_1", "digest")
	store := NewScheduleStore(db)
	ctx := context.Background()

	plain, err := store.Save(ctx, SaveScheduleInput{WorkspaceID: "ws_test", Name: "plain", TargetPipelineID: "pipe_1",
		CronExpr: "0 * * * *", Timezone: "UTC", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if plain.SpreadSeconds != 0 || plain.SpreadOffsetSeconds != 0 || !plain.EffectiveNextRunAt().Equal(*plain.NextRunAt) {
		t.Fatalf("default must keep today's behaviour: %+v", plain)
	}

	if _, err := store.Save(ctx, SaveScheduleInput{WorkspaceID: "ws_test", Name: "bad", TargetPipelineID: "pipe_1",
		CronExpr: "0 * * * *", Timezone: "UTC", Enabled: true, SpreadSeconds: 3600}); !errors.Is(err, ErrInvalidTrigger) {
		t.Fatalf("spread equal to the interval must be refused on create, got %v", err)
	}

	sp, err := store.Save(ctx, SaveScheduleInput{WorkspaceID: "ws_test", Name: "spread", TargetPipelineID: "pipe_1",
		CronExpr: "0 * * * *", Timezone: "UTC", Enabled: true, SpreadSeconds: 600})
	if err != nil {
		t.Fatal(err)
	}
	if sp.SpreadSeconds != 600 || sp.SpreadOffsetSeconds != ScheduleSpreadOffset(sp.ID, 600) {
		t.Fatalf("spread not stored: %+v", sp)
	}
	want := sp.NextRunAt.Add(time.Duration(sp.SpreadOffsetSeconds) * time.Second)
	if got := sp.EffectiveNextRunAt(); !got.Equal(want) {
		t.Fatalf("displayed next run %v, want nominal + offset %v", got, want)
	}

	// Update: tighten the cron below the stored spread -> refused; clearing works.
	upd := SaveScheduleInput{ID: sp.ID, WorkspaceID: "ws_test", Name: "spread", TargetPipelineID: "pipe_1",
		CronExpr: "* * * * *", Timezone: "UTC", Enabled: true, SpreadSeconds: 600}
	if _, err := store.Save(ctx, upd); !errors.Is(err, ErrInvalidTrigger) {
		t.Fatalf("a spread longer than the new interval must be refused on update, got %v", err)
	}
	upd.SpreadSeconds = 0
	cleared, err := store.Save(ctx, upd)
	if err != nil {
		t.Fatal(err)
	}
	if cleared.SpreadSeconds != 0 || cleared.SpreadOffsetSeconds != 0 {
		t.Fatalf("spread 0 must clear it: %+v", cleared)
	}
}

// A due-but-held schedule is not picked up until nominal + offset; the nominal
// occurrence stays the identity and the catch-up bookkeeping is untouched.
func TestScheduleStore_Spread_DueBoundary(t *testing.T) {
	db := openScheduleTestDB(t)
	defer db.Close()
	seedPipeline(t, db, "pipe_1", "digest")
	store := NewScheduleStore(db)
	ctx := context.Background()
	sp, err := store.Save(ctx, SaveScheduleInput{WorkspaceID: "ws_test", Name: "spread", TargetPipelineID: "pipe_1",
		CronExpr: "*/10 * * * *", Timezone: "UTC", Enabled: true, SpreadSeconds: 300})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE pipeline_schedules SET spread_offset_seconds = 120 WHERE id = ?`, sp.ID); err != nil {
		t.Fatal(err)
	}
	setNominal := func(ago time.Duration) {
		t.Helper()
		if _, err := db.ExecContext(ctx, `UPDATE pipeline_schedules SET next_run_at = ? WHERE id = ?`,
			tsformatForTest(time.Now().Add(-ago)), sp.ID); err != nil {
			t.Fatal(err)
		}
	}
	due := func() int {
		t.Helper()
		list, err := store.listDueSchedules(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return len(list)
	}
	setNominal(-time.Minute) // nominal in the future
	if n := due(); n != 0 {
		t.Fatalf("future occurrence due = %d", n)
	}
	setNominal(60 * time.Second) // nominal passed, offset 120s not yet
	if n := due(); n != 0 {
		t.Fatalf("held by the offset, but listed due (n=%d)", n)
	}
	setNominal(119 * time.Second)
	if n := due(); n != 0 {
		t.Fatalf("one second before the offset elapsed, but listed due (n=%d)", n)
	}
	setNominal(122 * time.Second)
	if n := due(); n != 1 {
		t.Fatalf("offset elapsed but not listed due (n=%d)", n)
	}
}

// Occurrence identity ignores the spread: the same nominal occurrence fired
// with and without a spread dedupes to a single run.
func TestScheduledFire_SpreadDoesNotChangeOccurrenceIdentity(t *testing.T) {
	db := openFactoryTestDB(t)
	defer db.Close()
	ctx := context.Background()
	runner := newMockRunner()
	runner.outputsBySlug["agent_lead"] = []string{"ok"}
	deps := fullExecutorDeps(t, db, runner)
	exec := NewWiredExecutor(deps)
	p := saveResumePipeline(t, deps.Store, "cron-spread", agentStepDef)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	scheduler := NewPipelineScheduler(NewScheduleStore(db), deps.Store, exec, logger)

	occ := time.Date(2026, 1, 1, 8, 0, 0, 0, time.UTC)
	mk := func(spread, offset int) *Schedule {
		o := occ
		return &Schedule{ID: "psched_spread_idem", WorkspaceID: "ws_test", TargetPipelineID: p.ID,
			CronExpr: "0 8 * * *", Timezone: "UTC", NextRunAt: &o, SpreadSeconds: spread, SpreadOffsetSeconds: offset}
	}
	scheduler.fireOne(ctx, mk(300, 120))
	scheduler.fireOne(ctx, mk(0, 0))
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pipeline_runs WHERE workspace_id = ?`, "ws_test").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("run count = %d, want 1: spread must not change the occurrence identity", n)
	}
}

// Catch-up is unchanged by a spread: catchup=all still replays each missed
// nominal occurrence, and next_run_at stays on the nominal cron boundary (the
// offset is only added for display and pick-up).
func TestPipelineScheduler_Spread_CatchupAllUnchanged(t *testing.T) {
	db := openScheduleTestDB(t)
	defer db.Close()
	seedPipeline(t, db, "pipe_1", "x")
	if _, err := db.ExecContext(context.Background(),
		`UPDATE pipelines SET definition_json = ?, last_test_run_at = ?, last_test_run_passed = 1 WHERE id = 'pipe_1'`,
		`{"name":"x","steps":[{"id":"s1","type":"agent_run","agent":"agent_lead","prompt":"hi"}]}`,
		time.Now().UTC().Format(time.RFC3339Nano), // tsformat:allow: freshness checked in Go
	); err != nil {
		t.Fatal(err)
	}
	scheduleStore := NewScheduleStore(db)
	pipelineStore := NewStore(db)
	runner := &stubAgentRunner{}
	exec := newScheduleExecutor(pipelineStore, NewResolver(db), runner, db)

	tickAt := time.Now().UTC().Truncate(time.Minute).Add(59 * time.Second)
	dueAt := tickAt.Add(-5 * time.Minute).Truncate(time.Minute)
	now := time.Now().UTC().Format(time.RFC3339Nano) // tsformat:allow: created_at/updated_at are not compared in SQL
	if _, err := db.ExecContext(context.Background(), `
INSERT INTO pipeline_schedules
  (id, workspace_id, name, target_pipeline_id, cron_expr, timezone, inputs_json, enabled, next_run_at, catchup_policy, spread_seconds, spread_offset_seconds, created_at, updated_at)
VALUES ('psched_sp_all', 'ws_test', 'all', 'pipe_1', '* * * * *', 'UTC', '{}', 1, ?, 'all', 30, 10, ?, ?)`,
		tsformatForTest(dueAt), now, now); err != nil {
		t.Fatal(err)
	}
	scheduler := NewPipelineScheduler(scheduleStore, pipelineStore, exec, nil)
	scheduler.nowFn = func() time.Time { return tickAt }
	scheduler.tick(context.Background())

	if got := runner.calls.Load(); got < 4 {
		t.Errorf("catchup=all with a spread: expected >= 4 executor calls, got %d", got)
	}
	sc, err := scheduleStore.GetByID(context.Background(), "psched_sp_all")
	if err != nil {
		t.Fatal(err)
	}
	wantNext := tickAt.Truncate(time.Minute).Add(time.Minute)
	if sc.NextRunAt == nil || !sc.NextRunAt.Equal(wantNext) {
		t.Errorf("next_run_at must stay nominal: want %v, got %v", wantNext, sc.NextRunAt)
	}
	if eff := sc.EffectiveNextRunAt(); eff == nil || !eff.Equal(wantNext.Add(10*time.Second)) {
		t.Errorf("displayed next run must include the offset: want %v, got %v", wantNext.Add(10*time.Second), eff)
	}
	if sc.LastMissedCount != 0 {
		t.Errorf("last_missed_count = %d, want 0", sc.LastMissedCount)
	}
}
