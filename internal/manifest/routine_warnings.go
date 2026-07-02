package manifest

import (
	"fmt"

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
