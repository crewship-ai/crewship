package pipeline

import (
	"context"
	"errors"
	"testing"
	"time"
)

// The scheduler polls every 30s, so a spread is only safe when the latest
// offset plus one tick still lands before the next occurrence.
func TestValidateSpread_TickSlack(t *testing.T) {
	tests := []struct {
		name    string
		cron    string
		spread  int
		wantErr bool
	}{
		{"five minutes, 299", "*/5 * * * *", 299, true},
		{"five minutes, 271", "*/5 * * * *", 271, true},
		{"five minutes, 270", "*/5 * * * *", 270, false},
		{"minutely, 59", "* * * * *", 59, true},
		{"minutely, 30", "* * * * *", 30, false},
		{"minutely, 31", "* * * * *", 31, true},
		{"hourly, 3599", "0 * * * *", 3599, true},
		{"hourly, 3570", "0 * * * *", 3570, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateSpread(tc.cron, "UTC", tc.spread)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if err != nil && !errors.Is(err, ErrInvalidTrigger) {
				t.Fatalf("must wrap ErrInvalidTrigger: %v", err)
			}
		})
	}
}

// A row stored before the tick slack rule (offset 290 on */5) must not be
// picked up after the next occurrence: the runtime clamps the offset.
func TestScheduleStore_Spread_LegacyOffsetClampedAtRuntime(t *testing.T) {
	db := openScheduleTestDB(t)
	defer db.Close()
	seedPipeline(t, db, "pipe_1", "digest")
	store := NewScheduleStore(db)
	ctx := context.Background()
	sp, err := store.Save(ctx, SaveScheduleInput{WorkspaceID: "ws_test", Name: "legacy", TargetPipelineID: "pipe_1",
		CronExpr: "*/5 * * * *", Timezone: "UTC", Enabled: true, SpreadSeconds: 100})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE pipeline_schedules SET spread_seconds = 299, spread_offset_seconds = 290,
		next_run_at = ? WHERE id = ?`, tsformatForTest(time.Now().Add(-275*time.Second)), sp.ID); err != nil {
		t.Fatal(err)
	}
	list, err := store.listDueSchedules(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Clamped offset is 269s (interval 300 - 30 tick - 1); 275s have elapsed.
	if len(list) != 1 {
		t.Fatalf("legacy oversized offset must be clamped so the row is due, got %d", len(list))
	}
}

// Re-upserting a trigger while its occurrence is due but held by the spread
// offset must keep that occurrence.
func TestUpsertTriggerSchedule_PreservesHeldOccurrence(t *testing.T) {
	db := openScheduleTestDB(t)
	defer db.Close()
	seedPipeline(t, db, "pipe_1", "digest")
	ctx := context.Background()
	in := SaveScheduleInput{WorkspaceID: "ws_test", Name: "t", TargetPipelineID: "pipe_1",
		CronExpr: "*/10 * * * *", Timezone: "UTC", Enabled: true}
	first, err := upsertTriggerSchedule(ctx, db, in)
	if err != nil {
		t.Fatal(err)
	}
	held := time.Now().UTC().Add(-90 * time.Second).Truncate(time.Second)
	if _, err := db.ExecContext(ctx, `UPDATE pipeline_schedules SET next_run_at = ? WHERE id = ?`,
		tsformatForTest(held), first.ID); err != nil {
		t.Fatal(err)
	}
	second, err := upsertTriggerSchedule(ctx, db, in)
	if err != nil {
		t.Fatal(err)
	}
	if second.NextRunAt == nil || !second.NextRunAt.Equal(held) {
		t.Fatalf("due occurrence lost on re-upsert: next_run_at = %v, want %v", second.NextRunAt, held)
	}
	// A changed cron legitimately recomputes.
	in.CronExpr = "0 3 * * *"
	third, err := upsertTriggerSchedule(ctx, db, in)
	if err != nil {
		t.Fatal(err)
	}
	if third.NextRunAt.Equal(held) {
		t.Fatalf("cron change must recompute next_run_at")
	}
}
