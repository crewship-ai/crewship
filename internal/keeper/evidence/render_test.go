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
