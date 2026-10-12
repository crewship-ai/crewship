package evidence

import (
	"strings"
	"testing"
)

// The judge profile lets an operator narrow the block for a small-context model.
// A selection that is stored, validated and echoed back by `profile get` while
// the prompt still carries every fact is worse than no setting: the operator
// believes they shrank the prompt and did not, and the reason they were shrinking
// it was that their model was already struggling.
func TestRenderOnly_NarrowsToTheSelectedFacts(t *testing.T) {
	f := Facts{
		Binding:      &Binding{Bound: true, EnvVarName: "PROD_DB", BoundAt: "2026-06-14T09:00:00Z"},
		RecentDenies: &RecentDenies{Count: 2, Days: 7},
		OpenWork:     &OpenWork{},
	}

	full := f.RenderOnly(nil)
	for _, k := range []string{FactBinding, FactRecentDenies, FactOpenAssignedWork} {
		if !strings.Contains(full, k) {
			t.Errorf("unrestricted render dropped %s", k)
		}
	}

	only := f.RenderOnly([]string{FactBinding})
	if !strings.Contains(only, FactBinding) {
		t.Errorf("selected fact missing:\n%s", only)
	}
	for _, k := range []string{FactRecentDenies, FactOpenAssignedWork} {
		if strings.Contains(only, k) {
			t.Errorf("%s survived a selection that excluded it:\n%s", k, only)
		}
	}
	if len(only) >= len(full) {
		t.Error("narrowing the selection did not shrink the block")
	}
}

func TestRenderDistinguishesAbsentEvidenceAndOldRequests(t *testing.T) {
	facts := Facts{
		LastBackup:         &LastBackup{Exists: false},
		NarrowerCredential: &NarrowerCredential{Exists: false},
		PairHistory:        &PairHistory{Total: 1, Denied: 1, LastDecision: "DENY", HoursSinceLast: 24 * 90, FirstAt: "unknown"},
	}
	got := facts.Render()
	for _, want := range []string{"last_backup: none recorded", "narrower_credential", "no (last 90d ago, DENY)", "unknown"} {
		if !strings.Contains(got, want) {
			t.Errorf("render missing %q: %s", want, got)
		}
	}
	facts.PairHistory.HoursSinceLast = -1
	if got := facts.RenderOnly([]string{FactPairHistory}); !strings.Contains(got, "yes — 0h ago, decided DENY") {
		t.Fatalf("future timestamp rendered misleading age: %s", got)
	}
	if got := facts.RenderOnly([]string{"unknown_fact"}); got != "" {
		t.Fatalf("unknown fact selection leaked facts: %s", got)
	}
}
