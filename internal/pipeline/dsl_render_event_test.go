package pipeline

import "testing"

// The {{ event.* }} namespace exists so an automation's inputs are written in
// the SAME substitution language routine steps use. These cases pin the four
// scope fields the spec names, the payload reach-in, and — the part that
// matters for a rule nobody is watching — that a miss renders empty instead of
// leaking the literal placeholder into an agent prompt.
func TestRenderEventNamespace(t *testing.T) {
	ctx := RenderContext{
		Event: map[string]any{
			"mission_id": "m_1",
			"agent_id":   "a_1",
			"crew_id":    "c_1",
			"run_id":     "r_1",
			"payload": map[string]any{
				"from":  "TODO",
				"to":    "DONE",
				"count": 3,
			},
		},
	}

	cases := []struct{ tmpl, want string }{
		{"{{ event.mission_id }}", "m_1"},
		{"{{ event.agent_id }}", "a_1"},
		{"{{ event.crew_id }}", "c_1"},
		{"{{ event.run_id }}", "r_1"},
		{"{{ event.payload.from }}", "TODO"},
		{"{{ event.payload.to }}", "DONE"},
		{"{{ event.payload.count }}", "3"},
		{"issue {{ event.mission_id }} went {{ event.payload.from }}→{{ event.payload.to }}",
			"issue m_1 went TODO→DONE"},
		// Misses. An unknown field, an unknown payload key, and a path one
		// level too deep all render empty — the same contract every other
		// namespace has.
		{"{{ event.nope }}", ""},
		{"{{ event.payload.nope }}", ""},
		{"{{ event.payload.from.deeper }}", ""},
	}
	for _, tc := range cases {
		if got := Render(tc.tmpl, ctx); got != tc.want {
			t.Errorf("Render(%q) = %q, want %q", tc.tmpl, got, tc.want)
		}
	}
}

// A routine step render has no triggering event. It must not blow up, and it
// must not resolve — otherwise a routine could read a namespace that only an
// automation is supposed to populate.
func TestRenderEventNamespaceEmptyWithoutEvent(t *testing.T) {
	if got := Render("[{{ event.mission_id }}]", RenderContext{}); got != "[]" {
		t.Errorf("Render with nil Event = %q, want %q", got, "[]")
	}
}
