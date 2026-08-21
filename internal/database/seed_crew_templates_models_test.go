package database

import (
	"regexp"
	"strings"
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

	// The curated half is Anthropic-specific, so it can only run on agents
	// that declare that provider. Counted here rather than skipped per
	// subtest: a t.Skip reports the same "ok" as a pass, so a template set
	// that drifted entirely off ANTHROPIC would turn the curated assertion
	// into a no-op while the suite still read green.
	anthropicChecked := 0

	for _, tc := range cases {
		t.Run(tc.template+"/"+tc.agent, func(t *testing.T) {
			// The bare-alias rule is provider-independent — a dated id
			// pins a snapshot that gets withdrawn whoever serves it.
			if dateSuffixedModelID.MatchString(tc.model) {
				t.Errorf("llm_model %q carries a date suffix; model ids must be bare aliases", tc.model)
			}
			if !strings.EqualFold(tc.provider, "anthropic") {
				return
			}
			if !known[tc.model] {
				t.Errorf("llm_model %q is not in llm.CuratedModels(%q); the backend does not know this id", tc.model, "anthropic")
			}
		})
		if strings.EqualFold(tc.provider, "anthropic") {
			anthropicChecked++
		}
	}

	if anthropicChecked == 0 {
		t.Error("no template agent declares llm_provider ANTHROPIC — the curated-membership half of this test checked nothing")
	}
}
