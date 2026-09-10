package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// runCrewshipStep dispatches one `crewship` verb through the CrewshipActions
// seam. The step's output is the internal route's JSON response, so a later
// step can transform it (`.identifier`) and act on what was created.
//
// Dry-run does not dispatch. A `crewship` step is a WRITE against real rows —
// an issue, an assignment, an escalation — and a preview that creates them is
// not a preview. It reports what it would have done instead, matching how
// dry_run treats every other side-effecting kind.
func (e *Executor) runCrewshipStep(ctx context.Context, step Step, render RenderContext, in RunInput, runID string) (string, float64, int64, error) {
	stepStart := time.Now()

	// Save-time validation rejects both of these; this is the belt for a
	// definition that reached the executor another way (a hand-edited row, a
	// draft run, an older build's saved DSL).
	if err := ValidateCrewshipAction(step.ID, step.Action); err != nil {
		return "", 0, 0, err
	}
	if err := ValidateCrewshipArgs(step.ID, step.Action, step.Args); err != nil {
		return "", 0, 0, err
	}

	args := renderCrewshipArgs(step.Args, render)

	if in.Mode == ModeDryRun {
		return fmt.Sprintf("<dry-run: would call %s>", step.Action), 0,
			time.Since(stepStart).Milliseconds(), nil
	}
	// Page payloads are JSON objects, not text. Preserve a whole-object template
	// from a collector without changing string interpolation for other verbs.
	if step.Action == "page.write" {
		if raw, ok := step.Args["data"].(string); ok {
			source := strings.TrimSpace(raw)
			match := templateRE.FindStringIndex(source)
			if match != nil && match[0] == 0 && match[1] == len(source) {
				rendered, _ := args["data"].(string)
				var object map[string]any
				if err := json.Unmarshal([]byte(rendered), &object); err != nil || object == nil {
					return "", 0, time.Since(stepStart).Milliseconds(), fmt.Errorf("crewship step %q: page.write data template must resolve to a JSON object", step.ID)
				}
				args["data"] = object
			}
		}
	}

	if e.crewship == nil {
		return "", 0, 0, fmt.Errorf("crewship step %q: no CrewshipActions wired on this executor "+
			"(production wiring is ExecutorDeps.Crewship in cmd_start.go)", step.ID)
	}

	out, err := e.crewship.Do(ctx, CrewshipRequest{
		Verb:        step.Action,
		Args:        args,
		WorkspaceID: in.WorkspaceID,
		// The AUTHOR crew, not the invoker: a routine acts as the crew that
		// owns it, so that crew's autonomy level is what bounds the action
		// and that crew is what the created row is attributed to. Same rule
		// buildNestedRunInput applies to a nested routine's identity.
		CrewID:  in.AuthorCrewID,
		AgentID: in.AuthorAgentID,
		RunID:   runID,
		// Anything this verb creates belongs to THIS chain. The internal
		// routes record author_run_id, so an automation reacting to the
		// resulting event resolves this run and inherits depth+1 rather than
		// starting a fresh budget — which is what would make the cap
		// unenforceable across the journal hop.
		ChainDepth: in.ChainDepth,
	})
	if err != nil {
		return "", 0, 0, fmt.Errorf("crewship step %q (%s): %w", step.ID, step.Action, err)
	}
	return out, 0, time.Since(stepStart).Milliseconds(), nil
}

// renderCrewshipArgs template-renders string values (including strings nested
// inside maps and slices) against the run's render context, leaving non-string
// values verbatim. Same treatment call_pipeline gives nested inputs, extended
// through containers because an issue's `labels` is a list of strings and a
// relation's payload is an object.
func renderCrewshipArgs(args map[string]any, render RenderContext) map[string]any {
	if len(args) == 0 {
		return map[string]any{}
	}
	out := make(map[string]any, len(args))
	for k, v := range args {
		out[k] = renderCrewshipValue(v, render)
	}
	return out
}

func renderCrewshipValue(v any, render RenderContext) any {
	switch t := v.(type) {
	case string:
		return Render(t, render)
	case map[string]any:
		m := make(map[string]any, len(t))
		for k, inner := range t {
			m[k] = renderCrewshipValue(inner, render)
		}
		return m
	case []any:
		s := make([]any, len(t))
		for i, inner := range t {
			s[i] = renderCrewshipValue(inner, render)
		}
		return s
	default:
		return v
	}
}
