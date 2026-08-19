package kinds

import (
	"testing"

	"github.com/crewship-ai/crewship/internal/pages"
)

// The authored half is compared only when the server says it sent it.
//
// Both branches matter and they fail in opposite directions. Comparing without
// the marker makes every reader's `apply --dry-run` propose deleting buttons
// they were never shown; not comparing with it makes a manifest whose only
// change IS a button plan as "unchanged". The marker is the only thing that
// tells the two absences apart.
func TestPagePanelsDiffer_AuthoredHalfIsComparedOnlyWhenItWasSent(t *testing.T) {
	declared := []pages.PanelSpec{{
		ID: "sluzby", Schema: pages.SchemaStatus, Owner: "crew/lookout",
		Producer: "routine/watch", SLA: "30s", Span: 8,
		Actions: []pages.PanelAction{{ID: "restart", Kind: "call", Label: "Restart", Routine: "line-restart"}},
	}}
	// What a READER receives: the same panel with no authored half at all.
	withoutHalf := []PagePanelRemote{{
		ID: "sluzby", Schema: "status.v1", Owner: "crew/lookout",
		Producer: "routine/watch", SLASeconds: 30, Span: 8,
	}}

	if pagePanelsDiffer(declared, withoutHalf, false) {
		t.Error("a reader's plan proposes a change it cannot see — the panel differs only in fields that were never sent")
	}
	if !pagePanelsDiffer(declared, withoutHalf, true) {
		t.Error("an editor was told the half was sent and it is empty; a declared action really is missing and that is a change")
	}
}

func TestPagePanelsDiffer_AnEditorSeesAButtonChange(t *testing.T) {
	base := pages.PanelSpec{
		ID: "sluzby", Schema: pages.SchemaStatus, Owner: "crew/lookout",
		Producer: "routine/watch", SLA: "30s", Span: 8,
	}
	remote := []PagePanelRemote{{
		ID: "sluzby", Schema: "status.v1", Owner: "crew/lookout",
		Producer: "routine/watch", SLASeconds: 30, Span: 8,
		Actions: []pages.PanelAction{{ID: "restart", Kind: "call", Label: "Restart", Routine: "line-restart"}},
	}}

	same := base
	same.Actions = []pages.PanelAction{{ID: "restart", Kind: "call", Label: "Restart", Routine: "line-restart"}}
	if pagePanelsDiffer([]pages.PanelSpec{same}, remote, true) {
		t.Error("an identical action reads as drift")
	}

	// A relabelled button. Before the marker this planned as "unchanged",
	// which is the whole reason the marker exists.
	relabelled := base
	relabelled.Actions = []pages.PanelAction{{ID: "restart", Kind: "call", Label: "Restartovat linku", Routine: "line-restart"}}
	if !pagePanelsDiffer([]pages.PanelSpec{relabelled}, remote, true) {
		t.Error("a renamed button planned as unchanged")
	}

	// And a nested field, which is what a hand-written comparison forgets.
	deeper := base
	deeper.Actions = []pages.PanelAction{{
		ID: "restart", Kind: "call", Label: "Restart", Routine: "line-restart",
		Confirm: &pages.PanelActionConfirm{Title: "Restartovat?"},
	}}
	if !pagePanelsDiffer([]pages.PanelSpec{deeper}, remote, true) {
		t.Error("a confirmation added to an existing button planned as unchanged")
	}
}

func TestPagePanelsDiffer_PublicAndRefreshTravelWithTheAuthoredHalf(t *testing.T) {
	base := pages.PanelSpec{
		ID: "incident", Schema: pages.SchemaNarrative, Owner: "crew/devops",
		Producer: "routine/incident-rozbor", SLA: "1h", Span: 12,
	}
	remote := []PagePanelRemote{{
		ID: "incident", Schema: "narrative.v1", Owner: "crew/devops",
		Producer: "routine/incident-rozbor", SLASeconds: 3600, Span: 12,
	}}

	published := base
	published.Public = true
	if !pagePanelsDiffer([]pages.PanelSpec{published}, remote, true) {
		t.Error("publishing a panel planned as unchanged")
	}
	if pagePanelsDiffer([]pages.PanelSpec{published}, remote, false) {
		t.Error("a reader's plan proposed unpublishing a panel it was not shown")
	}

	refreshed := base
	refreshed.Refresh = pages.PanelRefresh("on:wake")
	if !pagePanelsDiffer([]pages.PanelSpec{refreshed}, remote, true) {
		t.Error("adding a refresh trigger planned as unchanged")
	}
}
