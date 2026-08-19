package orchestrator

import "testing"

// The two one-shot captures in NewBufferingHandler have to agree about which
// system event they trust. The session-init capture is gated on subtype "init"
// with the reason written next to it — "a mid-run api_retry system event would
// otherwise take the one-shot slot and lock out the init event we actually
// want" — and the resolved-model capture, three lines above it, fires on ANY
// system event carrying a non-empty model.
//
// That is not hypothetical outside the Claude parser. parser_droid.go and
// parser_cursor.go handle `case "system":` subtype-agnostically: they stamp
// whatever subtype the CLI sent alongside a model, so every system envelope
// those CLIs emit after the bootstrap line lands in this capture. The first one
// wins, and the run record then reports a model the run did not start on.
//
// Driven through the real droid parser rather than hand-built events: the point
// is the shape a parser actually produces, and a hand-built map would pass
// whatever the test author assumed.
//
// This test lives in its own file because buffering_handler_test.go is owned by
// another change in flight; it belongs next to TestBufferingHandler_* and can be
// folded in once that lands.
func TestBufferingHandler_ResolvedModelComesOnlyFromInit(t *testing.T) {
	const initLine = `{"type":"system","subtype":"init","session_id":"d-1","model":"claude-sonnet-4-6","cwd":"/work"}`
	// A later system envelope from the same CLI. Droid's parser stamps the
	// model on this one exactly as it does on the bootstrap line.
	const midRunLine = `{"type":"system","subtype":"context_compacted","session_id":"d-1","model":"claude-haiku-4-5"}`

	cases := []struct {
		name        string
		lines       []string
		wantModel   string
		wantSession bool
		why         string
	}{
		{
			name:        "init alone",
			lines:       []string{initLine},
			wantModel:   "claude-sonnet-4-6",
			wantSession: true,
			why:         "the ordinary case must be unchanged",
		},
		{
			name:        "a mid-run system envelope arrives first",
			lines:       []string{midRunLine, initLine},
			wantModel:   "claude-sonnet-4-6",
			wantSession: true,
			why:         "the one-shot slot belongs to the init event; a compaction notice is not the model the run resolved to",
		},
		{
			name:        "no init event at all",
			lines:       []string{midRunLine},
			wantModel:   "",
			wantSession: false,
			why:         "an unknown resolved model is honest; a mid-run model recorded as the run's own is a wrong answer nobody can spot",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			handler, acc := NewBufferingHandler(BufferingHandlerOpts{CaptureResultMeta: true})
			for _, line := range tc.lines {
				parseDroidStreamJSON([]byte(line), handler)
			}
			if got := acc.ResolvedModel(); got != tc.wantModel {
				t.Errorf("ResolvedModel() = %q, want %q — %s", got, tc.wantModel, tc.why)
			}
			if got := acc.SessionInit(); (got != nil) != tc.wantSession {
				t.Errorf("SessionInit() != nil = %v, want %v — the two captures must agree about which event they trust",
					got != nil, tc.wantSession)
			}
		})
	}
}
