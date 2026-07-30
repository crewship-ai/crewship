package orchestrator

import (
	"strings"
	"testing"
)

// A lead that is linked to another crew still could not use the link: the
// prompt listed only its OWN crew, never named the crews it may reach, and
// documented /assign in a form with no crew field. A live delegation from
// Engineering to a linked Ops crew therefore came back "Morgan is not found
// in the Ops crew (or any connected crew accessible to me)" — the model had
// no way to know Ops was reachable, or how to address it.

func TestBuildLeadContext_NamesTheCrewsThisLeadCanReach(t *testing.T) {
	out := BuildLeadContext(
		[]CrewMember{{Name: "Sam", Slug: "sam", RoleTitle: "Backend Engineer"}},
		[]ConnectedCrew{{
			Name: "Ops", Slug: "ops", Direction: "bidirectional",
			Agents: []ConnectedAgent{
				{Slug: "morgan", RoleTitle: "SRE / Ops Lead", IsLead: true},
				{Slug: "riley", RoleTitle: "Platform Engineer"},
			},
		}},
	)

	for _, want := range []string{"ops", "morgan", "SRE / Ops Lead", "riley"} {
		if !strings.Contains(out, want) {
			t.Errorf("lead context does not mention %q:\n%s", want, out)
		}
	}
	// And the form that actually reaches them.
	if !strings.Contains(out, `"crew"`) {
		t.Errorf("lead context never shows the crew field of /assign:\n%s", out)
	}
}

func TestBuildLeadContext_NoConnectedCrews_SaysNothingAboutThem(t *testing.T) {
	out := BuildLeadContext([]CrewMember{{Name: "Sam", Slug: "sam"}}, nil)
	if strings.Contains(strings.ToLower(out), "crews you can reach") {
		t.Errorf("unlinked lead was told about cross-crew reach:\n%s", out)
	}
	// The own-crew half is unchanged.
	if !strings.Contains(out, "@sam") {
		t.Errorf("own crew roster missing:\n%s", out)
	}
}

// A one-way link out means this lead may dispatch; a one-way link IN means it
// may not, and saying "you can reach them" would send the model at a door
// that answers 403.
func TestBuildLeadContext_InboundOnlyLink_IsNotListedAsReachable(t *testing.T) {
	out := BuildLeadContext(
		[]CrewMember{{Name: "Sam", Slug: "sam"}},
		[]ConnectedCrew{{Name: "Quality", Slug: "quality", Direction: "inbound"}},
	)
	// Match the roster line, not the bare word: the static cheat-sheet
	// already says "self-assessed quality".
	if strings.Contains(out, "crew slug: quality") {
		t.Errorf("inbound-only crew listed as reachable:\n%s", out)
	}
}

// Solo lead with no crew members but a link outward still needs the block —
// the reach is the only thing it has.
func TestBuildLeadContext_NoMembersButLinked_StillRendersTheReach(t *testing.T) {
	out := BuildLeadContext(nil, []ConnectedCrew{{
		Name: "Ops", Slug: "ops", Direction: "unidirectional",
		Agents: []ConnectedAgent{{Slug: "morgan", RoleTitle: "Ops Lead", IsLead: true}},
	}})
	if !strings.Contains(out, "morgan") {
		t.Errorf("solo lead lost its only reachable target:\n%s", out)
	}
}
