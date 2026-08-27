package cli

import "testing"

// RoutesToHuman must agree with AutoHuman for every format, always.
//
// The rule was previously restated at call sites that cannot use AutoHuman —
// paths emitting a machine envelope and a human line from different branches.
// Two such copies drifted: `oauth connect` classified `quiet` as human while
// `oauth auto-connect` classified it as machine, so the same --format value
// produced a serialized envelope in one command and not the other. An earlier
// divergence on the same rule suppressed the authorize URL entirely, without
// which an OAuth flow cannot be completed.
//
// This pins the predicate to the behaviour it describes, so a future change to
// AutoHuman's routing cannot leave RoutesToHuman lying about it.
func TestRoutesToHuman_AgreesWithAutoHuman(t *testing.T) {
	t.Parallel()

	// Every format the CLI advertises, plus the empty default.
	formats := []string{"", "table", "quiet", "json", "yaml", "ndjson"}

	for _, format := range formats {
		format := format
		t.Run("format="+format, func(t *testing.T) {
			t.Parallel()
			f := NewFormatter(format)

			humanRan := false
			if err := f.AutoHuman(map[string]string{"k": "v"}, func() { humanRan = true }); err != nil {
				t.Fatalf("AutoHuman(%q): %v", format, err)
			}

			if got := f.RoutesToHuman(); got != humanRan {
				t.Errorf("RoutesToHuman() = %v but AutoHuman %s the human renderer (format %q)",
					got, map[bool]string{true: "ran", false: "did not run"}[humanRan], format)
			}
		})
	}
}

// The specific classification the drift got wrong. `quiet` is a terminal
// presentation choice, not a serialization format — a caller that treats it as
// machine emits an envelope where the operator asked for silence.
func TestRoutesToHuman_QuietIsHuman(t *testing.T) {
	t.Parallel()
	for _, format := range []string{"", "table", "quiet"} {
		if !NewFormatter(format).RoutesToHuman() {
			t.Errorf("format %q should route to human", format)
		}
	}
	for _, format := range []string{"json", "yaml", "ndjson"} {
		if NewFormatter(format).RoutesToHuman() {
			t.Errorf("format %q should route to machine", format)
		}
	}
}
