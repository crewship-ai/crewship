package keepercfg_test

import (
	"testing"

	"github.com/crewship-ai/crewship/internal/keeper/evidence"
	"github.com/crewship-ai/crewship/internal/keepercfg"
)

// The profile validates --evidence-facts against a list of fact names, and the
// collector produces facts under names of its own. Two hand-maintained lists of
// the same vocabulary drift, and this pair already had:
//
//	profile:   prior_grants_same_pair
//	collector: prior_requests_same_pair
//
// plus two names the profile accepted that nothing computes. Both directions
// fail silently, and both fail toward danger. An operator selecting a name the
// collector cannot produce gets a fact quietly missing from a security decision
// — and a judge reasoning without the binding fact is a judge that approves an
// unbound credential, which is the exact case measured in the PRD.
//
// So the vocabulary has ONE owner: the collector, because it is the half that
// can actually produce a fact. This test is the mechanism that keeps the claim
// true — the earlier version was a comment asking a sibling package nicely.
func TestEvidenceFacts_ProfileAndCollectorShareOneVocabulary(t *testing.T) {
	t.Parallel()

	collector := evidence.FactKeys()
	if len(collector) == 0 {
		t.Fatal("collector exposes no facts")
	}

	profile := map[string]bool{}
	for _, f := range keepercfg.EvidenceFacts {
		profile[f] = true
	}

	for _, f := range collector {
		if !profile[f] {
			t.Errorf("collector produces %q but the profile rejects it — an operator cannot select a fact that exists", f)
		}
		if !keepercfg.KnownEvidenceFact(f) {
			t.Errorf("KnownEvidenceFact(%q) = false for a fact the collector produces", f)
		}
	}

	known := map[string]bool{}
	for _, f := range collector {
		known[f] = true
	}
	for _, f := range keepercfg.EvidenceFacts {
		if !known[f] {
			t.Errorf("profile accepts %q but nothing computes it — selecting it silently drops a fact from the decision", f)
		}
	}
}
