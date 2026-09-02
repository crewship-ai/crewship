package scheduler

// #2269 follow-up, defect 6: AgentRunLock had only 2 of 7 producers wired.
// The scheduler is one of the two DECIDED to wire (a routine firing while
// its agent is mid-assignment is a realistic collision — both exec into
// the identical tmux session, setupTmuxExec's opening `tmux kill-session`
// deletes whichever run got there first).

import (
	"testing"

	"github.com/crewship-ai/crewship/internal/chatbridge"
	"github.com/crewship-ai/crewship/internal/orchestrator"
)

// TestTriggerAgent_BusyAgent_SkipsWithoutCreatingChat proves a scheduled
// fire for an agent that already holds AgentRunLock (e.g. mid-assignment)
// does NOT create a chat/run or touch the container — it skips the
// occurrence entirely, the same "cheapest check first" contract
// runAssignment's own TryStart check documents. On unfixed code (no
// agentRunLock wired into Scheduler at all) this test's lock claim has no
// effect and the fire proceeds normally, creating a chat.
func TestTriggerAgent_BusyAgent_SkipsWithoutCreatingChat(t *testing.T) {
	db := testDB(t)
	seedCrew(t, db, "crew1", "ws1", "Alpha", "alpha")
	seedAgent(t, db, "a1", "bob", "Bob", "crew1", "ws1", "0 8 * * MON", "do work", true)

	resolver := &mockResolver{
		resolveInfo: &chatbridge.ChatInfo{
			AgentID:     "a1",
			AgentSlug:   "bob",
			AgentRole:   "AGENT",
			CrewID:      "crew1",
			CrewSlug:    "alpha",
			CLIAdapter:  "CLAUDE_CODE",
			WorkspaceID: "ws1",
		},
	}
	container := &mockContainer{ensureID: "container-123"}
	orch := orchestrator.New(container, newMemState(), testLogger())

	s := newTestScheduler(db, resolver, container, orch)
	lock := chatbridge.NewAgentRunLock()
	s.SetAgentRunLock(lock)

	// Simulate the agent already mid-assignment.
	if !lock.TryStart("a1") {
		t.Fatal("setup: lock should be free")
	}
	defer lock.End("a1")

	ag := scheduledAgent{
		ID: "a1", Slug: "bob", Name: "Bob",
		CrewID: "crew1", CrewSlug: "alpha",
		Cron: "0 8 * * MON", Prompt: "do work", Workspace: "ws1",
	}
	s.triggerAgent(ag)

	resolver.mu.Lock()
	defer resolver.mu.Unlock()
	if len(resolver.createdChats) != 0 {
		t.Fatalf("created %d chats, want 0 — a busy agent's scheduled fire must skip before creating any "+
			"chat/run, not race the live assignment's tmux session", len(resolver.createdChats))
	}
	if len(resolver.createdRuns) != 0 {
		t.Fatalf("created %d runs, want 0", len(resolver.createdRuns))
	}
}

// TestTriggerAgent_LockFreed_FiresNormally is the sibling positive case: an
// idle agent's scheduled fire is unaffected by a wired (but unclaimed) lock.
func TestTriggerAgent_LockFreed_FiresNormally(t *testing.T) {
	db := testDB(t)
	seedCrew(t, db, "crew1", "ws1", "Alpha", "alpha")
	seedAgent(t, db, "a1", "bob", "Bob", "crew1", "ws1", "0 8 * * MON", "do work", true)

	resolver := &mockResolver{
		resolveInfo: &chatbridge.ChatInfo{
			AgentID:     "a1",
			AgentSlug:   "bob",
			AgentRole:   "AGENT",
			CrewID:      "crew1",
			CrewSlug:    "alpha",
			CLIAdapter:  "CLAUDE_CODE",
			WorkspaceID: "ws1",
		},
	}
	container := &mockContainer{ensureID: "container-123"}
	orch := orchestrator.New(container, newMemState(), testLogger())

	s := newTestScheduler(db, resolver, container, orch)
	s.SetAgentRunLock(chatbridge.NewAgentRunLock())

	ag := scheduledAgent{
		ID: "a1", Slug: "bob", Name: "Bob",
		CrewID: "crew1", CrewSlug: "alpha",
		Cron: "0 8 * * MON", Prompt: "do work", Workspace: "ws1",
	}
	s.triggerAgent(ag)

	resolver.mu.Lock()
	defer resolver.mu.Unlock()
	if len(resolver.createdChats) != 1 {
		t.Fatalf("created %d chats, want 1 — an idle agent's fire must proceed normally", len(resolver.createdChats))
	}
}
