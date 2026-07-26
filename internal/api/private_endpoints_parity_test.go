package api

// CREWSHIP_ALLOW_PRIVATE_ENDPOINTS is a security ceiling: it decides whether
// this instance may reach RFC1918 / loopback at all. It used to be parsed in
// two places with independently-maintained truthiness tables — the orchestrator
// accessor that gates agent traffic, and a local copy in ollama_discovery.go
// that gates daemon-side model discovery.
//
// Two tables can drift, and the drift is silent and asymmetric: discovery could
// dial a private host the traffic path refuses, or refuse one it allows. This
// pins them to a single answer.

import (
	"testing"

	"github.com/crewship-ai/crewship/internal/orchestrator"
)

func TestInstanceAllowsPrivateEndpoints_MatchesTheEnforcingAccessor(t *testing.T) {
	// Every spelling the enforcing accessor treats as truthy, plus the ones it
	// must NOT — "yes"/"on" are the interesting pair, since a naive
	// strconv.ParseBool rejects them and that is exactly how a re-implementation
	// drifts.
	for _, v := range []string{
		"1", "true", "TRUE", "True", "yes", "YES", "on", "ON",
		"", "0", "false", "no", "off", "maybe", " ", "  true  ",
	} {
		t.Run("value="+v, func(t *testing.T) {
			t.Setenv("CREWSHIP_ALLOW_PRIVATE_ENDPOINTS", v)
			got := instanceAllowsPrivateEndpoints()
			want := orchestrator.InstanceAllowsPrivateEndpoints()
			if got != want {
				t.Errorf("api reader = %v but orchestrator (the enforcing one) = %v for %q — "+
					"the discovery gate and the traffic gate disagree about whether private egress is open",
					got, want, v)
			}
		})
	}
}
