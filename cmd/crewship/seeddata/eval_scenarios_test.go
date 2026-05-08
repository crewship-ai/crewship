package seeddata_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/cmd/crewship/seeddata"
	"github.com/crewship-ai/crewship/internal/pipeline"
)

// TestEvalScenarios_ParseAndValidate guards against three classes of seed
// regression that the existing `crewship seed` smoke test won't catch:
//
//  1. The Definition map produces invalid DSL JSON (typoed key names,
//     wrong nesting). Surfaces only at /pipelines/save HTTP 422 today —
//     and the seeder tolerates per-routine 422 silently. This test
//     promotes those failures to compile-time-equivalent unit failures.
//
//  2. A step references an agent_slug that no Agents fixture provides.
//     The pipeline DSL validator rejects unknown slugs only when given
//     the author crew's agent set — which the seeder DOES pass — but
//     the seeder never reaches that path during `go test`. This test
//     wires every scenario against the union of all seeded agents so
//     one renamed Agent surfaces immediately.
//
//  3. An outcomes block references a grader_agent_slug that doesn't
//     exist (or is mistyped). Same regression class as #2; called out
//     separately because outcomes runs through a slightly different
//     dsl.go validation arm and a regression on either side is annoying
//     to diagnose without the explicit assertion.
func TestEvalScenarios_ParseAndValidate(t *testing.T) {
	if len(seeddata.EvalScenarios) == 0 {
		t.Fatal("EvalScenarios slice is empty — at least one scenario expected")
	}

	// Build a CREW-SCOPED agent map so the validator catches
	// scenarios that reference an agent slug existing somewhere
	// in the workspace but NOT in this scenario's author crew.
	// Runtime resolution is crew-scoped, so a global union would
	// silently let a Quality-crew scenario reference an Engineering
	// agent and pass the unit test, then 422 at /run.
	agentsByCrewMap := buildAgentSlugSetPerCrew()

	// Pre-seed the pipeline-slug set with every eval scenario's slug
	// so that any future call_pipeline reference between scenarios
	// resolves cleanly. Today no scenario uses call_pipeline, but
	// keeping the set populated documents intent.
	pipelineSlugs := make(map[string]struct{}, len(seeddata.EvalScenarios)+len(seeddata.Routines))
	for _, r := range seeddata.EvalScenarios {
		pipelineSlugs[r.Slug] = struct{}{}
	}
	for _, r := range seeddata.Routines {
		pipelineSlugs[r.Slug] = struct{}{}
	}

	for _, scenario := range seeddata.EvalScenarios {
		t.Run(scenario.Slug, func(t *testing.T) {
			// Top-level shape sanity. The seeder relies on these,
			// the asserts give a clearer error than later JSON
			// failures would.
			if scenario.Slug == "" {
				t.Fatal("scenario has empty slug")
			}
			if !strings.HasPrefix(scenario.Slug, "eval-") {
				t.Errorf("scenario slug %q must use the eval- prefix so users can filter the routine list", scenario.Slug)
			}
			if scenario.CrewSlug == "" {
				t.Fatalf("%s: empty CrewSlug — seeder would skip with 'crew not seeded'", scenario.Slug)
			}
			if scenario.Definition == nil {
				t.Fatalf("%s: nil Definition map", scenario.Slug)
			}

			data, err := json.Marshal(scenario.Definition)
			if err != nil {
				t.Fatalf("%s: marshal Definition: %v", scenario.Slug, err)
			}

			dsl, err := pipeline.Parse(data)
			if err != nil {
				t.Fatalf("%s: pipeline.Parse: %v\nraw: %s", scenario.Slug, err, string(data))
			}

			// Validate against ONLY the agents in this scenario's
			// author crew — same scope the production /pipelines/save
			// handler uses when wiring the validator.
			crewAgents, hasCrewMap := agentsByCrewMap[scenario.CrewSlug]
			if !hasCrewMap {
				t.Fatalf("%s: author crew %q has no seeded agents", scenario.Slug, scenario.CrewSlug)
			}
			if err := pipeline.Validate(dsl, crewAgents, pipelineSlugs); err != nil {
				t.Fatalf("%s (crew=%s): pipeline.Validate: %v", scenario.Slug, scenario.CrewSlug, err)
			}

			// Internal slug must match the outer routine slug so the
			// /pipelines/save handler treats them consistently. A
			// mismatch silently routes the routine under the
			// definition's name and breaks `crewship routine run
			// <slug>` for the operator.
			if dsl.Name != scenario.Slug {
				t.Errorf("%s: dsl.Name %q does not match scenario.Slug %q", scenario.Slug, dsl.Name, scenario.Slug)
			}
		})
	}
}

// TestEvalScenarios_AgentReferencesResolve walks every agent_slug and
// outcomes.grader_agent_slug reference inside the scenario set and
// asserts each one is present IN THE SCENARIO'S AUTHOR CREW. Cross-
// crew references are runtime resolution failures — the seed will
// succeed (skip_test_gate=true bypasses the live LLM check) but the
// routine will fail at first invocation with a "agent not found in
// crew" error.
//
// Tested separately from ParseAndValidate because that test only
// invokes pipeline.Validate (which catches missing slugs), while
// this test also catches the cross-crew misroute that pipeline
// validation alone would silently miss when given a union map.
func TestEvalScenarios_AgentReferencesResolve(t *testing.T) {
	agentsByCrew := buildAgentSlugSetPerCrew()
	for _, scenario := range seeddata.EvalScenarios {
		stepsAny, ok := scenario.Definition["steps"].([]map[string]interface{})
		if !ok {
			t.Errorf("%s: steps is not []map[string]interface{} (got %T)", scenario.Slug, scenario.Definition["steps"])
			continue
		}
		crewAgents, hasCrewMap := agentsByCrew[scenario.CrewSlug]
		if !hasCrewMap {
			t.Errorf("%s: author crew %q has no seeded agents", scenario.Slug, scenario.CrewSlug)
			continue
		}
		for i, step := range stepsAny {
			if slug, _ := step["agent_slug"].(string); slug != "" {
				if _, found := crewAgents[slug]; !found {
					t.Errorf("%s: step[%d] references agent_slug %q which is NOT in author crew %q",
						scenario.Slug, i, slug, scenario.CrewSlug)
				}
			}
			if outcomes, ok := step["outcomes"].(map[string]interface{}); ok {
				if grader, _ := outcomes["grader_agent_slug"].(string); grader != "" {
					if _, found := crewAgents[grader]; !found {
						t.Errorf("%s: step[%d] outcomes.grader_agent_slug %q is NOT in author crew %q",
							scenario.Slug, i, grader, scenario.CrewSlug)
					}
				}
			}
		}
	}
}

// buildAgentSlugSetPerCrew returns crewSlug → set(agentSlug) so
// validators can scope agent-resolution by crew, matching the
// production runtime's lookup. The pipeline validator takes the
// inner set keyed by struct{}; we materialise it once per test
// run to avoid each subtest re-walking the agent slice.
func buildAgentSlugSetPerCrew() map[string]map[string]struct{} {
	out := make(map[string]map[string]struct{})
	for _, a := range seeddata.Agents {
		if _, ok := out[a.CrewSlug]; !ok {
			out[a.CrewSlug] = make(map[string]struct{})
		}
		out[a.CrewSlug][a.Slug] = struct{}{}
	}
	return out
}
