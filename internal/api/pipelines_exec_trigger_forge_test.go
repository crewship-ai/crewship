package api

import (
	"testing"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

// A caller may not name itself rule-fired.
//
// triggered_by_id is body-supplied on the user-facing run endpoint, so
// accepting triggered_via="automation" alongside it lets any member stamp a
// hand-started run with an existing rule's id. internal/chain reads exactly
// that pair to draw "this rule caused this run", so the topology — whose whole
// job is explaining what caused what — would report a cause that never
// happened, with nothing to distinguish it from a real one.
//
// Downgraded to manual rather than refused, matching the endpoint's existing
// forgive-and-carry-on handling of an unknown value: the run itself is
// legitimate, only its claimed provenance is not.
func TestRunTriggerSource_AutomationIsNotAcceptedFromTheBody(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want pipeline.TriggeredVia
	}{
		{"automation", pipeline.TriggeredViaManual},
		{"nonsense", pipeline.TriggeredViaManual},
		{"", pipeline.TriggeredViaManual},
		// The ones a caller legitimately owns stay untouched.
		{"manual", pipeline.TriggeredViaManual},
		{"schedule", pipeline.TriggeredViaSchedule},
		{"webhook", pipeline.TriggeredViaWebhook},
		{"call_pipeline", pipeline.TriggeredViaCallPipeline},
		{"issue", pipeline.TriggeredViaIssue},
	} {
		t.Run(tc.in, func(t *testing.T) {
			if got := acceptedTriggerSource(tc.in); got != tc.want {
				t.Errorf("acceptedTriggerSource(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
