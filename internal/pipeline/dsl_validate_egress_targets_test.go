package pipeline

import (
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// validateEgressTargets — routine-level egress allowlist sanity (#832).
//
// The runtime allowlist (hostInEgressTargets) is a literal + subdomain-suffix
// match, not a glob. `*`, `*.*` and an empty host therefore match NO real host
// — an allowlist containing one is dead config that silently denies every http
// step's egress (the opposite of "allow all"; unrestricted egress means
// omitting egress_targets entirely). Validate rejects these dead entries so the
// mistake fails at save, not as mystery connection errors at run time. Loopback
// hosts (localhost, 127.*) are real, matchable targets and stay a doctor WARN —
// legitimate on dev / self-hosted boxes, so save/validate don't block them.
// ---------------------------------------------------------------------------

func egressProbeDSL() *DSL {
	return &DSL{
		Name:          "fetcher",
		EgressTargets: []string{"api.example.com"},
		Steps: []Step{
			{ID: "get", Type: StepHTTP, HTTP: &HTTPStep{Method: "GET", URL: "https://api.example.com/x"}},
		},
	}
}

func TestValidate_Egress_ConcreteHostOK(t *testing.T) {
	if err := Validate(egressProbeDSL(), nil, nil); err != nil {
		t.Fatalf("concrete egress host should validate, got: %v", err)
	}
}

func TestValidate_Egress_StarWildcardRejected(t *testing.T) {
	dsl := egressProbeDSL()
	dsl.EgressTargets = []string{"api.example.com", "*"}
	err := Validate(dsl, nil, nil)
	if err == nil {
		t.Fatal("`*` matches no host at run time (dead entry) — must be rejected")
	}
	if !strings.Contains(err.Error(), "egress") {
		t.Errorf("error should name egress, got: %v", err)
	}
}

func TestValidate_Egress_StarDotStarWildcardRejected(t *testing.T) {
	dsl := egressProbeDSL()
	dsl.EgressTargets = []string{"*.*"}
	if err := Validate(dsl, nil, nil); err == nil {
		t.Fatal("`*.*` matches no host at run time (dead entry) — must be rejected")
	}
}

func TestValidate_Egress_EmptyHostRejected(t *testing.T) {
	dsl := egressProbeDSL()
	dsl.EgressTargets = []string{""}
	if err := Validate(dsl, nil, nil); err == nil {
		t.Fatal("empty-string egress target must be rejected")
	}
}

func TestValidate_Egress_LoopbackAllowedAtSave(t *testing.T) {
	dsl := egressProbeDSL()
	// Loopback is a doctor WARN, not a Validate error — legit on dev boxes.
	dsl.EgressTargets = []string{"localhost", "127.0.0.1"}
	if err := Validate(dsl, nil, nil); err != nil {
		t.Fatalf("loopback egress must NOT hard-fail save/validate (doctor warns instead), got: %v", err)
	}
}
