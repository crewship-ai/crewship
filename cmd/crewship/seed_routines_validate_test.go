package main

// Validates that every seeded routine + eval scenario actually parses and
// passes the pipeline DSL validator BEFORE it ever reaches a live server —
// the seed `pipelines/save` path runs the same Parse+Validate, so a typo in
// a step shape, a template var that references an undefined input/step, or an
// agent_slug / grader_agent_slug that isn't in the routine's AUTHOR CREW
// would otherwise only surface as a runtime seed failure.
//
// agentSlugs is scoped per author crew (exactly how the save handler scopes
// it) so this also guards the crew-consistency invariant: a routine owned by
// crew X may only reference agents that live in crew X.

import (
	"encoding/json"
	"testing"

	"github.com/crewship-ai/crewship/cmd/crewship/seeddata"
	"github.com/crewship-ai/crewship/internal/pipeline"
)

func TestSeedRoutinesValidateAgainstDSL(t *testing.T) {
	all := append(append([]seeddata.RoutineDef{}, seeddata.Routines...), seeddata.EvalScenarios...)
	if len(all) == 0 {
		t.Fatal("no seed routines/scenarios — loader regression?")
	}

	// Per-crew agent slug sets (author-crew scoping, like the save handler).
	agentsByCrew := map[string]map[string]struct{}{}
	for _, a := range seeddata.Agents {
		set := agentsByCrew[a.CrewSlug]
		if set == nil {
			set = map[string]struct{}{}
			agentsByCrew[a.CrewSlug] = set
		}
		set[a.Slug] = struct{}{}
	}

	// Every known routine/scenario slug — call_pipeline targets resolve here.
	// Fail fast on a slug clash: seeding saves by slug, so two defs sharing
	// one would conflict at save time — and a plain map assignment masks it.
	pipelineSlugs := map[string]struct{}{}
	for _, d := range all {
		if _, dup := pipelineSlugs[d.Slug]; dup {
			t.Fatalf("duplicate seed slug %q across routines/eval-scenarios — save-by-slug would conflict", d.Slug)
		}
		pipelineSlugs[d.Slug] = struct{}{}
	}

	for _, d := range all {
		t.Run(d.Slug, func(t *testing.T) {
			data, err := json.Marshal(d.Definition)
			if err != nil {
				t.Fatalf("marshal definition: %v", err)
			}
			dsl, err := pipeline.Parse(data)
			if err != nil {
				t.Fatalf("parse DSL: %v", err)
			}
			crewAgents := agentsByCrew[d.CrewSlug]
			if crewAgents == nil {
				t.Fatalf("routine %q owned by crew %q which has no seeded agents", d.Slug, d.CrewSlug)
			}
			if err := pipeline.Validate(dsl, crewAgents, pipelineSlugs); err != nil {
				t.Fatalf("validate: %v", err)
			}
		})
	}
}
