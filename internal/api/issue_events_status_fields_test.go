package api

import "testing"

// "Fire when an issue moves to DONE" is the first automation anyone reaches
// for, and until now it could not be written.
//
// The status-change payload carried only {action, details}, where details is
// prose — "BACKLOG → TODO". Both emit sites HAVE the old and new status as
// separate values and join them into that sentence, throwing the structure
// away. So the documented example `--payload-equals to=DONE` was right about
// the intent and impossible to satisfy: matching would have meant parsing an
// arrow out of a human-readable string.
//
// The fields the emitter already holds belong in the payload.
func TestIssueEvents_StatusChangeCarriesFromAndTo(t *testing.T) {
	got := issueEventPayload(issueEvent{
		Action:  actionStatusChanged,
		Details: "BACKLOG → TODO",
		From:    "BACKLOG",
		To:      "TODO",
	})

	if got["to"] != "TODO" {
		t.Errorf(`payload["to"] = %v, want "TODO" — without it the most obvious `+
			`automation in the product cannot be expressed as a predicate`, got["to"])
	}
	if got["from"] != "BACKLOG" {
		t.Errorf(`payload["from"] = %v, want "BACKLOG"`, got["from"])
	}
	// The prose stays: it is what a human reads in the timeline, and removing
	// it would break every surface that renders the activity feed.
	if got["details"] != "BACKLOG → TODO" {
		t.Errorf(`payload["details"] = %v, want the prose kept`, got["details"])
	}
	if got["action"] != string(actionStatusChanged) {
		t.Errorf(`payload["action"] = %v, want status_changed`, got["action"])
	}
}

// Only a status change has a from/to. Emitting empty ones everywhere would
// invite predicates that silently match nothing on every other action.
func TestIssueEvents_OtherActionsCarryNoStatusFields(t *testing.T) {
	got := issueEventPayload(issueEvent{
		Action:  actionCommented,
		Details: "left a comment",
	})
	for _, k := range []string{"from", "to"} {
		if _, present := got[k]; present {
			t.Errorf("payload carries %q on a non-status action; a key that is always "+
				"empty is a predicate that always fails", k)
		}
	}
}
