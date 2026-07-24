package pipeline

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/crewship-ai/crewship/internal/untrusted"
)

// SupportedDSLVersion is the only DSL version this build understands.
// Pipelines saved with a different version are rejected at parse
// time. When we ship a v2 schema we add it here and gate forward-
// compat behaviour from the version string — never from heuristics.
const SupportedDSLVersion = "1.0"

// MaxPipelineSteps bounds the number of top-level steps a definition may
// declare (#1416 item 4). The body-size cap on Save/Import/CreateSchedule/
// CreateWebhook bounds request BYTES; it doesn't bound step COUNT — a
// definition of many tiny steps could still pin memory/CPU (per-step
// render context, journal entries, DAG scheduling) well under the byte
// cap. 500 is generous for any legitimate routine (the DAG scheduler and
// per-step render/journal overhead start to dominate long before a
// hand-authored or agent-authored routine would need more).
const MaxPipelineSteps = 500

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
	// Normalize integration slugs in place (lowercase + trim) so the
	// parsed DSL carries the canonical form — the round-trip the run gate
	// and the GET response both rely on. Empties are deliberately KEPT so
	// Validate can reject a malformed (whitespace-only) entry rather than
	// silently dropping it.
	for i, s := range dsl.IntegrationsRequired {
		dsl.IntegrationsRequired[i] = strings.ToLower(strings.TrimSpace(s))
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

	// #1423 item 1: accumulate every static check failure into one
	// ValidationErrors instead of returning on the first, with a
	// JSON-pointer path per entry for editor jump-to. `errs` is the single
	// accumulator every check below feeds; `errs.add` flattens whatever
	// shape a sibling dsl_validate_*.go check returns (plain error,
	// *ValidationError, or ValidationErrors) into it.
	var errs ValidationErrors

	if dsl.DSLVersion != "" && dsl.DSLVersion != SupportedDSLVersion {
		errs.add("/dsl_version", fmt.Errorf("pipeline: unsupported DSL version %q (this build understands %q)", dsl.DSLVersion, SupportedDSLVersion))
	}
	if dsl.Name == "" {
		errs.add("/name", errors.New("pipeline: name required"))
	} else if !slugRE.MatchString(dsl.Name) {
		errs.add("/name", fmt.Errorf("pipeline: name %q must be lowercase kebab-case (1–64 chars, a-z 0-9 - _)", dsl.Name))
	}
	switch dsl.Parallelism {
	case "", ParallelismExplicit, ParallelismAuto, ParallelismOff:
	default:
		errs.add("/parallelism", fmt.Errorf("pipeline: parallelism %q invalid (allowed: explicit, auto, off)", dsl.Parallelism))
	}
	if len(dsl.Steps) == 0 {
		errs.add("/steps", errors.New("pipeline: at least one step required"))
	}
	// MaxPipelineSteps stays an early, first-class, single-error RETURN
	// (#1416 added this bound; #1423 item 1 explicitly keeps it this way):
	// a definition already past the step-count cap isn't safe to keep
	// walking — the per-step render/journal/DAG overhead the cap exists to
	// bound would apply to every subsequent check too — so this does NOT
	// fold into `errs`, and its message/behavior is unchanged from before
	// multi-error accumulation existed.
	if len(dsl.Steps) > MaxPipelineSteps {
		return fmt.Errorf("pipeline: %d steps exceeds the %d step limit", len(dsl.Steps), MaxPipelineSteps)
	}

	errs.add("/agentless", validateAgentless(dsl))
	errs.add("/integrations_required", validateIntegrationsRequired(dsl))
	errs.add("/resources", validateResources(dsl))

	seenStepIDs := make(map[string]struct{}, len(dsl.Steps))
	for i, st := range dsl.Steps {
		stepPath := fmt.Sprintf("/steps/%d", i)
		// validateStepSlugs returns *ValidationError directly (not error) so
		// this stays a plain nil check — passing it through errs.add would
		// hit the classic typed-nil-in-interface trap (err != nil even when
		// the returned *ValidationError itself is nil).
		if ve := validateStepSlugs(i, st, dsl, agentSlugs, seenStepIDs); ve != nil {
			errs = append(errs, ve)
		}
		errs.add(stepPath+"/"+stepBodyField(st.Type), validateStepEgress(st))
		errs.add(stepPath+"/http/credential_ref", validateStepCredentials(st))
		errs.add(stepPath+"/gates", validateStepGates(st, agentSlugs))
		errs.add(stepPath+"/validation", validateStepOutputGate(st))
		errs.add(stepPath+"/hooks", validateStepHooks(st))
		errs.add(stepPath+"/foreach", validateForeachStep(st, dsl, agentSlugs))
	}

	errs.add("/hooks", validateHooks(dsl))
	errs.add("/egress_targets", validateEgressTargets(dsl))
	errs.add("/concurrency_key", validateConcurrencyKey(dsl))
	errs.add("", validateTemplates(dsl))

	return errs.asErr()
}

// stepBodyField maps a step type to the DSL field its body-shape checks
// (validateStepEgress) live under, so Validate can attach a JSON-pointer
// path without validateStepEgress itself needing to become path-aware.
// Falls back to "type" for step kinds validated elsewhere (agent_run,
// call_pipeline — handled in validateStepSlugs) or an unrecognized type,
// where the error is about the type field itself.
func stepBodyField(t StepType) string {
	switch t {
	case StepHTTP:
		return "http"
	case StepCode:
		return "code"
	case StepWait:
		return "wait"
	case StepTransform:
		return "transform"
	case StepNotify:
		return "notify"
	case StepScript:
		return "script"
	default:
		return "type"
	}
}

// validateStepHooks checks a step's per-step before/after hooks (Wave
// 4.1) — same deterministic-side-channel restriction as routine hooks.
func validateStepHooks(st Step) error {
	if st.Hooks == nil {
		return nil
	}
	for name, hook := range map[string]*Step{"before": st.Hooks.Before, "after": st.Hooks.After} {
		if hook == nil {
			continue
		}
		switch hook.Type {
		case StepHTTP, StepCode, StepTransform:
		default:
			return fmt.Errorf("pipeline: step %q %s hook must be type code, http, or transform (got %q)", st.ID, name, hook.Type)
		}
		if err := validateStepEgress(*hook); err != nil {
			return fmt.Errorf("pipeline: step %q %s hook: %w", st.ID, name, err)
		}
	}
	return nil
}

// validateHooks checks routine-level lifecycle hooks (Wave 4.1). Hook
// steps must be deterministic side-channels — code | http | transform —
// so a hook can never recurse (call_pipeline), spend tokens (agent_run),
// or block the run (wait). Each present hook is validated with the same
// per-step shape checks as a normal step.
func validateHooks(dsl *DSL) error {
	if dsl.Hooks == nil {
		return nil
	}
	for name, hook := range map[string]*Step{
		"before_all": dsl.Hooks.BeforeAll,
		"after_all":  dsl.Hooks.AfterAll,
		"on_failure": dsl.Hooks.OnFailure,
	} {
		if hook == nil {
			continue
		}
		switch hook.Type {
		case StepHTTP, StepCode, StepTransform:
			// allowed deterministic side-channels
		default:
			return fmt.Errorf("pipeline: hook %q must be type code, http, or transform (got %q)", name, hook.Type)
		}
		if err := validateStepEgress(*hook); err != nil {
			return fmt.Errorf("pipeline: hook %q: %w", name, err)
		}
	}
	return nil
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

	var errs ValidationErrors

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
		//
		// This stays a single early return (not folded into `errs`):
		// an invalid graph has no well-defined reachable-set per step,
		// so every per-step template check below it would be
		// meaningless noise on top of the real problem.
		if err := validateDAG(dsl); err != nil {
			return fmt.Errorf("pipeline: %w", err)
		}
		stepByID := make(map[string]*Step, len(dsl.Steps))
		for i := range dsl.Steps {
			stepByID[dsl.Steps[i].ID] = &dsl.Steps[i]
		}
		for i, st := range dsl.Steps {
			reachable := make(map[string]struct{})
			collectReachableNeeds(st.ID, stepByID, reachable)
			validateTemplatesInStep(i, st, inputNames, reachable, &errs)
		}
		return errs.asErr()
	}

	// Linear mode (preserve historical behaviour): source-order.
	earlierSteps := make(map[string]struct{}, len(dsl.Steps))
	for i, st := range dsl.Steps {
		validateTemplatesInStep(i, st, inputNames, earlierSteps, &errs)
		earlierSteps[st.ID] = struct{}{}
	}
	return errs.asErr()
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
//
// #1423 item 1: every bad template ref in the step is appended to `errs`
// with a JSON-pointer path (rather than returning on the first), so a step
// with three typo'd refs across prompt/http/code reports all three in one
// validate pass.
func validateTemplatesInStep(i int, st Step, inputs, earlier map[string]struct{}, errs *ValidationErrors) {
	base := fmt.Sprintf("/steps/%d", i)
	walk := func(path, s string) {
		if s == "" {
			return
		}
		matches := templateRE.FindAllStringSubmatch(s, -1)
		for _, m := range matches {
			ref := strings.TrimSpace(m[1])
			if err := checkTemplateRef(ref, inputs, earlier); err != nil {
				*errs = append(*errs, &ValidationError{
					Path:    path,
					Message: fmt.Sprintf("pipeline: step %q: %s", st.ID, err.Error()),
				})
			}
		}
	}

	// agent_run prompt + nested inputs (recursive: NestedInputs can be
	// nested objects/arrays — call_pipeline forwards a structured map
	// of inputs to the child routine, and any string anywhere inside
	// can carry a {{ ... }} template). Only walking top-level string
	// values used to let bad templates inside nested objects pass save.
	// NestedInputs paths aren't tracked per-key (arbitrary depth/shape),
	// so every bad ref found inside it points at the shared "/inputs"
	// bucket rather than a specific leaf.
	walk(base+"/prompt", st.Prompt)
	_ = walkNestedTemplates(st.NestedInputs, func(s string) error {
		walk(base+"/inputs", s)
		return nil // never short-circuit — we want every bad ref, not just the first
	})

	// Conditional `if` expression
	walk(base+"/if", st.If)

	// HTTP step fields
	if st.HTTP != nil {
		walk(base+"/http/url", st.HTTP.URL)
		walk(base+"/http/body", st.HTTP.Body)
		for k, v := range st.HTTP.Headers {
			walk(base+"/http/headers/"+k, v)
		}
	}

	// Wait step fields
	if st.Wait != nil {
		walk(base+"/wait/until", st.Wait.Until)
		walk(base+"/wait/event_filter", st.Wait.EventFilter)
		walk(base+"/wait/approval_prompt", st.Wait.ApprovalPrompt)
	}

	// Code step fields
	if st.Code != nil {
		walk(base+"/code/code", st.Code.Code)
		for k, v := range st.Code.Env {
			walk(base+"/code/env/"+k, v)
		}
	}

	// Transform step fields
	if st.Transform != nil {
		walk(base+"/transform/input", st.Transform.Input)
		walk(base+"/transform/expression", st.Transform.Expression)
	}

	// foreach: the items template resolves against this step's own inputs/
	// earlier-steps context; the body steps additionally see the loop
	// variable (inputs.<as>) and each other in source order (#1419).
	// #1423 item 1: bad refs accumulate into `errs` (JSON-pointer path)
	// rather than returning on the first, same as the rest of this walk.
	if st.Foreach != nil {
		walk(base+"/foreach/items", st.Foreach.Items)
		as := st.Foreach.As
		if as == "" {
			as = "item"
		}
		bodyInputs := make(map[string]struct{}, len(inputs)+1)
		for k := range inputs {
			bodyInputs[k] = struct{}{}
		}
		bodyInputs[as] = struct{}{}
		bodyEarlier := make(map[string]struct{}, len(earlier)+len(st.Foreach.Steps))
		for k := range earlier {
			bodyEarlier[k] = struct{}{}
		}
		for _, bs := range st.Foreach.Steps {
			validateTemplatesInStep(i, bs, bodyInputs, bodyEarlier, errs)
			bodyEarlier[bs.ID] = struct{}{}
		}
	}
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
			// #1423 item 1: input-name typos are exactly the other
			// highest-leverage did-you-mean case named in the issue,
			// alongside agent_slug — same fuzzy.Nearest ranking.
			hint := didYouMean(name, sortedSetKeys(inputs))
			return fmt.Errorf("template ref %q points at unknown input %q%s", ref, name, hint)
		}
		// inputs.X.something — JSON path into a structured input.
		// We don't validate the path itself, just allow it.
	case "steps":
		if len(parts) < 3 {
			return fmt.Errorf("invalid template ref %q (expected steps.Y.output)", ref)
		}
		stepID := parts[1]
		if _, ok := earlier[stepID]; !ok {
			hint := didYouMean(stepID, sortedSetKeys(earlier))
			return fmt.Errorf("template ref %q points at step %q which hasn't run yet at this point%s", ref, stepID, hint)
		}
		// parts[2] = "output" or "output.path"; we don't enforce
		// shape here. The renderer will produce an empty string if
		// the path is missing; the executor's validation gate will
		// catch the resulting empty input as a downstream issue.
	case "env":
		// env.* allowlist enforced at render time, not parse time —
		// the allowed set may differ between dry-run and live run.
	case "run":
		// run.metadata.<key> / run.is_replay / run.replay_of — resolved
		// at render time from the run's metadata + env (Wave 2.4). A
		// missing key renders empty, like inputs/steps.
	case "secrets":
		// secrets.<type> — resolved at render time from the workspace
		// vault via the credential resolver (#1418), scrubbed from every
		// step output / journal entry / error. Allowed like env/run at
		// save time; whether the type actually resolves is enforced
		// separately by credentials_required (declare it there to make an
		// unresolvable secret a hard validation failure).
	case "routine":
		// routine.state.<key> — cross-run routine state (#1420), resolved
		// at render time from the per-schedule state bucket. A missing key
		// renders empty; the shape (routine.state.X) is checked here.
		if len(parts) < 3 || parts[1] != "state" {
			return fmt.Errorf("invalid template ref %q (expected routine.state.<key>)", ref)
		}
	default:
		return fmt.Errorf("template ref %q uses unknown namespace %q (allowed: inputs, steps, env, run, secrets, routine)", ref, parts[0])
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
// DraftAwareResolver wraps a workspace pipeline resolver so a lookup for the
// pipeline currently being SAVED (draftSlug) returns the in-memory draft
// instead of its last-persisted definition (#1427, 2.3a). Without this, a
// B→A / A→B pair authored in the wrong order slips past CycleDetect: when B
// (the draft, which now calls A) is saved, the walk resolves A from the DB
// (A calls B), then resolves B again — but the inner resolver returns B's
// STALE persisted definition (which doesn't yet call A), so the back-edge is
// invisible and the cycle is missed until it churns at runtime. Feeding the
// draft back for its own slug closes the graph so the save-time walk sees the
// real back-edge and rejects the cycle. Any other slug falls through to inner.
func DraftAwareResolver(draftSlug string, draft *DSL, inner func(slug string) (*DSL, error)) func(slug string) (*DSL, error) {
	return func(slug string) (*DSL, error) {
		if draftSlug != "" && slug == draftSlug {
			return draft, nil
		}
		return inner(slug)
	}
}

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

// ReferencedStepOutputs returns the distinct upstream step ids a template
// reads via {{ steps.<id>.output[.path] }}. The single-step debug path
// (/step_run) uses it to WARN when a step's prompt depends on an upstream
// output the caller didn't seed (--outputs): otherwise the ref renders empty
// and the debug run silently exercises a different prompt than production —
// exactly the misleading iteration step-run exists to prevent.
func ReferencedStepOutputs(template string) []string {
	var ids []string
	seen := map[string]bool{}
	for _, m := range templateRE.FindAllStringSubmatch(template, -1) {
		body := strings.TrimSpace(m[1])
		parts := strings.SplitN(body, ".", 3)
		if len(parts) < 3 || parts[0] != "steps" {
			continue
		}
		if parts[2] != "output" && !strings.HasPrefix(parts[2], "output.") {
			continue
		}
		if id := parts[1]; id != "" && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	return ids
}

// RenderContext carries the data a single render call needs. The
// executor builds a fresh one for every step (with the previous
// step's outputs accumulated in StepOutputs).
type RenderContext struct {
	Inputs      map[string]any
	StepOutputs map[string]string // step_id → output (raw string from agent)
	Env         map[string]string // safe env keys only — author_crew_name, run_id, etc.
	Metadata    map[string]any    // run metadata scratchpad — {{ run.metadata.x }}
	// EgressTargets is the routine's declared host allowlist. When
	// non-empty, an http step (or http hook) may only reach a host in
	// this set — enforced in runHTTPStep alongside the httpsafe
	// private-IP/rebind guard. Empty means no ROUTINE-level restriction
	// (back-compat for routines that declare none); the crew
	// network-policy gate and the httpsafe guard still apply.
	EgressTargets []string
	// UntrustedInputs names the top-level Inputs keys whose resolved
	// value must be wrapped in the untrusted-ingress fence
	// (internal/untrusted) before substitution (#1416 item 1). Applied
	// at the LEAF value — after any nested-path traversal — so
	// {{ inputs.event.title }} still resolves the real nested field and
	// only the final string gets fenced, keeping payload-shaped inputs
	// (a GitHub/Stripe event object) usable in templates.
	//
	// Nil (the zero value every Render call site except the dedicated
	// agent_run-prompt render used) means no fencing — HTTP/code/
	// transform/if rendering is deliberately unaffected, since wrapping
	// a URL, JSON body, or script arg in `<untrusted ...>` tags would
	// corrupt it rather than protect anything; those vectors are closed
	// by the egress hardening in egress_gate.go instead.
	UntrustedInputs map[string]struct{}
	// Secrets maps a credential TYPE (e.g. "stripe") to its decrypted
	// value for the {{ secrets.<type> }} render namespace (#1418). It is
	// populated per-step by the executor (resolveStepSecrets) from the
	// workspace vault — workspace + author-crew scoped, ACTIVE-only, the
	// SAME resolver an http step's credential_ref uses. It is NEVER part
	// of the versioned DSL: the definition carries only the template ref,
	// the value is resolved at render time and scrubbed from every step
	// output / journal entry / error. A type absent from this map renders
	// empty (like a missing input) so a public step keeps working; a
	// missing REQUIRED credential is caught separately by
	// credentials_required enforcement.
	Secrets map[string]string
	// State is the routine's cross-run state bucket for the {{ routine.state.<key> }}
	// read namespace (#1420). Loaded once per run from RoutineStateStore keyed on
	// (pipeline_id, schedule_id) — the snapshot AS OF run start, so a step reads
	// what a PRIOR run wrote (a step's own state_write lands for the NEXT run,
	// not mid-run). A key with no stored value renders empty, like a missing
	// input. Isolated per schedule; survives process restart (it is durable in
	// SQL, not in-memory).
	State map[string]string
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
					return fenceIfUntrusted(ctx, parts[1], stringify(nested)), true
				}
				return "", false
			}
		}
		return fenceIfUntrusted(ctx, parts[1], stringify(v)), true
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
	case "run":
		// run.metadata.<key> reads the run's metadata scratchpad;
		// run.is_replay / run.replay_of mirror the env signals.
		if parts[1] == "metadata" && len(parts) == 3 {
			if v, ok := ctx.Metadata[parts[2]]; ok {
				return stringify(v), true
			}
			return "", false
		}
		if v, ok := ctx.Env[parts[1]]; ok { // is_replay, replay_of, run_id
			return v, true
		}
		return "", false
	case "secrets":
		// secrets.<type> — resolved at render time from the workspace
		// vault (populated in ctx.Secrets by resolveStepSecrets). A type
		// with no ACTIVE credential renders empty, like a missing input:
		// the value never appears here unless it was actually resolved,
		// and the runner scrubs it back out of any output/error it lands
		// in. parts[1] is the credential type; deeper paths aren't
		// supported (a secret is an opaque scalar).
		if v, ok := ctx.Secrets[parts[1]]; ok {
			return v, true
		}
		return "", false
	case "routine":
		// routine.state.<key> — cross-run routine state (#1420), loaded per
		// (pipeline, schedule) at run start. A missing key renders empty,
		// like an input. Deeper paths aren't supported (a state value is an
		// opaque scalar string).
		if parts[1] == "state" && len(parts) == 3 {
			if v, ok := ctx.State[parts[2]]; ok {
				return v, true
			}
		}
		return "", false
	}
	return "", false
}

// fenceIfUntrusted wraps value in the untrusted-ingress fence when
// inputName is listed in ctx.UntrustedInputs (#1416 item 1). source is
// fixed to "webhook" — the only producer of UntrustedInputs today is the
// webhook-triggered agent_run prompt render; a future second source would
// need to thread its own label through, not spoof this one, since the
// label is caller-derived and never taken from the payload itself (see
// untrusted.Wrap's contract).
func fenceIfUntrusted(ctx RenderContext, inputName, value string) string {
	if ctx.UntrustedInputs == nil {
		return value
	}
	if _, ok := ctx.UntrustedInputs[inputName]; !ok {
		return value
	}
	return untrusted.Wrap("webhook", value)
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
