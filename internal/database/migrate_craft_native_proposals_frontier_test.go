package database

import "testing"

// craft_native_proposals shipped with a version (20261029165715) below the
// newest migration of its base (20261029173053), so applying it did not move
// MAX(_migrations.version) and the version-skew guard let the base binary
// boot over a schema it does not back up (G4 B25). The fix is a later no-op
// migration; this pins that the frontier is past the base for good.
func TestCraftNativeProposalsFrontierPastBase(t *testing.T) {
	const baseMax = 20261029173053
	var proposals, frontier int
	for _, m := range migrations {
		switch m.name {
		case "craft_native_proposals":
			proposals = m.version
		case "craft_native_proposals_frontier":
			frontier = m.version
		}
	}
	if proposals == 0 || proposals >= baseMax {
		t.Fatalf("fixture drift: proposals migration v%d, base max v%d", proposals, baseMax)
	}
	if frontier <= baseMax {
		t.Fatalf("craft_native_proposals_frontier v%d must exceed base max v%d so applying the table moves the ledger maximum", frontier, baseMax)
	}
}
