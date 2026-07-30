package scheduler

import (
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
