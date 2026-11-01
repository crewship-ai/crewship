package main

import "testing"

func TestLastUserMessageOwnership(t *testing.T) {
	mine := map[string]any{"role": "user", "author_user_id": "caller", "content": "mine"}
	theirs := map[string]any{"role": "user", "author_user_id": "other", "content": "theirs"}
	legacy := map[string]any{"role": "user", "content": "legacy"}
	reply := map[string]any{"role": "assistant", "content": "answer"}
	for _, tc := range []struct {
		name           string
		messages       []map[string]any
		caller         string
		legacyOwned    bool
		want           string
		needsOwnership bool
	}{
		{"own-turn", []map[string]any{mine, reply}, "caller", false, "mine", false},
		{"skips-other-member", []map[string]any{mine, theirs, reply}, "caller", false, "mine", false},
		{"only-other-member", []map[string]any{theirs}, "caller", true, "", false},
		{"legacy-needs-ownership", []map[string]any{legacy, reply}, "caller", false, "", true},
		{"legacy-owned-private-chat", []map[string]any{legacy, reply}, "caller", true, "legacy", false},
		{"attributed-own-turn-wins-over-legacy", []map[string]any{mine, legacy}, "caller", false, "mine", false},
		{"other-member-newer-than-legacy", []map[string]any{legacy, theirs}, "caller", false, "", true},
		{"unknown-caller", []map[string]any{legacy, mine}, "", true, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, needs := lastUserMessage(tc.messages, tc.caller, tc.legacyOwned)
			content, _ := got["content"].(string)
			if content != tc.want || needs != tc.needsOwnership {
				t.Fatalf("got %q needsOwnership=%v, want %q/%v", content, needs, tc.want, tc.needsOwnership)
			}
		})
	}
}
