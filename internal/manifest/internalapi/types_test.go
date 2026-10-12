package internalapi

import "testing"

func TestPlanActionCLIText(t *testing.T) {
	for _, tc := range []struct {
		action PlanAction
		want   string
	}{
		{ActionCreate, "create"}, {ActionUpdate, "update"}, {ActionDelete, "delete"}, {ActionUnchanged, "unchanged"},
	} {
		t.Run(tc.want, func(t *testing.T) {
			if got := tc.action.String(); got != tc.want {
				t.Fatalf("plan verb: got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestWorkspaceReferencesIncludeDeclaredAndRemoteObjects(t *testing.T) {
	declared := []SlugLookup{{Slug: "declared", Name: "Declared"}}
	remote := []SlugLookup{{Slug: "remote", Name: "Remote"}}
	w := WorkspaceContext{DeclaredProjects: declared, RemoteProjects: remote, DeclaredLabels: declared, RemoteLabels: remote, DeclaredCrews: declared, RemoteCrews: remote, DeclaredAgents: declared, RemoteAgents: remote, DeclaredRoutines: declared, RemoteRoutines: remote}
	for _, tc := range []struct {
		name string
		has  func(string) bool
	}{
		{"project", w.HasProject}, {"label", w.HasLabel}, {"crew", w.HasCrew}, {"agent", w.HasAgent}, {"routine", w.HasRoutine},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, slug := range []string{"declared", "remote"} {
				if !tc.has(slug) {
					t.Errorf("missing %s reference", slug)
				}
			}
			for _, slug := range []string{"absent", "Declared", ""} {
				if tc.has(slug) {
					t.Errorf("invented %q reference", slug)
				}
			}
		})
	}
	if !w.KnowsAgents() {
		t.Fatal("declared agents not known")
	}
	w.DeclaredAgents = nil
	if !w.KnowsAgents() {
		t.Fatal("remote agents not known")
	}
	w.RemoteAgents = nil
	if w.KnowsAgents() {
		t.Fatal("empty context pretends to know all agents")
	}
}
