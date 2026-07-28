package notifyroute

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/journal"
)

// A journal-sourced notification arrived as a title and nothing else:
//
//	Pipeline demo-fetch-and-report completed
//	Open journal: https://…/journal
//
// Two reasons, and the second is the one that mattered. journalItem built its
// payload from the entry's IDENTITY — entry id, type, crew, agent, mission —
// and dropped e.Payload, which is where the entry's actual facts live
// (duration, cost, which routine). So there was no body, and there was
// nothing to build one from either: the variables a template will render
// against were being discarded one layer before anyone could use them.

func TestJournalItem_CarriesTheEntrysOwnFacts(t *testing.T) {
	item := journalItem(journal.Entry{
		WorkspaceID: "w1",
		Type:        journal.EntryPipelineRunCompleted,
		Summary:     "Pipeline demo-fetch-and-report completed",
		Payload: map[string]any{
			"pipeline_slug":     "demo-fetch-and-report",
			"run_id":            "run_1",
			"total_duration_ms": 1234,
			"total_cost_usd":    0.0,
		},
	}, "routines.completed")

	for _, k := range []string{"pipeline_slug", "run_id", "total_duration_ms", "total_cost_usd"} {
		if _, ok := item.Payload[k]; !ok {
			t.Errorf("the entry's %q never reached the notification: %+v", k, item.Payload)
		}
	}
	// Identity still rides along — it is what the links are built from.
	if item.Payload["journal_entry_id"] == nil {
		t.Error("journal_entry_id must survive; links resolve from it")
	}
}

func TestJournalItem_IdentityWinsOverACollidingPayloadKey(t *testing.T) {
	// An entry whose payload happens to carry "entry_type" must not be able
	// to relabel the notification's own bookkeeping.
	item := journalItem(journal.Entry{
		WorkspaceID: "w1",
		Type:        journal.EntryPipelineRunCompleted,
		Payload:     map[string]any{"entry_type": "something.else"},
	}, "routines.completed")
	if item.Payload["entry_type"] != string(journal.EntryPipelineRunCompleted) {
		t.Errorf("entry_type = %v, want the real type", item.Payload["entry_type"])
	}
}

func TestJournalItem_BodyListsTheFactsAPersonWants(t *testing.T) {
	item := journalItem(journal.Entry{
		WorkspaceID: "w1",
		Type:        journal.EntryPipelineRunCompleted,
		Summary:     "Pipeline demo-fetch-and-report completed",
		CrewID:      "crew_1",
		Payload: map[string]any{
			"pipeline_slug":     "demo-fetch-and-report",
			"run_id":            "run_1",
			"total_duration_ms": 1234,
		},
	}, "routines.completed")

	if item.BodyMD == "" {
		t.Fatal("a journal notification with facts must not arrive empty")
	}
	if !strings.Contains(item.BodyMD, "demo-fetch-and-report") {
		t.Errorf("body should carry the routine name:\n%s", item.BodyMD)
	}
	// Ids are for links and machines. A chat message reading
	// "crew_id: crew_1" spends a line on something nobody can act on.
	for _, noise := range []string{"crew_1", "run_id", "journal_entry_id"} {
		if strings.Contains(item.BodyMD, noise) {
			t.Errorf("body should not print raw ids (%q):\n%s", noise, item.BodyMD)
		}
	}
}

func TestJournalItem_NoFactsMeansNoBody(t *testing.T) {
	// An entry with nothing but identity has nothing to say beyond its
	// summary. An empty "Details:" heading is worse than no body.
	item := journalItem(journal.Entry{
		WorkspaceID: "w1",
		Type:        journal.EntryPipelineRunCompleted,
		Summary:     "Pipeline x completed",
	}, "routines.completed")
	if item.BodyMD != "" {
		t.Errorf("want no body when there are no facts, got %q", item.BodyMD)
	}
}

func TestJournalItem_BodyIsBounded(t *testing.T) {
	// A payload is producer-authored and can be large (a lookout entry
	// carries every finding). A notification is a chat message.
	payload := map[string]any{}
	for i := range 60 {
		payload[string(rune('a'+i%26))+strings.Repeat("x", i)] = "value"
	}
	item := journalItem(journal.Entry{
		WorkspaceID: "w1", Type: journal.EntryPipelineRunCompleted, Payload: payload,
	}, "routines.completed")
	if lines := strings.Count(item.BodyMD, "\n") + 1; lines > journalBodyMaxFacts+1 {
		t.Errorf("body has %d lines, want at most %d", lines, journalBodyMaxFacts+1)
	}
}
