package notify

import (
	"testing"

	"github.com/crewship-ai/crewship/internal/inbox"
)

// TestCategoryGroupsCoverAllCategories pins that every category has exactly
// one UI home. A category with no group would silently never render in the
// settings matrix — switchable in the API, invisible in the product, which is
// the exact failure mode taxonomy v2 exists to fix.
func TestCategoryGroupsCoverAllCategories(t *testing.T) {
	seen := map[string]int{}
	for _, g := range CategoryGroups {
		if g.Key == "" || g.Label == "" {
			t.Errorf("group %+v is missing a key or label", g)
		}
		for _, c := range g.Categories {
			seen[c]++
		}
	}
	for c, n := range seen {
		if n != 1 {
			t.Errorf("category %q appears in %d groups, want exactly 1", c, n)
		}
	}
	if len(seen) != len(AllCategories) {
		t.Errorf("groups cover %d categories, AllCategories has %d", len(seen), len(AllCategories))
	}
	for _, c := range AllCategories {
		if seen[c] == 0 {
			t.Errorf("category %q is in AllCategories but belongs to no group", c)
		}
		if GroupForCategory(c) == "" {
			t.Errorf("GroupForCategory(%q) returned no group", c)
		}
	}
}

// TestEveryInboxKindMapsToACategory is the coverage guard on the actionable
// half of the vocabulary. inbox.KindScheduleMissed shipped in v162 and was
// never added to categoryByKind, so "your routine did not fire as scheduled"
// was written to the in-product inbox and never left the product. A new kind
// must either route somewhere or be explicitly listed as in-product-only.
func TestEveryInboxKindMapsToACategory(t *testing.T) {
	// Kinds that deliberately stay in-product. Empty today; an entry here is
	// a documented decision, not an oversight.
	inProductOnly := map[string]bool{}

	for _, kind := range inbox.AllKinds {
		cat := CategoryForKind(kind)
		if cat == "" {
			if inProductOnly[kind] {
				continue
			}
			t.Errorf("inbox kind %q maps to no category — it will never reach a channel. "+
				"Add it to categoryByKind, or to inProductOnly with a reason.", kind)
			continue
		}
		if !ValidCategory(cat) {
			t.Errorf("inbox kind %q maps to %q, which is not a valid category", kind, cat)
		}
	}
}

// TestLegacyCategoriesRemapToValidCategories pins that the migration's rewrite
// table cannot strand a user's preference on a category that no longer exists.
func TestLegacyCategoriesRemapToValidCategories(t *testing.T) {
	// The exact pre-taxonomy-v2 vocabulary (#1412). Hard-coded rather than
	// derived: the point is to detect drift from what is actually in the
	// database, which no current constant describes any more.
	old := []string{
		"approvals", "escalations", "runs.failed", "runs.completed",
		"chat.replies", "security", "budget", "system", "memory",
	}
	for _, o := range old {
		targets, ok := LegacyCategories[o]
		if !ok {
			t.Errorf("legacy category %q has no mapping — a user's stored preference would be stranded", o)
			continue
		}
		if len(targets) == 0 {
			t.Errorf("legacy category %q maps to nothing", o)
		}
		for _, tgt := range targets {
			if !ValidCategory(tgt) {
				t.Errorf("legacy category %q maps to %q, which is not a valid category", o, tgt)
			}
		}
	}
	if len(LegacyCategories) != len(old) {
		t.Errorf("LegacyCategories has %d entries, the old vocabulary had %d — "+
			"it should be a complete statement of the old set", len(LegacyCategories), len(old))
	}
}

// TestBypassesRateGate pins that the two blocking, human-in-the-loop
// categories keep their rate-gate exemption across the rename. Losing it would
// let an approval request be silently dropped as "too many notifications".
func TestBypassesRateGate(t *testing.T) {
	for _, c := range []string{CategoryAgentsApproval, CategoryAgentsEscalation} {
		if !BypassesRateGate(c) {
			t.Errorf("%s must bypass the rate gate — a blocking item must never be rate-dropped", c)
		}
	}
	for _, c := range []string{CategoryRoutinesCompleted, CategoryChatReplies, CategoryAgentsBudget} {
		if BypassesRateGate(c) {
			t.Errorf("%s must NOT bypass the rate gate — it is a storm candidate", c)
		}
	}
	// The pre-rename names must not keep an exemption by accident.
	for _, c := range []string{"approvals", "escalations"} {
		if BypassesRateGate(c) {
			t.Errorf("legacy name %q still bypasses the rate gate; only the new names should", c)
		}
	}
}

func TestValidPrefCategoryAdmitsMuteSentinel(t *testing.T) {
	if !ValidPrefCategory(CategoryMuteAll) {
		t.Error("the mute-all sentinel must be legal on a prefs row")
	}
	if ValidCategory(CategoryMuteAll) {
		t.Error("the mute-all sentinel must NOT be a selectable category")
	}
	if ValidPrefCategory("runs.failed") {
		t.Error("the pre-rename category name must no longer validate")
	}
}
