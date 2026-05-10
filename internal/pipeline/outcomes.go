package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// graderResult is what runOutcomesGrader returns to the executor.
// passed reflects ALL criteria; feedback is human-readable text the
// executor can append to the worker's next attempt or surface to the
// user when the rubric ultimately fails.
type graderResult struct {
	passed   bool
	feedback string
	// perCriterion lets future surfaces (UI, journal, eval analytics)
	// show which specific rubric items failed. The executor today
	// only uses the aggregate passed bool; we capture the detail here
	// so it's already plumbed when the UI lands.
	perCriterion map[string]bool
}

// runOutcomesGrader invokes the configured grader agent against the
// worker's output, evaluates the rubric, and returns a pass/fail +
// feedback bundle.
//
// Mechanics: we build a structured prompt that lists each criterion
// with its name + rule, hands the worker's output as the artifact
// to evaluate, and asks the grader to return a strict JSON verdict.
// The grader runs through the same AgentRunner the worker did, so
// it inherits all the same security boundaries (no API keys, CLI
// adapter auth, container isolation per step).
//
// Why a separate file: outcomes is a meaningful subsystem (one
// CodeRabbit-flagged feature parity claim against Anthropic Managed
// Agents) and the prompt-construction + verdict-parsing logic is
// big enough to deserve its own scope. Keeps executor.go focused on
// step dispatch + tier escalation.
func (e *Executor) runOutcomesGrader(ctx context.Context, step Step, workerOutput string, in RunInput) (graderResult, float64, error) {
	if step.Outcomes == nil || len(step.Outcomes.Criteria) == 0 {
		// Nothing to grade. The caller's outcomes != nil check
		// should prevent this, but defensive return saves a panic
		// if the executor flow drifts.
		return graderResult{passed: true}, 0, nil
	}

	prompt := buildGraderPrompt(step, workerOutput, in.Inputs)

	// Build a synthetic AgentStepRequest pointing at the grader
	// agent slug. We DON'T inherit step.Complexity / ModelOverride
	// — the grader runs at "fast" tier by default. Rubric grading
	// is a structured-output task; reasoning depth doesn't help and
	// the cost should stay below the worker's. Custom override via
	// the DSL would be a Phase 2 enhancement.
	graderReq := AgentStepRequest{
		WorkspaceID:  in.WorkspaceID,
		AuthorCrewID: in.AuthorCrewID,
		AgentSlug:    step.Outcomes.GraderAgentSlug,
		// Tier resolution: outcomes-step.go does its own resolution
		// rather than re-using the worker's primary tier. The
		// grader is its own role; treat it like a step with
		// complexity=fast.
		Adapter:         "", // resolver fills in
		Model:           "", // resolver fills in
		Prompt:          prompt,
		TimeoutSec:      120, // grading is short by design — long timeouts hide a stuck grader
		PipelineID:      step.ID + ":grader",
		StepID:          step.ID + ":grader",
		InvokingCrewID:  in.InvokingCrewID,
		InvokingAgentID: in.InvokingAgentID,
	}

	// Resolve grader's tier ourselves so the worker's escalation
	// chain doesn't pollute the grader's run. Outcomes steps are
	// "fast"-tier by default; if a workspace's "fast" maps to
	// Ollama, the grader still runs locally without any code
	// change here.
	primary, _, err := e.resolver.Resolve(ctx, in.WorkspaceID, Step{Complexity: ComplexityFast})
	if err == nil {
		graderReq.Adapter = primary.Adapter
		graderReq.Model = primary.Model
	}

	res, err := e.runner.RunStep(ctx, graderReq)
	if err != nil {
		return graderResult{}, 0, fmt.Errorf("grader run: %w", err)
	}

	verdict, parseErr := parseGraderVerdict(res.Output, step.Outcomes.Criteria)
	if parseErr != nil {
		// Grader didn't return parsable JSON. Treat as
		// infrastructure failure (caller logs + returns worker
		// output) rather than fail-loud — a flaky grader
		// shouldn't gate every output if the schema slips.
		return graderResult{}, res.CostUSD, fmt.Errorf("parse grader verdict: %w (raw: %s)", parseErr, truncateForGraderLog(res.Output))
	}
	return verdict, res.CostUSD, nil
}

// buildGraderPrompt produces the structured prompt the grader agent
// reads. Format matches what most LLMs handle reliably for
// structured-verdict tasks: explicit XML-ish boundary markers
// around the artifact + JSON schema for the response.
//
// We deliberately avoid free-form natural language — Anthropic's
// Outcomes feature uses a constrained JSON-out approach for the
// same reason (parse reliability). The grader reads named criteria,
// emits per-criterion booleans + a single feedback paragraph.
//
// `runInputs` is the run's input map. Criteria like "preserves the
// meaning of the original input" need access to that input to make
// any judgement at all; without it the grader correctly refuses to
// rate. Marshalled into the prompt under <run_inputs> so the rubric
// can reference fields by name (e.g. inputs.text) the same way the
// step prompt did.
func buildGraderPrompt(step Step, workerOutput string, runInputs map[string]any) string {
	var b strings.Builder
	b.WriteString("You are an output grader. Evaluate the artifact below against the listed criteria.\n\n")
	b.WriteString("Return STRICT JSON in this exact shape (no markdown, no prose outside the JSON):\n")
	b.WriteString(`{"passed":bool,"per_criterion":{"<name>":bool,...},"feedback":"<one paragraph, concrete, actionable>"}` + "\n\n")
	b.WriteString("Rules:\n")
	b.WriteString("- `passed` is true ONLY when every criterion in `per_criterion` is true.\n")
	b.WriteString("- Each criterion in `per_criterion` MUST appear by its exact name from the rubric below.\n")
	b.WriteString("- `feedback` is a single paragraph (1-3 sentences) explaining what to fix when `passed` is false. When `passed` is true, summarize what made the artifact good.\n\n")

	b.WriteString("Rubric:\n")
	for _, c := range step.Outcomes.Criteria {
		fmt.Fprintf(&b, "  - name: %s\n    rule: %s\n", c.Name, c.Rule)
		if c.Description != "" {
			fmt.Fprintf(&b, "    description: %s\n", c.Description)
		}
	}

	if len(runInputs) > 0 {
		// Marshal errors here would be exotic (the same map already
		// round-tripped through inputs_json). Defensive fallback to
		// an empty block keeps the rest of the prompt usable.
		if blob, err := json.MarshalIndent(runInputs, "", "  "); err == nil {
			// Spell out the relationship explicitly. Earlier wording
			// ("the original payload") had graders refusing to evaluate
			// criteria like "preserves the original meaning" or "every
			// claim appears verbatim in the context" because they
			// didn't realize <run_inputs> WAS the source of those
			// reference values. Naming the common patterns up front
			// (input / original / context / source) covers the rubrics
			// that actually appear in routines today.
			b.WriteString("\nRun inputs — REFERENCE these when evaluating criteria. Anything the rubric calls\n")
			b.WriteString("\"the input\", \"the original\", \"the context\", \"the source\", \"the source document\",\n")
			b.WriteString("or names a specific input field (e.g. inputs.text, inputs.context) lives here:\n")
			b.WriteString("<run_inputs>\n")
			b.Write(blob)
			b.WriteString("\n</run_inputs>\n")
		}
	}

	b.WriteString("\nArtifact to grade:\n<artifact>\n")
	b.WriteString(workerOutput)
	b.WriteString("\n</artifact>\n\nNow return the JSON verdict.\n")
	return b.String()
}

// parseGraderVerdict extracts the structured verdict from the
// grader's output. We accept loose framing (some models like to
// wrap JSON in ```json ... ```) and surface a hard error only when
// no parseable JSON object is found at all. Missing per-criterion
// entries are treated as failures — a grader that quietly skips a
// rule should be treated as if that rule didn't pass.
func parseGraderVerdict(raw string, criteria []OutcomeCriterion) (graderResult, error) {
	jsonBlob := extractJSONBlob(raw)
	if jsonBlob == "" {
		return graderResult{}, fmt.Errorf("no JSON object found in grader output")
	}
	var parsed struct {
		Passed       bool            `json:"passed"`
		PerCriterion map[string]bool `json:"per_criterion"`
		Feedback     string          `json:"feedback"`
	}
	if err := json.Unmarshal([]byte(jsonBlob), &parsed); err != nil {
		return graderResult{}, fmt.Errorf("unmarshal: %w", err)
	}

	out := graderResult{
		feedback:     parsed.Feedback,
		perCriterion: parsed.PerCriterion,
	}
	if out.perCriterion == nil {
		out.perCriterion = map[string]bool{}
	}

	// Trust per_criterion as the source of truth, not parsed.Passed.
	// A grader that returns passed=true but has any criterion=false
	// is contradicting itself — we resolve in the safer direction
	// (treat as fail) and fold the missed criteria into feedback.
	allPass := true
	var missing []string
	for _, c := range criteria {
		v, ok := out.perCriterion[c.Name]
		if !ok {
			allPass = false
			missing = append(missing, c.Name)
			out.perCriterion[c.Name] = false
			continue
		}
		if !v {
			allPass = false
		}
	}
	out.passed = allPass

	if len(missing) > 0 && out.feedback == "" {
		out.feedback = fmt.Sprintf("grader did not return verdicts for criteria: %s", strings.Join(missing, ", "))
	}
	return out, nil
}

// extractJSONBlob pulls the first balanced { ... } from raw text.
// Handles graders that wrap JSON in ```json fences or include a
// preamble. We don't try to repair malformed JSON — if the brace
// balance is off, parseGraderVerdict's Unmarshal will surface the
// error to the caller.
func extractJSONBlob(raw string) string {
	// Common case: pure JSON.
	trimmed := strings.TrimSpace(raw)
	if strings.HasPrefix(trimmed, "{") && strings.HasSuffix(trimmed, "}") {
		return trimmed
	}
	// Markdown fences: ```json\n{...}\n```.
	if i := strings.Index(raw, "```"); i >= 0 {
		fenced := raw[i+3:]
		// strip optional language tag (json) on first line
		if nl := strings.IndexByte(fenced, '\n'); nl >= 0 {
			fenced = fenced[nl+1:]
		}
		if end := strings.LastIndex(fenced, "```"); end >= 0 {
			candidate := strings.TrimSpace(fenced[:end])
			if strings.HasPrefix(candidate, "{") && strings.HasSuffix(candidate, "}") {
				return candidate
			}
		}
	}
	// Last resort: walk for the first '{', track brace depth, return
	// when we close it. Doesn't handle braces inside strings
	// perfectly, but verdict JSON shouldn't contain them.
	start := strings.IndexByte(raw, '{')
	if start < 0 {
		return ""
	}
	depth := 0
	for i := start; i < len(raw); i++ {
		switch raw[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return raw[start : i+1]
			}
		}
	}
	return ""
}

// truncateForGraderLog is a small wrapper around the journal's
// preview cap so error messages don't dump 50KB of grader output
// into a journal entry. Mirrors truncateForPreview but is
// intentionally separate — grader logs and journal previews can
// diverge in cap size as we tune.
func truncateForGraderLog(s string) string {
	const cap = 300
	if len(s) <= cap {
		return s
	}
	cut := cap
	for cut > 0 && cut > cap-4 && (s[cut]&0xc0) == 0x80 {
		cut--
	}
	return s[:cut] + "...(truncated)"
}
