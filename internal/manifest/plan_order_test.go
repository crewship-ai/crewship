package manifest

import "testing"

// kindOrder encodes the dependency order the plan is sorted by: routines
// before the issues that bind them, projects before milestones, and so on.
//
// It has never applied to the SPEC-2 kinds. Their plan items are emitted with
// LOWERCASE kind names ("routine", "issue") by the per-kind packages, while
// this map is keyed on the capitalised document names ("Routine", "Issue"), so
// every one of them fell through to the unknown-kind fallback and got ordered
// by its description string instead. Nothing noticed, because until an issue
// could reference a routine nothing in the manifest depended on another SPEC-2
// kind created in the same apply. The first file that did — the demo — failed
// with "routine not found" while the routine sat two lines below it in the
// plan, alphabetically after "issue".

func TestKindOrder_MatchesEmittedLowercaseNames(t *testing.T) {
	const fallback = 99
	for _, kind := range []string{
		"project", "label", "milestone", "routine", "schedule",
		"issue", "recurring_issue", "recipe", "connector", "hook", "page",
	} {
		if got := kindOrder(kind, ActionCreate); got == fallback {
			t.Errorf("kindOrder(%q) fell through to the unknown-kind fallback — "+
				"the plan would order it alphabetically instead of by dependency", kind)
		}
	}
}

func TestKindOrder_RoutinesLandBeforeIssues(t *testing.T) {
	// The dependency the demo manifest relies on: an issue may bind a
	// routine, never the other way round.
	routine := kindOrder("routine", ActionCreate)
	issue := kindOrder("issue", ActionCreate)
	if routine >= issue {
		t.Errorf("routine ranks %d and issue %d — an issue would be created before the routine it binds",
			routine, issue)
	}
}

func TestKindOrder_CaseDoesNotChangeTheAnswer(t *testing.T) {
	// Both spellings exist in the codebase today; neither should be the one
	// that silently works.
	for _, pair := range [][2]string{
		{"routine", "Routine"},
		{"issue", "Issue"},
		{"project", "Project"},
		{"page", "Page"},
	} {
		if kindOrder(pair[0], ActionCreate) != kindOrder(pair[1], ActionCreate) {
			t.Errorf("%q and %q rank differently", pair[0], pair[1])
		}
	}
}

func TestKindOrder_DeletesReverseTheDependency(t *testing.T) {
	// Tearing down in creation order would drop a routine while an issue
	// still points at it.
	routine := kindOrder("routine", ActionDelete)
	issue := kindOrder("issue", ActionDelete)
	if issue >= routine {
		t.Errorf("on delete, issue ranks %d and routine %d — the routine would go first",
			issue, routine)
	}
}

func TestKindOrder_PagesLandAfterTheProducersTheyName(t *testing.T) {
	// Every panel on a page names a producer and an owning crew, and the
	// server's authoring gate refuses a page whose routine does not exist
	// yet — so the routine has to be created first.
	routine := kindOrder("routine", ActionCreate)
	page := kindOrder("page", ActionCreate)
	if routine >= page {
		t.Errorf("routine ranks %d and page %d — the page would be created before the routine its panel names",
			routine, page)
	}
	// And torn down in the opposite order, so no page is left pointing at
	// a routine that has already gone.
	if kindOrder("page", ActionDelete) >= kindOrder("routine", ActionDelete) {
		t.Errorf("on delete, page ranks %d and routine %d — the routine would go first",
			kindOrder("page", ActionDelete), kindOrder("routine", ActionDelete))
	}
}
