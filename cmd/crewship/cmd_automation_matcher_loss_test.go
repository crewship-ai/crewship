package main

import (
	"strings"
	"testing"
)

// isEmptyPredicate decides what counts as "the rule has this predicate".
//
// The server round-trips a never-set predicate as [] or {} rather than
// omitting it, so treating presence as truth would refuse an update that drops
// nothing — a guard that cries wolf gets --replace-matcher pasted into every
// command, which is worse than no guard.
func TestIsEmptyPredicate(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   any
		want bool
	}{
		{"nil", nil, true},
		{"empty slice", []any{}, true},
		{"empty map", map[string]any{}, true},
		{"empty string", "", true},
		{"populated slice", []any{"crew_1"}, false},
		{"populated map", map[string]any{"to": "DONE"}, false},
		{"non-empty string", "x", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := isEmptyPredicate(tc.in); got != tc.want {
				t.Errorf("isEmptyPredicate(%#v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// The guard names what would be lost. An error that says "some predicates
// would be dropped" sends the reader back to the API to find out which — the
// whole point is that they did not notice, so the message has to tell them.
func TestRefuseSilentMatcherLoss_NamesTheDroppedPredicates(t *testing.T) {
	cur := map[string]any{
		"crew_ids":       []any{"crew_1"},
		"payload_equals": map[string]any{"to": "DONE"},
		"agent_ids":      []any{},          // never set — not a loss
		"severities":     map[string]any{}, // never set — not a loss
	}
	next := map[string]any{"payload_equals": map[string]any{"to": "TODO"}}

	lost := droppedPredicates(cur, next)
	if len(lost) != 1 || lost[0] != "crew_ids" {
		t.Fatalf("dropped = %v, want [crew_ids] — an empty stored predicate is not a loss, and a "+
			"re-supplied one is not either", lost)
	}
}

// --replace-matcher is the caller saying they meant it, so the guard steps
// aside. Without an opt-out the guard would make a legitimate narrowing edit
// impossible.
func TestRefuseSilentMatcherLoss_ReplaceFlagSkipsTheCheck(t *testing.T) {
	if err := refuseSilentMatcherLoss("aut_1", map[string]any{}, true); err != nil {
		t.Errorf("--replace-matcher must skip the check entirely, got %v", err)
	}
}

func TestDroppedPredicates_MessageMentionsTheOptOut(t *testing.T) {
	// The error has to point at the way out, or the reader's only option is to
	// guess. Checked on the formatted string the user actually sees.
	msg := matcherLossError([]string{"crew_ids"})
	for _, want := range []string{"crew_ids", "--replace-matcher", "whole object"} {
		if !strings.Contains(msg.Error(), want) {
			t.Errorf("the refusal must mention %q; got %q", want, msg)
		}
	}
}
