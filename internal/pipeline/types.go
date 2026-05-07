// Package pipeline implements Crewship's declarative pipeline primitive:
// AI-authored, workspace-scoped, reusable workflow recipes that run on a
// cheaper execution tier than the model that authored them.
//
// See .claude/context/prd/PIPELINES.md for the full design.
package pipeline

import (
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
	Steps            []Step         `json:"steps"`
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
type InputSpec struct {
	Name        string `json:"name"`
	Type        string `json:"type"` // string | integer | number | boolean | array | object
	Required    bool   `json:"required,omitempty"`
	Default     any    `json:"default,omitempty"`
	Description string `json:"description,omitempty"`
	Min         *int   `json:"min,omitempty"`
	Max         *int   `json:"max,omitempty"`
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
	OnFail        OnFailAction    `json:"on_fail,omitempty"`
	Raw           json.RawMessage `json:"-"` // captured raw step body for type-specific re-decoding

	// agent_run fields (only populated when Type == StepAgentRun)
	AgentSlug string `json:"agent_slug,omitempty"`
	Prompt    string `json:"prompt,omitempty"`

	// call_pipeline fields (only populated when Type == StepCallPipeline)
	PipelineSlug string         `json:"pipeline_slug,omitempty"`
	NestedInputs map[string]any `json:"inputs,omitempty"`
}

// StepType is the closed set of step kinds the executor recognises.
// Adding a new kind requires updating the parser, the executor switch,
// and the runtime tier resolver. Keep the list short and well-tested.
type StepType string

const (
	StepAgentRun     StepType = "agent_run"
	StepCallPipeline StepType = "call_pipeline"
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
