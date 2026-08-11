package pipeline

import (
	"context"
	"testing"
)

// A save that does not mention the acting agent must not delete it.
//
// author_agent_id became load-bearing with the crewship step kind: the
// dispatcher injects it as the agent every verb acts as, so losing it files an
// issue.update's audit row under "system" instead of the agent, and drops the
// actor the delegation-depth cap measures from.
//
// Meanwhile several save clients never send the field at all — the dashboard's
// rename and duplicate payloads and the manifest's buildSaveBody among them —
// because it was not load-bearing when they were written. The UPDATE wrote it
// unconditionally, so renaming a routine in the UI silently unset its acting
// agent, and the routine kept working in a quieter, wronger way.
//
// Empty therefore means "not mentioned", and the stored value survives.
// Clearing it deliberately is a thing no caller does today; when one needs to,
// it gets an explicit door rather than inheriting this one by accident.
func TestStoreSave_DoesNotClearTheActingAgentWhenTheSaveOmitsIt(t *testing.T) {
	store, _, cleanup := openExecutorTestDB(t)
	defer cleanup()
	ctx := context.Background()

	in := validSaveInput("keeps-its-agent")
	in.Author.AgentID = "agent_lead"
	first, err := store.Save(ctx, in)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if first.AuthorAgentID != "agent_lead" {
		t.Fatalf("AuthorAgentID = %q after create, want agent_lead", first.AuthorAgentID)
	}

	// A rename: same slug, new name, no author_agent_id in the payload.
	rename := validSaveInput("keeps-its-agent")
	rename.Name = "Renamed"
	rename.Author.AgentID = ""
	after, err := store.Save(ctx, rename)
	if err != nil {
		t.Fatalf("re-save: %v", err)
	}
	if after.AuthorAgentID != "agent_lead" {
		t.Errorf("AuthorAgentID = %q after a save that omitted it, want agent_lead preserved — "+
			"a rename must not silently unset the agent every crewship verb acts as", after.AuthorAgentID)
	}
}

// And a save that DOES name an agent still moves it. Preserving on empty must
// not turn into "the first agent wins forever".
func TestStoreSave_StillUpdatesTheActingAgentWhenTheSaveNamesOne(t *testing.T) {
	store, _, cleanup := openExecutorTestDB(t)
	defer cleanup()
	ctx := context.Background()

	in := validSaveInput("moves-its-agent")
	in.Author.AgentID = "agent_lead"
	if _, err := store.Save(ctx, in); err != nil {
		t.Fatalf("save: %v", err)
	}

	next := validSaveInput("moves-its-agent")
	next.Author.AgentID = "agent_worker"
	after, err := store.Save(ctx, next)
	if err != nil {
		t.Fatalf("re-save: %v", err)
	}
	if after.AuthorAgentID != "agent_worker" {
		t.Errorf("AuthorAgentID = %q, want agent_worker — an explicit value must still win",
			after.AuthorAgentID)
	}
}
