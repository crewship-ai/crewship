// Package kinds — kind: Integration.
//
// This file implements the declarative manifest kind for MCP server
// integrations. An integration is one connected MCP endpoint — either a
// remote streamable-http URL (e.g. Linear's hosted MCP) or a locally
// spawned stdio process (e.g. `npx -y @some/mcp-server`). The handler
// stores rows in either `workspace_mcp_servers` (workspace-scoped, all
// crews can reference them) or `crew_mcp_servers` (crew-scoped, only
// agents of that crew see them). The manifest layer surfaces both with a
// single `scope:` discriminator.
//
// Relationship to the existing inline `kind: Crew` MCP block
// ----------------------------------------------------------
//
// The legacy MCPServer type in internal/manifest/schema.go is nested
// under CrewSpec.MCPServers and is created/diffed by the apply path in
// plan.go. That code stays put — it's still the most ergonomic shape
// for operators who bundle integrations with their crew definitions.
// kind: Integration is the AUTHORING SURFACE for the standalone case:
// teams that want one integration declared once and shared across many
// crews (workspace scope), or want to declare a crew override outside
// the bulky Crew document.
//
// REST surface this kind uses
// ---------------------------
//
//	Workspace scope:
//	  POST   /api/v1/integrations
//	  GET    /api/v1/integrations
//	  PATCH  /api/v1/integrations/{integrationId}
//	  DELETE /api/v1/integrations/{integrationId}
//
//	Crew scope:
//	  POST   /api/v1/crews/{crewId}/integrations
//	  GET    /api/v1/crews/{crewId}/integrations
//	  PATCH  /api/v1/crews/{crewId}/integrations/{integrationId}
//	  DELETE /api/v1/crews/{crewId}/integrations/{integrationId}
//
// Wire-shape translation
// ----------------------
//
// The server stores `args` as a JSON-encoded STRING in the `args_json`
// column (not a JSON array on the wire). Same story for `env_json` —
// it's a JSON-encoded map[string]string string. The manifest authors as
// proper YAML lists/maps (humane); buildCreateBody / buildUpdatePatch
// here marshal those into the JSON strings the handler request types
// expect (see createWorkspaceIntegrationRequest.ArgsJSON,
// updateIntegrationRequest.EnvJSON — both `*string`).
//
// env_mapping vs env semantics
// ----------------------------
//
// The manifest exposes two env-related maps:
//
//   - env: plain static environment variables for the MCP process
//     (e.g. NODE_ENV=production). The value is the literal string the
//     process sees.
//   - env_mapping: the indirection layer. Keys are the env-var name
//     the MCP server EXPECTS; values are the workspace credential's
//     env_var_name (which is conventionally identical to the key but
//     can differ — e.g. the recipes path uses {GITHUB_PERSONAL_ACCESS_TOKEN:
//     GH_TOKEN}). The runtime resolver in agent_config.go reads these
//     pairs from `env_json`, looks up the credential by name, and
//     substitutes the resolved value before the MCP process starts.
//
// Both end up merged into the SAME env_json column. The convention:
// env values win on key collision (a literal "production" beats a
// credential reference for NODE_ENV) — that matches the recipes path
// at internal/api/recipes.go where EnvMapping populates env_json
// directly and no separate static-env field exists.
//
// All non-exported helpers carry an `integration` prefix to avoid
// collisions with the crew/agent/label helpers already in this
// package (Go would otherwise emit duplicate-symbol errors at link
// time for shared names like `listAll` or `checkStatus`).
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

// integrationAPIVersion is the only apiVersion this kind accepts.
// Future versions get their own constant + parse fork so we never
// silently downgrade a v2 manifest to v1 semantics.
const integrationAPIVersion = "crewship/v1"

// integrationKind is the literal `kind:` value used in YAML envelopes
// and recorded on PlanItem.Kind for CLI plan output.
const integrationKind = "Integration"

// integrationScopeWorkspace / integrationScopeCrew are the only two
// values accepted for spec.scope. The empty string is treated as
// "workspace" in Validate so the simplest possible document
// (`kind: Integration` + name + transport + endpoint) implies
// workspace scope.
const (
	integrationScopeWorkspace = "workspace"
	integrationScopeCrew      = "crew"
)

// integrationTransportHTTP / integrationTransportStdio are the two
// transports the server validates. Mirrored from the
// internal/api/workspace_integrations.go handler check.
const (
	integrationTransportHTTP  = "streamable-http"
	integrationTransportStdio = "stdio"
)

// ── Types ───────────────────────────────────────────────────────────────────

// IntegrationSpec is the shape under `spec:` for kind: Integration.
//
// Most fields are optional; Validate enforces transport+scope coherence
// and Plan emits only the fields the user actually declared so an
// unset Icon won't overwrite an icon set via the UI.
type IntegrationSpec struct {
	// Scope picks the table the row lands in. One of "workspace" |
	// "crew". Empty defaults to "workspace" — that matches the most
	// common case (operators sharing one integration across crews).
	Scope string `yaml:"scope,omitempty" json:"scope,omitempty"`

	// CrewSlug names the parent crew when Scope == "crew". REQUIRED in
	// that mode, IGNORED otherwise (Validate enforces — a CrewSlug set
	// with scope=workspace is a typo we surface loudly rather than
	// silently dropping).
	CrewSlug string `yaml:"crew_slug,omitempty" json:"crew_slug,omitempty"`

	// DisplayName is the human-facing label shown in the UI. Defaults
	// to metadata.name on the server when omitted; we mirror that
	// default at Create time so the round-trip Plan after Create sees
	// the same value the server records.
	DisplayName string `yaml:"display_name,omitempty" json:"display_name,omitempty"`

	// Transport selects the MCP wire protocol. One of "streamable-http"
	// | "stdio". REQUIRED — the server has no sensible default for
	// "what kind of MCP server is this".
	Transport string `yaml:"transport" json:"transport"`

	// Endpoint is the remote URL for streamable-http transport.
	// REQUIRED when Transport == "streamable-http"; IGNORED otherwise.
	Endpoint string `yaml:"endpoint,omitempty" json:"endpoint,omitempty"`

	// Command is the executable for stdio transport. REQUIRED when
	// Transport == "stdio"; IGNORED otherwise.
	Command string `yaml:"command,omitempty" json:"command,omitempty"`

	// Args are the positional CLI arguments passed to Command. Only
	// meaningful when Transport == "stdio". Marshaled to the
	// args_json string at Plan time.
	Args []string `yaml:"args,omitempty" json:"args,omitempty"`

	// Env carries plain (literal) environment variables for the MCP
	// process. Merged with EnvMapping into env_json at Plan time; Env
	// values WIN on key collision so a literal "production" beats a
	// credential reference for NODE_ENV.
	Env map[string]string `yaml:"env,omitempty" json:"env,omitempty"`

	// EnvMapping is the credential-indirection layer. Keys are the
	// env-var the MCP process expects; values are the workspace
	// credential's `name` (== env_var_name convention). At agent run
	// time the resolver looks up each credential by name and
	// substitutes the value before the MCP process starts. Merged
	// with Env into env_json — see Env above for collision rules.
	EnvMapping map[string]string `yaml:"env_mapping,omitempty" json:"env_mapping,omitempty"`

	// Icon is an optional lucide-react slug (e.g. "linear"). The
	// server stores verbatim; the UI maps the string to an icon
	// component or falls back to a generic placeholder.
	Icon string `yaml:"icon,omitempty" json:"icon,omitempty"`

	// Enabled toggles the runtime connect. Pointer so we can
	// distinguish "user didn't say" (server default = true) from
	// "user said false" (skip connect). The YAML decoder fills a
	// non-nil *bool only when the key was present.
	Enabled *bool `yaml:"enabled,omitempty" json:"enabled,omitempty"`
}

// IntegrationDocument is the YAML envelope produced/consumed by the
// manifest pipeline for kind: Integration. metadata.slug is the
// workspace-unique idempotency key — Plan keys on it (filtered by
// scope) to decide Create vs Update.
//
// Convention: metadata.slug == metadata.name == the MCP server's
// `name` column. The server keys uniqueness on `name` within the
// (workspace, crew) tuple; the manifest mirrors that by enforcing
// slug == name in Validate. Cross-kind references (a future kind
// that wants to link an agent to an integration) reach for the slug,
// so the duplication keeps the lookup surface uniform.
type IntegrationDocument struct {
	APIVersion string               `yaml:"apiVersion" json:"apiVersion"`
	Kind       string               `yaml:"kind"       json:"kind"`
	Metadata   internalapi.Metadata `yaml:"metadata"   json:"metadata"`
	Spec       IntegrationSpec      `yaml:"spec"       json:"spec"`
}

// IntegrationRemote captures one row of either GET /api/v1/integrations
// (workspace scope) or GET /api/v1/crews/{crewId}/integrations (crew
// scope). The two endpoints emit nearly identical shapes; the only
// difference is the crew response has CrewID + WorkspaceMCPServerID
// columns the workspace response lacks.
//
// We model both with one struct + a `Scope` discriminator so Plan can
// take a single *IntegrationRemote and route on it. The CrewID field
// stays empty for workspace rows; WorkspaceMCPServerID stays nil for
// both (the manifest doesn't expose linking-to-workspace-integration
// from the crew row — that's a runtime concept, not a declaration).
//
// Args/Env are stored as JSON strings on the server. We KEEP them as
// strings in this struct (rather than decoding to slice/map) so the
// diff path can do a normalised string comparison via the shared
// jsonStringEqual helper (defined in crew.go) — that avoids
// false-positive drift from key reordering.
type IntegrationRemote struct {
	ID                   string  `json:"id"`
	WorkspaceID          string  `json:"workspace_id,omitempty"`
	CrewID               string  `json:"crew_id,omitempty"`
	WorkspaceMCPServerID *string `json:"workspace_mcp_server_id,omitempty"`
	Name                 string  `json:"name"`
	DisplayName          string  `json:"display_name"`
	Transport            string  `json:"transport"`
	Endpoint             *string `json:"endpoint"`
	Command              *string `json:"command"`
	ArgsJSON             *string `json:"args_json"`
	EnvJSON              *string `json:"env_json"`
	ConfigJSON           *string `json:"config_json"`
	Icon                 *string `json:"icon"`
	Enabled              bool    `json:"enabled"`

	// Scope is filled in by the lookup helpers so Plan can decide
	// which REST surface to PATCH against without re-asking the
	// document. NOT decoded from the JSON response (the server
	// doesn't emit it as a column); set explicitly by
	// listWorkspaceIntegrations / listCrewIntegrations.
	Scope string `json:"-"`
}

// integrationCrewStub is the minimal shape this kind needs from
// GET /api/v1/crews to resolve crew_slug → crew_id. Defined locally so
// the file stays self-contained — cross-kind imports inside the kinds
// package create initialisation-ordering surprises and break test
// isolation. The agent.go file has its own agentCrewStub for the
// same reason.
type integrationCrewStub struct {
	ID   string `json:"id"`
	Slug string `json:"slug"`
	Name string `json:"name"`
}

// ── Validate ────────────────────────────────────────────────────────────────

// Validate enforces the structural rules without any HTTP round-trip:
//
//   - apiVersion must equal "crewship/v1" when present
//   - kind must equal "Integration" when present
//   - metadata.name + metadata.slug REQUIRED
//   - slug must equal name (the cross-kind lookup convention; the
//     server itself only keys on `name` but every other kind that
//     might reference an integration uses slug as the lookup key)
//   - transport REQUIRED, must be one of streamable-http | stdio
//   - streamable-http requires a non-empty endpoint
//   - stdio requires a non-empty command
//   - scope, if set, must be workspace | crew
//   - crew_slug REQUIRED iff scope == crew; REJECTED otherwise
//   - env / env_mapping values must be plain strings (the YAML
//     decoder enforces shape; we add a no-empty-key check)
//   - if wsCtx is populated, crew_slug (when set) must reference a
//     declared or remote crew
//
// We deliberately do NOT round-trip the assembled args_json /
// env_json strings through json.Unmarshal here — Validate runs
// offline and the marshaling is straightforward enough that a
// failure would be a Go-side panic, not user input.
func (d *IntegrationDocument) Validate(wsCtx internalapi.WorkspaceContext) error {
	if d.APIVersion != "" && d.APIVersion != integrationAPIVersion {
		return fmt.Errorf("integration %q: apiVersion %q must be %q",
			d.Metadata.Slug, d.APIVersion, integrationAPIVersion)
	}
	if d.Kind != "" && d.Kind != integrationKind {
		return fmt.Errorf("integration %q: kind %q must be %q",
			d.Metadata.Slug, d.Kind, integrationKind)
	}
	if strings.TrimSpace(d.Metadata.Name) == "" {
		return fmt.Errorf("integration %q: metadata.name is required", d.Metadata.Slug)
	}
	if strings.TrimSpace(d.Metadata.Slug) == "" {
		return fmt.Errorf("integration: metadata.slug is required")
	}
	// slug == name is the load-bearing rule that lets future kinds
	// reference integrations by slug even though the backend keys on
	// name. Surface both values so the user can fix either side
	// without guessing which one drifted (label.go uses the same
	// pattern for the same reason).
	if d.Metadata.Slug != d.Metadata.Name {
		return fmt.Errorf("integration %q: metadata.slug must equal metadata.name (got slug=%q, name=%q)",
			d.Metadata.Name, d.Metadata.Slug, d.Metadata.Name)
	}

	// Transport: required, enum-checked. The server itself defaults
	// to "streamable-http" when missing (see workspace_integrations.go),
	// but a manifest that omits transport is almost certainly a
	// thinko — fail loudly rather than ship a surprise default.
	if strings.TrimSpace(d.Spec.Transport) == "" {
		return fmt.Errorf("integration %q: spec.transport is required (one of %q, %q)",
			d.Metadata.Slug, integrationTransportHTTP, integrationTransportStdio)
	}
	if d.Spec.Transport != integrationTransportHTTP && d.Spec.Transport != integrationTransportStdio {
		return fmt.Errorf("integration %q: spec.transport %q must be %q or %q",
			d.Metadata.Slug, d.Spec.Transport, integrationTransportHTTP, integrationTransportStdio)
	}

	// Transport-specific REQUIRED fields. Catching these here means
	// the operator sees a clear "missing endpoint" message in the
	// same pass that lists other validation issues, instead of a 400
	// halfway through Apply.
	if d.Spec.Transport == integrationTransportHTTP {
		if strings.TrimSpace(d.Spec.Endpoint) == "" {
			return fmt.Errorf("integration %q: spec.endpoint is required for transport %q",
				d.Metadata.Slug, integrationTransportHTTP)
		}
	}
	if d.Spec.Transport == integrationTransportStdio {
		if strings.TrimSpace(d.Spec.Command) == "" {
			return fmt.Errorf("integration %q: spec.command is required for transport %q",
				d.Metadata.Slug, integrationTransportStdio)
		}
	}

	// Scope: optional with default. Reject anything that's not
	// "workspace" or "crew" so a typo (scope: workspaces) is caught
	// at validate time rather than silently defaulting to workspace
	// and surprising the user when the manifest stops working after
	// they fix the typo (because the new value is now respected).
	scope := d.Spec.Scope
	if scope == "" {
		scope = integrationScopeWorkspace
	}
	if scope != integrationScopeWorkspace && scope != integrationScopeCrew {
		return fmt.Errorf("integration %q: spec.scope %q must be %q or %q",
			d.Metadata.Slug, d.Spec.Scope, integrationScopeWorkspace, integrationScopeCrew)
	}

	// crew_slug coherence with scope. Both halves of the rule are
	// checked: missing crew_slug under scope=crew is a hard error;
	// crew_slug set under scope=workspace is also rejected because
	// it's almost certainly an authoring mistake (the user meant
	// scope=crew or meant to delete the crew_slug line).
	if scope == integrationScopeCrew && strings.TrimSpace(d.Spec.CrewSlug) == "" {
		return fmt.Errorf("integration %q: spec.crew_slug is required when spec.scope is %q",
			d.Metadata.Slug, integrationScopeCrew)
	}
	if scope == integrationScopeWorkspace && strings.TrimSpace(d.Spec.CrewSlug) != "" {
		return fmt.Errorf("integration %q: spec.crew_slug must be empty when spec.scope is %q (set scope: %q to attach to a crew)",
			d.Metadata.Slug, integrationScopeWorkspace, integrationScopeCrew)
	}

	// FK: crew_slug must be in the declared+remote crew universe IF
	// wsCtx carries any crew data at all. Same degradation policy as
	// agent.go — an empty wsCtx (Validate invoked in isolation, e.g.
	// unit tests for the document itself) skips the FK check; Plan
	// will fail at the resolution step if the slug is bogus.
	if scope == integrationScopeCrew &&
		(len(wsCtx.DeclaredCrews) > 0 || len(wsCtx.RemoteCrews) > 0) &&
		!wsCtx.HasCrew(d.Spec.CrewSlug) {
		return fmt.Errorf("integration %q: spec.crew_slug %q does not reference any declared or remote crew",
			d.Metadata.Slug, d.Spec.CrewSlug)
	}

	// Env / env_mapping shape sanity. The YAML decoder enforces the
	// map[string]string type, but a key like "" (empty) or "   "
	// (whitespace) would silently pass and surface later as an
	// invalid env var name on the MCP process side. Reject up front.
	for k := range d.Spec.Env {
		if strings.TrimSpace(k) == "" {
			return fmt.Errorf("integration %q: spec.env has an empty key", d.Metadata.Slug)
		}
	}
	for k, v := range d.Spec.EnvMapping {
		if strings.TrimSpace(k) == "" {
			return fmt.Errorf("integration %q: spec.env_mapping has an empty key", d.Metadata.Slug)
		}
		if strings.TrimSpace(v) == "" {
			return fmt.Errorf("integration %q: spec.env_mapping[%q] is empty (must reference a credential's env_var_name)",
				d.Metadata.Slug, k)
		}
	}

	// Args: stdio-only sanity. We don't reject Args under
	// streamable-http (the server silently drops them — it's an
	// "ignored" field, not an "invalid" one), but no-empty-entry on
	// each arg catches the common shell-quoting mistake where a YAML
	// list ended up with a trailing blank entry.
	for i, a := range d.Spec.Args {
		if a == "" {
			return fmt.Errorf("integration %q: spec.args[%d] is empty", d.Metadata.Slug, i)
		}
	}

	return nil
}

// ── Plan ────────────────────────────────────────────────────────────────────

// Plan compares the declared integration against `remote` (nil = not
// yet on server) and returns the plan items the apply loop should
// execute. A single PlanItem is returned even for the Unchanged case
// so the CLI can count "0 changed, 1 unchanged" truthfully.
//
// Scope-change handling
// ---------------------
//
// When the declared scope differs from the remote row's scope
// (workspace → crew or crew → workspace), the two REST surfaces are
// different tables on the server side. There's no "move" endpoint —
// you have to DELETE from one and CREATE in the other. We emit TWO
// plan items in that case so the apply log shows the replace
// explicitly; the operator sees the deletion in the dry-run and can
// reconsider before re-running with --yes. The description on each
// item calls out "scope change" so it's obvious in the plan output.
//
// Crew-scope plans need a resolved crew_id before they can dispatch.
// We look that up via the live client (mirroring agent.go's
// LookupCrewIDBySlug pattern) so a typo in crew_slug fails loud at
// Plan time rather than silently landing the integration on the
// wrong crew.
func (d *IntegrationDocument) Plan(ctx context.Context, c internalapi.Client, remote *IntegrationRemote) ([]internalapi.PlanItem, error) {
	scope := d.Spec.Scope
	if scope == "" {
		scope = integrationScopeWorkspace
	}

	// Resolve crew_id up front for crew-scoped plans. We need it for
	// the create body (POST /api/v1/crews/{crewId}/integrations
	// expects {crewId} in the path) and for the update path
	// (similarly path-scoped). Doing the lookup once at Plan time
	// keeps the per-Exec closures simple and lets us surface a
	// crew-slug typo before any mutation runs.
	var crewID string
	if scope == integrationScopeCrew {
		id, err := integrationLookupCrewIDBySlug(ctx, c, d.Spec.CrewSlug)
		if err != nil {
			return nil, fmt.Errorf("integration %q: resolve crew_slug: %w", d.Metadata.Slug, err)
		}
		crewID = id
	}

	// Scope-change case: remote exists but on the OTHER table. Emit
	// a Delete + Create pair so the dry-run shows both halves. We
	// also flag it loudly in the description because it's destructive
	// (any agent bindings on the remote row get cascaded by the
	// server-side DELETE handler).
	if remote != nil && remote.Scope != "" && remote.Scope != scope {
		delItem := integrationDeleteItem(remote, "scope change "+remote.Scope+" → "+scope)
		createItem, err := d.createPlanItem(crewID, scope)
		if err != nil {
			return nil, err
		}
		return []internalapi.PlanItem{delItem, createItem}, nil
	}

	// Create case: no remote on this scope. Build a single Create
	// item; the Exec closure POSTs to the workspace OR crew endpoint
	// depending on scope.
	if remote == nil {
		item, err := d.createPlanItem(crewID, scope)
		if err != nil {
			return nil, err
		}
		return []internalapi.PlanItem{item}, nil
	}

	// Update case: remote on the matching scope, compute a sparse
	// patch. The patch is empty when the manifest matches the server
	// exactly — Plan turns that into an Unchanged item.
	patch, err := d.updatePatch(remote)
	if err != nil {
		return nil, fmt.Errorf("integration %q: build update patch: %w", d.Metadata.Slug, err)
	}
	if len(patch) == 0 {
		return []internalapi.PlanItem{{
			Kind:        "integration",
			Slug:        d.Metadata.Slug,
			Action:      internalapi.ActionUnchanged,
			Description: fmt.Sprintf("integration %q already matches manifest", d.Metadata.Slug),
		}}, nil
	}

	// Capture the path components by value so the Exec closure
	// doesn't capture the document by reference (a future caller
	// might mutate it after Plan returns).
	intID := remote.ID
	crewIDForPath := remote.CrewID
	slug := d.Metadata.Slug

	return []internalapi.PlanItem{{
		Kind:        "integration",
		Slug:        slug,
		Action:      internalapi.ActionUpdate,
		Description: fmt.Sprintf("update integration %q (%s scope, %d field(s))", slug, scope, len(patch)),
		Exec: func(ctx context.Context, c internalapi.Client) error {
			path := integrationPatchPath(scope, crewIDForPath, intID)
			resp, err := c.Patch(ctx, path, patch)
			if err != nil {
				return fmt.Errorf("PATCH %s: %w", path, err)
			}
			return checkStatus(resp, "update integration "+slug)
		},
	}}, nil
}

// createPlanItem builds the single Create PlanItem used by both the
// pure-create path and the second half of the scope-change replace.
// Extracted so the closure capture rules stay consistent across the
// two call sites.
func (d *IntegrationDocument) createPlanItem(crewID, scope string) (internalapi.PlanItem, error) {
	body, err := d.createBody()
	if err != nil {
		return internalapi.PlanItem{}, fmt.Errorf("integration %q: assemble create body: %w", d.Metadata.Slug, err)
	}
	slug := d.Metadata.Slug
	desc := fmt.Sprintf("create integration %q (%s scope, transport=%s)", slug, scope, d.Spec.Transport)
	if scope == integrationScopeCrew {
		desc = fmt.Sprintf("create integration %q (crew=%s, transport=%s)", slug, d.Spec.CrewSlug, d.Spec.Transport)
	}

	return internalapi.PlanItem{
		Kind:        "integration",
		Slug:        slug,
		Action:      internalapi.ActionCreate,
		Description: desc,
		Exec: func(ctx context.Context, c internalapi.Client) error {
			path := integrationCreatePath(scope, crewID)
			resp, err := c.Post(ctx, path, body)
			if err != nil {
				return fmt.Errorf("POST %s: %w", path, err)
			}
			return checkStatus(resp, "create integration "+slug)
		},
	}, nil
}

// integrationDeleteItem builds a Delete PlanItem from a remote row.
// Used by the scope-change replace path. The reason string is folded
// into the description so the dry-run output explains WHY the deletion
// is happening (otherwise an operator skimming `crewship plan` sees
// "delete integration linear" with no context).
func integrationDeleteItem(remote *IntegrationRemote, reason string) internalapi.PlanItem {
	slug := remote.Name
	intID := remote.ID
	crewID := remote.CrewID
	scope := remote.Scope
	return internalapi.PlanItem{
		Kind:        "integration",
		Slug:        slug,
		Action:      internalapi.ActionDelete,
		Description: fmt.Sprintf("delete integration %q (%s)", slug, reason),
		Exec: func(ctx context.Context, c internalapi.Client) error {
			path := integrationDeletePath(scope, crewID, intID)
			resp, err := c.Delete(ctx, path)
			if err != nil {
				return fmt.Errorf("DELETE %s: %w", path, err)
			}
			return checkStatus(resp, "delete integration "+slug)
		},
	}
}

// ── Body builders ───────────────────────────────────────────────────────────

// createBody renders the POST body for either /api/v1/integrations
// (workspace scope) or /api/v1/crews/{crewId}/integrations (crew
// scope). The two endpoints accept the SAME request shape modulo the
// workspace_mcp_server_id field that only the crew endpoint
// understands — we don't expose that field in the manifest (it's a
// runtime linking concept) so the body is identical for both.
//
// Args are JSON-marshaled into args_json; Env + EnvMapping are merged
// (Env wins on collision — see package comment) and marshaled into
// env_json. Both fields end up as JSON strings, matching the handler
// request types' `*string` declarations.
func (d *IntegrationDocument) createBody() (map[string]any, error) {
	body := map[string]any{
		"name":      d.Metadata.Name,
		"transport": d.Spec.Transport,
	}
	if d.Spec.DisplayName != "" {
		body["display_name"] = d.Spec.DisplayName
	} else {
		// Mirror the server's default-to-name behaviour so the
		// round-trip Plan after Create doesn't see a phantom drift
		// (server stores "Linear" because name was "linear"; our
		// next diff against a manifest without DisplayName would
		// emit a no-op patch otherwise).
		body["display_name"] = d.Metadata.Name
	}
	if d.Spec.Transport == integrationTransportHTTP && d.Spec.Endpoint != "" {
		body["endpoint"] = d.Spec.Endpoint
	}
	if d.Spec.Transport == integrationTransportStdio && d.Spec.Command != "" {
		body["command"] = d.Spec.Command
	}
	if len(d.Spec.Args) > 0 {
		argsJSON, err := json.Marshal(d.Spec.Args)
		if err != nil {
			return nil, fmt.Errorf("marshal args: %w", err)
		}
		body["args_json"] = string(argsJSON)
	}
	envMap := integrationMergedEnv(d.Spec.Env, d.Spec.EnvMapping)
	if len(envMap) > 0 {
		envJSON, err := json.Marshal(envMap)
		if err != nil {
			return nil, fmt.Errorf("marshal env: %w", err)
		}
		body["env_json"] = string(envJSON)
	}
	if d.Spec.Icon != "" {
		body["icon"] = d.Spec.Icon
	}
	// Honor spec.enabled at create time. Skipping this for the
	// nil pointer (= field not declared) lets the server's default
	// (enabled=true) apply; honoring it for non-nil means a manifest
	// with explicit `enabled: false` lands disabled on first apply
	// instead of converging only on a follow-up PATCH.
	if d.Spec.Enabled != nil {
		body["enabled"] = *d.Spec.Enabled
	}
	// Crew-scoped Create handler also accepts env_mapping as a
	// distinct field per task spec — but the workspace handler
	// silently drops it (readJSON tolerates extra keys). Sending it
	// on both paths costs nothing and makes the body shape uniform.
	if len(d.Spec.EnvMapping) > 0 {
		body["env_mapping"] = d.Spec.EnvMapping
	}
	return body, nil
}

// updatePatch returns ONLY the fields whose declared value differs
// from `remote`. Empty declared fields are skipped — they mean
// "leave server value alone" — so a manifest that omits Icon won't
// blank out an icon a UI user picked after the initial apply.
//
// args_json / env_json comparisons normalise via jsonStringEqual
// (defined in crew.go) so a server-side re-emission with different
// key order doesn't trigger a phantom drift.
func (d *IntegrationDocument) updatePatch(remote *IntegrationRemote) (map[string]any, error) {
	patch := map[string]any{}

	if d.Spec.DisplayName != "" && d.Spec.DisplayName != remote.DisplayName {
		patch["display_name"] = d.Spec.DisplayName
	}
	if d.Spec.Transport != "" && d.Spec.Transport != remote.Transport {
		patch["transport"] = d.Spec.Transport
	}
	// Transport-specific: only diff endpoint when streamable-http;
	// only diff command when stdio. The server tolerates a stray
	// endpoint under stdio (it's just an unused column), but emitting
	// it would re-set a value that the user is in the process of
	// migrating away from.
	if d.Spec.Transport == integrationTransportHTTP {
		if d.Spec.Endpoint != "" && d.Spec.Endpoint != deref(remote.Endpoint) {
			patch["endpoint"] = d.Spec.Endpoint
		}
	}
	if d.Spec.Transport == integrationTransportStdio {
		if d.Spec.Command != "" && d.Spec.Command != deref(remote.Command) {
			patch["command"] = d.Spec.Command
		}
		if len(d.Spec.Args) > 0 {
			argsJSON, err := json.Marshal(d.Spec.Args)
			if err != nil {
				return nil, fmt.Errorf("marshal args: %w", err)
			}
			if !jsonStringEqual(string(argsJSON), deref(remote.ArgsJSON)) {
				patch["args_json"] = string(argsJSON)
			}
		}
	}
	envMap := integrationMergedEnv(d.Spec.Env, d.Spec.EnvMapping)
	if len(envMap) > 0 {
		envJSON, err := json.Marshal(envMap)
		if err != nil {
			return nil, fmt.Errorf("marshal env: %w", err)
		}
		if !jsonStringEqual(string(envJSON), deref(remote.EnvJSON)) {
			patch["env_json"] = string(envJSON)
		}
	}
	if d.Spec.Icon != "" && d.Spec.Icon != deref(remote.Icon) {
		patch["icon"] = d.Spec.Icon
	}
	if d.Spec.Enabled != nil && *d.Spec.Enabled != remote.Enabled {
		patch["enabled"] = *d.Spec.Enabled
	}
	return patch, nil
}

// integrationMergedEnv combines the literal `env` map with the
// credential-reference `env_mapping` map into a single env_json
// payload. EnvMapping values are written first so a literal Env
// entry for the same key WINS — that matches the package-comment
// contract and lets operators override one slot of an otherwise
// credential-driven env block.
//
// Returns nil for an entirely empty input so callers can no-op on
// `if len(envMap) == 0` without distinguishing nil from
// empty-but-allocated.
func integrationMergedEnv(env, envMapping map[string]string) map[string]string {
	if len(env) == 0 && len(envMapping) == 0 {
		return nil
	}
	out := make(map[string]string, len(env)+len(envMapping))
	for k, v := range envMapping {
		out[k] = v
	}
	for k, v := range env {
		out[k] = v
	}
	return out
}

// integrationCreatePath returns the correct POST URL for the given
// scope. crewID is ignored for workspace scope.
func integrationCreatePath(scope, crewID string) string {
	if scope == integrationScopeCrew {
		return "/api/v1/crews/" + crewID + "/integrations"
	}
	return "/api/v1/integrations"
}

// integrationPatchPath returns the correct PATCH URL. crewID is
// ignored for workspace scope.
func integrationPatchPath(scope, crewID, integrationID string) string {
	if scope == integrationScopeCrew {
		return "/api/v1/crews/" + crewID + "/integrations/" + integrationID
	}
	return "/api/v1/integrations/" + integrationID
}

// integrationDeletePath returns the correct DELETE URL. crewID is
// ignored for workspace scope.
func integrationDeletePath(scope, crewID, integrationID string) string {
	if scope == integrationScopeCrew {
		return "/api/v1/crews/" + crewID + "/integrations/" + integrationID
	}
	return "/api/v1/integrations/" + integrationID
}

// ── Remote lookup ───────────────────────────────────────────────────────────

// LookupIntegrationRemoteBySlug fetches the live state of one
// integration by slug. Scope picks which table to query; the helper
// returns (nil, nil) when no row matches so Plan can treat that as
// ActionCreate.
//
// For scope=crew the helper needs crewSlug → crew_id resolution
// first (the crew list endpoint is the canonical mapping). For
// scope=workspace the workspace list endpoint is queried directly.
//
// Cost: one GET per call (plus one extra GET /api/v1/crews for the
// crew-scope variant). Per-workspace lists are small enough that we
// don't paginate; apply pipelines that look up many integrations in
// a row should cache the list themselves — this helper stays
// independent so a kind can call it without coordination.
func LookupIntegrationRemoteBySlug(ctx context.Context, c internalapi.Client, slug, scope, crewSlug string) (*IntegrationRemote, error) {
	if strings.TrimSpace(slug) == "" {
		return nil, fmt.Errorf("integration slug is required")
	}
	if scope == "" {
		scope = integrationScopeWorkspace
	}
	switch scope {
	case integrationScopeWorkspace:
		rows, err := integrationListWorkspace(ctx, c)
		if err != nil {
			return nil, err
		}
		for i := range rows {
			if rows[i].Name == slug {
				row := rows[i]
				row.Scope = integrationScopeWorkspace
				return &row, nil
			}
		}
		return nil, nil
	case integrationScopeCrew:
		if strings.TrimSpace(crewSlug) == "" {
			return nil, fmt.Errorf("integration %q: crew_slug is required for crew-scope lookup", slug)
		}
		crewID, err := integrationLookupCrewIDBySlug(ctx, c, crewSlug)
		if err != nil {
			return nil, err
		}
		rows, err := integrationListCrew(ctx, c, crewID)
		if err != nil {
			return nil, err
		}
		for i := range rows {
			if rows[i].Name == slug {
				row := rows[i]
				row.Scope = integrationScopeCrew
				row.CrewID = crewID
				return &row, nil
			}
		}
		return nil, nil
	default:
		return nil, fmt.Errorf("integration %q: unknown scope %q", slug, scope)
	}
}

// integrationLookupCrewIDBySlug resolves a crew slug to its CUID.
// Returns a not-found error when no row matches; the caller decorates
// with "integration %q:" context. Mirrors agent.go's LookupCrewIDBySlug
// (we don't reuse the agent helper because importing across kinds
// within this package creates a hard ordering dependency that's
// easier to avoid by duplicating the 10 lines).
func integrationLookupCrewIDBySlug(ctx context.Context, c internalapi.Client, slug string) (string, error) {
	crews, err := integrationListCrews(ctx, c)
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

// integrationListWorkspace pulls GET /api/v1/integrations and decodes
// the rows. Tolerates both a flat array and a {integrations:[...]}
// wrapper for forward compatibility with a future paginated list
// response (the current handler returns the flat shape).
func integrationListWorkspace(ctx context.Context, c internalapi.Client) ([]IntegrationRemote, error) {
	resp, err := c.Get(ctx, "/api/v1/integrations")
	if err != nil {
		return nil, fmt.Errorf("GET /api/v1/integrations: %w", err)
	}
	if err := checkStatus(resp, "list workspace integrations"); err != nil {
		return nil, err
	}
	return integrationDecodeList(resp.Body)
}

// integrationListCrew pulls GET /api/v1/crews/{crewId}/integrations
// and decodes the rows. Same shape-tolerance as the workspace lister.
func integrationListCrew(ctx context.Context, c internalapi.Client, crewID string) ([]IntegrationRemote, error) {
	resp, err := c.Get(ctx, "/api/v1/crews/"+crewID+"/integrations")
	if err != nil {
		return nil, fmt.Errorf("GET /api/v1/crews/%s/integrations: %w", crewID, err)
	}
	if err := checkStatus(resp, "list crew integrations"); err != nil {
		return nil, err
	}
	rows, err := integrationDecodeList(resp.Body)
	if err != nil {
		return nil, err
	}
	// The crew endpoint returns rows without a stable crew_id field
	// in some response shapes (the handler embeds it but the
	// manifest's IntegrationRemote treats it as omitempty). Stamp
	// the resolved crewID back onto every row so the Plan path can
	// reuse it for the PATCH URL without re-resolving.
	for i := range rows {
		if rows[i].CrewID == "" {
			rows[i].CrewID = crewID
		}
	}
	return rows, nil
}

// integrationListCrews pulls GET /api/v1/crews and decodes the
// minimal shape we need for slug↔id round-tripping.
func integrationListCrews(ctx context.Context, c internalapi.Client) ([]integrationCrewStub, error) {
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
	var rows []integrationCrewStub
	if err := json.Unmarshal(body, &rows); err != nil {
		// Try wrapped shape before giving up.
		var wrapped struct {
			Crews []integrationCrewStub `json:"crews"`
		}
		if err2 := json.Unmarshal(body, &wrapped); err2 != nil {
			return nil, fmt.Errorf("decode /api/v1/crews: %w", err)
		}
		return wrapped.Crews, nil
	}
	return rows, nil
}

// integrationDecodeList reads a list response body and tolerates both
// a flat array and a {integrations:[...]} wrapper. Extracted from
// the workspace/crew listers because the two share identical decode
// logic.
func integrationDecodeList(r io.Reader) ([]IntegrationRemote, error) {
	body, err := readAll(r)
	if err != nil {
		return nil, fmt.Errorf("read integrations list body: %w", err)
	}
	if len(body) == 0 {
		return nil, nil
	}
	var flat []IntegrationRemote
	if err := json.Unmarshal(body, &flat); err == nil {
		return flat, nil
	}
	var wrapped struct {
		Integrations []IntegrationRemote `json:"integrations"`
	}
	if err := json.Unmarshal(body, &wrapped); err != nil {
		return nil, fmt.Errorf("decode integrations list: %w", err)
	}
	return wrapped.Integrations, nil
}

// ── Export ──────────────────────────────────────────────────────────────────

// ExportIntegrations fetches every integration in the workspace
// (across BOTH workspace scope and every crew's crew scope) and
// renders each as an IntegrationDocument suitable for re-applying.
// The inverse of Plan/Create — fields the manifest doesn't model
// (config_json, agent binding counts, auth_status, timestamps) are
// dropped.
//
// args_json and env_json are decoded back into the typed Args slice
// and merged Env map; we don't split env_json into EnvMapping vs Env
// on export because the server has no way to tell them apart (both
// land in the same column). Round-trip behaviour: every env_json
// entry comes back under `Env` on export. Operators who want the
// EnvMapping shape preserved should keep their source-of-truth YAML
// and re-export to a side file rather than overwriting the original.
//
// Sorted by slug + scope so snapshot diffs are stable across runs.
func ExportIntegrations(ctx context.Context, c internalapi.Client) ([]*IntegrationDocument, error) {
	out := make([]*IntegrationDocument, 0)

	// Workspace scope.
	wsRows, err := integrationListWorkspace(ctx, c)
	if err != nil {
		return nil, fmt.Errorf("export integrations: list workspace: %w", err)
	}
	for _, r := range wsRows {
		doc, derr := integrationRowToDoc(&r, integrationScopeWorkspace, "")
		if derr != nil {
			// Decode failure on one row shouldn't kill the whole
			// export — log via the error path is not possible here
			// (no logger in this layer), so we drop the row silently
			// and continue. Operators see the gap when they diff
			// the exported manifest against the live workspace.
			continue
		}
		out = append(out, doc)
	}

	// Crew scope: walk every crew.
	crews, err := integrationListCrews(ctx, c)
	if err != nil {
		return nil, fmt.Errorf("export integrations: list crews: %w", err)
	}
	for _, cr := range crews {
		rows, lerr := integrationListCrew(ctx, c, cr.ID)
		if lerr != nil {
			// Per-crew list failure: skip this crew, continue with
			// the others. Same rationale as the per-row drop above —
			// a workspace-wide export shouldn't fail the whole apply
			// because one crew's integration listing 500'd.
			continue
		}
		for _, r := range rows {
			doc, derr := integrationRowToDoc(&r, integrationScopeCrew, cr.Slug)
			if derr != nil {
				continue
			}
			out = append(out, doc)
		}
	}

	// Deterministic ordering for stable diffs. Workspace rows sort
	// before crew rows (alphabetical-by-scope); within a scope, sort
	// by slug; within a crew scope, sub-sort by crew_slug so
	// per-crew sections cluster.
	sort.Slice(out, func(i, j int) bool {
		si, sj := out[i].Spec.Scope, out[j].Spec.Scope
		if si != sj {
			return si < sj
		}
		if out[i].Spec.CrewSlug != out[j].Spec.CrewSlug {
			return out[i].Spec.CrewSlug < out[j].Spec.CrewSlug
		}
		return out[i].Metadata.Slug < out[j].Metadata.Slug
	})
	return out, nil
}

// integrationRowToDoc converts one remote row into an
// IntegrationDocument. Args/Env get JSON-decoded back into typed
// slices/maps. crewSlug is only used when scope == "crew"; ignored
// otherwise.
func integrationRowToDoc(r *IntegrationRemote, scope, crewSlug string) (*IntegrationDocument, error) {
	doc := &IntegrationDocument{
		APIVersion: integrationAPIVersion,
		Kind:       integrationKind,
		Metadata: internalapi.Metadata{
			Name: r.Name,
			Slug: r.Name, // slug == name invariant
		},
		Spec: IntegrationSpec{
			Scope:       scope,
			DisplayName: r.DisplayName,
			Transport:   r.Transport,
			Endpoint:    deref(r.Endpoint),
			Command:     deref(r.Command),
			Icon:        deref(r.Icon),
		},
	}
	if scope == integrationScopeCrew {
		doc.Spec.CrewSlug = crewSlug
	}
	// Decode args_json into the typed slice. Empty / nil → nil
	// slice (Args is omitempty so it disappears from the exported
	// YAML when there's nothing to emit).
	if r.ArgsJSON != nil && *r.ArgsJSON != "" {
		var args []string
		if err := json.Unmarshal([]byte(*r.ArgsJSON), &args); err == nil && len(args) > 0 {
			doc.Spec.Args = args
		}
	}
	// Decode env_json into Env. We deliberately do NOT try to split
	// the result into Env vs EnvMapping — the server has no
	// distinguishing column, so any split would be a heuristic. See
	// the function comment for the round-trip contract.
	if r.EnvJSON != nil && *r.EnvJSON != "" {
		var env map[string]string
		if err := json.Unmarshal([]byte(*r.EnvJSON), &env); err == nil && len(env) > 0 {
			doc.Spec.Env = env
		}
	}
	// Enabled is a *bool on export so the round-trip Plan doesn't
	// fall into the "not declared, use server default" branch and
	// emit a phantom patch. Allocate a fresh bool so the pointer
	// isn't shared across docs.
	enabled := r.Enabled
	doc.Spec.Enabled = &enabled
	return doc, nil
}

// ── HTTP helpers (all integration-prefixed) ─────────────────────────────────

