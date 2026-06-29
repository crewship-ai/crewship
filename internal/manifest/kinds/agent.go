// Package kinds — kind: Agent.
//
// This file implements the standalone `kind: Agent` document used by
// the declarative manifest pipeline to author/update individual agents
// outside the wrapping `kind: Crew` shape. The cousin kind CrewTemplate
// in this directory handles bulk deploys from a template; Agent is the
// per-record CRUD entry point.
//
// REST surface this kind uses
// ---------------------------
//
//	POST   /api/v1/agents                — agents.Create
//	PATCH  /api/v1/agents/{agentId}      — agents.Update
//	GET    /api/v1/agents                — agents.List (workspace-scoped)
//	GET    /api/v1/crews                 — to resolve spec.crew_slug → crew_id
//	POST   /api/v1/agents/{id}/skills    — bind skill (separate POST per skill)
//	POST   /api/v1/agents/{id}/credentials — bind credential (separate POST)
//
// Two important asymmetries we have to paper over here:
//
//  1. The Create handler accepts ONLY crew_id (a CUID), never crew_slug
//     (see internal/api/agents_create.go: createAgentRequest.CrewID).
//     Plan therefore has to resolve the manifest's `crew_slug` to a
//     crew_id by listing /api/v1/crews — exactly mirroring how
//     milestone.go resolves project_slug → project_id.
//
//  2. The Create handler does NOT accept inline skills/env_refs in the
//     POST body. Each binding is a separate POST against
//     /api/v1/agents/{id}/skills (or .../credentials). The Exec closure
//     therefore runs as a sequence: create the agent, then loop over
//     skills + env_refs issuing one POST each. We swallow 409 on
//     duplicate skill bindings (idempotent) but surface other errors so
//     a typo in a skill slug fails loud at apply.
//
// All non-exported helpers carry an `agent` prefix so they don't collide
// with the project / milestone / crew_template helpers already in this
// package (Go would otherwise emit duplicate-symbol errors at link time
// for shared names like `fetchAgents` or `checkStatus`).
package kinds

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/crewship-ai/crewship/internal/manifest/internalapi"
)

// ── Constants ───────────────────────────────────────────────────────────────

// agentAPIVersion is the only apiVersion this kind accepts. Future
// versions get their own constant + parse fork so we never silently
// downgrade a v2 manifest to v1 semantics.
const agentAPIVersion = "crewship/v1"

// agentKind is the literal `kind:` value used in YAML envelopes and
// recorded on PlanItem.Kind for CLI plan output.
const agentKind = "Agent"

// defaultAgentTimeoutSeconds mirrors the server-side default applied
// when the Create handler sees timeout_seconds == 0. We re-apply the
// same default here so the manifest's diff logic sees a non-zero value
// and doesn't repeatedly try to "fix" a server row that was created
// with the implicit default.
const defaultAgentTimeoutSeconds = 1800

// ── Enums ───────────────────────────────────────────────────────────────────
//
// Each set mirrors the corresponding `valid*` map in
// internal/api/agents.go. The handler itself re-validates, but
// duplicating here means a typo fails at `crewship plan` time without
// any HTTP round-trip and the error message can spell out the allowed
// values for the user.

var validAgentRoles = map[string]struct{}{
	"AGENT":       {},
	"LEAD":        {},
	"COORDINATOR": {}, // accepted in the manifest schema per task spec;
	// the API may itself reject it (the server enum was trimmed to
	// AGENT/LEAD in v0.1), in which case Apply surfaces the 400. We
	// keep COORDINATOR in the front-end validator so a future server
	// rollback is a one-line change here.
}

var validAgentCLIAdapters = map[string]struct{}{
	"CLAUDE_CODE":   {},
	"OPENCODE":      {},
	"CODEX_CLI":     {},
	"GEMINI_CLI":    {},
	"CURSOR_CLI":    {},
	"FACTORY_DROID": {},
}

var validAgentLLMProviders = map[string]struct{}{
	"ANTHROPIC": {},
	"OPENAI":    {},
	"GOOGLE":    {},
	"NONE":      {}, // explicit "no LLM" — useful for adapters that pin
	// their own provider (Cursor, Factory) and shouldn't carry a
	// stray ANTHROPIC default.
}

var validAgentToolProfiles = map[string]struct{}{
	"FULL":    {},
	"CODING":  {},
	"MINIMAL": {},
}

// ── Types ───────────────────────────────────────────────────────────────────

// LLMSpec is the nested `llm:` block. Loosely validated: provider is
// enum-checked, model is a free-form string because each adapter has
// its own model namespace and pinning the full Cartesian product in
// this file would rot the moment any provider ships a new model.
type LLMSpec struct {
	// Provider is the LLM family the adapter should route to. Validated
	// against validAgentLLMProviders. Empty means "leave server-side
	// default in place" (the diff path skips empty fields).
	Provider string `yaml:"provider,omitempty" json:"provider,omitempty"`

	// Model is the adapter-specific model identifier. Free-form by
	// design — see comment on the type. Empty means "let the server
	// pick the workspace default".
	Model string `yaml:"model,omitempty" json:"model,omitempty"`
}

// AgentSpec is the shape under `spec:` for kind: Agent.
//
// Mirrors createAgentRequest in internal/api/agents_create.go field-
// for-field where the names match. `crew_slug` is the workspace-local
// reference Plan resolves to a crew_id before the POST; everything
// else maps 1:1 onto the request body.
//
// Prompt/PromptFile carry the system prompt body. Exactly one MUST be
// set (Validate enforces); PromptFile is resolved relative to the
// manifest file at parse time (in internal/manifest/parse.go's
// resolveLocalReferences), so by the time Plan sees the document the
// content is already inlined into Prompt and PromptFile is empty. We
// keep both fields in the type so Export → re-Apply round-trips
// preserve the user's original layout if they ever want to break the
// prompt back out into a sibling file.
type AgentSpec struct {
	// CrewSlug is the parent crew this agent belongs to. REQUIRED for
	// LEAD; optional for AGENT/COORDINATOR (a crewless AGENT is a
	// workspace-scoped utility — uncommon but supported).
	CrewSlug string `yaml:"crew_slug,omitempty" json:"crew_slug,omitempty"`

	// RoleTitle is the human-facing title shown in the UI (e.g.
	// "Technical Architect"). Optional.
	RoleTitle string `yaml:"role_title,omitempty" json:"role_title,omitempty"`

	// AgentRole is the orchestrator role. One of LEAD | AGENT |
	// COORDINATOR. Empty means "let the server default (AGENT)".
	AgentRole string `yaml:"agent_role,omitempty" json:"agent_role,omitempty"`

	// CLIAdapter selects the runtime adapter (which binary the
	// orchestrator launches). Validated against validAgentCLIAdapters.
	CLIAdapter string `yaml:"cli_adapter,omitempty" json:"cli_adapter,omitempty"`

	// LLM bundles provider + model. See LLMSpec.
	LLM LLMSpec `yaml:"llm,omitempty" json:"llm,omitempty"`

	// ToolProfile picks the tool allow-list bucket. One of FULL |
	// CODING | MINIMAL.
	ToolProfile string `yaml:"tool_profile,omitempty" json:"tool_profile,omitempty"`

	// TimeoutSeconds caps single-turn execution time. Defaults to 1800
	// when omitted — matches the server-side default.
	TimeoutSeconds int `yaml:"timeout_seconds,omitempty" json:"timeout_seconds,omitempty"`

	// MemoryEnabled toggles the per-agent memory tier. Pointer because
	// we need to distinguish "set to false" from "not declared" when
	// diffing (a bare bool zero-values to false either way). The YAML
	// decoder fills a non-nil *bool only when the key was present.
	MemoryEnabled *bool `yaml:"memory_enabled,omitempty" json:"memory_enabled,omitempty"`

	// Prompt is the inline system prompt body. Mutually exclusive with
	// PromptFile (Validate enforces). After resolveLocalReferences the
	// content of PromptFile is folded into Prompt and PromptFile is
	// cleared.
	Prompt string `yaml:"prompt,omitempty" json:"prompt,omitempty"`

	// PromptFile is a manifest-relative path to a prompt body. Resolved
	// at parse time; see Prompt.
	PromptFile string `yaml:"prompt_file,omitempty" json:"prompt_file,omitempty"`

	// Skills is a list of skill slugs to bind to this agent. Each
	// becomes a separate POST /api/v1/agents/{id}/skills call in the
	// Exec closure (the Create handler does not accept inline skills).
	Skills []string `yaml:"skills,omitempty" json:"skills,omitempty"`

	// EnvRefs is a list of credential names (the credential's `name`
	// field is conventionally the env-var, e.g. ANTHROPIC_API_KEY) to
	// bind as environment variables. Each becomes a separate
	// POST /api/v1/agents/{id}/credentials call.
	EnvRefs []string `yaml:"env_refs,omitempty" json:"env_refs,omitempty"`
}

// AgentDocument is the YAML envelope produced/consumed by the manifest
// pipeline. metadata.slug is the workspace-unique idempotency key —
// Plan keys on it to decide Create vs Update.
type AgentDocument struct {
	APIVersion string               `yaml:"apiVersion" json:"apiVersion"`
	Kind       string               `yaml:"kind"       json:"kind"`
	Metadata   internalapi.Metadata `yaml:"metadata"   json:"metadata"`
	Spec       AgentSpec            `yaml:"spec"       json:"spec"`
}

// AgentRemote is the slice of GET /api/v1/agents each row produces —
// only the fields the manifest needs to diff or round-trip through
// export. Drift detection compares AgentSpec field by field; counts /
// timestamps / status (RUNNING/IDLE/ERROR) stay out of the plan
// because they're pure runtime state.
//
// CrewID is the resolved crew identifier; ExportAgents maps it back to
// CrewSlug via a parallel /api/v1/crews fetch so the exported
// AgentDocument carries a stable, re-applyable reference instead of a
// CUID that would break across workspace re-creates.
type AgentRemote struct {
	ID             string  `json:"id"`
	WorkspaceID    string  `json:"workspace_id"`
	Slug           string  `json:"slug"`
	Name           string  `json:"name"`
	Description    *string `json:"description"`
	RoleTitle      *string `json:"role_title"`
	AgentRole      string  `json:"agent_role"`
	CLIAdapter     string  `json:"cli_adapter"`
	LLMProvider    *string `json:"llm_provider"`
	LLMModel       *string `json:"llm_model"`
	SystemPrompt   *string `json:"system_prompt"`
	TimeoutSeconds int     `json:"timeout_seconds"`
	ToolProfile    string  `json:"tool_profile"`
	MemoryEnabled  bool    `json:"memory_enabled"`
	CrewID         *string `json:"crew_id"`
}

// agentCrewStub is the minimal shape this kind needs from
// GET /api/v1/crews to resolve crew_slug → crew_id. Defined locally so
// the file stays self-contained; cross-kind imports inside the kinds
// package create initialisation-ordering surprises and break test
// isolation.
type agentCrewStub struct {
	ID   string `json:"id"`
	Slug string `json:"slug"`
	Name string `json:"name"`
}

// agentSkillStub is the minimal shape from GET /api/v1/skills the
// binding step uses to map a skill slug → skill_id.
type agentSkillStub struct {
	ID   string `json:"id"`
	Slug string `json:"slug"`
	Name string `json:"name"`
}

// agentCredentialStub mirrors what we need from GET /api/v1/credentials
// to look up a credential by its env-var-style `name` field.
type agentCredentialStub struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ── Validate ────────────────────────────────────────────────────────────────

// Validate enforces the structural rules without any HTTP round-trip.
// Required: metadata.name, metadata.slug, spec.crew_slug, enum fields
// must be in the allow-list when set, exactly one of prompt /
// prompt_file must be set, LEAD requires a crew_slug. FK references
// (crew_slug, skills, env_refs) are checked against `wsCtx` when the
// corresponding declared/remote slice is populated — Validate is
// offline-tolerant and degrades gracefully when wsCtx is empty.
func (d *AgentDocument) Validate(wsCtx internalapi.WorkspaceContext) error {
	if d.APIVersion != "" && d.APIVersion != agentAPIVersion {
		return fmt.Errorf("agent %q: apiVersion %q must be %q",
			d.Metadata.Slug, d.APIVersion, agentAPIVersion)
	}
	if d.Kind != "" && d.Kind != agentKind {
		return fmt.Errorf("agent %q: kind %q must be %q",
			d.Metadata.Slug, d.Kind, agentKind)
	}
	if strings.TrimSpace(d.Metadata.Name) == "" {
		return fmt.Errorf("agent %q: metadata.name is required", d.Metadata.Slug)
	}
	if strings.TrimSpace(d.Metadata.Slug) == "" {
		return fmt.Errorf("agent: metadata.slug is required")
	}
	if strings.TrimSpace(d.Spec.CrewSlug) == "" {
		// crew_slug is required by the task spec ("REQUIRED — which
		// crew this agent belongs to"). The server itself allows
		// crewless agents, but the declarative manifest pipeline
		// enforces a stricter rule: every agent must live in exactly
		// one named crew so cross-document references stay
		// unambiguous.
		return fmt.Errorf("agent %q: spec.crew_slug is required", d.Metadata.Slug)
	}

	if d.Spec.AgentRole != "" {
		if _, ok := validAgentRoles[d.Spec.AgentRole]; !ok {
			return fmt.Errorf("agent %q: invalid agent_role %q (want one of LEAD, AGENT, COORDINATOR)",
				d.Metadata.Slug, d.Spec.AgentRole)
		}
	}
	if d.Spec.AgentRole == "LEAD" && strings.TrimSpace(d.Spec.CrewSlug) == "" {
		// Defensive — the crew_slug check above already caught this,
		// but spelling it out keeps the error message specific when a
		// future relaxation makes crew_slug optional again.
		return fmt.Errorf("agent %q: agent_role=LEAD requires spec.crew_slug", d.Metadata.Slug)
	}
	if d.Spec.CLIAdapter != "" {
		if _, ok := validAgentCLIAdapters[d.Spec.CLIAdapter]; !ok {
			return fmt.Errorf("agent %q: invalid cli_adapter %q (want one of CLAUDE_CODE, OPENCODE, CODEX_CLI, GEMINI_CLI, CURSOR_CLI, FACTORY_DROID)",
				d.Metadata.Slug, d.Spec.CLIAdapter)
		}
	}
	if d.Spec.LLM.Provider != "" {
		if _, ok := validAgentLLMProviders[d.Spec.LLM.Provider]; !ok {
			return fmt.Errorf("agent %q: invalid llm.provider %q (want one of ANTHROPIC, OPENAI, GOOGLE, NONE)",
				d.Metadata.Slug, d.Spec.LLM.Provider)
		}
	}
	if d.Spec.ToolProfile != "" {
		if _, ok := validAgentToolProfiles[d.Spec.ToolProfile]; !ok {
			return fmt.Errorf("agent %q: invalid tool_profile %q (want one of FULL, CODING, MINIMAL)",
				d.Metadata.Slug, d.Spec.ToolProfile)
		}
	}
	if d.Spec.TimeoutSeconds < 0 {
		return fmt.Errorf("agent %q: timeout_seconds %d must be non-negative",
			d.Metadata.Slug, d.Spec.TimeoutSeconds)
	}

	// Exactly one of Prompt / PromptFile must be set. parse.go's
	// resolveLocalReferences usually folds PromptFile into Prompt
	// before Validate runs, so under the normal pipeline this only
	// rejects "both empty" or (in the rare inline-bundle case where
	// parse never ran) "both set". The check is symmetric to the
	// inline-Agent equivalent in parse.go.
	hasPrompt := strings.TrimSpace(d.Spec.Prompt) != ""
	hasPromptFile := strings.TrimSpace(d.Spec.PromptFile) != ""
	switch {
	case hasPrompt && hasPromptFile:
		return fmt.Errorf("agent %q: spec.prompt and spec.prompt_file are mutually exclusive (set exactly one)",
			d.Metadata.Slug)
	case !hasPrompt && !hasPromptFile:
		return fmt.Errorf("agent %q: one of spec.prompt or spec.prompt_file is required",
			d.Metadata.Slug)
	}

	// FK: crew_slug must be in the declared+remote crew universe IF
	// wsCtx carries any crew data at all. An empty wsCtx (Validate
	// invoked in isolation, e.g. unit tests for the document itself)
	// degrades silently — Plan will fail at the resolution step.
	if (len(wsCtx.DeclaredCrews) > 0 || len(wsCtx.RemoteCrews) > 0) && !wsCtx.HasCrew(d.Spec.CrewSlug) {
		return fmt.Errorf("agent %q: spec.crew_slug %q does not reference any declared or remote crew",
			d.Metadata.Slug, d.Spec.CrewSlug)
	}

	// Skills + env_refs: we don't yet have a generic "declared
	// skills/credentials" surface on WorkspaceContext, so cross-
	// document FK checks for these run at Plan time when the live
	// client is available. We only sanity-check the slice shape here
	// (no empty entries, no whitespace-only).
	for i, s := range d.Spec.Skills {
		if strings.TrimSpace(s) == "" {
			return fmt.Errorf("agent %q: spec.skills[%d] is empty", d.Metadata.Slug, i)
		}
	}
	for i, e := range d.Spec.EnvRefs {
		if strings.TrimSpace(e) == "" {
			return fmt.Errorf("agent %q: spec.env_refs[%d] is empty", d.Metadata.Slug, i)
		}
	}

	return nil
}

// ── Plan ────────────────────────────────────────────────────────────────────

// Plan compares the declared document against `remote` (nil = not yet
// on server) and returns the plan items the apply loop should
// execute. A single PlanItem is returned even for the Unchanged case
// so the CLI can count "0 changed, 1 unchanged" truthfully.
//
// The Exec closure on a Create item performs THREE phases in order:
//
//  1. POST /api/v1/agents             — main create, returns the new id
//  2. POST /api/v1/agents/{id}/skills — one call per declared skill
//  3. POST /api/v1/agents/{id}/credentials — one call per env_ref
//
// Failure in phase 2 or 3 leaves the agent created but partially
// bound; the apply pipeline's journal records what succeeded so
// re-applying after fixing the typo converges (the agent is now
// remote, Plan emits Update, and the skill/cred bindings are
// idempotent on the server side).
//
// On Update we currently re-bind skills/credentials only when the
// manifest declares them (the diff path doesn't yet remove
// server-side bindings the manifest dropped — that's a sync-mode
// concern handled upstream in internal/manifest/sync.go for the
// existing embedded-Agent flow). This matches the cousin kinds'
// "add-only" Update semantics.
func (d *AgentDocument) Plan(ctx context.Context, c internalapi.Client, remote *AgentRemote) ([]internalapi.PlanItem, error) {
	crewID, err := LookupCrewIDBySlug(ctx, c, d.Spec.CrewSlug)
	if err != nil {
		return nil, fmt.Errorf("agent %q: resolve crew_slug: %w", d.Metadata.Slug, err)
	}

	if remote == nil {
		body := d.toCreateBody(crewID)
		skills := append([]string(nil), d.Spec.Skills...)   // capture by value
		envRefs := append([]string(nil), d.Spec.EnvRefs...) // for the closure
		slug := d.Metadata.Slug

		return []internalapi.PlanItem{{
			Kind:        "agent",
			Slug:        slug,
			Action:      internalapi.ActionCreate,
			Description: fmt.Sprintf("create agent %q in crew %q", slug, d.Spec.CrewSlug),
			Exec: func(ctx context.Context, c internalapi.Client) error {
				// Phase 1: create the agent. The response carries the
				// server-assigned id we need for phase 2/3.
				resp, err := c.Post(ctx, "/api/v1/agents", body)
				if err != nil {
					return fmt.Errorf("POST /api/v1/agents: %w", err)
				}
				if err := checkStatus(resp, "create agent "+slug); err != nil {
					return err
				}
				created, err := agentDecodeCreateResponse(resp.Body)
				if err != nil {
					return fmt.Errorf("agent %q: decode create response: %w", slug, err)
				}
				if created.ID == "" {
					return fmt.Errorf("agent %q: create response missing id", slug)
				}

				// Phase 2 + 3: bindings. agentBindSkills /
				// agentBindEnvRefs handle their own per-call error
				// wrapping so the user sees which skill / env ref
				// failed.
				if err := agentBindSkills(ctx, c, created.ID, slug, skills); err != nil {
					return err
				}
				if err := agentBindEnvRefs(ctx, c, created.ID, slug, envRefs); err != nil {
					return err
				}
				return nil
			},
		}}, nil
	}

	// Update path. Diff produces a sparse PATCH body containing only
	// the fields whose declared value differs from `remote`. Empty
	// declared fields are skipped (they mean "leave server value
	// alone") so a manifest that omits role_title won't overwrite the
	// title a user set via the UI.
	patch := d.diffPatch(remote, crewID)
	skills := append([]string(nil), d.Spec.Skills...)
	envRefs := append([]string(nil), d.Spec.EnvRefs...)
	agentID := remote.ID
	slug := d.Metadata.Slug

	if len(patch) == 0 && len(skills) == 0 && len(envRefs) == 0 {
		return []internalapi.PlanItem{{
			Kind:        "agent",
			Slug:        slug,
			Action:      internalapi.ActionUnchanged,
			Description: fmt.Sprintf("agent %q already matches manifest", slug),
		}}, nil
	}

	return []internalapi.PlanItem{{
		Kind:        "agent",
		Slug:        slug,
		Action:      internalapi.ActionUpdate,
		Description: fmt.Sprintf("update agent %q (%d field(s))", slug, len(patch)),
		Exec: func(ctx context.Context, c internalapi.Client) error {
			if len(patch) > 0 {
				resp, err := c.Patch(ctx, "/api/v1/agents/"+agentID, patch)
				if err != nil {
					return fmt.Errorf("PATCH /api/v1/agents/%s: %w", agentID, err)
				}
				if err := checkStatus(resp, "update agent "+slug); err != nil {
					return err
				}
			}
			// Always re-assert declared bindings. The POST endpoint
			// swallows duplicates (already_assigned=true), so this is
			// safe to re-run.
			if err := agentBindSkills(ctx, c, agentID, slug, skills); err != nil {
				return err
			}
			if err := agentBindEnvRefs(ctx, c, agentID, slug, envRefs); err != nil {
				return err
			}
			return nil
		},
	}}, nil
}

// ── Body builders ───────────────────────────────────────────────────────────

// toCreateBody renders the POST /api/v1/agents body. crew_id is the
// already-resolved CUID (caller resolved it via LookupCrewIDBySlug).
// We always send the explicit timeout default so the manifest's own
// view of the world matches whatever the server records — otherwise
// the first Update Plan after a Create would emit a spurious
// timeout_seconds patch.
func (d *AgentDocument) toCreateBody(crewID string) map[string]any {
	body := map[string]any{
		"name":         d.Metadata.Name,
		"slug":         d.Metadata.Slug,
		"crew_id":      crewID,
		"agent_role":   d.Spec.AgentRole,
		"cli_adapter":  d.Spec.CLIAdapter,
		"tool_profile": d.Spec.ToolProfile,
	}
	if d.Metadata.Description != "" {
		body["description"] = d.Metadata.Description
	}
	if d.Spec.RoleTitle != "" {
		body["role_title"] = d.Spec.RoleTitle
	}
	if d.Spec.LLM.Provider != "" && d.Spec.LLM.Provider != "NONE" {
		body["llm_provider"] = d.Spec.LLM.Provider
	}
	if d.Spec.LLM.Model != "" {
		body["llm_model"] = d.Spec.LLM.Model
	}
	if d.Spec.Prompt != "" {
		body["system_prompt"] = d.Spec.Prompt
	}
	timeout := d.Spec.TimeoutSeconds
	if timeout == 0 {
		timeout = defaultAgentTimeoutSeconds
	}
	body["timeout_seconds"] = timeout

	// MemoryEnabled is a *bool to distinguish "user didn't say" from
	// "user said false". When unset we let the server default (true)
	// stand; when set we forward the explicit value.
	if d.Spec.MemoryEnabled != nil {
		body["memory_enabled"] = *d.Spec.MemoryEnabled
	} else {
		// Default for the manifest is memory_enabled=true per task
		// spec. We send it explicitly so the round-trip Plan sees the
		// same value the server records.
		body["memory_enabled"] = true
	}
	return body
}

// diffPatch returns ONLY the fields whose declared value differs from
// `remote`. Empty declared fields are skipped — they mean "leave
// server value alone" — so a manifest that omits role_title won't
// blank out a title a UI user set after the initial apply.
//
// The diff is intentionally narrow: we don't touch description /
// system_prompt / memory_enabled when the user didn't declare them.
// system_prompt is the most consequential omission target — agents in
// the wild often grow prompts via the UI and the manifest shouldn't
// silently clobber that.
func (d *AgentDocument) diffPatch(remote *AgentRemote, crewID string) map[string]any {
	patch := map[string]any{}

	if d.Metadata.Name != "" && d.Metadata.Name != remote.Name {
		patch["name"] = d.Metadata.Name
	}
	if d.Metadata.Description != "" && d.Metadata.Description != deref(remote.Description) {
		patch["description"] = d.Metadata.Description
	}
	if d.Spec.RoleTitle != "" && d.Spec.RoleTitle != deref(remote.RoleTitle) {
		patch["role_title"] = d.Spec.RoleTitle
	}
	if d.Spec.AgentRole != "" && d.Spec.AgentRole != remote.AgentRole {
		patch["agent_role"] = d.Spec.AgentRole
	}
	if d.Spec.CLIAdapter != "" && d.Spec.CLIAdapter != remote.CLIAdapter {
		patch["cli_adapter"] = d.Spec.CLIAdapter
	}
	if d.Spec.LLM.Provider != "" && d.Spec.LLM.Provider != "NONE" &&
		d.Spec.LLM.Provider != deref(remote.LLMProvider) {
		patch["llm_provider"] = d.Spec.LLM.Provider
	}
	if d.Spec.LLM.Model != "" && d.Spec.LLM.Model != deref(remote.LLMModel) {
		patch["llm_model"] = d.Spec.LLM.Model
	}
	if d.Spec.ToolProfile != "" && d.Spec.ToolProfile != remote.ToolProfile {
		patch["tool_profile"] = d.Spec.ToolProfile
	}
	if d.Spec.TimeoutSeconds > 0 && d.Spec.TimeoutSeconds != remote.TimeoutSeconds {
		patch["timeout_seconds"] = d.Spec.TimeoutSeconds
	}
	if d.Spec.Prompt != "" && d.Spec.Prompt != deref(remote.SystemPrompt) {
		patch["system_prompt"] = d.Spec.Prompt
	}
	if d.Spec.MemoryEnabled != nil && *d.Spec.MemoryEnabled != remote.MemoryEnabled {
		patch["memory_enabled"] = *d.Spec.MemoryEnabled
	}
	// Crew reassignment via manifest. Rare but supported — the Update
	// handler accepts crew_id in its allow-list.
	if crewID != "" && (remote.CrewID == nil || crewID != *remote.CrewID) {
		patch["crew_id"] = crewID
	}
	return patch
}

// ── Lookup helpers ──────────────────────────────────────────────────────────

// LookupAgentRemoteBySlug fetches the live state of one agent by slug.
// Returns (nil, nil) when no row matches — Plan treats that as
// ActionCreate. We pull the full /api/v1/agents list and filter
// client-side because the handler has no slug-filter query parameter
// and the list is workspace-scoped (small enough that a single round-
// trip beats per-slug lookup churn).
func LookupAgentRemoteBySlug(ctx context.Context, c internalapi.Client, slug string) (*AgentRemote, error) {
	rows, err := agentListAll(ctx, c)
	if err != nil {
		return nil, err
	}
	for i := range rows {
		if rows[i].Slug == slug {
			row := rows[i]
			return &row, nil
		}
	}
	return nil, nil
}

// LookupCrewIDBySlug resolves a crew slug to its CUID. Returns a
// not-found error when no row matches; the caller decorates with
// "agent %q:" context. Used by both Plan (to populate crew_id in the
// POST body) and Export (round-trip is straight slug→id→slug; the
// inverse lookup lives in ExportAgents which builds the map once).
func LookupCrewIDBySlug(ctx context.Context, c internalapi.Client, slug string) (string, error) {
	crews, err := agentListCrews(ctx, c)
	if err != nil {
		return "", err
	}
	for _, cr := range crews {
		if cr.Slug == slug {
			return cr.ID, nil
		}
	}
	return "", fmt.Errorf("crew with slug %q not found", slug)
}

// ── Export ──────────────────────────────────────────────────────────────────

// ExportAgents fetches every agent the caller can see and renders
// each as an AgentDocument suitable for re-applying. The function is
// the inverse of Plan/Create — fields the manifest doesn't model
// (status, schedule_*, counts) are dropped.
//
// Skills + env_refs are pulled per-agent via the bindings endpoints.
// This is N+1 in the HTTP sense but the typical agent has < 10
// skills and < 5 credentials; a workspace with 200 agents incurs ~400
// round-trips for a full export which is acceptable for an explicit
// operator action. If this becomes a hotspot a `?include=bindings`
// query parameter on the agents list endpoint is the natural fix.
func ExportAgents(ctx context.Context, c internalapi.Client) ([]*AgentDocument, error) {
	rows, err := agentListAll(ctx, c)
	if err != nil {
		return nil, fmt.Errorf("export agents: %w", err)
	}
	crews, err := agentListCrews(ctx, c)
	if err != nil {
		// Crew lookup failure isn't fatal — we just can't fold crew_id
		// back to crew_slug, so agents without a resolved slug get
		// skipped. Surface a non-nil error so apply pipelines that
		// want strict export can fail loud; callers that prefer
		// best-effort can ignore the error and use the partial slice.
		return nil, fmt.Errorf("export agents: list crews: %w", err)
	}
	crewSlugByID := make(map[string]string, len(crews))
	for _, cr := range crews {
		crewSlugByID[cr.ID] = cr.Slug
	}

	out := make([]*AgentDocument, 0, len(rows))
	for _, r := range rows {
		doc := &AgentDocument{
			APIVersion: agentAPIVersion,
			Kind:       agentKind,
			Metadata: internalapi.Metadata{
				Name: r.Name,
				Slug: r.Slug,
			},
			Spec: AgentSpec{
				RoleTitle:      deref(r.RoleTitle),
				AgentRole:      r.AgentRole,
				CLIAdapter:     r.CLIAdapter,
				ToolProfile:    r.ToolProfile,
				TimeoutSeconds: r.TimeoutSeconds,
			},
		}
		if r.Description != nil && *r.Description != "" {
			doc.Metadata.Description = *r.Description
		}
		if r.CrewID != nil {
			if slug, ok := crewSlugByID[*r.CrewID]; ok {
				doc.Spec.CrewSlug = slug
			}
		}
		if r.LLMProvider != nil && *r.LLMProvider != "" {
			doc.Spec.LLM.Provider = *r.LLMProvider
		}
		if r.LLMModel != nil && *r.LLMModel != "" {
			doc.Spec.LLM.Model = *r.LLMModel
		}
		if r.SystemPrompt != nil && *r.SystemPrompt != "" {
			doc.Spec.Prompt = *r.SystemPrompt
		}
		// MemoryEnabled is a *bool in the manifest — set it
		// explicitly on export so the round-trip diff doesn't fall
		// into the "not declared, use server default" branch and emit
		// a phantom patch.
		mem := r.MemoryEnabled
		doc.Spec.MemoryEnabled = &mem

		// Bindings: best-effort. A skills/cred fetch failure on one
		// agent shouldn't kill the whole export — log via the error
		// path and continue with whatever we got.
		if skillSlugs, sErr := agentListSkillSlugs(ctx, c, r.ID); sErr == nil {
			doc.Spec.Skills = skillSlugs
		}
		if envNames, eErr := agentListCredentialEnvNames(ctx, c, r.ID); eErr == nil {
			doc.Spec.EnvRefs = envNames
		}
		out = append(out, doc)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Metadata.Slug < out[j].Metadata.Slug })
	return out, nil
}

// ── HTTP helpers (all agent-prefixed) ───────────────────────────────────────

// agentListAll pulls /api/v1/agents and decodes the rows into the
// manifest's wire-type. We use the workspace-scoped endpoint without
// a crew_id filter — Plan does the crew matching itself, and Export
// wants every agent in the workspace anyway.
func agentListAll(ctx context.Context, c internalapi.Client) ([]AgentRemote, error) {
	resp, err := c.Get(ctx, "/api/v1/agents")
	if err != nil {
		return nil, fmt.Errorf("GET /api/v1/agents: %w", err)
	}
	if err := checkStatus(resp, "list agents"); err != nil {
		return nil, err
	}
	body, err := readAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read /api/v1/agents body: %w", err)
	}
	if len(body) == 0 {
		return nil, nil
	}
	var rows []AgentRemote
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, fmt.Errorf("decode /api/v1/agents: %w", err)
	}
	return rows, nil
}

// agentListCrews pulls /api/v1/crews and decodes the minimal shape we
// need for slug↔id round-tripping.
func agentListCrews(ctx context.Context, c internalapi.Client) ([]agentCrewStub, error) {
	resp, err := c.Get(ctx, "/api/v1/crews")
	if err != nil {
		return nil, fmt.Errorf("GET /api/v1/crews: %w", err)
	}
	if err := checkStatus(resp, "list crews"); err != nil {
		return nil, err
	}
	body, err := readAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read /api/v1/crews body: %w", err)
	}
	if len(body) == 0 {
		return nil, nil
	}
	var rows []agentCrewStub
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, fmt.Errorf("decode /api/v1/crews: %w", err)
	}
	return rows, nil
}

// agentListSkills pulls /api/v1/skills (workspace-scoped). Used by
// the binding step to resolve a skill slug → skill_id.
func agentListSkills(ctx context.Context, c internalapi.Client) ([]agentSkillStub, error) {
	resp, err := c.Get(ctx, "/api/v1/skills")
	if err != nil {
		return nil, fmt.Errorf("GET /api/v1/skills: %w", err)
	}
	if err := checkStatus(resp, "list skills"); err != nil {
		return nil, err
	}
	body, err := readAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read /api/v1/skills body: %w", err)
	}
	if len(body) == 0 {
		return nil, nil
	}
	var rows []agentSkillStub
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, fmt.Errorf("decode /api/v1/skills: %w", err)
	}
	return rows, nil
}

// agentListCredentials pulls /api/v1/credentials (workspace-scoped).
// Used by the binding step to resolve an env-var name (the
// credential's `name` field) → credential_id.
func agentListCredentials(ctx context.Context, c internalapi.Client) ([]agentCredentialStub, error) {
	resp, err := c.Get(ctx, "/api/v1/credentials")
	if err != nil {
		return nil, fmt.Errorf("GET /api/v1/credentials: %w", err)
	}
	if err := checkStatus(resp, "list credentials"); err != nil {
		return nil, err
	}
	body, err := readAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read /api/v1/credentials body: %w", err)
	}
	if len(body) == 0 {
		return nil, nil
	}
	var rows []agentCredentialStub
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, fmt.Errorf("decode /api/v1/credentials: %w", err)
	}
	return rows, nil
}

// agentBindSkills binds each declared skill slug to the agent. The
// server's POST endpoint is idempotent on UNIQUE conflict (returns
// 200 + already_assigned=true) so a re-apply that declares the same
// skill twice is safe; we surface other 4xx/5xx errors with the
// offending slug so a typo is easy to spot.
func agentBindSkills(ctx context.Context, c internalapi.Client, agentID, agentSlug string, skills []string) error {
	if len(skills) == 0 {
		return nil
	}
	catalog, err := agentListSkills(ctx, c)
	if err != nil {
		return fmt.Errorf("agent %q: list skills for binding: %w", agentSlug, err)
	}
	idBySlug := make(map[string]string, len(catalog))
	for _, s := range catalog {
		idBySlug[s.Slug] = s.ID
	}
	for _, slug := range skills {
		skillID, ok := idBySlug[slug]
		if !ok {
			return fmt.Errorf("agent %q: skill %q not found in workspace catalog", agentSlug, slug)
		}
		resp, err := c.Post(ctx, "/api/v1/agents/"+agentID+"/skills", map[string]any{
			"skill_id": skillID,
		})
		if err != nil {
			return fmt.Errorf("agent %q: bind skill %q: %w", agentSlug, slug, err)
		}
		// 409 = already bound. The handler's idempotent fast-path
		// returns 200, but a parallel write could land between the
		// idempotency check and our POST, so we accept 409 too.
		if resp != nil && resp.StatusCode == 409 {
			continue
		}
		if err := checkStatus(resp, fmt.Sprintf("bind skill %q to agent %q", slug, agentSlug)); err != nil {
			return err
		}
	}
	return nil
}

// agentBindEnvRefs binds each declared env-var name to a workspace
// credential. The convention is that the credential's `name` field
// matches the env-var (e.g. "ANTHROPIC_API_KEY"); the binding uses
// that same string as env_var_name on the agent. Priority defaults to
// 0 — first-declared-wins inside the agent's env, matching the
// existing UI behaviour.
func agentBindEnvRefs(ctx context.Context, c internalapi.Client, agentID, agentSlug string, envRefs []string) error {
	if len(envRefs) == 0 {
		return nil
	}
	catalog, err := agentListCredentials(ctx, c)
	if err != nil {
		return fmt.Errorf("agent %q: list credentials for binding: %w", agentSlug, err)
	}
	idByName := make(map[string]string, len(catalog))
	for _, cred := range catalog {
		idByName[cred.Name] = cred.ID
	}
	for _, env := range envRefs {
		credID, ok := idByName[env]
		if !ok {
			return fmt.Errorf("agent %q: env_ref %q has no matching workspace credential (the credential's name must equal the env-var)",
				agentSlug, env)
		}
		resp, err := c.Post(ctx, "/api/v1/agents/"+agentID+"/credentials", map[string]any{
			"credential_id": credID,
			"env_var_name":  env,
			"priority":      0,
		})
		if err != nil {
			return fmt.Errorf("agent %q: bind env_ref %q: %w", agentSlug, env, err)
		}
		// 409 = already bound — accept and move on.
		if resp != nil && resp.StatusCode == 409 {
			continue
		}
		if err := checkStatus(resp, fmt.Sprintf("bind credential %q to agent %q", env, agentSlug)); err != nil {
			return err
		}
	}
	return nil
}

// agentListSkillSlugs fetches the slugs of every skill currently
// bound to one agent. Used by ExportAgents to fold bindings back into
// the AgentSpec.Skills slice. Best-effort: a network blip on one
// agent shouldn't kill a workspace-wide export.
func agentListSkillSlugs(ctx context.Context, c internalapi.Client, agentID string) ([]string, error) {
	resp, err := c.Get(ctx, "/api/v1/agents/"+agentID+"/skills")
	if err != nil {
		return nil, err
	}
	if err := checkStatus(resp, "list agent skills"); err != nil {
		return nil, err
	}
	body, err := readAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if len(body) == 0 {
		return nil, nil
	}
	// The bindings endpoint returns the join row plus a nested skill
	// object. We only need skill.slug for round-trip.
	var rows []struct {
		Skill struct {
			Slug string `json:"slug"`
		} `json:"skill"`
	}
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		if r.Skill.Slug != "" {
			out = append(out, r.Skill.Slug)
		}
	}
	sort.Strings(out) // deterministic export
	return out, nil
}

// agentListCredentialEnvNames fetches the env_var_name of every
// credential currently bound to one agent. We use env_var_name (the
// per-binding label) rather than the underlying credential.name
// because the manifest treats env-var as the user-facing identifier
// and the two are conventionally identical.
func agentListCredentialEnvNames(ctx context.Context, c internalapi.Client, agentID string) ([]string, error) {
	resp, err := c.Get(ctx, "/api/v1/agents/"+agentID+"/credentials")
	if err != nil {
		return nil, err
	}
	if err := checkStatus(resp, "list agent credentials"); err != nil {
		return nil, err
	}
	body, err := readAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if len(body) == 0 {
		return nil, nil
	}
	var rows []struct {
		EnvVarName string `json:"env_var_name"`
	}
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		if r.EnvVarName != "" {
			out = append(out, r.EnvVarName)
		}
	}
	sort.Strings(out)
	return out, nil
}

// agentCreatedResponse is the subset of the POST /api/v1/agents
// response we care about. The full payload is much larger (counts,
// timestamps, …) but Exec only needs the id for the follow-on
// bindings.
type agentCreatedResponse struct {
	ID string `json:"id"`
}

// agentDecodeCreateResponse pulls the id from the create handler's
// JSON body. Tolerant of an empty body so misbehaving fakes in tests
// (or a future server that returns 201 with no body) don't trip a
// nil-reader panic — the caller's "missing id" check catches it
// downstream with a clearer message.
func agentDecodeCreateResponse(r io.Reader) (*agentCreatedResponse, error) {
	if r == nil {
		return &agentCreatedResponse{}, nil
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return &agentCreatedResponse{}, nil
	}
	out := &agentCreatedResponse{}
	if err := json.Unmarshal(data, out); err != nil {
		return nil, err
	}
	return out, nil
}
