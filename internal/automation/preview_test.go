package automation

import (
	"testing"

	"github.com/crewship-ai/crewship/internal/journal"
)

func statusEntry(payload map[string]any) journal.Entry {
	return journal.Entry{
		WorkspaceID: "ws_1", Type: journal.EntryMissionStatus,
		CrewID: "crew_1", MissionID: "m_1", Severity: journal.SeverityInfo, Payload: payload,
	}
}

func TestPreview_CountsWhatWouldHaveFired(t *testing.T) {
	entries := []journal.Entry{
		statusEntry(map[string]any{"action": "status_changed"}),
		statusEntry(map[string]any{"action": "review_approved"}),
		statusEntry(map[string]any{"action": "status_changed"}),
		{WorkspaceID: "ws_1", Type: journal.EntryRunFailed}, // wrong event type
	}
	r := Preview(Matcher{PayloadEquals: map[string]any{"action": "status_changed"}},
		string(journal.EntryMissionStatus), entries)

	if r.Scanned != 3 {
		t.Errorf("Scanned = %d, want 3 — entries of another event type are not this rule's business", r.Scanned)
	}
	if r.Matched != 2 {
		t.Errorf("Matched = %d, want 2", r.Matched)
	}
}

// The reason this exists. A rule that matches nothing is the failure mode the
// create command warns about in its own help text, and until now the only way
// to find out was to wait and notice nothing happened.
func TestPreview_NamesWhyNothingMatched(t *testing.T) {
	entries := []journal.Entry{
		statusEntry(map[string]any{"action": "status_changed", "details": "BACKLOG → TODO"}),
		statusEntry(map[string]any{"action": "review_approved", "details": "Approved"}),
	}
	// The documented example, verbatim. It can never match: there is no `to`.
	r := Preview(Matcher{PayloadEquals: map[string]any{"to": "DONE"}},
		string(journal.EntryMissionStatus), entries)

	if r.Matched != 0 {
		t.Fatalf("Matched = %d, want 0", r.Matched)
	}
	if r.TopRejection.Clause != "payload_equals.to" {
		t.Errorf("Clause = %q, want payload_equals.to", r.TopRejection.Clause)
	}
	if r.TopRejection.Count != 2 {
		t.Errorf("Count = %d, want 2 — the clause rejected every entry", r.TopRejection.Count)
	}
	if !r.TopRejection.KeyAbsent {
		t.Error("KeyAbsent must survive into the preview: it is the difference between " +
			"'change DONE' and 'this rule can never fire'")
	}
}

// With several failing clauses, the one to report is the one doing the most
// damage — otherwise a reader fixes a predicate that was excluding one entry
// while another excludes all of them.
func TestPreview_ReportsTheClauseThatExcludedTheMost(t *testing.T) {
	entries := []journal.Entry{
		statusEntry(map[string]any{"action": "a"}),
		statusEntry(map[string]any{"action": "a"}),
		{WorkspaceID: "ws_1", Type: journal.EntryMissionStatus, CrewID: "other",
			Payload: map[string]any{"action": "b"}},
	}
	r := Preview(Matcher{
		CrewIDs:       []string{"crew_1"},
		PayloadEquals: map[string]any{"action": "b"},
	}, string(journal.EntryMissionStatus), entries)

	if r.Matched != 0 {
		t.Fatalf("Matched = %d, want 0", r.Matched)
	}
	if r.TopRejection.Clause != "payload_equals.action" {
		t.Errorf("Clause = %q, want the clause that rejected 2 of 3, not the one that rejected 1",
			r.TopRejection.Clause)
	}
}

func TestPreview_AMatchingRuleReportsNoRejection(t *testing.T) {
	r := Preview(Matcher{}, string(journal.EntryMissionStatus),
		[]journal.Entry{statusEntry(map[string]any{"action": "a"})})
	if r.Matched != 1 || r.TopRejection.Clause != "" {
		t.Errorf("a rule that matches must name no rejection, got %d matched / %q",
			r.Matched, r.TopRejection.Clause)
	}
}

// Nothing to replay is not the same as a broken rule, and saying "0 matched"
// without that distinction sends a reader to edit a matcher that is fine.
func TestPreview_NoEntriesOfThatTypeIsNotARejection(t *testing.T) {
	r := Preview(Matcher{PayloadEquals: map[string]any{"to": "DONE"}},
		string(journal.EntryMissionStatus),
		[]journal.Entry{{WorkspaceID: "ws_1", Type: journal.EntryRunFailed}})
	if r.Scanned != 0 {
		t.Fatalf("Scanned = %d, want 0", r.Scanned)
	}
	if r.TopRejection.Clause != "" {
		t.Errorf("with nothing to judge there is no rejection to report, got %q", r.TopRejection.Clause)
	}
}
