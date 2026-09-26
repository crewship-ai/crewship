package seeddata

// RoutineDef defines a seed routine — a workspace-scoped declarative
// AI workflow recipe. Mirrors how IssueDef seeds demo missions: gives
// a fresh dev-server an immediate population of believable, working
// routines so users see the feature populated rather than an empty
// list.
//
// Definition is the full DSL JSON tree (parsed and validated by the
// pipeline package on save). AgentSlug + CrewSlug get resolved to IDs
// during seed; the runtime author = the seed admin user.
type RoutineDef struct {
	Slug            string                 // workspace-unique kebab-case identifier
	Name            string                 // human-readable display name
	Description     string                 // one-line summary shown in lists
	CrewSlug        string                 // crew that owns this routine (resolves to author_crew_id)
	AuthorAgentSlug string                 // optional acting agent for issue comments
	Definition      map[string]interface{} // parsed DSL JSON
}

// agentSlugRef is a tiny helper marker for readability — the seeder
// keeps the slug as-is in the definition and doesn't try to resolve
// to an agent ID. The pipeline executor's runtime resolveAgentID
// handles the slug→ID lookup at first invocation, scoped to the
// author crew. So if the named agent is renamed later, the routine
// gets a clean error rather than a silent wrong-agent execution.
func agentSlugRef(slug string) string { return slug }

// Routine definitions for the business demo. The historical generic recipe
// library this file carried was removed with the pack-based seed it served;
// git history keeps it as the source archive. Model-regression scenarios
// live in EvalScenarios and are opt-in from the CLI.
//
// THE RECIPE PHILOSOPHY (why these exist and how they're built):
//
// A routine is authored once by a strong model (Opus) and then executed
// cheaply and repeatedly on a fast model (Haiku) with a near-100%
// IDENTICAL output. That only works when the task is a TRANSFORMATION
// (input fully determines output), the output is CANONICAL (sorted,
// fixed JSON schema, closed label set, fixed template), and a double
// gate locks it down:
//
//   - validation  → locks the SHAPE (json schema, must_contain, length)
//   - outcomes    → a stronger grader agent checks SEMANTIC equality
//     against a rubric; step on_fail: escalate_tier means
//     if the fast tier ever drifts off-rubric the run
//     escalates to a smarter model rather than shipping a
//     wrong answer. The goal, though, is Haiku stability.
//
// The recipes below span the determinism classes: pure extraction /
// normalization, closed-set classification, validation / linting,
// redaction, decision tables, structured review, faithful
// summarization, and multi-step / orchestration. The eval-* scenarios in
// eval_scenarios.go are an opt-in regression harness (`seed --with-evals`).
//
// Alongside the deterministic recipes, four routines exist for the
// live-workspace demo loop rather than determinism. None of them ship
// with a cron schedule — the demo seed intentionally has zero scheduled
// routines; wire one by hand (`crewship routine schedules create`) to
// see the loop fire:
//   - morning-briefing    — lead briefing (agent routine whose
//     completion lands an inbox notification)
//   - feed-watch-probe    — agentless GitHub Status wake gate (http +
//     transform + code:expr), suitable as a schedule's wake gate
//   - feed-change-report  — the GitHub incident brief that only runs
//     (and only spends tokens) when the probe fires
//   - page-watch          — the routine→page loop: writes both panels
//     of the `watch` page as their declared producer. The seeder
//     fires one run of it (seedPageProducerRoutines), which is the
//     ONLY way those panels ever hold data — an owner may not push to
//     a routine-produced panel.
//
// Design conventions (kept identical across every recipe):
//   - Each routine runs with empty inputs (sensible defaults set)
//   - Every agent_run validation carries must_not_contain creds guards
//   - estimated_cost_usd kept low so the cost cap never trips
//   - Deterministic recipes pin complexity: fast (Haiku); graded ones
//     add on_fail: escalate_tier so drift escalates instead of shipping

// Routines is the focused catalogue shown in a normal demo workspace;
// model regression coverage is available through `seed --with-evals`.
// A fresh product demo should read like an operating team, not a test fixture.
var Routines = append(append(businessRoutines(), operationsRoutine()), liveRoutines()...)

// WorkspaceDigestDefinition is the DSL for the "workspace-digest" seed
// routine (#1422 item 4), exported so `crewship digest enable` can save it
// on demand for a workspace that predates this template (or nuked it) —
// one source of truth for both the seeder and the CLI wrapper.
//
// Three deterministic steps, zero LLM spend, zero network egress:
//  1. query pipeline_runs over a trailing 24h window
//  2. transform: extract the pre-rendered `summary_md` field
//  3. notify the workspace — lands in the inbox and fans out to Slack/
//     email/etc per each recipient's notification channel preferences
//     (issue #1412's existing category × channel matrix, untouched)
//
// agentless: true is accurate here (not "agentless(-ish)" hedging) — query
// and transform are both server-side/deterministic and notify never
// touches an LLM either, so the whole routine is genuinely token-zero.
// window_hours on the query step is NOT template-substituted — like
// HTTPStep.MaxResponseBytes / step timeout_seconds, it's a static
// per-step config value set at author time, not a per-run variable (the
// DSL template engine only resolves string-typed fields). 24h is the
// digest's fixed lookback; author a second routine (or a copy with a
// different window_hours) for a different cadence.
var WorkspaceDigestDefinition = map[string]interface{}{
	"dsl_version":        "1.0",
	"name":               "workspace-digest",
	"display_name":       "Workspace digest",
	"description":        "Token-zero ops digest: run counts, cost, and top failures over a trailing window, posted to the workspace inbox.",
	"estimated_cost_usd": 0.0,
	"agentless":          true,
	"outputs": []map[string]interface{}{
		{"name": "summary_md", "type": "string"},
	},
	"steps": []map[string]interface{}{
		{
			"id":   "stats",
			"type": "query",
			"query": map[string]interface{}{
				"source":       "pipeline_runs",
				"window_hours": 24,
			},
		},
		{
			"id":    "summary",
			"type":  "transform",
			"needs": []string{"stats"},
			"transform": map[string]interface{}{
				"input":      "{{ steps.stats.output }}",
				"expression": ".summary_md",
			},
		},
		{
			"id":    "post",
			"type":  "notify",
			"needs": []string{"summary"},
			"notify": map[string]interface{}{
				"to":    "workspace",
				"title": "Workspace digest",
				"body":  "{{ steps.summary.output }}",
			},
		},
	},
}

// canonicalJSONRecipes maps a recipe slug → the id of the agent_run step
// whose JSON output should be canonicalised. Authored once here rather
// than inlined into every Definition literal so the recipes stay readable
// and the canonicalisation policy lives in one place.
var canonicalJSONRecipes = map[string]string{
	"extract-contacts":      "extract",
	"incident-timeline":     "extract",
	"classify-ticket":       "classify",
	"normalize-dates":       "normalize",
	"json-schema-validate":  "validate",
	"invoice-extract":       "extract",
	"routing-decision":      "route",
	"diff-risk-score":       "score",
	"website-content-audit": "extract",
}

// init appends a final `@json` transform step to every deterministic
// JSON recipe. An LLM's JSON output is only SEMANTICALLY stable on a
// fast tier — its whitespace and key order drift run-to-run (e.g. Haiku
// emitted both `{"a":1}` and `{"a": 1}` for the same input). The
// transform parses that output (stripping any code fence) and
// re-serialises it canonically (compact, alphabetically-sorted keys),
// so the routine's FINAL output is byte-identical every run and across
// tiers — the property that makes these recipes hard, reproducible test
// scenarios. The agent_run step keeps its own validation/grader; this
// only normalises the bytes that flow out of the routine.
func init() {
	for i := range Routines {
		stepID, ok := canonicalJSONRecipes[Routines[i].Slug]
		if !ok {
			continue
		}
		steps, ok := Routines[i].Definition["steps"].([]map[string]interface{})
		if !ok {
			panic("seeddata: routine " + Routines[i].Slug + " has unexpected steps type for canonicalisation")
		}
		// Confirm the target step actually exists before wiring `needs` +
		// `{{ steps.<id>.output }}` to it. A stale canonicalJSONRecipes entry
		// (e.g. a recipe step renamed) would otherwise silently produce a
		// canonical step pointing at nothing and fail only at run time —
		// panic here so the mistake surfaces at init/build instead.
		foundStep := false
		for _, s := range steps {
			if id, _ := s["id"].(string); id == stepID {
				foundStep = true
				break
			}
		}
		if !foundStep {
			panic("seeddata: routine " + Routines[i].Slug + " canonicalisation targets unknown step id " + stepID)
		}
		Routines[i].Definition["steps"] = append(steps, map[string]interface{}{
			"id":    "canonical",
			"type":  "transform",
			"needs": []string{stepID},
			"transform": map[string]interface{}{
				"input":      "{{ steps." + stepID + ".output }}",
				"expression": "@json",
			},
		})
	}
}
