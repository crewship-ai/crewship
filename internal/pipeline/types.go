// Package pipeline implements Crewship's declarative pipeline primitive:
// AI-authored, workspace-scoped, reusable workflow recipes that run on a
// cheaper execution tier than the model that authored them.
//
// See .claude/context/prd/PIPELINES.md for the full design.
package pipeline

import (
	"context"
	"encoding/json"
	"time"
)

// DSL is the top-level pipeline document. The on-disk shape lives in
// pipelines.definition_json; this struct is the parsed in-memory form.
//
// Fields with zero-value defaults that we still want to persist round
// through json.Marshal cleanly — keep `omitempty` off for those.
type DSL struct {
	DSLVersion       string         `json:"dsl_version"`
	Name             string         `json:"name"`
	DisplayName      string         `json:"display_name,omitempty"`
	Description      string         `json:"description,omitempty"`
	ExecutionTier    *ExecutionTier `json:"execution_tier,omitempty"`
	Inputs           []InputSpec    `json:"inputs,omitempty"`
	Outputs          []OutputSpec   `json:"outputs,omitempty"`
	EstimatedCostUSD float64        `json:"estimated_cost_usd,omitempty"`
	EstimatedDurSec  int            `json:"estimated_duration_seconds,omitempty"`
	EgressTargets    []string       `json:"egress_targets,omitempty"`
	CredsRequired    []CredReq      `json:"credentials_required,omitempty"`
	// ConcurrencyKey gates how many runs of this pipeline can be in
	// flight at once for the same workspace + key value. A typical
	// pattern is `concurrency_key: "{{ inputs.account_id }}"` so the
	// platform serialises per-tenant runs but lets unrelated tenants
	// run in parallel. Empty = no gate (unlimited parallelism).
	ConcurrencyKey string `json:"concurrency_key,omitempty"`
	// MaxConcurrent is the cap on simultaneous runs for the resolved
	// ConcurrencyKey. Defaults to 1 when ConcurrencyKey is set
	// (serialised execution per key), ignored when key is empty.
	MaxConcurrent int    `json:"max_concurrent,omitempty"`
	Steps         []Step `json:"steps"`
}

// ExecutionTier overrides the workspace-level tier mapping for a single
// pipeline. Each named tier ("trivial", "fast", "moderate", "smart")
// resolves through workspaces.execution_tiers_json into an adapter +
// model pair. The pipeline-level Preferred wins over individual step
// .Complexity if both are present.
type ExecutionTier struct {
	Preferred string   `json:"preferred,omitempty"`
	Fallback  []string `json:"fallback,omitempty"`
}

// InputSpec declares one named input the pipeline accepts. Type matches
// JSON Schema primitive types so the validation layer can reuse the
// same semantics for inputs and step outputs.
//
// Min/Max are float64 so they cover both `type: integer` and
// `type: number` inputs. JSON numbers don't distinguish ints from
// floats on the wire, so the validator coerces — typing the bounds
// as *int would silently truncate fractional caps for number inputs
// (e.g. Max=0.5 would round to 0). Validation rejects fractional
// bounds when the input Type is "integer".
type InputSpec struct {
	Name        string   `json:"name"`
	Type        string   `json:"type"` // string | integer | number | boolean | array | object
	Required    bool     `json:"required,omitempty"`
	Default     any      `json:"default,omitempty"`
	Description string   `json:"description,omitempty"`
	Min         *float64 `json:"min,omitempty"`
	Max         *float64 `json:"max,omitempty"`
}

// OutputSpec declares one named output the pipeline produces. Outputs
// are read from the final step's output by name; we do not enforce
// strict typing in MVP, the spec is documentary + UI-rendering.
type OutputSpec struct {
	Name        string `json:"name"`
	Type        string `json:"type"`
	Description string `json:"description,omitempty"`
}

// CredReq declares a credential the pipeline needs at runtime. Type-
// matched (not ID-matched) so the same pipeline runs against any
// workspace's credential of the right type. Critical for marketplace
// portability — a "stripe" template never references credential
// "cred_abc123", it references type=stripe and the runtime resolves.
type CredReq struct {
	Type  string `json:"type"`
	Scope string `json:"scope,omitempty"`
}

// Step is the discriminated union of step types. The Type field
// drives polymorphic decoding into one of the concrete step shapes.
// Step kinds in MVP: agent_run, call_pipeline. Others (http, code,
// wait, transform, branch) are deferred to Phase 2; the parser
// rejects them with a clear error at save time.
type Step struct {
	ID            string          `json:"id"`
	Type          StepType        `json:"type"`
	Complexity    Complexity      `json:"complexity,omitempty"`
	ModelOverride string          `json:"model_override,omitempty"`
	TimeoutSec    int             `json:"timeout_seconds,omitempty"`
	Validation    *Validation     `json:"validation,omitempty"`
	Outcomes      *Outcomes       `json:"outcomes,omitempty"`
	OnFail        OnFailAction    `json:"on_fail,omitempty"`
	Retry         *RetryPolicy    `json:"retry,omitempty"`
	Raw           json.RawMessage `json:"-"` // captured raw step body for type-specific re-decoding

	// agent_run fields (only populated when Type == StepAgentRun)
	AgentSlug string `json:"agent_slug,omitempty"`
	Prompt    string `json:"prompt,omitempty"`

	// call_pipeline fields (only populated when Type == StepCallPipeline)
	PipelineSlug string         `json:"pipeline_slug,omitempty"`
	NestedInputs map[string]any `json:"inputs,omitempty"`

	// http fields (only populated when Type == StepHTTP).
	// HTTP steps run a single outbound HTTP call without invoking
	// any agent — useful for non-LLM workflow steps (Slack post,
	// terraform plan webhook, status check). The runtime resolves
	// CredentialRef against the workspace's credentials and injects
	// the token into Headers (e.g. Authorization: Bearer <token>).
	HTTP *HTTPStep `json:"http,omitempty"`

	// code fields (Type == StepCode). Executes a script in a
	// short-lived sandbox container with the agent's existing
	// network/credential boundaries.
	Code *CodeStep `json:"code,omitempty"`

	// wait fields (Type == StepWait). Pauses the pipeline until
	// the configured condition resolves: human approval, datetime,
	// or event signal.
	Wait *WaitStep `json:"wait,omitempty"`

	// transform fields (Type == StepTransform). Pure-Go data
	// reshaping between steps — jq-style projection over a previous
	// step's output, no LLM, no network.
	Transform *TransformStep `json:"transform,omitempty"`
}

// HTTPStep is an outbound HTTP call. Method + URL are required;
// Body + Headers + CredentialRef are optional. The runtime applies
// template substitution to URL, Body, and header VALUES so callers
// can thread inputs and previous step outputs into the request.
//
// Security: the URL host is checked against the workspace's egress
// allowlist (already enforced by the sidecar proxy on agent_run
// steps; the pipeline runtime applies the same gate so a malicious
// pipeline can't exfiltrate via http step). CredentialRef is
// type-matched (e.g. "stripe", "slack") so a marketplace template
// references credentials by purpose, never by ID.
type HTTPStep struct {
	Method        string            `json:"method"` // GET | POST | PUT | PATCH | DELETE
	URL           string            `json:"url"`    // template-substituted before request
	Headers       map[string]string `json:"headers,omitempty"`
	Body          string            `json:"body,omitempty"` // template-substituted; raw string (caller controls JSON-encoding)
	CredentialRef *CredentialRef    `json:"credential_ref,omitempty"`
	// SuccessCodes is the set of HTTP status codes considered a
	// successful response. Default: [200,201,202,204]. Anything
	// outside the set triggers OnFail / escalate logic.
	SuccessCodes []int `json:"success_codes,omitempty"`
	// MaxResponseBytes caps how much of the body the executor
	// reads back into the step output (output flows downstream as
	// {{ steps.X.output }}). Default 1 MB; large responses are
	// truncated with a clear marker so a pipeline doesn't OOM on
	// a runaway endpoint.
	MaxResponseBytes int `json:"max_response_bytes,omitempty"`
}

// CredentialRef points at a workspace credential by TYPE (purpose),
// not by ID. The runtime resolves type → active credential at run
// time. InjectAs controls how the credential value reaches the
// request: "bearer" (Authorization: Bearer <value>), "header" with
// HeaderName, or "query" with QueryName.
type CredentialRef struct {
	Type       string `json:"type"`
	InjectAs   string `json:"inject_as,omitempty"`   // bearer | header | query (default bearer)
	HeaderName string `json:"header_name,omitempty"` // when InjectAs == header
	QueryName  string `json:"query_name,omitempty"`  // when InjectAs == query
}

// CodeStep runs a script in a sandbox container. Runtime is one of
// "python" | "go" | "bash"; the container image is workspace-
// configurable (defaults to debian-slim with the named runtime).
//
// Inputs are passed as environment variables (one per declared
// input) so the script can read them without bespoke parsing. The
// script's stdout becomes the step output; stderr lands in the
// journal entry's error_message preview.
//
// Security: --cap-drop=ALL, no host mounts, network constrained by
// the same egress allowlist as agent_run + http. Timeout enforced
// at container level (runtime hard-kills at TimeoutSec; default
// 300 s).
type CodeStep struct {
	Runtime string `json:"runtime"` // python | go | bash
	Version string `json:"version,omitempty"`
	Code    string `json:"code"`
	// Env is additional environment variables passed to the
	// process (in addition to inputs.* which are auto-mapped to
	// CREWSHIP_INPUT_<NAME>).
	Env map[string]string `json:"env,omitempty"`
}

// WaitStep pauses the run until the configured condition resolves.
// Three kinds in MVP: human approval (token in DB, UI completes),
// datetime (sleep until ISO timestamp), event (waits for a journal
// event matching the filter).
//
// Wait steps don't burn tokens — they're a pure scheduler primitive
// in the runtime. Long waits (>1 h) survive process restart via the
// pipeline_runs DB row's saved cursor. The executor parks the
// goroutine on a condition channel and resumes when the waitpoint
// fires.
type WaitStep struct {
	Kind string `json:"kind"` // approval | datetime | event
	// approval fields
	ApprovalPrompt string `json:"approval_prompt,omitempty"`
	// datetime fields
	Until string `json:"until,omitempty"` // RFC3339 or template
	// event fields
	EventType   string `json:"event_type,omitempty"`
	EventFilter string `json:"event_filter,omitempty"` // simple equality match on payload
	// TimeoutSec wraps the wait — exhausting it falls through to OnFail.
	// 0 = no timeout (wait forever).
}

// TransformStep is pure-Go data reshaping. No LLM, no network,
// fully deterministic. Useful for wiring step outputs together
// without calling another agent_run just to format JSON.
//
// Expression is a small jq-flavored subset: ".path", ".path | tostring",
// ".items[0]", ".name + '-' + .surname". Full grammar in
// internal/pipeline/transform.go (separate file for parser tests).
type TransformStep struct {
	Input      string `json:"input"`      // template-substituted; usually {{ steps.X.output }}
	Expression string `json:"expression"` // jq-flavored projection
}

// StepType is the closed set of step kinds the executor recognises.
// Adding a new kind requires updating the parser, the executor switch,
// and the runtime tier resolver. Keep the list short and well-tested.
type StepType string

const (
	StepAgentRun     StepType = "agent_run"
	StepCallPipeline StepType = "call_pipeline"
	StepHTTP         StepType = "http"
	StepCode         StepType = "code"
	StepWait         StepType = "wait"
	StepTransform    StepType = "transform"
)

// Complexity tags a step's reasoning depth, mapping to a workspace-
// configured adapter+model pair. Workspace defaults: trivial→Haiku,
// fast→Haiku w/ Sonnet fallback, moderate→Sonnet, smart→Opus.
//
// Steps without a complexity tag fall back to "moderate" — the safe
// middle ground that handles most real work without overspending.
type Complexity string

const (
	ComplexityTrivial  Complexity = "trivial"
	ComplexityFast     Complexity = "fast"
	ComplexityModerate Complexity = "moderate"
	ComplexitySmart    Complexity = "smart"
)

// OnFailAction governs what happens when a step's output fails its
// validation gate. escalate_tier is the most useful default for the
// two-tier execution story: try cheaper model first, escalate to a
// smarter one only when the output proves the cheaper one couldn't
// hack it.
type OnFailAction string

const (
	OnFailEscalateTier OnFailAction = "escalate_tier"
	OnFailAbort        OnFailAction = "abort"
	OnFailRetryStep    OnFailAction = "retry_step"
)

// RetryPolicy controls how the executor retries a failed step. This
// is distinct from OnFail: OnFail kicks in after VALIDATION failure
// (output didn't match the gate), Retry kicks in after the step
// EXECUTION returned an error (HTTP 5xx, code timeout, network blip,
// transient agent crash). Retry exhausts before OnFail engages, so
// a step with both Retry=3 and OnFail=escalate_tier tries the same
// tier 3 times then bumps tier on validation fail.
//
// Backoff modes:
//   - "constant"    — InitialDelayMs between every attempt
//   - "exponential" — InitialDelayMs * 2^(attempt-1), capped at MaxDelayMs
//
// RetryOn is an optional allowlist of substring matches against the
// error message. Empty = retry on any error. Use this to scope
// retries: e.g. ["timeout", "5"] only retries timeouts and 5xx,
// never retries 4xx or validation errors.
type RetryPolicy struct {
	MaxAttempts    int      `json:"max_attempts"`
	Backoff        string   `json:"backoff,omitempty"`          // constant | exponential (default constant)
	InitialDelayMs int      `json:"initial_delay_ms,omitempty"` // default 1000
	MaxDelayMs     int      `json:"max_delay_ms,omitempty"`     // default 60000
	RetryOn        []string `json:"retry_on,omitempty"`
}

// Validation gates a step's output before its result is exposed to
// downstream steps. Schema is a JSON Schema (draft 2020-12 subset);
// the must_not_contain / must_contain extensions catch common LLM
// failure modes (refusals, leaked credentials) that schemas alone
// don't easily express.
type Validation struct {
	Schema         json.RawMessage `json:"schema,omitempty"`
	MustNotContain []string        `json:"must_not_contain,omitempty"`
	MustContain    []string        `json:"must_contain,omitempty"`
	MinLength      *int            `json:"min_length,omitempty"`
	MaxLength      *int            `json:"max_length,omitempty"`
}

// Outcomes is rubric-based output evaluation by a separate grader
// agent. It runs AFTER the step's structural Validation passes (so
// cheap byte-level checks short-circuit before we burn LLM tokens
// on grading), and can iterate the worker if the rubric isn't met.
//
// Crewship's answer to Anthropic's Managed Agents "outcomes" feature
// (announced May 6, 2026 at Code with Claude). Same shape, but
// because our runtime is multi-CLI, the grader can be a different
// CLI adapter than the worker — Opus authors, Haiku grades.
//
// Crucially, the grader is an AGENT in the author crew (referenced
// by slug, just like StepAgentRun.AgentSlug), not a raw LLM call.
// That preserves Pavel's "no API keys" model: the grader auths via
// its own CLI tool, the same way every other Crewship agent does.
//
// On rubric failure:
//   - "abort"          → step fails, run fails
//   - "retry_step"     → re-run worker with grader's feedback in prompt
//   - "escalate_tier"  → re-run worker on a smarter tier (existing
//     execution-tier escalation), grade again
//
// MaxIterations caps the retry loop so a stubborn output can't
// burn unbounded tokens. Default is 3 (one initial run + 2 revisions).
type Outcomes struct {
	// Criteria are the named pass/fail rules the grader evaluates
	// against. Each Rule is a natural-language statement; the grader
	// agent reads them all in one prompt and returns structured
	// pass/fail per criterion. Keep them few (5–10) and well-scoped
	// — long lists turn the grader into a hallucination machine.
	Criteria []OutcomeCriterion `json:"criteria"`
	// GraderAgentSlug names the agent that runs the rubric eval.
	// MUST be a slug in the pipeline's AUTHOR crew. Resolves the
	// same way as StepAgentRun.AgentSlug — security boundary is
	// identical.
	GraderAgentSlug string `json:"grader_agent_slug"`
	// MaxIterations caps the worker→grade→revise→grade loop.
	// 1 = single shot (grade once, no revision). Default 3.
	MaxIterations int `json:"max_iterations,omitempty"`
	// OnFail is what the executor does when the rubric ultimately
	// can't be satisfied (after MaxIterations exhausted). Defaults
	// to OnFailAbort — never propagate unrubric'd output downstream.
	OnFail OnFailAction `json:"on_fail,omitempty"`
}

// OutcomeCriterion is one named rubric entry. Name is a stable
// identifier the grader returns in its verdict; Rule is the
// human-readable statement the grader evaluates.
//
// Examples:
//
//	{"name":"length",         "rule":"between 100 and 500 characters"}
//	{"name":"tone",           "rule":"professional, no slang"}
//	{"name":"no_hallucinate", "rule":"every fact appears in the inputs"}
type OutcomeCriterion struct {
	Name        string `json:"name"`
	Rule        string `json:"rule"`
	Description string `json:"description,omitempty"` // optional longer-form context for the grader
}

// Pipeline is the persisted record. Mirrors the pipelines table 1:1
// with the JSON columns parsed into typed fields where it helps.
type Pipeline struct {
	ID                   string
	WorkspaceID          string
	Slug                 string
	Name                 string
	Description          string
	DSLVersion           string
	DefinitionJSON       string // raw, source of truth
	DefinitionHash       string // sha256(DefinitionJSON)
	Ephemeral            bool
	WorkspaceVisible     bool
	InvocationCount      int
	LastInvokedAt        *time.Time
	LastInvocationStatus string

	AuthorCrewID  string
	AuthorAgentID string
	AuthorUserID  string
	AuthorChatID  string
	AuthorRunID   string
	AuthoredVia   AuthoredVia
	ImportedFrom  string

	LastTestRunAt     *time.Time
	LastTestRunPassed bool

	ExecutionTierJSON string // empty = use workspace default

	CreatedAt time.Time
	UpdatedAt time.Time
	DeletedAt *time.Time
}

// AuthoredVia tracks the provenance of a pipeline. The CHECK constraint
// on the column matches this enum — keep them in sync.
type AuthoredVia string

const (
	AuthoredViaAgent    AuthoredVia = "agent_tool_call"
	AuthoredViaUser     AuthoredVia = "user_api"
	AuthoredViaImported AuthoredVia = "imported"
	AuthoredViaSeed     AuthoredVia = "seed"
)

// AuthorMeta captures everything we know about who/where/how a
// pipeline was created. Passed to Save so all the provenance fields
// land in one row write.
type AuthorMeta struct {
	CrewID      string
	AgentID     string
	UserID      string
	ChatID      string
	RunID       string
	Via         AuthoredVia
	ImportedURL string
}

// SaveInput is the payload a caller (sidecar or user-facing API) hands
// to Store.Save. The store enforces author metadata, hash, and the
// test-run gate; the caller owns the raw DSL JSON + parsed slug.
type SaveInput struct {
	WorkspaceID    string
	Slug           string
	Name           string
	Description    string
	DSLVersion     string
	DefinitionJSON string
	Author         AuthorMeta
	// LastTestRunAt + LastTestRunPassed are written when the save
	// handler has confirmed a test_run within the gate window. The
	// store does NOT enforce the freshness rule itself — the handler
	// does, because the gate is policy, not persistence.
	// LastTestRunAt + LastTestRunPassed encode "the caller already
	// ran the test_run gate and these are its results". The store's
	// Save method does NOT trust these naively: it validates the
	// timestamp is recent (within testRunFreshness) AND not in the
	// future, both against time.Now() server-side. Callers cannot
	// mint a passing gate by claiming a fake distant-future
	// timestamp — the server's clock is the source of truth for
	// freshness.
	LastTestRunAt     *time.Time
	LastTestRunPassed bool
	// ExecutionTierJSON optional override of workspace tier mapping.
	// Empty string ("") means "use workspace default".
	ExecutionTierJSON string
}

// ListFilters narrows a Store.List query. Zero value = "all
// non-deleted, workspace-visible, non-ephemeral pipelines for the
// workspace, sorted by invocation_count DESC then name ASC".
type ListFilters struct {
	WorkspaceID      string
	IncludeEphemeral bool
	IncludeHidden    bool // include workspace_visible=false
	AuthorCrewID     string
	Limit            int
	OrderBy          ListOrder
}

// ListOrder controls the ordering in Store.List. Default
// (zero-value) is OrderByPopularity which sorts by invocation_count
// DESC — the natural "show me the pipelines my crews actually use"
// view.
type ListOrder int

const (
	OrderByPopularity ListOrder = iota
	OrderByRecent
	OrderByName
)

// RunMode controls whether the executor performs side effects, runs
// the pipeline against real agents to validate the DSL, or just
// reports what it would have done.
//
//   - ModeRun: live invocation. Agents are called, side effects
//     happen, journal entries land, invocation_count increments.
//   - ModeTestRun: identical to ModeRun in mechanics, but the run
//     does NOT increment invocation_count and is marked in the
//     journal as a test run. Used by the save endpoint to enforce
//     the test-run gate.
//   - ModeDryRun: no agent invocation. Templates are rendered against
//     inputs, the executor walks the step list and reports what it
//     WOULD have done (Ansible --check). Returns a structured
//     "would_execute" report. No journal entries beyond a single
//     pipeline.dry_run audit row.
type RunMode string

const (
	ModeRun     RunMode = "run"
	ModeTestRun RunMode = "test_run"
	ModeDryRun  RunMode = "dry_run"
)

// AgentRunner is the narrow contract the executor needs from the
// orchestrator. The pipeline package depends on this interface
// rather than on internal/orchestrator directly so:
//  1. Tests can inject a deterministic mock without spinning up a
//     real Docker container.
//  2. The orchestrator package owns the wire-up adapter (in a
//     separate file) and pipeline stays a leaf package.
//  3. Future runtimes (e.g. local-only agent execution via Ollama)
//     can satisfy the same interface.
//
// RunStep is synchronous from the executor's POV: it blocks until
// the step finishes, returning the agent's final output as a string.
// The orchestrator implementation buffers streaming events
// internally and only returns once the run reaches a terminal state.
type AgentRunner interface {
	RunStep(ctx context.Context, req AgentStepRequest) (AgentStepResult, error)
}

// AgentStepRequest is the input to AgentRunner.RunStep. WorkspaceID
// + AuthorCrewID + AgentSlug uniquely identify which agent to
// invoke; the runner is responsible for translating slug → agent
// row in the author crew (this is where author-crew-context
// execution actually takes effect).
type AgentStepRequest struct {
	WorkspaceID  string
	AuthorCrewID string
	AgentSlug    string
	Adapter      string
	Model        string
	Prompt       string
	TimeoutSec   int
	// Provenance for the orchestrator's own journal/audit:
	PipelineID      string
	PipelineRunID   string
	StepID          string
	InvokingCrewID  string
	InvokingAgentID string
}

// AgentStepResult is the executor's view of a completed step. The
// orchestrator collects token counts + cost via its existing
// paymaster middleware; the pipeline package does not double-count.
type AgentStepResult struct {
	Output     string // final assistant message text
	DurationMs int64
	CostUSD    float64
	TokensIn   int
	TokensOut  int
}

// PipelineResolver is how the executor looks up a pipeline by slug
// when it encounters a call_pipeline step. Implemented by *Store
// for production; tests can pass a fake to exercise composition
// paths without DB writes.
type PipelineResolver interface {
	GetBySlug(ctx context.Context, workspaceID, slug string) (*Pipeline, error)
}

// RunResult is what Executor.Run returns to the caller. Output
// holds the final step's output; StepOutputs holds every step's
// output by ID for richer caller logic. WouldExecute is populated
// only when Mode == ModeDryRun.
type RunResult struct {
	RunID        string
	PipelineID   string
	PipelineSlug string
	// Status is one of:
	//   COMPLETED  — all steps passed
	//   FAILED     — a step errored or its validation/outcome gate
	//                couldn't be satisfied
	//   CANCELLED  — Cancel(runID) called via /runs/{id}/cancel
	//                (or parent context tripped); the run stopped
	//                between steps
	//   DEDUPED    — idempotency key matched a prior run; this
	//                response is a recovery handle, not a fresh
	//                execution. RunID points at the original run.
	//   DRY_RUN_OK — preview mode, nothing actually executed
	Status       string
	Output       string
	StepOutputs  map[string]string
	WouldExecute []DryRunStep
	DurationMs   int64
	CostUSD      float64
	FailedAtStep string // empty unless Status == FAILED
	ErrorMessage string
	// Deduped is true when the run resolved via an idempotency key
	// hit. Distinct from Status="DEDUPED" so callers can detect
	// dedupe even when they don't pattern-match Status. The Status
	// field is the wire-friendly form; Deduped is the structured
	// flag.
	Deduped bool
}

// DryRunStep is one entry in WouldExecute: what the executor WOULD
// have done at this step in ModeDryRun. Mirrors AgentStepRequest
// but with rendered prompts and resolved tier so a UI / caller
// can inspect exactly what would be sent on a live run.
type DryRunStep struct {
	StepID         string  `json:"step_id"`
	StepType       string  `json:"step_type"`
	WouldCallAgent string  `json:"would_call_agent,omitempty"`
	WouldCallSlug  string  `json:"would_call_pipeline,omitempty"`
	WouldPass      string  `json:"would_pass,omitempty"`
	TierAdapter    string  `json:"tier_adapter,omitempty"`
	TierModel      string  `json:"tier_model,omitempty"`
	EstimatedCost  float64 `json:"estimated_cost_usd,omitempty"`
}
