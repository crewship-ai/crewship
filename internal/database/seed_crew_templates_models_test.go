package database

import (
	"regexp"
	"testing"

	"github.com/crewship-ai/crewship/internal/llm"
)

// dateSuffixedModelID matches the dated-snapshot form of an Anthropic
// model id (a trailing "-20YYMMDD"). Model ids in this repo are bare
// aliases by convention — see the comment above ANTHROPIC_MODELS in
// lib/cli-adapters.ts — because a date-suffixed alias 404s against the
// Messages API once that snapshot retires.
var dateSuffixedModelID = regexp.MustCompile(`-20\d{6}$`)

// TestBuiltinCrewTemplates_ModelsAreCuratedBareAliases guards the
// llm_model of every agent in every builtin crew template.
//
// Why this exists: the templates shipped pinned to dated 2025
// snapshots (claude-sonnet-4-20250514, claude-opus-4-20250514,
// claude-haiku-4-20250514), all deprecated and retiring 2026-06-15.
// Every crew deployed from a template therefore started life on a
// model that was being switched off, and nothing in the test suite
// noticed — the loader tests only asserted llm_model was non-empty.
// This pins both halves of the convention: the id must be a bare
// alias (no date suffix) and it must be one the backend actually
// knows about, so the next silent drift fails here instead of in a
// customer's crew.
func TestBuiltinCrewTemplates_ModelsAreCuratedBareAliases(t *testing.T) {
	t.Parallel()

	// Load the templates exactly as SeedBuiltinCrewTemplates does.
	docs, err := loadBuiltinCrewTemplates()
	if err != nil {
		t.Fatalf("load builtin crew templates: %v", err)
	}

	// internal/llm is the source of truth for which ids the backend
	// knows. Build the lookup once; a template naming anything outside
	// it can't be resolved by the model picker or the orchestrator.
	curated := llm.CuratedModels("anthropic")
	if len(curated) == 0 {
		t.Fatal("llm.CuratedModels(\"anthropic\") returned no models — the curated set is the reference for this test")
	}
	known := make(map[string]bool, len(curated))
	for _, m := range curated {
		known[m.ID] = true
	}

	// Table is derived from the template data rather than hardcoded so
	// the next model bump only touches the YAML, never this test.
	type modelCase struct {
		template string // template slug
		agent    string // agent slug
		provider string
		model    string
	}
	var cases []modelCase
	for _, d := range docs {
		for _, a := range d.Agents {
			cases = append(cases, modelCase{
				template: d.Slug,
				agent:    a.Slug,
				provider: a.LLMProvider,
				model:    a.LLMModel,
			})
		}
	}
	if len(cases) == 0 {
		t.Fatal("no template agents to check — loader returned templates with zero agents")
	}

	for _, tc := range cases {
		t.Run(tc.template+"/"+tc.agent, func(t *testing.T) {
			t.Parallel()
			if dateSuffixedModelID.MatchString(tc.model) {
				t.Errorf("llm_model %q carries a date suffix; model ids must be bare aliases", tc.model)
			}
			// The curated set this test checks against is Anthropic's.
			// A template on another provider is out of scope here
			// rather than silently passing an Anthropic lookup.
			if tc.provider != "ANTHROPIC" && tc.provider != "anthropic" {
				t.Skipf("llm_provider %q is not ANTHROPIC — not covered by the Anthropic curated set", tc.provider)
			}
			if !known[tc.model] {
				t.Errorf("llm_model %q is not in llm.CuratedModels(%q); the backend does not know this id", tc.model, "anthropic")
			}
		})
	}
}
