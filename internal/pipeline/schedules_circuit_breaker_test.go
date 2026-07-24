package pipeline

// Schedule circuit breaker (#1405).
//
// Today only an unparsable cron auto-disables a schedule. A schedule
// whose TARGET routine is broken (deleted agent, bad step, expired
// credential) fires every tick, fails every time, and
// alertFailedScheduledRun raises a MANAGER inbox card per failed run
// forever — an unbounded inbox-spam + agent-cost bleed. These tests pin
// the fixed contract:
//
//   - K consecutive FAILED fires (default 5, overridable per schedule)
//     disable the schedule with disabled_reason="circuit_breaker",
//     emit ONE journal event, and raise exactly ONE actionable alert
//     for the trip itself (on top of the existing per-run failed_run
//     alerts).
//   - A COMPLETED fire resets consecutive_failures to 0.
//   - Re-enabling a tripped schedule (Save with enabled false→true)
//     clears both consecutive_failures and disabled_reason.

import (
	"context"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
)

// failingTransformDSL is a deterministic, LLM-free routine whose single
// transform step always errors (field access on a non-object) — a
// reliable way to force a FAILED scheduled run without touching agents.
const failingTransformDSL = `{"dsl_version":"1.0","name":"always-fails","steps":[{"id":"t","type":"transform","transform":{"input":"1","expression":".missing"}}]}`

func newCircuitBreakerRig(t *testing.T) *pinningRig {
	t.Helper()
	return newPinningRig(t)
}

func (r *pinningRig) saveScheduleWithMax(t *testing.T, pipelineID string, maxFailures int) *Schedule {
	t.Helper()
	sched, err := r.store.Save(context.Background(), SaveScheduleInput{
		WorkspaceID:            "ws_test",
		Name:                   "cb-sched",
		TargetPipelineID:       pipelineID,
		CronExpr:               "0 8 * * *",
		Timezone:               "UTC",
		Enabled:                true,
		MaxConsecutiveFailures: maxFailures,
	})
	if err != nil {
		t.Fatalf("save schedule: %v", err)
	}
	return sched
}

func countInboxAlerts(t *testing.T, r *pinningRig, kind string) int {
	t.Helper()
	var n int
	if err := r.db.QueryRow(`SELECT COUNT(*) FROM inbox_items WHERE kind = ?`, kind).Scan(&n); err != nil {
		t.Fatalf("count inbox alerts (%s): %v", kind, err)
	}
	return n
}

func TestScheduler_CircuitBreaker_TripsAfterKConsecutiveFailures(t *testing.T) {
	r := newCircuitBreakerRig(t)
	seedPipelineDef(t, r.db, "pipe_fail", "always-fails", failingTransformDSL)
	sched := r.saveScheduleWithMax(t, "pipe_fail", 3)

	emitter := &captureEmitter{}
	r.scheduler.SetEmitter(emitter)

	// Fire it 3 times — each a DISTINCT occurrence (a fresh NextRunAt),
	// otherwise the idempotency chokepoint (keyed on schedule ID +
	// NextRunAt) would dedupe repeat fires within the same test into a
	// single DEDUPED run instead of 3 independent FAILED ones. Each read
	// is a fresh in-memory Schedule the way the real tick() loop would
	// see it (listDueSchedules re-reads the row so each fire sees the
	// freshly-persisted consecutive_failures count).
	var got *Schedule
	for i := 0; i < 3; i++ {
		got, _ = r.store.GetByID(context.Background(), sched.ID)
		occ := time.Date(2026, 1, i+1, 8, 0, 0, 0, time.UTC)
		got.NextRunAt = &occ
		r.scheduler.fireOne(context.Background(), got)
	}

	final, err := r.store.GetByID(context.Background(), sched.ID)
	if err != nil {
		t.Fatalf("get schedule: %v", err)
	}
	if final.ConsecutiveFailures != 3 {
		t.Errorf("consecutive_failures = %d, want 3", final.ConsecutiveFailures)
	}
	if final.Enabled {
		t.Errorf("schedule should be disabled after tripping the circuit breaker")
	}
	if final.DisabledReason != "circuit_breaker" {
		t.Errorf("disabled_reason = %q, want %q", final.DisabledReason, "circuit_breaker")
	}

	// Exactly one journal event for the trip itself.
	tripped := 0
	for _, e := range emitter.entries {
		if e.Type == journal.EntryPipelineScheduleCircuitBreaker {
			tripped++
		}
	}
	if tripped != 1 {
		t.Errorf("expected exactly 1 circuit-breaker journal event, got %d", tripped)
	}

	// Exactly one trip alert, distinct from the per-run failed_run alerts
	// (which still fire once per failed run — 3 of those).
	if n := countInboxAlerts(t, r, "schedule_circuit_breaker_tripped"); n != 1 {
		t.Errorf("expected exactly 1 circuit-breaker inbox alert, got %d", n)
	}
	if n := countInboxAlerts(t, r, "failed_run"); n != 3 {
		t.Errorf("expected 3 failed_run alerts (one per failed fire), got %d", n)
	}

	// A 4th fire on an already-disabled schedule shouldn't happen in
	// production (listDueSchedules filters enabled=1), but guard that a
	// manual fireOne call against the disabled row doesn't re-trip /
	// double-alert.
	occ4 := time.Date(2026, 1, 4, 8, 0, 0, 0, time.UTC)
	final.NextRunAt = &occ4
	r.scheduler.fireOne(context.Background(), final)
	if n := countInboxAlerts(t, r, "schedule_circuit_breaker_tripped"); n != 1 {
		t.Errorf("re-firing a tripped schedule must not raise a second trip alert, got %d", n)
	}
}

func TestScheduler_CircuitBreaker_SuccessResetsCounter(t *testing.T) {
	r := newCircuitBreakerRig(t)
	seedPipelineDef(t, r.db, "pipe_fail", "always-fails", failingTransformDSL)
	seedPipelineDef(t, r.db, "pipe_ok", "always-ok", transformPipelineDef("ok", "fine"))

	failSched := r.saveScheduleWithMax(t, "pipe_fail", 5)
	got, _ := r.store.GetByID(context.Background(), failSched.ID)
	occ1 := time.Date(2026, 1, 1, 8, 0, 0, 0, time.UTC)
	got.NextRunAt = &occ1
	r.scheduler.fireOne(context.Background(), got)
	got, _ = r.store.GetByID(context.Background(), failSched.ID)
	occ2 := time.Date(2026, 1, 2, 8, 0, 0, 0, time.UTC)
	got.NextRunAt = &occ2
	r.scheduler.fireOne(context.Background(), got)

	mid, _ := r.store.GetByID(context.Background(), failSched.ID)
	if mid.ConsecutiveFailures != 2 {
		t.Fatalf("expected 2 consecutive failures before the success, got %d", mid.ConsecutiveFailures)
	}

	// Repoint the schedule at a routine that succeeds and fire again —
	// a COMPLETED fire must reset the counter to 0.
	if _, err := r.store.Save(context.Background(), SaveScheduleInput{
		ID:               failSched.ID,
		WorkspaceID:      "ws_test",
		Name:             "cb-sched",
		TargetPipelineID: "pipe_ok",
		CronExpr:         "0 8 * * *",
		Timezone:         "UTC",
		Enabled:          true,
	}); err != nil {
		t.Fatalf("repoint schedule: %v", err)
	}
	repointed, _ := r.store.GetByID(context.Background(), failSched.ID)
	occ3 := time.Date(2026, 1, 3, 8, 0, 0, 0, time.UTC)
	repointed.NextRunAt = &occ3
	r.scheduler.fireOne(context.Background(), repointed)

	after, _ := r.store.GetByID(context.Background(), failSched.ID)
	if after.ConsecutiveFailures != 0 {
		t.Errorf("consecutive_failures after a COMPLETED fire = %d, want 0", after.ConsecutiveFailures)
	}
	if after.LastStatus != "COMPLETED" {
		t.Errorf("last_status = %q, want COMPLETED", after.LastStatus)
	}
}

func TestScheduleStore_Save_EnablingTrippedScheduleResetsCircuitBreaker(t *testing.T) {
	r := newCircuitBreakerRig(t)
	seedPipelineDef(t, r.db, "pipe_fail", "always-fails", failingTransformDSL)
	sched := r.saveScheduleWithMax(t, "pipe_fail", 1)

	got, _ := r.store.GetByID(context.Background(), sched.ID)
	r.scheduler.fireOne(context.Background(), got)

	tripped, _ := r.store.GetByID(context.Background(), sched.ID)
	if tripped.Enabled || tripped.DisabledReason != "circuit_breaker" || tripped.ConsecutiveFailures != 1 {
		t.Fatalf("precondition: schedule should be tripped, got enabled=%v reason=%q failures=%d",
			tripped.Enabled, tripped.DisabledReason, tripped.ConsecutiveFailures)
	}

	// `schedules enable` round-trips through Save with enabled=true and
	// otherwise-identical fields (whole-row replace semantics).
	reenabled, err := r.store.Save(context.Background(), SaveScheduleInput{
		ID:               sched.ID,
		WorkspaceID:      "ws_test",
		Name:             tripped.Name,
		TargetPipelineID: tripped.TargetPipelineID,
		CronExpr:         tripped.CronExpr,
		Timezone:         tripped.Timezone,
		Enabled:          true,
	})
	if err != nil {
		t.Fatalf("re-enable: %v", err)
	}
	if !reenabled.Enabled {
		t.Errorf("expected schedule to be enabled")
	}
	if reenabled.ConsecutiveFailures != 0 {
		t.Errorf("consecutive_failures after re-enable = %d, want 0", reenabled.ConsecutiveFailures)
	}
	if reenabled.DisabledReason != "" {
		t.Errorf("disabled_reason after re-enable = %q, want empty", reenabled.DisabledReason)
	}
}
