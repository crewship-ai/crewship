package notifyroute

import (
	"testing"

	"github.com/crewship-ai/crewship/internal/inbox"
)

// Two producers derived a notification's deep link, and both did it the same
// way: read exactly one key, "chat_url", off the payload. So a notification
// was clickable if and only if it came from a chat reply. An approval, a
// failed run, a tripped circuit breaker, a journal entry — none carried
// anywhere to go, even though every one of their payloads already holds the
// ids a link needs.
//
// The links (and the template variables) a notification carries are a pure
// function of its source, so they are derived in ONE place both producers
// call rather than at each delivery site.

func factsFor(t *testing.T, kind string, payload map[string]any) (map[string]string, map[string]any) {
	t.Helper()
	links, vars := notificationFacts(kind, payload)
	byLabel := make(map[string]string, len(links))
	for _, l := range links {
		byLabel[l.Label] = l.Path
	}
	return byLabel, vars
}

func TestNotificationFacts_ChatReplyKeepsTheLinkItAlreadyHad(t *testing.T) {
	// The one case that worked before must keep working, byte for byte —
	// this is the regression the whole change is most likely to cause.
	links, _ := factsFor(t, inbox.KindMessage, map[string]any{
		"chat_url": "/chat/riley?session=cs_1",
		"chat_id":  "cs_1",
	})
	if links["Open chat"] != "/chat/riley?session=cs_1" {
		t.Errorf("chat link = %+v, want the payload's chat_url", links)
	}
}

func TestNotificationFacts_JournalEntryOnAMissionLinksToThatMission(t *testing.T) {
	// A journal entry has no detail route of its own, but one carrying a
	// mission_id has something precise to point at — the mission timeline
	// the entry is part of. That beats dropping the reader on an unfiltered
	// journal list and making them hunt.
	links, _ := factsFor(t, "journal:agent.run.failed", map[string]any{
		"journal_entry_id": "je_1",
		"mission_id":       "ms_7",
	})
	if links["Open mission"] != "/missions/ms_7/timeline" {
		t.Errorf("want a mission timeline link, got %+v", links)
	}
}

func TestNotificationFacts_JournalEntryWithNoMissionFallsBackToTheJournal(t *testing.T) {
	links, _ := factsFor(t, "journal:system.config.changed", map[string]any{
		"journal_entry_id": "je_2",
	})
	if links["Open journal"] != "/journal" {
		t.Errorf("want a journal link, got %+v", links)
	}
}

func TestNotificationFacts_EscalationLinksToThatAgentsApprovals(t *testing.T) {
	// /approvals reads agent_id — the only filter that page supports — so
	// an escalation carrying an agent narrows to it.
	links, _ := factsFor(t, inbox.KindEscalation, map[string]any{
		"agent_id": "ag_1",
		"audit_id": "au_1",
	})
	if links["Review"] != "/approvals?agent_id=ag_1" {
		t.Errorf("want the approvals page filtered to the agent, got %+v", links)
	}
}

func TestNotificationFacts_LinksOnlyWhereAPageActuallyResolves(t *testing.T) {
	// There is no per-run and no per-journal-entry route in the app today.
	// Inventing "/runs?run=xyz" would produce a link that looks precise and
	// silently lands on an unfiltered list — worse than an honest coarse
	// link, because it wastes the reader's trust once per notification.
	for _, tc := range []struct {
		name    string
		kind    string
		payload map[string]any
		label   string
		want    string
	}{
		{"failed run", inbox.KindFailedRun, map[string]any{"run_id": "run_1"}, "Open runs", "/runs"},
		{"missed schedule", inbox.KindScheduleMissed, map[string]any{"schedule_id": "sc_1"}, "Open routines", "/routines"},
		{"tripped breaker", inbox.KindScheduleCircuitBreakerTripped, map[string]any{"schedule_id": "sc_1"}, "Open routines", "/routines"},
		{"waitpoint", inbox.KindWaitpoint, map[string]any{"step_id": "s1"}, "Review", "/approvals"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			links, _ := factsFor(t, tc.kind, tc.payload)
			if links[tc.label] != tc.want {
				t.Errorf("links = %+v, want %q → %q", links, tc.label, tc.want)
			}
			for _, path := range links {
				if len(path) > 0 && path[0] != '/' {
					t.Errorf("link %q is not app-relative — delivery makes links absolute, producers must not", path)
				}
			}
		})
	}
}

func TestNotificationFacts_VarsAreThePayloadSoNewKeysNeedNoCodeHere(t *testing.T) {
	// Vars is what a message template renders against. Deriving it from the
	// payload wholesale means a producer that starts recording a new fact
	// gets a new template variable for free — the alternative is a
	// per-kind allowlist that silently lags every producer that changes.
	_, vars := factsFor(t, inbox.KindFailedRun, map[string]any{
		"run_id":      "run_1",
		"schedule_id": "sc_1",
		"brand_new":   "value",
	})
	for _, k := range []string{"run_id", "schedule_id", "brand_new"} {
		if _, ok := vars[k]; !ok {
			t.Errorf("vars is missing %q: %+v", k, vars)
		}
	}
}

func TestNotificationFacts_UnknownKindIsSafe(t *testing.T) {
	// A kind this table has never heard of must not panic and must not
	// invent a link. New inbox kinds land here before anyone updates this
	// file, and a nil payload is what a producer that set none gives us.
	links, vars := notificationFacts("some_future_kind", nil)
	if len(links) != 0 {
		t.Errorf("want no links for an unknown kind, got %+v", links)
	}
	if vars == nil {
		t.Error("vars must be usable (empty, not nil) so callers need no nil check")
	}
}
