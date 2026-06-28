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
// runner. The deterministic `runtime: expr` runner IS wired
// (internal/pipeline/runner_code_expr.go) — agentless probes using it
// run fine, so they must NOT warn. Other runtimes (bash/python/go)
// have no sandbox wired: such a step saves successfully but every
// invocation fails at runtime, so we surface that at apply time rather
// than via a failed cron run at 03:00.
//
// Add new advisory rules here as more "saves cleanly but fails at
// runtime" gaps surface; keep the rules narrow so the warning channel
// stays signal-rich.
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
			"routine %q: step %q is type: code with runtime %q, which has no wired runner — invocations will fail until it is converted to type: agent_run with a shell-tool-enabled agent, or to runtime: expr for agentless probes (see docs/manifest/routine.md `Code-step limitation`)",
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
