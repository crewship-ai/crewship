package notifyroute

import (
	"testing"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/notify"
)

// TestJournalBridgeNeverLoops is the safety property of the whole bridge.
//
// The router emits a notification.delivered / .failed / .dropped journal
// entry for every outbound send so the delivery shows up on the Activity
// timeline. Those entries commit through the same journal writer the bridge
// observes. If any of them resolved to a category, delivering a notification
// would notify about having notified, and THAT delivery would emit another
// record — an unbounded loop hammering every configured channel until
// someone noticed the bill.
//
// Cheap to get wrong (one map entry), expensive to discover in production.
func TestJournalBridgeNeverLoops(t *testing.T) {
	for _, tp := range []journal.EntryType{
		journal.EntryNotificationDelivered,
		journal.EntryNotificationFailed,
		journal.EntryNotificationDropped,
	} {
		if got := CategoryForJournalType(tp); got != "" {
			t.Errorf("%s resolved to category %q — delivering a notification would notify about it, "+
				"and that notification would emit another record. This must stay unmapped.", tp, got)
		}
	}
}

// TestJournalCategoriesAreValid pins that every mapped category actually
// exists. A typo would produce a category no preference cell can match, so
// the event would route, find no opted-in cell, and vanish — the same silent
// failure taxonomy v2 exists to eliminate.
func TestJournalCategoriesAreValid(t *testing.T) {
	for tp, cat := range journalCategories {
		if !notify.ValidCategory(cat) {
			t.Errorf("journal type %s maps to %q, which is not a valid category", tp, cat)
		}
		if neverNotify[tp] {
			t.Errorf("journal type %s is both mapped to a category and marked never-notify", tp)
		}
	}
}

// TestObservationalCategoriesHaveAProducer is the counterpart to
// TestEveryInboxKindMapsToACategory in internal/notify: between the two
// producers, every category in the vocabulary must be reachable.
//
// A category with no producer is exactly the defect taxonomy v2 fixed — four
// of the original nine were switchable rows in the settings matrix that
// nothing could ever deliver against. This test makes adding one back a CI
// failure rather than a discovery months later.
func TestObservationalCategoriesHaveAProducer(t *testing.T) {
	produced := map[string]bool{}
	for _, cat := range journalCategories {
		produced[cat] = true
	}
	// The actionable half, from internal/notify's categoryByKind. Listed
	// explicitly rather than reaching into that package's unexported map —
	// notify's own test guards the inbox side.
	for _, cat := range []string{
		notify.CategoryAgentsApproval,
		notify.CategoryAgentsEscalation,
		notify.CategoryRoutinesFailed,
		notify.CategoryChatReplies,
		notify.CategoryMemory,
		notify.CategoryRoutinesMissed,
	} {
		produced[cat] = true
	}

	// Categories with no producer yet, each needing a documented reason.
	// Anything listed here is a promise the settings matrix is making that
	// the product cannot currently keep, so the list should only shrink.
	knownUnwired := map[string]string{
		notify.CategorySystemHealth: "no instance-health monitor exists yet — nothing measures or " +
			"emits instance health, so wiring this needs a health emitter first (thresholds, " +
			"debounce, what counts as unhealthy), not just a map entry",
	}

	for _, cat := range notify.AllCategories {
		if produced[cat] {
			if reason, listed := knownUnwired[cat]; listed {
				t.Errorf("category %q now HAS a producer but is still listed as unwired (%q) — "+
					"remove it from knownUnwired", cat, reason)
			}
			continue
		}
		if _, ok := knownUnwired[cat]; ok {
			continue
		}
		t.Errorf("category %q has no producer in either the inbox or the journal bridge — "+
			"it would render as a switchable row in the preference matrix that can never deliver. "+
			"Map a journal type to it, or add it to knownUnwired with a reason.", cat)
	}
}

func TestSeverityPriorityMapping(t *testing.T) {
	cases := map[journal.Severity]string{
		journal.SeverityError:  "high",
		journal.SeverityWarn:   "medium",
		journal.SeverityNotice: "low",
		journal.SeverityInfo:   "low",
		"":                     "low",
	}
	for sev, want := range cases {
		if got := severityPriority(sev); got != want {
			t.Errorf("severityPriority(%q) = %q, want %q", sev, got, want)
		}
	}
}

// TestJournalItemProjection pins the shape the delivery log records for a
// journal-sourced event, including the source-kind prefix that keeps its
// dedup key from colliding with a real inbox item of the same id.
func TestJournalItemProjection(t *testing.T) {
	e := journal.Entry{
		ID:          "jrn_1",
		WorkspaceID: "ws1",
		CrewID:      "crew_1",
		MissionID:   "m_1",
		Type:        journal.EntryMissionStatus,
		Severity:    journal.SeverityWarn,
		Summary:     "Issue moved to In Review",
	}
	item := journalItem(e, notify.CategoryIssuesState)

	if item.Kind != "journal:mission.status_change" {
		t.Errorf("Kind = %q, want the journal: prefix so it can't collide with an inbox kind", item.Kind)
	}
	if item.SourceID != "jrn_1" {
		t.Errorf("SourceID = %q, want the journal entry id", item.SourceID)
	}
	if item.Title != "Issue moved to In Review" {
		t.Errorf("Title = %q, want the entry summary", item.Title)
	}
	if item.Priority != "medium" {
		t.Errorf("Priority = %q, want medium for a warn-severity entry", item.Priority)
	}
	if item.Payload["mission_id"] != "m_1" || item.Payload["crew_id"] != "crew_1" {
		t.Errorf("payload lost its scope refs: %v", item.Payload)
	}
	if item.TargetUserID != "" || item.TargetRole != "" {
		t.Error("a journal-sourced item must have no target, so the router broadcasts to the " +
			"workspace and each member's own preferences decide")
	}

	// An entry with no summary must still produce a usable title rather than
	// an empty notification body.
	bare := journalItem(journal.Entry{ID: "j2", WorkspaceID: "ws1", Type: journal.EntryBudgetExceed}, notify.CategoryAgentsBudget)
	if bare.Title != "budget.exceeded" {
		t.Errorf("Title for a summary-less entry = %q, want the entry type as a fallback", bare.Title)
	}
}
