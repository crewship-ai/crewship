package api

import (
	"context"
	"reflect"
	"sort"
	"testing"

	"github.com/crewship-ai/crewship/internal/automation"
	"github.com/crewship-ai/crewship/internal/journal"
)

// ---------------------------------------------------------------------------
// The payload an automation actually matches on
// ---------------------------------------------------------------------------
//
// `--payload-equals to=DONE` was the FIRST example in `crewship automation
// create --help`, in docs/cli/automation.mdx, in docs/guides/automations.mdx
// and in docs/api-reference/automations.mdx. mission.status_change has no `to`
// key and never had one — issueEvents.log emits exactly {action, details} — so
// a user following the documented example built a rule that was saved, listed,
// and silently never fired. The help text itself warns that this failure is
// silent; the headline example was an instance of it.
//
// It survived because the matcher's own tests fabricated the payload:
// internal/automation's entry() helper built {"to": "DONE"}, so the matcher was
// verified against a shape the emitter cannot produce. Two green sides, no
// contract between them.
//
// This file is that contract. It runs the REAL emitter, takes the entry it
// produced, and asks the REAL matcher — so a change to either the emitted keys
// or the documented predicate fails here, rather than at 03:00 in a workspace
// nobody is watching.

func TestIssueEvents_JournalPayloadIsWhatAutomationsMatchOn(t *testing.T) {
	db := setupTestDB(t)
	_, wsID, crewID, leadID, _ := seedIssueFixtures(t, db)
	id := seedIssue(t, db, wsID, crewID, leadID, "ENG-1", "BACKLOG")

	rec := &recordingEmitter{}
	e := issueEvents{db: db, hub: nil, logger: newTestLogger(), journal: rec}
	e.log(context.Background(), issueEvent{
		MissionID: id, ActorType: "user", ActorID: leadID,
		Action: actionStatusChanged, Details: "BACKLOG → TODO",
	})
	if len(rec.entries) != 1 {
		t.Fatalf("journal entries = %d, want 1", len(rec.entries))
	}
	entry := rec.entries[0]
	if entry.Type != journal.EntryMissionStatus {
		t.Fatalf("entry type = %q, want %q", entry.Type, journal.EntryMissionStatus)
	}

	// 1. The key set, pinned. ADDING a key here is compatible; renaming or
	//    dropping one silently breaks every rule in every workspace that
	//    matches on it — the change with no signal.
	wantKeys := []string{"action", "details"}
	gotKeys := make([]string, 0, len(entry.Payload))
	for k := range entry.Payload {
		gotKeys = append(gotKeys, k)
	}
	sort.Strings(gotKeys)
	if !reflect.DeepEqual(gotKeys, wantKeys) {
		t.Errorf("mission.status_change payload keys = %v, want %v — the documented "+
			"--payload-equals examples name these keys and nothing else", gotKeys, wantKeys)
	}
	if entry.Payload["action"] != string(actionStatusChanged) {
		t.Errorf("payload[action] = %v, want %q", entry.Payload["action"], actionStatusChanged)
	}
	if entry.Payload["details"] != "BACKLOG → TODO" {
		t.Errorf("payload[details] = %v, want the `from → to` prose", entry.Payload["details"])
	}

	// 2. The DOCUMENTED predicate matches a real entry…
	documented := automation.Matcher{PayloadEquals: map[string]any{"action": "status_changed"}}
	if !documented.Matches(entry) {
		t.Errorf("the documented --payload-equals action=status_changed does not match a real "+
			"mission.status_change entry (payload %#v) — the docs describe a rule that never fires",
			entry.Payload)
	}

	// 3. …and the one that shipped for months does not. Without this arm the
	//    test would still pass against a payload that carried BOTH keys.
	fiction := automation.Matcher{PayloadEquals: map[string]any{"to": "DONE"}}
	if fiction.Matches(entry) {
		t.Error("payload_equals to=DONE matched — if the emitter has grown a `to` key, this " +
			"test's premise needs revisiting, not deleting")
	}
}

// Every action that maps to mission.status_change carries the same two keys —
// so `--payload-equals action=<x>` is the ONE predicate shape that works for
// this event type, whichever thing happened.
func TestIssueEvents_EveryStatusChangeActionCarriesTheSameKeys(t *testing.T) {
	db := setupTestDB(t)
	_, wsID, crewID, leadID, _ := seedIssueFixtures(t, db)
	id := seedIssue(t, db, wsID, crewID, leadID, "ENG-1", "BACKLOG")

	for _, action := range knownIssueActions {
		if journalTypeForIssueAction(action) != journal.EntryMissionStatus {
			continue
		}
		rec := &recordingEmitter{}
		e := issueEvents{db: db, hub: nil, logger: newTestLogger(), journal: rec}
		e.log(context.Background(), issueEvent{
			MissionID: id, ActorType: "system", ActorID: "system",
			Action: action, Details: "whatever",
		})
		if len(rec.entries) != 1 {
			t.Fatalf("action %q: journal entries = %d, want 1", action, len(rec.entries))
		}
		m := automation.Matcher{PayloadEquals: map[string]any{"action": string(action)}}
		if !m.Matches(rec.entries[0]) {
			t.Errorf("action %q: --payload-equals action=%s does not match its own entry (payload %#v)",
				action, action, rec.entries[0].Payload)
		}
	}
}

// `details` is human-readable prose ("BACKLOG → TODO"), not a field. It is the
// only place the TARGET status appears, which is why "fire when an issue moves
// to DONE" cannot be written as a payload_equals at all. The docs now say so;
// this pins the reason they have to.
func TestIssueEvents_StatusChangeTargetIsProseNotAField(t *testing.T) {
	db := setupTestDB(t)
	_, wsID, crewID, leadID, _ := seedIssueFixtures(t, db)
	id := seedIssue(t, db, wsID, crewID, leadID, "ENG-1", "BACKLOG")

	rec := &recordingEmitter{}
	e := issueEvents{db: db, hub: nil, logger: newTestLogger(), journal: rec}
	e.log(context.Background(), issueEvent{
		MissionID: id, ActorType: "user", ActorID: leadID,
		Action: actionStatusChanged, Details: "TODO → DONE",
	})

	entry := rec.entries[0]
	for _, m := range []automation.Matcher{
		{PayloadEquals: map[string]any{"status": "DONE"}},
		{PayloadEquals: map[string]any{"to": "DONE"}},
		{PayloadEquals: map[string]any{"details": "DONE"}},
	} {
		if m.Matches(entry) {
			t.Errorf("matcher %#v matched — the target status is embedded in prose, not a field", m)
		}
	}
}
