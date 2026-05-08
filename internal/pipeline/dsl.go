package pipeline

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// SupportedDSLVersion is the only DSL version this build understands.
// Pipelines saved with a different version are rejected at parse
// time. When we ship a v2 schema we add it here and gate forward-
// compat behaviour from the version string — never from heuristics.
const SupportedDSLVersion = "1.0"

// MaxNestedPipelineDepth caps recursion through call_pipeline. The
// cycle-detection at save time prevents A→B→A loops, but a user
// could still chain A→B→C→D→...→Z legally. We cap at 10 to bound
// stack growth and per-run runtime. The executor enforces this at
// runtime; the parser only flags depths above the cap if the chain
// is statically resolvable (which in MVP it never fully is, since
// pipelines reference each other by slug at runtime).
const MaxNestedPipelineDepth = 10

// slugRE is the strict slug allowlist. Same shape as agent / crew
// slugs elsewhere in the codebase: lowercase kebab-case, 1–64 chars,
// must start with a letter or digit.
var slugRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// stepIDRE allows the same shape as slug, plus underscores. Step IDs
// are intra-pipeline references, never persisted as a route segment,
// so we can be a touch more permissive without security cost.
var stepIDRE = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)

// templateRE captures a single {{ ... }} placeholder. The body is
// trimmed of surrounding whitespace before resolution. We intentionally
// do NOT support nested braces, function calls, or arithmetic — the
// resolver walks a small allow-list of paths and fails on anything
// else. This keeps the template language a substitution, not an
// evaluator, and rules out a whole class of injection attacks.
var templateRE = regexp.MustCompile(`\{\{\s*([^{}]+?)\s*\}\}`)

// Parse decodes a raw DSL JSON document into the typed in-memory
// representation. It does NOT do semantic validation (see Validate)
// or template resolution (see Render) — keeping them separate makes
// the failure modes legible at each layer.
//
// The function leaves Step.Raw populated with the original step body
// so downstream code (e.g. forward-compat tooling) can re-decode for
// step types this build doesn't yet recognise.
func Parse(data []byte) (*DSL, error) {
	var dsl DSL
	if err := json.Unmarshal(data, &dsl); err != nil {
		return nil, fmt.Errorf("pipeline: parse DSL: %w", err)
	}
	// Walk the raw JSON so we can stash each step's source body in
	// Step.Raw; encoding/json drops fields we don't recognise.
	var raw struct {
		Steps []json.RawMessage `json:"steps"`
	}
	if err := json.Unmarshal(data, &raw); err == nil {
		for i := range dsl.Steps {
			if i < len(raw.Steps) {
				dsl.Steps[i].Raw = raw.Steps[i]
			}
		}
	}
	return &dsl, nil
}

// Validate runs every static check we can do without invoking
// agents — schema/version, slug shape, step shape, reference
// resolution within the document, cycle detection.
//
// agentSlugs, if non-nil, is the set of agent_slug values that exist
// in the author crew. The validator rejects any step that references
// a slug outside this set. Pass nil to skip the cross-reference
// check (used by the parser-only test path); production save handlers
// always pass it in.
//
// pipelineSlugs, if non-nil, is the set of pipeline slugs already
// registered in the workspace. call_pipeline steps that reference
// outside this set are flagged as warnings, not errors — the target
// might be authored later in the same session.
func Validate(dsl *DSL, agentSlugs map[string]struct{}, pipelineSlugs map[string]struct{}) error {
	if dsl == nil {
		return errors.New("pipeline: nil DSL")
	}
	if dsl.DSLVersion != "" && dsl.DSLVersion != SupportedDSLVersion {
		return fmt.Errorf("pipeline: unsupported DSL version %q (this build understands %q)", dsl.DSLVersion, SupportedDSLVersion)
	}
	if dsl.Name == "" {
		return errors.New("pipeline: name required")
	}
	if !slugRE.MatchString(dsl.Name) {
		return fmt.Errorf("pipeline: name %q must be lowercase kebab-case (1–64 chars, a-z 0-9 - _)", dsl.Name)
	}
	if len(dsl.Steps) == 0 {
		return errors.New("pipeline: at least one step required")
	}

	// Step IDs must be unique and well-formed.
	seenStepIDs := make(map[string]struct{}, len(dsl.Steps))
	for i, st := range dsl.Steps {
		if st.ID == "" {
			return fmt.Errorf("pipeline: step %d missing id", i)
		}
		if !stepIDRE.MatchString(st.ID) {
			return fmt.Errorf("pipeline: step %d id %q invalid", i, st.ID)
		}
		if _, dup := seenStepIDs[st.ID]; dup {
			return fmt.Errorf("pipeline: duplicate step id %q", st.ID)
		}
		seenStepIDs[st.ID] = struct{}{}

		// Step type-specific validation.
		switch st.Type {
		case StepAgentRun:
			if st.AgentSlug == "" {
				return fmt.Errorf("pipeline: step %q (agent_run) missing agent_slug", st.ID)
			}
			if !slugRE.MatchString(st.AgentSlug) {
				return fmt.Errorf("pipeline: step %q agent_slug %q invalid shape", st.ID, st.AgentSlug)
			}
			if st.Prompt == "" {
				return fmt.Errorf("pipeline: step %q (agent_run) missing prompt", st.ID)
			}
			if agentSlugs != nil {
				if _, ok := agentSlugs[st.AgentSlug]; !ok {
					return fmt.Errorf("pipeline: step %q references unknown agent_slug %q", st.ID, st.AgentSlug)
				}
			}
		case StepCallPipeline:
			if st.PipelineSlug == "" {
				return fmt.Errorf("pipeline: step %q (call_pipeline) missing pipeline_slug", st.ID)
			}
			if !slugRE.MatchString(st.PipelineSlug) {
				return fmt.Errorf("pipeline: step %q pipeline_slug %q invalid shape", st.ID, st.PipelineSlug)
			}
			if st.PipelineSlug == dsl.Name {
				return fmt.Errorf("pipeline: step %q calls itself (%q) — direct self-recursion not allowed", st.ID, st.PipelineSlug)
			}
		case StepHTTP:
			if st.HTTP == nil {
				return fmt.Errorf("pipeline: step %q (http) missing http body", st.ID)
			}
			if st.HTTP.Method == "" {
				return fmt.Errorf("pipeline: step %q (http) missing method", st.ID)
			}
			switch strings.ToUpper(st.HTTP.Method) {
			case "GET", "POST", "PUT", "PATCH", "DELETE":
				// ok
			default:
				return fmt.Errorf("pipeline: step %q (http) method %q invalid (allowed: GET POST PUT PATCH DELETE)", st.ID, st.HTTP.Method)
			}
			if st.HTTP.URL == "" {
				return fmt.Errorf("pipeline: step %q (http) missing url", st.ID)
			}
			if st.HTTP.MaxResponseBytes < 0 {
				return fmt.Errorf("pipeline: step %q (http) max_response_bytes cannot be negative", st.ID)
			}
			if st.HTTP.MaxResponseBytes > 50_000_000 {
				return fmt.Errorf("pipeline: step %q (http) max_response_bytes too high (>50MB) — use code step for large payloads", st.ID)
			}
			if st.HTTP.CredentialRef != nil {
				if st.HTTP.CredentialRef.Type == "" {
					return fmt.Errorf("pipeline: step %q (http) credential_ref missing type", st.ID)
				}
				switch st.HTTP.CredentialRef.InjectAs {
				case "", "bearer", "header", "query":
					// ok
				default:
					return fmt.Errorf("pipeline: step %q (http) credential_ref.inject_as %q invalid (allowed: bearer header query)", st.ID, st.HTTP.CredentialRef.InjectAs)
				}
				if st.HTTP.CredentialRef.InjectAs == "header" && st.HTTP.CredentialRef.HeaderName == "" {
					return fmt.Errorf("pipeline: step %q (http) credential_ref inject_as=header requires header_name", st.ID)
				}
				if st.HTTP.CredentialRef.InjectAs == "query" && st.HTTP.CredentialRef.QueryName == "" {
					return fmt.Errorf("pipeline: step %q (http) credential_ref inject_as=query requires query_name", st.ID)
				}
			}
		case StepCode:
			if st.Code == nil {
				return fmt.Errorf("pipeline: step %q (code) missing code body", st.ID)
			}
			switch st.Code.Runtime {
			case "python", "go", "bash":
				// ok
			default:
				return fmt.Errorf("pipeline: step %q (code) runtime %q invalid (allowed: python go bash)", st.ID, st.Code.Runtime)
			}
			if st.Code.Code == "" {
				return fmt.Errorf("pipeline: step %q (code) missing code", st.ID)
			}
			if len(st.Code.Code) > 1_000_000 {
				return fmt.Errorf("pipeline: step %q (code) script >1MB — externalize via skills/files instead", st.ID)
			}
		case StepWait:
			if st.Wait == nil {
				return fmt.Errorf("pipeline: step %q (wait) missing wait body", st.ID)
			}
			switch st.Wait.Kind {
			case "approval":
				if st.Wait.ApprovalPrompt == "" {
					return fmt.Errorf("pipeline: step %q (wait approval) missing approval_prompt", st.ID)
				}
			case "datetime":
				if st.Wait.Until == "" {
					return fmt.Errorf("pipeline: step %q (wait datetime) missing until", st.ID)
				}
			case "event":
				if st.Wait.EventType == "" {
					return fmt.Errorf("pipeline: step %q (wait event) missing event_type", st.ID)
				}
			default:
				return fmt.Errorf("pipeline: step %q (wait) kind %q invalid (allowed: approval datetime event)", st.ID, st.Wait.Kind)
			}
		case StepTransform:
			if st.Transform == nil {
				return fmt.Errorf("pipeline: step %q (transform) missing transform body", st.ID)
			}
			if st.Transform.Input == "" {
				return fmt.Errorf("pipeline: step %q (transform) missing input", st.ID)
			}
			if st.Transform.Expression == "" {
				return fmt.Errorf("pipeline: step %q (transform) missing expression", st.ID)
			}
		default:
			return fmt.Errorf("pipeline: step %q has unsupported type %q (allowed: agent_run, call_pipeline, http, code, wait, transform)", st.ID, st.Type)
		}

		// Complexity is optional but if set must be one of the four
		// known tiers. Unknown values would silently fall back to
		// moderate; making it an error is safer.
		switch st.Complexity {
		case "", ComplexityTrivial, ComplexityFast, ComplexityModerate, ComplexitySmart:
			// ok
		default:
			return fmt.Errorf("pipeline: step %q complexity %q invalid (allowed: trivial|fast|moderate|smart)", st.ID, st.Complexity)
		}

		switch st.OnFail {
		case "", OnFailEscalateTier, OnFailAbort, OnFailRetryStep:
			// ok
		default:
			return fmt.Errorf("pipeline: step %q on_fail %q invalid (allowed: escalate_tier|abort|retry_step)", st.ID, st.OnFail)
		}

		// Outcomes (rubric-based grading) is only meaningful on
		// agent_run steps — call_pipeline already runs through the
		// nested pipeline's own validation/outcomes. Reject early
		// so authors don't think rubrics will magically apply to
		// nested runs.
		if st.Outcomes != nil {
			if st.Type != StepAgentRun {
				return fmt.Errorf("pipeline: step %q outcomes are only supported on agent_run steps (got %q)", st.ID, st.Type)
			}
			if st.Outcomes.GraderAgentSlug == "" {
				return fmt.Errorf("pipeline: step %q outcomes missing grader_agent_slug", st.ID)
			}
			if !slugRE.MatchString(st.Outcomes.GraderAgentSlug) {
				return fmt.Errorf("pipeline: step %q outcomes.grader_agent_slug %q invalid shape", st.ID, st.Outcomes.GraderAgentSlug)
			}
			if agentSlugs != nil {
				if _, ok := agentSlugs[st.Outcomes.GraderAgentSlug]; !ok {
					return fmt.Errorf("pipeline: step %q outcomes.grader_agent_slug %q not found in author crew", st.ID, st.Outcomes.GraderAgentSlug)
				}
			}
			if len(st.Outcomes.Criteria) == 0 {
				return fmt.Errorf("pipeline: step %q outcomes.criteria empty (rubric needs at least one rule)", st.ID)
			}
			if len(st.Outcomes.Criteria) > 20 {
				return fmt.Errorf("pipeline: step %q outcomes.criteria too long (max 20; got %d) — long rubrics produce noisy grader output", st.ID, len(st.Outcomes.Criteria))
			}
			seenCriteriaNames := make(map[string]struct{}, len(st.Outcomes.Criteria))
			for i, c := range st.Outcomes.Criteria {
				if c.Name == "" {
					return fmt.Errorf("pipeline: step %q outcomes.criteria[%d] missing name", st.ID, i)
				}
				if c.Rule == "" {
					return fmt.Errorf("pipeline: step %q outcomes.criteria[%d] (%q) missing rule", st.ID, i, c.Name)
				}
				if _, dup := seenCriteriaNames[c.Name]; dup {
					return fmt.Errorf("pipeline: step %q outcomes.criteria duplicate name %q", st.ID, c.Name)
				}
				seenCriteriaNames[c.Name] = struct{}{}
			}
			if st.Outcomes.MaxIterations < 0 {
				return fmt.Errorf("pipeline: step %q outcomes.max_iterations cannot be negative", st.ID)
			}
			if st.Outcomes.MaxIterations > 10 {
				return fmt.Errorf("pipeline: step %q outcomes.max_iterations too high (max 10)", st.ID)
			}
			switch st.Outcomes.OnFail {
			case "", OnFailEscalateTier, OnFailAbort, OnFailRetryStep:
				// ok
			default:
				return fmt.Errorf("pipeline: step %q outcomes.on_fail %q invalid", st.ID, st.Outcomes.OnFail)
			}
		}
	}

	// Template references must point at known input names or earlier
	// step IDs. We catch this here instead of at Render time so the
	// author gets the error at save (with a useful message) rather
	// than only at first run.
	inputNames := make(map[string]struct{}, len(dsl.Inputs))
	for _, in := range dsl.Inputs {
		inputNames[in.Name] = struct{}{}
	}

	// "Earlier" determination: linear pipelines (no needs[] anywhere)
	// use source order — the historical, predictable behaviour. DAG
	// pipelines (any step has needs[]) compute a per-step set of
	// reachable predecessors via the needs graph, so a step that
	// references {{ steps.Y.output }} only needs Y in its transitive
	// `needs` chain, regardless of source position. Without this, a
	// DAG with `B (needs:[A])` placed BEFORE `A` in dsl.Steps would
	// reject at save with "forward template ref" — that's a false
	// negative for topologically-valid pipelines.
	hasAnyNeeds := false
	for _, st := range dsl.Steps {
		if len(st.Needs) > 0 {
			hasAnyNeeds = true
			break
		}
	}
	if hasAnyNeeds {
		// DAG mode: every Needs entry must reference a real step (and
		// the graph must be acyclic) before we trust the reachable
		// closure for template validation. Without this, a DSL with
		// `needs: ["ghost"]` plus `{{ steps.ghost.output }}` slips
		// past Save and only blows up at runtime in validateDAG.
		if err := validateDAG(dsl); err != nil {
			return fmt.Errorf("pipeline: %w", err)
		}
		stepByID := make(map[string]*Step, len(dsl.Steps))
		for i := range dsl.Steps {
			stepByID[dsl.Steps[i].ID] = &dsl.Steps[i]
		}
		for _, st := range dsl.Steps {
			reachable := make(map[string]struct{})
			collectReachableNeeds(st.ID, stepByID, reachable)
			if err := validateTemplatesInStep(st, inputNames, reachable); err != nil {
				return err
			}
		}
	} else {
		// Linear mode (preserve historical behaviour): source-order.
		earlierSteps := make(map[string]struct{}, len(dsl.Steps))
		for _, st := range dsl.Steps {
			if err := validateTemplatesInStep(st, inputNames, earlierSteps); err != nil {
				return err
			}
			earlierSteps[st.ID] = struct{}{}
		}
	}

	return nil
}

// collectReachableNeeds walks the `needs` chain from stepID and
// populates `out` with every transitive predecessor's ID. Used by
// the DAG-mode template validator so a step can reference any
// ancestor's output regardless of source position. Stop-loops are
// handled by the cycle detector at save_time, but we guard here too
// in case a malformed DSL gets validated outside the normal path.
func collectReachableNeeds(stepID string, stepByID map[string]*Step, out map[string]struct{}) {
	st, ok := stepByID[stepID]
	if !ok {
		return
	}
	for _, dep := range st.Needs {
		if _, exists := stepByID[dep]; !exists {
			continue
		}
		if _, seen := out[dep]; seen {
			continue
		}
		out[dep] = struct{}{}
		collectReachableNeeds(dep, stepByID, out)
	}
}

// validateTemplatesInStep checks every {{ ... }} placeholder across
// ALL template-bearing fields of the step. Each placeholder must
// resolve against either inputs.X (for any declared input) or
// steps.Y.output (for a Y that has executed before this step).
//
// The "earlier" set is supplied by the caller in `earlier`. For
// LINEAR pipelines (no needs[] anywhere) the caller passes a
// running set of "steps seen so far in source order" — preserves the
// pre-DAG validator behaviour. For DAG pipelines the caller passes
// the step's transitive `needs` closure so a step that's later in
// source order can still reference its declared predecessors. See
// the call site in Validate for the dispatch.
//
// Coverage extends beyond Prompt + NestedInputs to: If condition,
// HTTP (URL / Body / Headers), Wait (Until / EventFilter), Code
// (Code / Env values), Transform (Input / Expression). Without this
// breadth, a malformed template in (e.g.) HTTP.URL passes save and
// crashes the runtime — discovered at first invocation rather than
// at author time.
func validateTemplatesInStep(st Step, inputs, earlier map[string]struct{}) error {
	walk := func(s string) error {
		if s == "" {
			return nil
		}
		matches := templateRE.FindAllStringSubmatch(s, -1)
		for _, m := range matches {
			ref := strings.TrimSpace(m[1])
			if err := checkTemplateRef(ref, inputs, earlier); err != nil {
				return fmt.Errorf("pipeline: step %q: %w", st.ID, err)
			}
		}
		return nil
	}

	// agent_run prompt + nested inputs (recursive: NestedInputs can be
	// nested objects/arrays — call_pipeline forwards a structured map
	// of inputs to the child routine, and any string anywhere inside
	// can carry a {{ ... }} template). Only walking top-level string
	// values used to let bad templates inside nested objects pass save.
	if err := walk(st.Prompt); err != nil {
		return err
	}
	if err := walkNestedTemplates(st.NestedInputs, walk); err != nil {
		return err
	}

	// Conditional `if` expression
	if err := walk(st.If); err != nil {
		return err
	}

	// HTTP step fields
	if st.HTTP != nil {
		if err := walk(st.HTTP.URL); err != nil {
			return err
		}
		if err := walk(st.HTTP.Body); err != nil {
			return err
		}
		for _, v := range st.HTTP.Headers {
			if err := walk(v); err != nil {
				return err
			}
		}
	}

	// Wait step fields
	if st.Wait != nil {
		if err := walk(st.Wait.Until); err != nil {
			return err
		}
		if err := walk(st.Wait.EventFilter); err != nil {
			return err
		}
		if err := walk(st.Wait.ApprovalPrompt); err != nil {
			return err
		}
	}

	// Code step fields
	if st.Code != nil {
		if err := walk(st.Code.Code); err != nil {
			return err
		}
		for _, v := range st.Code.Env {
			if err := walk(v); err != nil {
				return err
			}
		}
	}

	// Transform step fields
	if st.Transform != nil {
		if err := walk(st.Transform.Input); err != nil {
			return err
		}
		if err := walk(st.Transform.Expression); err != nil {
			return err
		}
	}

	return nil
}

// checkTemplateRef validates a single template body like
// "inputs.since" or "steps.fetch.output" or "steps.fetch.output.path".
// Returns nil iff the reference resolves at this point in the pipeline.
func checkTemplateRef(ref string, inputs, earlier map[string]struct{}) error {
	parts := strings.SplitN(ref, ".", 3)
	if len(parts) < 2 {
		return fmt.Errorf("invalid template ref %q (expected inputs.X or steps.Y.output)", ref)
	}
	switch parts[0] {
	case "inputs":
		name := parts[1]
		if _, ok := inputs[name]; !ok {
			return fmt.Errorf("template ref %q points at unknown input %q", ref, name)
		}
		// inputs.X.something — JSON path into a structured input.
		// We don't validate the path itself, just allow it.
	case "steps":
		if len(parts) < 3 {
			return fmt.Errorf("invalid template ref %q (expected steps.Y.output)", ref)
		}
		stepID := parts[1]
		if _, ok := earlier[stepID]; !ok {
			return fmt.Errorf("template ref %q points at step %q which hasn't run yet at this point", ref, stepID)
		}
		// parts[2] = "output" or "output.path"; we don't enforce
		// shape here. The renderer will produce an empty string if
		// the path is missing; the executor's validation gate will
		// catch the resulting empty input as a downstream issue.
	case "env":
		// env.* allowlist enforced at render time, not parse time —
		// the allowed set may differ between dry-run and live run.
	default:
		return fmt.Errorf("template ref %q uses unknown namespace %q (allowed: inputs, steps, env)", ref, parts[0])
	}
	return nil
}

// CycleDetect reports an error if the call graph defined by call_pipeline
// steps in `dsl` plus the supplied `resolveTargets` reaches a cycle
// involving `dsl.Name`. The resolveTargets callback is given a
// pipeline_slug and returns the DSL of that pipeline (or nil + error
// if not found in the workspace). For MVP we only detect cycles that
// are reachable through already-saved pipelines plus `dsl` itself —
// the moment a referenced pipeline is unknown, we stop walking that
// branch (no false positives).
//
// Cycle detection runs at save time, not at run time: cycles are
// architectural errors, not transient runtime conditions, and catching
// them early gives the author a clean error message before any agent
// invocation.
func CycleDetect(dsl *DSL, resolveTargets func(slug string) (*DSL, error)) error {
	if dsl == nil {
		return nil
	}
	visited := make(map[string]bool)
	visiting := make(map[string]bool)

	var walk func(target *DSL) error
	walk = func(target *DSL) error {
		if target == nil {
			return nil
		}
		if visiting[target.Name] {
			return fmt.Errorf("pipeline: cycle detected — %q is reachable from itself via call_pipeline", target.Name)
		}
		if visited[target.Name] {
			return nil
		}
		visiting[target.Name] = true
		defer delete(visiting, target.Name)
		for _, st := range target.Steps {
			if st.Type != StepCallPipeline {
				continue
			}
			child, err := resolveTargets(st.PipelineSlug)
			if err != nil || child == nil {
				// Unknown target — skip, do not treat as cycle.
				continue
			}
			if err := walk(child); err != nil {
				return err
			}
		}
		visited[target.Name] = true
		return nil
	}
	return walk(dsl)
}

// Render performs template substitution on a single string against
// the supplied inputs and prior step outputs. Used by the executor
// just before each step is dispatched. Unknown references render as
// empty strings (the validator should already have caught most of
// these at save time; runtime fallback keeps the executor robust to
// drift between save-time validation and run-time data shapes).
func Render(s string, ctx RenderContext) string {
	return templateRE.ReplaceAllStringFunc(s, func(match string) string {
		body := strings.TrimSpace(match[2 : len(match)-2])
		val, ok := resolveRef(body, ctx)
		if !ok {
			return ""
		}
		return val
	})
}

// RenderContext carries the data a single render call needs. The
// executor builds a fresh one for every step (with the previous
// step's outputs accumulated in StepOutputs).
type RenderContext struct {
	Inputs      map[string]any
	StepOutputs map[string]string // step_id → output (raw string from agent)
	Env         map[string]string // safe env keys only — author_crew_name, run_id, etc.
}

// resolveRef walks one template body (already trimmed of {{ }}) against
// the render context. Returns the rendered string + ok=true on hit,
// or "" + false on miss / disallowed namespace.
func resolveRef(ref string, ctx RenderContext) (string, bool) {
	parts := strings.SplitN(ref, ".", 3)
	if len(parts) < 2 {
		return "", false
	}
	switch parts[0] {
	case "inputs":
		v, ok := ctx.Inputs[parts[1]]
		if !ok {
			return "", false
		}
		// inputs.X.path — best-effort JSON path traversal. For MVP
		// we only support one level of nesting via map[string]any.
		if len(parts) == 3 {
			if m, ok := v.(map[string]any); ok {
				if nested, ok := m[parts[2]]; ok {
					return stringify(nested), true
				}
				return "", false
			}
		}
		return stringify(v), true
	case "steps":
		if len(parts) < 3 {
			return "", false
		}
		out, ok := ctx.StepOutputs[parts[1]]
		if !ok {
			return "", false
		}
		// parts[2] starts with "output" possibly followed by ".path".
		if parts[2] == "output" {
			return out, true
		}
		// "output.json.path" — try to JSON-decode the output and
		// walk the path. Best-effort: failure returns "".
		if strings.HasPrefix(parts[2], "output.") {
			path := strings.TrimPrefix(parts[2], "output.")
			return jsonPath(out, path), true
		}
		return "", false
	case "env":
		if v, ok := ctx.Env[parts[1]]; ok {
			return v, true
		}
		return "", false
	}
	return "", false
}

// stringify converts an arbitrary input value to a string for template
// substitution. JSON encoding handles structured values; primitives
// pass through with %v formatting.
func stringify(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	if b, err := json.Marshal(v); err == nil {
		return string(b)
	}
	return fmt.Sprintf("%v", v)
}

// jsonPath does a single-level nested lookup into a JSON-encoded
// string output. Real path libraries are overkill for MVP — most
// pipelines reference {{ steps.X.output }} as a whole. When users
// need deeper paths we'll layer in tidwall/gjson behind this helper.
func jsonPath(raw, path string) string {
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return ""
	}
	for _, key := range strings.Split(path, ".") {
		m, ok := v.(map[string]any)
		if !ok {
			return ""
		}
		v, ok = m[key]
		if !ok {
			return ""
		}
	}
	return stringify(v)
}

// walkNestedTemplates recursively descends into a map / slice tree
// applying `walk` to every string value found. call_pipeline's
// NestedInputs is `map[string]any` whose values can themselves be
// objects, arrays, or strings; templates inside the deeper layers
// were skipped by the older flat string-only walk and crashed at
// runtime. This recursion mirrors what the renderer does when it
// substitutes — keeping save-time validation symmetric with run-time
// behaviour.
func walkNestedTemplates(v any, walk func(string) error) error {
	switch t := v.(type) {
	case nil:
		return nil
	case string:
		return walk(t)
	case map[string]any:
		for _, child := range t {
			if err := walkNestedTemplates(child, walk); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range t {
			if err := walkNestedTemplates(child, walk); err != nil {
				return err
			}
		}
	}
	// Other scalar types (int, float, bool) carry no templates — skip.
	return nil
}
