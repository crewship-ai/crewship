package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
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
func (e *Executor) runCrewshipStep(ctx context.Context, step Step, render RenderContext, in RunInput, runID string, depth int) (string, float64, int64, error) {
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

	req := CrewshipRequest{
		Verb:         step.Action,
		ChatOriginID: in.ChatOriginID,
		OperationKey: routineOperationKey(ctx, runID, step.ID),
		StepID:       step.ID,
		Args:         args,
		WorkspaceID:  in.WorkspaceID,
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
	}
	// On resume, a waitpoint this step already opened is the approval (or
	// pending question) for this exact call: pass it so an approved one is
	// honoured without asking twice.
	if finder, ok := e.waitpoints.(WaitpointResumer); ok && in.resume && runID != "" {
		if tok, ferr := finder.FindApprovalForStep(ctx, runID, step.ID); ferr == nil && tok != "" {
			req.ApprovalToken = tok
		}
	}
	out, err := e.crewship.Do(ctx, req)
	var held *CrewshipApprovalRequired
	if errors.As(err, &held) {
		return e.awaitCrewshipApproval(ctx, step, in, runID, depth, req, held, stepStart)
	}
	if err != nil {
		return "", 0, 0, fmt.Errorf("crewship step %q (%s): %w", step.ID, step.Action, err)
	}
	return out, 0, time.Since(stepStart).Milliseconds(), nil
}

// awaitCrewshipApproval turns a held autonomy decision into the same durable
// approval a `wait: approval` step uses: one inbox card naming the action,
// the run parks (top-level runs) or blocks (nested), and an approval repeats
// the call once with the waitpoint token and the SAME operation key, so the
// action executes exactly once. Denial, timeout and cancellation fail the
// step as what they are. The crew's autonomy level is never changed.
func (e *Executor) awaitCrewshipApproval(ctx context.Context, step Step, in RunInput, runID string, depth int, req CrewshipRequest, held *CrewshipApprovalRequired, stepStart time.Time) (string, float64, int64, error) {
	elapsed := func() int64 { return time.Since(stepStart).Milliseconds() }
	if e.waitpoints == nil || runID == "" {
		return "", 0, elapsed(), fmt.Errorf("crewship step %q: %w — no approval store is wired, so the routine cannot wait", step.ID, held)
	}
	token := req.ApprovalToken
	if token == "" {
		argsJSON, _ := json.Marshal(req.Args)
		created, err := e.waitpoints.CreateApproval(ctx, WaitpointApprovalRequest{
			WorkspaceID:    in.WorkspaceID,
			PipelineRunID:  runID,
			StepID:         step.ID,
			Title:          fmt.Sprintf("Approve %s from a routine", step.Action),
			Prompt:         fmt.Sprintf("The crew's autonomy level (%s) requires approval for %s. Step %q will run %s with arguments %s once approved.", held.Level, held.Action, step.ID, step.Action, truncateForPrompt(string(argsJSON), 1500)),
			InvokingCrewID: in.InvokingCrewID,
			TimeoutSec:     step.TimeoutSec,
		})
		if err != nil {
			return "", 0, elapsed(), fmt.Errorf("crewship step %q create approval: %w", step.ID, err)
		}
		token = created
	}
	if depth == 0 && in.Mode == ModeRun && e.runStore != nil && in.pipeline != nil {
		park := true
		if reader, ok := e.waitpoints.(WaitpointStatusReader); ok {
			if st, serr := reader.WaitpointStatus(ctx, token); serr == nil && st != "pending" {
				park = false
			}
		}
		if park {
			if err := e.runStore.MarkWaiting(ctx, runID, step.ID); err != nil {
				return "", 0, elapsed(), fmt.Errorf("crewship step %q mark waiting: %w", step.ID, err)
			}
			return "", 0, elapsed(), &suspendError{token: token, stepID: step.ID}
		}
	}
	approved, err := e.waitpoints.WaitFor(ctx, token)
	if ctx.Err() != nil {
		return "", 0, elapsed(), ctx.Err()
	}
	if err != nil {
		return "", 0, elapsed(), fmt.Errorf("crewship step %q wait for approval: %w", step.ID, err)
	}
	if !approved {
		if reader, ok := e.waitpoints.(WaitpointStatusReader); ok {
			if st, serr := reader.WaitpointStatus(ctx, token); serr == nil && (st == "timed_out" || st == "cancelled") {
				return "", 0, elapsed(), fmt.Errorf("crewship step %q (%s): approval %s", step.ID, step.Action, strings.ReplaceAll(st, "_", " "))
			}
		}
		return "", 0, elapsed(), fmt.Errorf("crewship step %q (%s): approval denied", step.ID, step.Action)
	}
	req.ApprovalToken = token
	out, err := e.crewship.Do(ctx, req)
	if err != nil {
		return "", 0, elapsed(), fmt.Errorf("crewship step %q (%s) after approval: %w", step.ID, step.Action, err)
	}
	return out, 0, elapsed(), nil
}

func truncateForPrompt(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
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

// The logical path distinguishes foreach items while retries retain a key.
func routineOperationKey(ctx context.Context, runID, stepID string) string {
	sum := sha256.Sum256([]byte(runID + "\x00" + executionIn(ctx).path + "\x00" + stepID))
	return fmt.Sprintf("routine:%x", sum)
}
