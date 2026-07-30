package scheduler

import (
	"context"
	"database/sql"
	"testing"

	"github.com/crewship-ai/crewship/internal/chatbridge"
	"github.com/crewship-ai/crewship/internal/orchestrator"
)

// ---------------------------------------------------------------------------
// Leader-gate consumption at Scheduler.triggerAgent (#1376).
//
// SetLeaderGate/leaderGate/isLeader is the multi-replica safety mechanism:
// when two crewshipd replicas both run the agent cron scheduler against the
// same DB, only the lease holder should trigger a due agent. The gate
// mechanism itself (internal/leader) is well tested; what was UNTESTED
// anywhere in the repo is that Scheduler.triggerAgent actually *consults* the
// gate it's given. Before this file, deleting
//
//	if !s.isLeader() { return }
//
// from triggerAgent (internal/scheduler/scheduler.go) did not fail a single
// test — a scheduled agent would fire on every replica regardless of
// leadership: duplicate chats, duplicate runs, duplicate API spend. These
// two tests pin that the gate is both consulted (false → no-op, nothing
// created or updated) and not over-applied (true → fires normally).
// ---------------------------------------------------------------------------

// stubLeaderGate is a minimal leader.Gate stub — IsLeader always returns the
// fixed value it was constructed with.
type stubLeaderGate struct{ leader bool }

func (g stubLeaderGate) IsLeader() bool { return g.leader }

func newLeaderGateTestScheduler(t *testing.T) (*Scheduler, *mockResolver, *sql.DB) {
	t.Helper()
	db := testDB(t)
	seedCrew(t, db, "crew-lg", "ws1", "Alpha", "alpha")
	seedAgent(t, db, "a-lg", "bob", "Bob", "crew-lg", "ws1", "0 8 * * MON", "do work", true)

	resolver := &mockResolver{
		resolveInfo: &chatbridge.ChatInfo{
			AgentID:     "a-lg",
			AgentSlug:   "bob",
			AgentRole:   "AGENT",
			CrewID:      "crew-lg",
			CrewSlug:    "alpha",
			CLIAdapter:  "CLAUDE_CODE",
			WorkspaceID: "ws1",
		},
	}
	container := &mockContainer{ensureID: "container-lg"}
	orch := orchestrator.New(container, newMemState(), testLogger())
	s := newTestScheduler(db, resolver, container, orch)
	return s, resolver, db
}

func TestScheduler_TriggerAgent_NoopWhenNotLeader(t *testing.T) {
	s, resolver, db := newLeaderGateTestScheduler(t)
	s.SetLeaderGate(stubLeaderGate{leader: false})

	ag := scheduledAgent{
		ID: "a-lg", Slug: "bob", Name: "Bob",
		CrewID: "crew-lg", CrewSlug: "alpha",
		Cron: "0 8 * * MON", Prompt: "do work", Workspace: "ws1",
	}
	s.triggerAgent(ag)

	resolver.mu.Lock()
	defer resolver.mu.Unlock()
	if len(resolver.createdChats) != 0 {
		t.Fatalf("created %d chats while not leader; want 0 — the leader gate was not consulted", len(resolver.createdChats))
	}
	if len(resolver.createdRuns) != 0 {
		t.Fatalf("created %d runs while not leader; want 0", len(resolver.createdRuns))
	}

	var lastRun sql.NullString
	if err := db.QueryRow("SELECT schedule_last_run FROM agents WHERE id = 'a-lg'").Scan(&lastRun); err != nil {
		t.Fatalf("scan schedule_last_run: %v", err)
	}
	if lastRun.Valid && lastRun.String != "" {
		t.Errorf("schedule_last_run = %q while not leader, want untouched (empty)", lastRun.String)
	}
}

func TestScheduler_TriggerAgent_FiresWhenLeader(t *testing.T) {
	s, resolver, _ := newLeaderGateTestScheduler(t)
	s.SetLeaderGate(stubLeaderGate{leader: true})

	ag := scheduledAgent{
		ID: "a-lg", Slug: "bob", Name: "Bob",
		CrewID: "crew-lg", CrewSlug: "alpha",
		Cron: "0 8 * * MON", Prompt: "do work", Workspace: "ws1",
	}
	s.triggerAgent(ag)

	resolver.mu.Lock()
	defer resolver.mu.Unlock()
	if len(resolver.createdChats) != 1 {
		t.Fatalf("created %d chats while leader; want exactly 1", len(resolver.createdChats))
	}
	if len(resolver.createdRuns) != 1 {
		t.Fatalf("created %d runs while leader; want exactly 1", len(resolver.createdRuns))
	}
}

// ---------------------------------------------------------------------------
// Leader-gate consumption at RegisterPlatformRoutine's fired closure (#1376).
//
// This is a SEPARATE call site from triggerAgent above — same underlying
// isLeader() method, but its own independent `if !s.isLeader() { return }`
// inside the cron closure RegisterPlatformRoutine hands to s.c.AddFunc.
// Keeper Phase 2 sweeps (skill_review, memory_health_check) run through this
// path and must fire once per cluster, not once per replica. Before this
// test, deleting that check did not fail a single test either.
//
// No production code change was needed: cron.Cron already exposes Entries()
// with a Job.Run() that invokes the registered closure synchronously and
// directly — no need to wait on the real cron ticker (avoids the sleep-based
// flakiness the existing @every-1s panic-recovery test accepts).
// ---------------------------------------------------------------------------

func TestScheduler_PlatformRoutine_NoopWhenNotLeader(t *testing.T) {
	db := testDB(t)
	s := New(db, nil, nil, &mockResolver{}, nil, nil, Config{}, testLogger())
	defer s.Stop()
	s.SetLeaderGate(stubLeaderGate{leader: false})

	fired := make(chan struct{}, 1)
	if err := s.RegisterPlatformRoutine("lg_platform_routine", "@every 1h", func(ctx context.Context) {
		fired <- struct{}{}
	}); err != nil {
		t.Fatalf("RegisterPlatformRoutine: %v", err)
	}

	entries := s.c.Entries()
	if len(entries) != 1 {
		t.Fatalf("expected 1 registered cron entry, got %d", len(entries))
	}
	entries[0].Job.Run() // invoke the registered closure directly — synchronous, no ticker wait

	select {
	case <-fired:
		t.Fatal("platform routine fired while not leader; want no-op — the leader gate was not consulted")
	default:
	}
}

func TestScheduler_PlatformRoutine_FiresWhenLeader(t *testing.T) {
	db := testDB(t)
	s := New(db, nil, nil, &mockResolver{}, nil, nil, Config{}, testLogger())
	defer s.Stop()
	s.SetLeaderGate(stubLeaderGate{leader: true})

	fired := make(chan struct{}, 1)
	if err := s.RegisterPlatformRoutine("lg_platform_routine", "@every 1h", func(ctx context.Context) {
		fired <- struct{}{}
	}); err != nil {
		t.Fatalf("RegisterPlatformRoutine: %v", err)
	}

	entries := s.c.Entries()
	if len(entries) != 1 {
		t.Fatalf("expected 1 registered cron entry, got %d", len(entries))
	}
	entries[0].Job.Run() // invoke the registered closure directly — synchronous, no ticker wait

	select {
	case <-fired:
	default:
		t.Fatal("platform routine did not fire while leader; want it to run")
	}
}
