package manifest

import (
	"fmt"
	"sort"

	"github.com/crewship-ai/crewship/internal/manifest/kinds"
	"github.com/crewship-ai/crewship/internal/pipeline"
)

// routinePlanWarnings returns non-fatal advisory lines for a routine
// document at plan time. Used by planNewKinds to populate
// Plan.Warnings so the CLI can print them before apply exits.
//
// Today this catches `type: code` steps whose runtime has no wired
// runner. The deterministic `runtime: expr` and `runtime: cel` runners
// ARE wired (internal/pipeline/runner_code_expr.go,
// runner_code_multi.go) — agentless steps using them run fine, so they
// must NOT warn. Other runtimes (bash/python/go) have no sandbox
// wired: the server-side save/apply/test_run validator
// (internal/pipeline/dsl_validate_egress.go) already rejects such a
// step at author time, so this is a client-side heads-up surfaced at
// `crewship apply` plan time — it flags the doomed apply before the
// round-trip to the server, it doesn't describe a step that "saves
// then fails later."
//
// Add new advisory rules here as more author-time-rejected gaps
// surface; keep the rules narrow so the warning channel stays
// signal-rich.
func routinePlanWarnings(doc *kinds.RoutineDocument) []string {
	if doc == nil {
		return nil
	}
	var out []string

	// Keys the manifest passes through untouched but this build's DSL
	// has no field for. RoutineSpec.Rest exists so a newer server can
	// receive fields an older CLI never heard of — that forward
	// compatibility is deliberate and must not become an error. But it
	// also means a typo is no longer caught anywhere: `guardrail:` for
	// `guardrails:` now reaches the server, which discards it just as
	// quietly. Naming it here is the only moment anyone looks.
	//
	// Sorted, because warnings get diffed and pasted into issues and Go
	// map order would reshuffle them between two runs on one file.
	unknown := make([]string, 0, len(doc.Spec.Rest))
	for key := range doc.Spec.Rest {
		if !pipeline.IsKnownDSLKey(key) {
			unknown = append(unknown, key)
		}
	}
	sort.Strings(unknown)
	for _, key := range unknown {
		out = append(out, fmt.Sprintf(
			"routine %q: spec key %q is not a routine DSL field — it is sent as-is and the server ignores it (misspelled? see `crewship routine schema`)",
			doc.Metadata.Slug, key,
		))
	}

	for _, step := range doc.Spec.Steps {
		if step.Type != "code" {
			continue
		}
		// Wired runtimes (expr, cel) are deterministic + token-zero — no
		// warning. The pipeline package owns the canonical registry.
		if pipeline.IsWiredCodeRuntime(codeStepRuntime(step)) {
			continue
		}
		out = append(out, fmt.Sprintf(
			"routine %q: step %q is type: code with runtime %q, which has no wired runner — routines using it are rejected at save/apply/test_run; use runtime: expr or cel for agentless logic, or convert to type: agent_run with a shell-tool-enabled agent (see docs/manifest/routine.md `Code steps`)",
			doc.Metadata.Slug, step.ID, codeStepRuntime(step),
		))
	}
	return out
}

// codeStepRuntime extracts the runtime from a code step's catch-all Rest map
// (the manifest RoutineStep stores non-typed fields under Rest["code"]).
func codeStepRuntime(step kinds.RoutineStep) string {
	if c, ok := step.Rest["code"].(map[string]any); ok {
		if rt, ok := c["runtime"].(string); ok {
			return rt
		}
	}
	return ""
}
