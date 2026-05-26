package manifest

import (
	"fmt"

	"github.com/crewship-ai/crewship/internal/manifest/kinds"
)

// routinePlanWarnings returns non-fatal advisory lines for a routine
// document at plan time. Used by planNewKinds to populate
// Plan.Warnings so the CLI can print them before apply exits.
//
// Today this only catches `type: code` steps — the production
// CodeRunner is not yet wired (see internal/pipeline/runner_code.go),
// so a routine that depends on a code step will save successfully but
// every invocation fails at the code step. Surfacing that gap at
// apply time means the operator doesn't learn about it via a failed
// cron run at 03:00.
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
		if step.Type == "code" {
			out = append(out, fmt.Sprintf(
				"routine %q: step %q is type: code, but the production CodeRunner is not yet wired — invocations will fail until the step is converted to type: agent_run with a shell-tool-enabled agent (see docs/manifest/routine.md `Code-step limitation`)",
				doc.Metadata.Slug, step.ID,
			))
		}
	}
	return out
}
