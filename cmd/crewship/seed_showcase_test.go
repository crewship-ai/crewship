package main

// Locks the demo-showcase contract of the seed content — the fresh-
// workspace population a launch recording runs over. These tests pin
// WHAT the seed must contain (the differentiators on camera), while
// seed_routines_validate_test.go pins that every definition is valid
// DSL. Written against the showcase redesign:
//
//   - an agentless wake-gate probe (http + transform + code:expr) that
//     demonstrates token-zero monitoring (unscheduled — the demo seed
//     ships no cron schedules; wire one via the CLI/UI to see it fire),
//   - a morning-briefing agent routine, likewise unscheduled by default,
//   - a deterministic extraction recipe kept as the recipe-determinism
//     example (canonical @json final step),
//   - a multi-agent issue where a LEAD delegates subtasks to two crew
//     members (exercises the assignment flow),
//   - none of the retired network-busywork issues.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/cmd/crewship/seeddata"
	"github.com/crewship-ai/crewship/internal/pipeline"
)

// parseSeedRoutine finds a seed routine by slug and parses its DSL.
func parseSeedRoutine(t *testing.T, slug string) *pipeline.DSL {
	t.Helper()
	for _, r := range seeddata.Routines {
		if r.Slug != slug {
			continue
		}
		data, err := json.Marshal(r.Definition)
		if err != nil {
			t.Fatalf("marshal %s: %v", slug, err)
		}
		dsl, err := pipeline.Parse(data)
		if err != nil {
			t.Fatalf("parse %s: %v", slug, err)
		}
		return dsl
	}
	t.Fatalf("seed routine %q not found — showcase regression", slug)
	return nil
}

// The token-zero monitoring demo: an agentless probe that fetches a
// feed over HTTP, reduces it deterministically via transform steps,
// and emits true/false from a wired expr code step. This is the exact
// step-type trio the wake-gate feature exists for.
func TestSeedShowcase_FeedWatchProbeIsAgentlessTokenZero(t *testing.T) {
	dsl := parseSeedRoutine(t, "feed-watch-probe")
	if !dsl.Agentless {
		t.Fatal("feed-watch-probe must declare agentless: true (token-zero wake-gate contract)")
	}
	if dsl.EstimatedCostUSD != 0 {
		t.Errorf("feed-watch-probe estimated_cost_usd = %v, want 0 (token-zero)", dsl.EstimatedCostUSD)
	}
	if len(dsl.EgressTargets) == 0 {
		t.Error("feed-watch-probe has an http step — egress_targets must be a narrow allowlist, not empty")
	}
	var hasHTTP, hasTransform bool
	var codeSteps []pipeline.Step
	for _, st := range dsl.Steps {
		switch st.Type {
		case pipeline.StepHTTP:
			hasHTTP = true
		case pipeline.StepTransform:
			hasTransform = true
		case pipeline.StepCode:
			codeSteps = append(codeSteps, st)
		}
	}
	if !hasHTTP || !hasTransform || len(codeSteps) == 0 {
		t.Fatalf("feed-watch-probe must demo http + transform + code steps; got http=%v transform=%v code=%d",
			hasHTTP, hasTransform, len(codeSteps))
	}
	for _, st := range codeSteps {
		if st.Code == nil || !pipeline.IsWiredCodeRuntime(st.Code.Runtime) {
			t.Errorf("feed-watch-probe code step %q must use a wired runtime (expr/cel)", st.ID)
		}
	}
}

// morning-briefing must still be a working agent routine even though the
// demo seed no longer wires it to a cron schedule — it's runnable on
// demand or scheduled by hand.
func TestSeedShowcase_MorningBriefingIsAgentRoutine(t *testing.T) {
	dsl := parseSeedRoutine(t, "morning-briefing")
	var hasAgentRun bool
	for _, st := range dsl.Steps {
		if st.Type == pipeline.StepAgentRun {
			hasAgentRun = true
		}
	}
	if !hasAgentRun {
		t.Fatal("morning-briefing must contain an agent_run step")
	}
}

// The recipe-determinism example: incident-timeline is the realistic
// framing of the extraction class, and it must end on the appended
// canonical @json transform so its output is byte-stable across tiers.
func TestSeedShowcase_IncidentTimelineIsCanonicalRecipe(t *testing.T) {
	dsl := parseSeedRoutine(t, "incident-timeline")
	last := dsl.Steps[len(dsl.Steps)-1]
	if last.ID != "canonical" || last.Type != pipeline.StepTransform {
		t.Fatalf("incident-timeline must end on the canonical transform step (got id=%q type=%q) — is it registered in canonicalJSONRecipes?",
			last.ID, last.Type)
	}
	if last.Transform == nil || last.Transform.Expression != "@json" {
		t.Error("incident-timeline canonical step must use the @json expression")
	}
}

// The delegation demo: at least one seeded issue is assigned to a LEAD
// agent and explicitly asks it to delegate subtasks to two named crew
// members — that's what makes the assignment flow visible on camera.
func TestSeedShowcase_LeadDelegationIssue(t *testing.T) {
	leadSlugs := map[string]bool{}
	membersByCrew := map[string][]seeddata.AgentDef{}
	for _, a := range seeddata.Agents {
		if a.AgentRole == "LEAD" {
			leadSlugs[a.Slug] = true
		} else {
			membersByCrew[a.CrewSlug] = append(membersByCrew[a.CrewSlug], a)
		}
	}

	var found bool
	for _, is := range seeddata.Issues {
		if !leadSlugs[is.Assignee] {
			continue
		}
		if !strings.Contains(strings.ToLower(is.Description), "delegate") {
			continue
		}
		// Must name at least two non-lead members of the lead's crew.
		named := 0
		for _, m := range membersByCrew[is.CrewSlug] {
			if strings.Contains(is.Description, m.Name) {
				named++
			}
		}
		if named >= 2 {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("no seeded issue has a LEAD assignee delegating to two named crew members — the multi-agent assignment demo is missing")
	}
}

// The retired network-busywork issues must stay retired.
func TestSeedShowcase_BusyworkIssuesRetired(t *testing.T) {
	retired := []string{
		"Ping google.com",
		"Check HTTP status of 5 popular websites",
		"Trace DNS resolution",
		"Measure download speed",
		"generates Fibonacci",
		"Inventory all installed tools",
	}
	for _, is := range seeddata.Issues {
		for _, frag := range retired {
			if strings.Contains(is.Title, frag) {
				t.Errorf("retired busywork issue is back in the seed: %q", is.Title)
			}
		}
	}
}
