package main

import (
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli/clitest"
	"github.com/crewship-ai/crewship/internal/pipeline"
	"github.com/spf13/cobra"
)

// #832 — offline agent_slug resolution on `routine validate`.
//
// The default (neither flag) keeps validate fully offline: agentSlugs is nil,
// so a typo'd agent_slug still passes validate and only fails at save.
// --agents enumerates the roster locally (airgapped/CI); --author-crew fetches
// it with one server call. Either way a typo then fails validate, pre-save.

// newValidateCmdForTest builds a throwaway command carrying the same
// --agents/--author-crew flags resolveValidateAgentSlugs reads, so the pure
// resolver logic can be exercised without touching global command state.
func newValidateCmdForTest(t *testing.T) *cobra.Command {
	t.Helper()
	c := &cobra.Command{Use: "validate"}
	c.Flags().StringSlice("agents", nil, "")
	c.Flags().String("author-crew", "", "")
	return c
}

func TestResolveValidateAgentSlugs_NeitherFlag_ReturnsNil(t *testing.T) {
	set, err := resolveValidateAgentSlugs(newValidateCmdForTest(t))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if set != nil {
		t.Errorf("no flags must yield nil (skip existence check), got: %v", set)
	}
}

func TestResolveValidateAgentSlugs_AgentsFlag_BuildsSet(t *testing.T) {
	c := newValidateCmdForTest(t)
	if err := c.Flags().Set("agents", "triage, writer , ,editor"); err != nil {
		t.Fatal(err)
	}
	set, err := resolveValidateAgentSlugs(c)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"triage", "writer", "editor"}
	if len(set) != len(want) {
		t.Fatalf("expected %d slugs (whitespace/empties dropped), got %d: %v", len(want), len(set), set)
	}
	for _, s := range want {
		if _, ok := set[s]; !ok {
			t.Errorf("missing slug %q in resolved set %v", s, set)
		}
	}
}

func TestResolveValidateAgentSlugs_BothFlags_MutuallyExclusive(t *testing.T) {
	c := newValidateCmdForTest(t)
	_ = c.Flags().Set("agents", "a")
	_ = c.Flags().Set("author-crew", "growth")
	_, err := resolveValidateAgentSlugs(c)
	if err == nil {
		t.Fatal("--agents and --author-crew together must error")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("error should say mutually exclusive, got: %v", err)
	}
}

// The whole point of the feature: with the roster known, a typo'd agent_slug
// is rejected by pipeline.Validate — the same check the server runs at save.
func TestValidate_WithAgentsSet_RejectsTypo(t *testing.T) {
	raw := []byte(`{
		"dsl_version": "1.0",
		"name": "demo",
		"steps": [{"id":"a","type":"agent_run","agent_slug":"triage","prompt":"hi"}]
	}`)
	dsl, err := pipeline.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	// "triage" is NOT in the known roster {writer, editor} → reject.
	if err := pipeline.Validate(dsl, slugSet([]string{"writer", "editor"}), nil); err == nil {
		t.Fatal("expected unknown agent_slug to be rejected when roster is known")
	}
	// Same DSL with the slug present → accepted.
	if err := pipeline.Validate(dsl, slugSet([]string{"triage", "writer"}), nil); err != nil {
		t.Errorf("known agent_slug should validate, got: %v", err)
	}
	// nil roster (default offline) → typo passes, back-compat preserved.
	if err := pipeline.Validate(dsl, nil, nil); err != nil {
		t.Errorf("nil roster must skip the existence check, got: %v", err)
	}
}

// --author-crew resolves the roster via one GET /api/v1/agents?crew_id= call.
// A CUID-shaped crew short-circuits resolveCrewID, so only the agents endpoint
// needs stubbing.
func TestFetchCrewAgentSlugs_ReadsRoster(t *testing.T) {
	stub := covStub(t)
	crewCUID := "ccrew00000000000000000aa"
	stub.OnGet("/api/v1/agents", clitest.JSONResponse(200, []map[string]any{
		{"id": "cag1", "slug": "triage"},
		{"id": "cag2", "slug": "writer"},
	}))
	set, err := fetchCrewAgentSlugs(crewCUID)
	if err != nil {
		t.Fatalf("fetchCrewAgentSlugs: %v", err)
	}
	if _, ok := set["triage"]; !ok {
		t.Errorf("roster missing triage: %v", set)
	}
	if _, ok := set["writer"]; !ok {
		t.Errorf("roster missing writer: %v", set)
	}
	if len(set) != 2 {
		t.Errorf("expected 2 slugs, got %d: %v", len(set), set)
	}
}
