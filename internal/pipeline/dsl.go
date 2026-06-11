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
//
// The body of this function is intentionally thin: each major check
// lives in a focused sibling file (dsl_validate_*.go) so adding a new
// step-shape rule, credential mode, or gate doesn't bloat the
// orchestrator.
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
	if err := validateAgentless(dsl); err != nil {
		return err
	}

	seenStepIDs := make(map[string]struct{}, len(dsl.Steps))
	for i, st := range dsl.Steps {
		if err := validateStepSlugs(i, st, dsl, agentSlugs, seenStepIDs); err != nil {
			return err
		}
		if err := validateStepEgress(st); err != nil {
			return err
		}
		if err := validateStepCredentials(st); err != nil {
			return err
		}
		if err := validateStepGates(st, agentSlugs); err != nil {
			return err
		}
	}

	return validateTemplates(dsl)
}

// validateTemplates resolves every {{ ... }} placeholder across the
// step graph against the inputs map and the step ordering. Source-
// order semantics in linear pipelines; transitive `needs` closure
// in DAG pipelines so a step can reference declared ancestors
// regardless of source position.
func validateTemplates(dsl *DSL) error {
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
		return nil
	}

	// Linear mode (preserve historical behaviour): source-order.
	earlierSteps := make(map[string]struct{}, len(dsl.Steps))
	for _, st := range dsl.Steps {
		if err := validateTemplatesInStep(st, inputNames, earlierSteps); err != nil {
			return err
		}
		earlierSteps[st.ID] = struct{}{}
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
//
// DecodeAgentJSON tolerates the same LLM quirks the schema gate does
// (markdown fence, prose preamble, trailing chatter) so a path lookup
// against an upstream agent output stays consistent with what the
// validator accepted.
func jsonPath(raw, path string) string {
	var v any
	if err := DecodeAgentJSON(raw, &v); err != nil {
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
