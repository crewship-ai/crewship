package pipeline

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"testing"
)

// ---------------------------------------------------------------------------
// Leader-gate consumption at PipelineScheduler.tick (#1376).
//
// SetLeaderGate/leaderGate is the multi-replica safety mechanism: when two
// crewshipd replicas run against the same DB, only the lease holder should
// fire a due schedule. The gate mechanism itself (internal/leader) is well
// tested (internal/leader/lease_test.go); what was UNTESTED anywhere in the
// repo is that PipelineScheduler.tick actually *consults* the gate it's
// given. Before this file, deleting the
//
//	if s.leaderGate != nil && !s.leaderGate.IsLeader() { return }
//
// line in schedules.go (internal/pipeline/schedules.go) did not fail a
// single test — a due schedule would fire on every replica regardless of
// leadership, double-running the routine (duplicate agent runs, duplicate
// notifications, double API spend). These two tests pin that the gate is
// both consulted (false → no-op) and not over-applied (true → fires
// normally, so the no-op test isn't just measuring "nothing happens by
// default").
// ---------------------------------------------------------------------------

// stubLeaderGate is a minimal leader.Gate stub — IsLeader always returns the
// fixed value it was constructed with. No DB, no lease, no timers: exactly
// what a gate-consumption test needs and nothing more.
type stubLeaderGate struct{ leader bool }

func (g stubLeaderGate) IsLeader() bool { return g.leader }

// newLeaderGateTestRig seeds one due schedule bound to a real one-step agent
// pipeline and returns everything a test needs to fire a tick and inspect
// the outcome.
func newLeaderGateTestRig(t *testing.T) (*ScheduleStore, *PipelineScheduler, *mockRunner) {
	t.Helper()
	db := openScheduleTestDB(t)
	seedPipelineDef(t, db, "pipe_lg", "leader-gate-target", fmt.Sprintf(oneStepAgentDefFmt, "leader-gate-target", "lg_agent"))
	seedDueScheduleRow(t, db, "psched_lg", "pipe_lg")

	runner := newMockRunner()
	store := NewScheduleStore(db)
	pipelines := NewStore(db)
	exec := NewExecutor(pipelines, NewResolver(db), runner, nil)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	sched := NewPipelineScheduler(store, pipelines, exec, logger)
	return store, sched, runner
}

func TestPipelineScheduler_Tick_NoopWhenNotLeader(t *testing.T) {
	store, sched, runner := newLeaderGateTestRig(t)
	sched.SetLeaderGate(stubLeaderGate{leader: false})

	sched.tick(context.Background())

	runner.mu.Lock()
	calls := len(runner.calls)
	runner.mu.Unlock()
	if calls != 0 {
		t.Fatalf("runner invoked %d times while not leader; want 0 — the leader gate was not consulted", calls)
	}

	got, err := store.GetByID(context.Background(), "psched_lg")
	if err != nil {
		t.Fatalf("load schedule: %v", err)
	}
	if got.LastRunID != "" || got.LastStatus != "" {
		t.Fatalf("schedule recorded a run while not leader: last_run_id=%q last_status=%q, want both empty",
			got.LastRunID, got.LastStatus)
	}
}

func TestPipelineScheduler_Tick_FiresWhenLeader(t *testing.T) {
	store, sched, runner := newLeaderGateTestRig(t)
	sched.SetLeaderGate(stubLeaderGate{leader: true})

	sched.tick(context.Background())

	runner.mu.Lock()
	calls := len(runner.calls)
	runner.mu.Unlock()
	if calls != 1 {
		t.Fatalf("runner invoked %d times while leader; want exactly 1", calls)
	}

	got, err := store.GetByID(context.Background(), "psched_lg")
	if err != nil {
		t.Fatalf("load schedule: %v", err)
	}
	if got.LastRunID == "" || got.LastStatus != "COMPLETED" {
		t.Fatalf("schedule did not record a completed run while leader: last_run_id=%q last_status=%q",
			got.LastRunID, got.LastStatus)
	}
}
