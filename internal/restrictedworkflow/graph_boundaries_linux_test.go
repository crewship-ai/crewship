//go:build linux

package restrictedworkflow

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestWorkflowUnstoredGraphRejectsConflictingFrozenBindings(t *testing.T) {
	s, _ := fixture(t)
	graph, err := s.compileGraph(t.Context(), "h1", "w", "routine")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(graph)
	if err != nil {
		t.Fatal(err)
	}
	base := job{ID: "not-enqueued", Workspace: "w", Principal: "h1", Pipeline: graph.Root.ID, Recipe: graph.Root.Raw, RecipeHash: graph.Root.Hash, Agent: graph.Agent, Profile: graph.Profile, Graph: string(raw), GraphHash: hash(string(raw))}
	if err = s.checkGraph(t.Context(), s.db, base); err != nil {
		t.Fatalf("valid prepared graph refused: %v", err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*frozenGraph)
	}{
		{"recipe digest", func(g *frozenGraph) {
			g.Root.Steps[0].Child = &graphRecipe{ID: "routine", Raw: "changed", Hash: "wrong"}
		}},
		{"conflicting same recipe", func(g *frozenGraph) {
			g.Root.Steps[0].Child = &graphRecipe{ID: "routine", Raw: "changed", Hash: hash("changed")}
		}},
		{"conflicting provider", func(g *frozenGraph) { g.Root.Steps[1].ProviderHash = "conflict" }},
		{"unknown profile", func(g *frozenGraph) { g.Root.Steps[0].Profile = "disabled" }},
		{"unknown agent", func(g *frozenGraph) { g.Root.Steps[0].Agent = "missing" }},
		{"wrong provider", func(g *frozenGraph) { g.Root.Steps[0].ProviderHash = "not-current" }},
		{"too deep", func(g *frozenGraph) {
			current := g.Root
			for range 5 {
				next := &graphRecipe{ID: g.Root.ID, Raw: g.Root.Raw, Hash: g.Root.Hash, Crew: g.Root.Crew}
				current.Steps = []graphStep{{ID: "nested", Child: next}}
				current = next
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var altered frozenGraph
			if err := json.Unmarshal(raw, &altered); err != nil {
				t.Fatal(err)
			}
			tc.mutate(&altered)
			encoded, err := json.Marshal(altered)
			if err != nil {
				t.Fatal(err)
			}
			j := base
			j.Graph = string(encoded)
			j.GraphHash = hash(j.Graph)
			if err = s.checkGraph(t.Context(), s.db, j); !errors.Is(err, ErrDenied) {
				t.Fatalf("conflicting frozen graph accepted: %v", err)
			}
		})
	}
}

func TestWorkflowStoredGraphRequiresBothRecipeAndProviderBindings(t *testing.T) {
	for _, table := range []string{"restricted_workflow_recipe_bindings", "restricted_workflow_provider_policies"} {
		t.Run(table, func(t *testing.T) {
			s, _ := fixture(t)
			receipt := admitFixtureJob(t, s)
			j, err := s.load(t.Context(), receipt.ID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.db.ExecContext(t.Context(), "DELETE FROM "+table+" WHERE job_id=?", receipt.ID); err != nil {
				t.Fatal(err)
			}
			if err = s.checkGraph(t.Context(), s.db, j); !errors.Is(err, ErrDenied) {
				t.Fatalf("deleted frozen binding accepted: %v", err)
			}
		})
	}
}

func TestWorkflowGraphRequiresDeclaredNestedInputSet(t *testing.T) {
	for _, inputs := range []string{`{}`, `{"context":"safe","unknown":"private"}`} {
		t.Run(inputs, func(t *testing.T) {
			s, _ := graphFixture(t)
			var declaration map[string]any
			if err := json.Unmarshal([]byte(graphRecipeJSON), &declaration); err != nil {
				t.Fatal(err)
			}
			var values map[string]any
			if err := json.Unmarshal([]byte(inputs), &values); err != nil {
				t.Fatal(err)
			}
			steps := declaration["steps"].([]any)
			steps[1].(map[string]any)["inputs"] = values
			raw, err := json.Marshal(declaration)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.db.ExecContext(t.Context(), `UPDATE pipelines SET definition_json=?,definition_hash=? WHERE id='routine'`, string(raw), hash(string(raw))); err != nil {
				t.Fatal(err)
			}
			if _, err = s.compileGraph(t.Context(), "h1", "w", "routine"); err == nil {
				t.Fatal("invalid child input contract accepted")
			}
		})
	}
}
