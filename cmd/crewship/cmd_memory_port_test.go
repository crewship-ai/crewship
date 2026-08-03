package main

import (
	"testing"

	"github.com/crewship-ai/crewship/internal/memory"
	"github.com/crewship-ai/crewship/internal/memport"
)

func docsFor(t *testing.T, got []memport.Doc) []string {
	t.Helper()
	out := make([]string, 0, len(got))
	for _, d := range got {
		out = append(out, d.RelPath)
	}
	return out
}

// A NanoClaw source produces both tiers at once: the group's own
// CLAUDE.md is agent knowledge, groups/global/CLAUDE.md is shared. They
// live in two different directories on the server, so one import
// request cannot carry both.
func TestRouteByTarget_AgentTargetHoldsBackCrewTier(t *testing.T) {
	plan := memport.Plan{Docs: []memport.Doc{
		{Tier: memory.TierAgent, Scope: memport.ScopeAgent, RelPath: "AGENT.md"},
		{Tier: memory.TierCrew, Scope: memport.ScopeCrew, RelPath: "CREW.md"},
	}}

	agentDocs, crewDocs, blocked := routeByTarget(plan, "alex", false)

	if got := docsFor(t, agentDocs); len(got) != 1 || got[0] != "AGENT.md" {
		t.Errorf("agent docs = %v, want [AGENT.md]", got)
	}
	// Crew-shared memory is read by every agent in the crew. Writing it
	// as a side effect of "import into alex" is a blast radius the
	// operator did not ask for.
	if len(crewDocs) != 0 {
		t.Errorf("crew docs = %v, want none without the explicit opt-in", docsFor(t, crewDocs))
	}
	if len(blocked) != 1 || blocked[0].Source != "CREW.md" {
		t.Fatalf("blocked = %+v, want CREW.md reported", blocked)
	}
	if blocked[0].Reason == "" {
		t.Error("held-back document carries no reason")
	}
}

func TestRouteByTarget_WithCrewOptIn(t *testing.T) {
	plan := memport.Plan{Docs: []memport.Doc{
		{Tier: memory.TierAgent, Scope: memport.ScopeAgent, RelPath: "AGENT.md"},
		{Tier: memory.TierCrew, Scope: memport.ScopeCrew, RelPath: "CREW.md"},
	}}

	agentDocs, crewDocs, blocked := routeByTarget(plan, "alex", true)

	if got := docsFor(t, agentDocs); len(got) != 1 || got[0] != "AGENT.md" {
		t.Errorf("agent docs = %v, want [AGENT.md]", got)
	}
	if got := docsFor(t, crewDocs); len(got) != 1 || got[0] != "CREW.md" {
		t.Errorf("crew docs = %v, want [CREW.md]", got)
	}
	if len(blocked) != 0 {
		t.Errorf("blocked = %+v, want none", blocked)
	}
}

// Importing into the crew tier with no agent named has nowhere to put
// an agent's private notes; saying so beats writing AGENT.md into a
// directory no agent reads.
func TestRouteByTarget_CrewTargetHoldsBackAgentTier(t *testing.T) {
	plan := memport.Plan{Docs: []memport.Doc{
		{Tier: memory.TierAgent, Scope: memport.ScopeAgent, RelPath: "AGENT.md"},
		{Tier: memory.TierCrew, Scope: memport.ScopeCrew, RelPath: "CREW.md"},
	}}

	agentDocs, crewDocs, blocked := routeByTarget(plan, "", false)

	if len(agentDocs) != 0 {
		t.Errorf("agent docs = %v, want none with no agent target", docsFor(t, agentDocs))
	}
	if got := docsFor(t, crewDocs); len(got) != 1 || got[0] != "CREW.md" {
		t.Errorf("crew docs = %v, want [CREW.md]", got)
	}
	if len(blocked) != 1 || blocked[0].Source != "AGENT.md" {
		t.Fatalf("blocked = %+v, want AGENT.md reported", blocked)
	}
}

// A crew's pinned notes carry tier "pins" and are still crew-shared.
// Routing on tier alone sent them into one agent's private directory.
func TestRouteByTarget_TreatsEveryCrewScopedDocAlike(t *testing.T) {
	plan := memport.Plan{Docs: []memport.Doc{
		{Tier: memory.TierCrew, Scope: memport.ScopeCrew, RelPath: "CREW.md"},
		{Tier: memory.TierPins, Scope: memport.ScopeCrew, RelPath: "eng/topics/pins.md"},
	}}
	agentDocs, crewDocs, blocked := routeByTarget(plan, "alex", false)
	if len(agentDocs) != 0 {
		t.Errorf("agent docs = %v, want none", docsFor(t, agentDocs))
	}
	if len(crewDocs) != 0 {
		t.Errorf("crew docs = %v, want none without opt-in", docsFor(t, crewDocs))
	}
	if len(blocked) != 2 {
		t.Errorf("blocked = %+v, want both held back", blocked)
	}
}

// The round trip a crew export must survive: pins exported from the
// crew tier come back with crew scope and go home, instead of being
// refused as agent-private or silently filed inside one agent.
func TestRouteByTarget_CrewPinsRoundTripHome(t *testing.T) {
	plan := memport.Plan{Docs: []memport.Doc{
		{Tier: memory.TierPins, Scope: memport.ScopeCrew, RelPath: "engineering/topics/pins.md"},
	}}

	agentDocs, crewDocs, blocked := routeByTarget(plan, "", false)

	if len(agentDocs) != 0 {
		t.Errorf("agent docs = %v, want none", docsFor(t, agentDocs))
	}
	if len(blocked) != 0 {
		t.Errorf("blocked = %+v, want the crew's own pins to be accepted", blocked)
	}
	if got := docsFor(t, crewDocs); len(got) != 1 || got[0] != "engineering/topics/pins.md" {
		t.Errorf("crew docs = %v, want the pins file", got)
	}
}
