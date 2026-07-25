package pipeline

// Operator-facing reads/mutations on pipeline_routine_state (#1420 follow-up).
// The DSL can only ever WRITE a key ({{ routine.state.* }} + state_write); a
// wrong watermark therefore had no recovery path short of a DB shell. Buckets /
// Delete / Clear are that path — these tests pin the behaviour the CLI relies
// on, in particular the schedule isolation that makes "which cursor is stuck?"
// answerable.

import (
	"context"
	"testing"

	_ "modernc.org/sqlite"
)

func seedState(t *testing.T, s *RoutineStateStore, pipelineID, scheduleID, key, value string) {
	t.Helper()
	if err := s.Write(context.Background(), pipelineID, scheduleID, key, value); err != nil {
		t.Fatalf("seed write %s/%s=%s: %v", scheduleID, key, value, err)
	}
}

func TestRoutineStateStore_Buckets_GroupsBySchedule(t *testing.T) {
	db := openStoreTestDB(t)
	defer db.Close()
	s := NewRoutineStateStore(db)
	ctx := context.Background()

	// Two schedules of the same routine plus the shared manual/webhook bucket.
	seedState(t, s, "pln_a", "sched_2", "cursor", "200")
	seedState(t, s, "pln_a", "sched_1", "cursor", "100")
	seedState(t, s, "pln_a", "sched_1", "alpha", "a")
	seedState(t, s, "pln_a", "", "cursor", "manual")
	// A sibling routine must never leak in.
	seedState(t, s, "pln_b", "sched_1", "cursor", "999")

	got, err := s.Buckets(ctx, "pln_a")
	if err != nil {
		t.Fatalf("Buckets: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 buckets (\"\", sched_1, sched_2), got %d: %+v", len(got), got)
	}
	// Ordered by schedule_id, so the manual ("") bucket sorts first.
	if got[0].ScheduleID != "" || got[1].ScheduleID != "sched_1" || got[2].ScheduleID != "sched_2" {
		t.Fatalf("buckets not ordered by schedule_id: %+v", got)
	}
	// …and entries within a bucket are ordered by key.
	if len(got[1].Entries) != 2 || got[1].Entries[0].Key != "alpha" || got[1].Entries[1].Key != "cursor" {
		t.Fatalf("sched_1 entries not key-ordered: %+v", got[1].Entries)
	}
	if got[1].Entries[1].Value != "100" {
		t.Errorf("sched_1 cursor = %q, want 100", got[1].Entries[1].Value)
	}
	// updated_at is the whole reason an operator opens this view.
	if got[1].Entries[1].UpdatedAt == "" {
		t.Error("UpdatedAt must be populated — a frozen cursor's timestamp is the tell")
	}
}

func TestRoutineStateStore_Buckets_EmptyIsNotNil(t *testing.T) {
	db := openStoreTestDB(t)
	defer db.Close()
	got, err := NewRoutineStateStore(db).Buckets(context.Background(), "pln_none")
	if err != nil {
		t.Fatalf("Buckets: %v", err)
	}
	if got == nil {
		t.Fatal("must return an empty slice, not nil — it is serialized straight to JSON")
	}
	if len(got) != 0 {
		t.Fatalf("want 0 buckets, got %+v", got)
	}
}

func TestRoutineStateStore_Delete_ScopedToBucket(t *testing.T) {
	db := openStoreTestDB(t)
	defer db.Close()
	s := NewRoutineStateStore(db)
	ctx := context.Background()

	seedState(t, s, "pln_a", "sched_1", "cursor", "100")
	seedState(t, s, "pln_a", "sched_2", "cursor", "200")

	removed, err := s.Delete(ctx, "pln_a", "sched_1", "cursor")
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if !removed {
		t.Fatal("Delete should report the row was removed")
	}
	// The sibling schedule's cursor is untouched — this is the isolation the
	// (pipeline_id, schedule_id) key exists to provide.
	other, err := s.Load(ctx, "pln_a", "sched_2")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if other["cursor"] != "200" {
		t.Errorf("sched_2 cursor should survive a sched_1 delete, got %q", other["cursor"])
	}
}

func TestRoutineStateStore_Delete_MissingKeyReportsFalse(t *testing.T) {
	db := openStoreTestDB(t)
	defer db.Close()
	s := NewRoutineStateStore(db)

	removed, err := s.Delete(context.Background(), "pln_a", "sched_1", "never-written")
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if removed {
		t.Error("a key that was never written must report removed=false so the caller can 404 a typo")
	}
}

func TestRoutineStateStore_Clear_BucketScoped(t *testing.T) {
	db := openStoreTestDB(t)
	defer db.Close()
	s := NewRoutineStateStore(db)
	ctx := context.Background()

	seedState(t, s, "pln_a", "sched_1", "cursor", "100")
	seedState(t, s, "pln_a", "sched_1", "alpha", "a")
	seedState(t, s, "pln_a", "sched_2", "cursor", "200")
	seedState(t, s, "pln_a", "", "cursor", "manual")

	n, err := s.Clear(ctx, "pln_a", "sched_1")
	if err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if n != 2 {
		t.Errorf("Clear removed %d rows, want 2", n)
	}
	left, err := s.Load(ctx, "pln_a", "sched_1")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(left) != 0 {
		t.Errorf("bucket should be empty after Clear, got %+v", left)
	}
	// Every OTHER bucket survives — clearing one schedule must not make the
	// rest of the routine reprocess from scratch.
	buckets, err := s.Buckets(ctx, "pln_a")
	if err != nil {
		t.Fatalf("Buckets: %v", err)
	}
	if len(buckets) != 2 {
		t.Fatalf("want the \"\" and sched_2 buckets left, got %+v", buckets)
	}
}

func TestRoutineStateStore_NilSafe(t *testing.T) {
	// The executor tolerates a nil store (degraded wiring); the operator paths
	// must too rather than panic on a server built without the state layer.
	var s *RoutineStateStore
	ctx := context.Background()
	if got, err := s.Buckets(ctx, "pln_a"); err != nil || len(got) != 0 {
		t.Errorf("nil Buckets = %+v, %v", got, err)
	}
	if removed, err := s.Delete(ctx, "pln_a", "", "k"); err != nil || removed {
		t.Errorf("nil Delete = %v, %v", removed, err)
	}
	if n, err := s.Clear(ctx, "pln_a", ""); err != nil || n != 0 {
		t.Errorf("nil Clear = %d, %v", n, err)
	}
}
