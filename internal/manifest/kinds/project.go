// Package kinds holds per-kind manifest types and behaviour. Each kind
// lives in its own file (project.go, label.go, …) and depends only on
// internal/manifest/internalapi for the Client interface and shared
// value types. Keeping the per-kind code out of the top-level
// internal/manifest package avoids the import cycle that would arise
// once apply.go dispatches over kinds.
package kinds

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/manifest/internalapi"
)

// ── Constants & shared regex ──────────────────────────────────────────────

// projectAPIVersion is the only apiVersion value Project documents
// accept. Future versions get their own constant + parse fork so we
// never silently accept a newer-than-supported manifest.
const projectAPIVersion = "crewship/v1"

// projectKind is the literal `kind:` value used in YAML and recorded
// on PlanItem.Kind for CLI output.
const projectKind = "Project"

// projectHexColorRe enforces the 6-digit hex pattern (`#RRGGBB`) the
// spec mandates for the `color` field. Pre-compiled at package init
// so Validate() doesn't pay the compile cost per call.
var projectHexColorRe = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)

// validProjectStatuses, validProjectPriorities, validProjectHealth
// enumerate the spec's allow-lists. Lookups use simple map presence
// rather than slice scans because Validate runs once per document
// and a map keeps the code self-documenting.
var validProjectStatuses = map[string]struct{}{
	"planned":   {},
	"active":    {},
	"completed": {},
	"archived":  {},
}
var validProjectPriorities = map[string]struct{}{
	"low":    {},
	"medium": {},
	"high":   {},
	"urgent": {},
}
var validProjectHealth = map[string]struct{}{
	"on_track":  {},
	"at_risk":   {},
	"off_track": {},
}

// ── Types ────────────────────────────────────────────────────────────────

// ProjectSpec is the shape under `spec:` for kind: Project. Fields
// follow the SPEC-2 §1 schema verbatim: optional color, enum-validated
// status/priority/health, ISO-date target_date, and a slug reference
// to an Agent acting as project lead.
//
// All fields are optional at parse time; Validate() enforces format
// and FK rules. Zero values mean "leave server-side default in
// place" — the Plan diff compares only fields the user actually
// declared, so an unset Priority won't overwrite a server value the
// user changed via the UI.
type ProjectSpec struct {
	Color         string `yaml:"color,omitempty"            json:"color,omitempty"`
	Status        string `yaml:"status,omitempty"           json:"status,omitempty"`
	Priority      string `yaml:"priority,omitempty"         json:"priority,omitempty"`
	Health        string `yaml:"health,omitempty"           json:"health,omitempty"`
	TargetDate    string `yaml:"target_date,omitempty"      json:"target_date,omitempty"`
	LeadAgentSlug string `yaml:"lead_agent_slug,omitempty"  json:"lead_agent_slug,omitempty"`
}

// ProjectDocument is the top-level document shape: the YAML envelope
// (apiVersion + kind + metadata) plus the per-kind spec. Mirrors the
// Per-kind Go package shape section of SPEC-2.
type ProjectDocument struct {
	APIVersion string               `yaml:"apiVersion" json:"apiVersion"`
	Kind       string               `yaml:"kind"       json:"kind"`
	Metadata   internalapi.Metadata `yaml:"metadata"   json:"metadata"`
	Spec       ProjectSpec          `yaml:"spec"       json:"spec"`
}

// ProjectRemote is the slice of GET /api/v1/projects each row produces
// — only the fields the manifest needs to diff or round-trip through
// export. Drift detection compares ProjectSpec against this struct
// field by field; fields we don't model here (issue_count, progress,
// timestamps) are pure server-state and never enter the plan.
//
// LeadType / LeadID are kept because resolution lives in the kinds
// package: ExportProjects resolves LeadID back to an agent slug, and
// Plan must avoid issuing an UPDATE just because the manifest's
// resolved lead matches the remote ID but the manifest text uses a
// slug.
type ProjectRemote struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspace_id"`
	Slug        string `json:"slug"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Color       string `json:"color"`
	Status      string `json:"status"`
	Priority    string `json:"priority"`
	Health      string `json:"health"`
	TargetDate  string `json:"target_date,omitempty"`
	LeadType    string `json:"lead_type,omitempty"`
	LeadID      string `json:"lead_id,omitempty"`
}

// projectRow accommodates the live server response, which serialises
// description/lead_id as nullable JSON strings (`*string`). Using a
// dedicated wire-type with pointers means a null lead_id in the
// response decodes cleanly to an empty string in ProjectRemote
// without panicking on a `nil` dereference.
type projectRow struct {
	ID          string  `json:"id"`
	WorkspaceID string  `json:"workspace_id"`
	Slug        string  `json:"slug"`
	Name        string  `json:"name"`
	Description *string `json:"description"`
	Color       string  `json:"color"`
	Status      string  `json:"status"`
	Priority    string  `json:"priority"`
	Health      string  `json:"health"`
	TargetDate  *string `json:"target_date"`
	LeadType    *string `json:"lead_type"`
	LeadID      *string `json:"lead_id"`
}

// projectAgentRow models the subset of GET /api/v1/agents fields
// ExportProjects uses to resolve `lead_id` (an opaque agent id) back
// to a stable slug for round-trip output. The full agent payload has
// many more fields; the manifest only needs id↔slug.
//
// The "project" prefix on this otherwise-generic-sounding type is
// deliberate: the kinds package is shared across every per-kind file
// in the directory and a bare `agentRow` would collide with anything
// the routine or recurring-issue files might invent for their own
// agent lookups.
type projectAgentRow struct {
	ID   string `json:"id"`
	Slug string `json:"slug"`
	Name string `json:"name"`
}

// ── Validate ─────────────────────────────────────────────────────────────

// Validate enforces every structural rule in SPEC-2 §1 "Validation".
// Returned errors are intended for end-user CLI output, so they
// include the offending slug, the bad value, and the allowed set
// where applicable.
//
// FK references (lead_agent_slug → workspaceCtx.Agents) are checked
// here because the agent set is known up-front from the manifest's
// declared crews plus the workspace's remote agents. If the lookup
// list is incomplete (e.g. the caller skipped the remote fetch),
// HasAgent's union of declared+remote handles the partial case
// without false negatives.
func (d *ProjectDocument) Validate(ctx internalapi.WorkspaceContext) error {
	if d.APIVersion != projectAPIVersion {
		return fmt.Errorf("project %q: unsupported apiVersion %q (want %q)",
			d.Metadata.Slug, d.APIVersion, projectAPIVersion)
	}
	if d.Kind != projectKind {
		return fmt.Errorf("project %q: kind must be %q, got %q",
			d.Metadata.Slug, projectKind, d.Kind)
	}
	if strings.TrimSpace(d.Metadata.Name) == "" {
		return fmt.Errorf("project %q: metadata.name is required", d.Metadata.Slug)
	}
	if strings.TrimSpace(d.Metadata.Slug) == "" {
		return fmt.Errorf("project: metadata.slug is required")
	}

	if d.Spec.Status != "" {
		if _, ok := validProjectStatuses[d.Spec.Status]; !ok {
			return fmt.Errorf("project %q: invalid status %q (want one of: planned, active, completed, archived)",
				d.Metadata.Slug, d.Spec.Status)
		}
	}
	if d.Spec.Priority != "" {
		if _, ok := validProjectPriorities[d.Spec.Priority]; !ok {
			return fmt.Errorf("project %q: invalid priority %q (want one of: low, medium, high, urgent)",
				d.Metadata.Slug, d.Spec.Priority)
		}
	}
	if d.Spec.Health != "" {
		if _, ok := validProjectHealth[d.Spec.Health]; !ok {
			return fmt.Errorf("project %q: invalid health %q (want one of: on_track, at_risk, off_track)",
				d.Metadata.Slug, d.Spec.Health)
		}
	}
	if d.Spec.Color != "" && !projectHexColorRe.MatchString(d.Spec.Color) {
		return fmt.Errorf("project %q: invalid color %q (want #RRGGBB)",
			d.Metadata.Slug, d.Spec.Color)
	}
	if d.Spec.TargetDate != "" {
		if _, err := time.Parse("2006-01-02", d.Spec.TargetDate); err != nil {
			return fmt.Errorf("project %q: invalid target_date %q (want YYYY-MM-DD)",
				d.Metadata.Slug, d.Spec.TargetDate)
		}
	}
	if d.Spec.LeadAgentSlug != "" && !ctx.HasAgent(d.Spec.LeadAgentSlug) {
		return fmt.Errorf("project %q: lead_agent_slug %q not found in workspace agents",
			d.Metadata.Slug, d.Spec.LeadAgentSlug)
	}
	return nil
}

// ── Plan ─────────────────────────────────────────────────────────────────

// Plan compares the declared document against `remote` (nil = not
// yet on server) and returns the plan items the apply loop should
// execute. The returned slice is non-empty in every case — even
// Unchanged emits a single zero-Exec item so the CLI can show "0
// changed, 1 unchanged" totals truthfully.
//
// FK resolution happens here, not in Validate: the agent slug → id
// lookup needs a live Client, which Validate doesn't have. We accept
// the cost of a `GET /api/v1/agents` per Plan call because plans run
// once per apply; for repeated calls in tests or batch ops the
// caller can cache via the upstream Client.
func (d *ProjectDocument) Plan(ctx context.Context, c internalapi.Client, remote *ProjectRemote) ([]internalapi.PlanItem, error) {
	leadID := ""
	if d.Spec.LeadAgentSlug != "" {
		id, err := projectResolveAgentSlugToID(ctx, c, d.Spec.LeadAgentSlug)
		if err != nil {
			return nil, fmt.Errorf("project %q: resolve lead_agent_slug: %w", d.Metadata.Slug, err)
		}
		leadID = id
	}

	body := d.postBody(leadID)

	if remote == nil {
		desc := fmt.Sprintf("create project %q", d.Metadata.Slug)
		return []internalapi.PlanItem{{
			Kind:        projectKind,
			Slug:        d.Metadata.Slug,
			Action:      internalapi.ActionCreate,
			Description: desc,
			Exec: func(ctx context.Context, c internalapi.Client) error {
				resp, err := c.Post(ctx, "/api/v1/projects", body)
				if err != nil {
					return fmt.Errorf("POST project %q: %w", d.Metadata.Slug, err)
				}
				return projectExpectSuccess(resp, "create project")
			},
		}}, nil
	}

	patch := d.diffPatch(remote, leadID)
	if len(patch) == 0 {
		return []internalapi.PlanItem{{
			Kind:        projectKind,
			Slug:        d.Metadata.Slug,
			Action:      internalapi.ActionUnchanged,
			Description: fmt.Sprintf("project %q already matches manifest", d.Metadata.Slug),
		}}, nil
	}

	projectID := remote.ID
	return []internalapi.PlanItem{{
		Kind:        projectKind,
		Slug:        d.Metadata.Slug,
		Action:      internalapi.ActionUpdate,
		Description: fmt.Sprintf("update project %q (%d field(s))", d.Metadata.Slug, len(patch)),
		Exec: func(ctx context.Context, c internalapi.Client) error {
			resp, err := c.Patch(ctx, "/api/v1/projects/"+projectID, patch)
			if err != nil {
				return fmt.Errorf("PATCH project %q: %w", d.Metadata.Slug, err)
			}
			return projectExpectSuccess(resp, "update project")
		},
	}}, nil
}

// PlanReplace emits the Delete-then-Create sequence ApplyReplace mode
// requires. Callers in the upstream apply.go switch on the apply
// mode and route to this method for `--replace`. Keeping it
// separate from Plan keeps the default path simple and avoids a
// boolean-parameter explosion.
func (d *ProjectDocument) PlanReplace(ctx context.Context, c internalapi.Client, remote *ProjectRemote) ([]internalapi.PlanItem, error) {
	leadID := ""
	if d.Spec.LeadAgentSlug != "" {
		id, err := projectResolveAgentSlugToID(ctx, c, d.Spec.LeadAgentSlug)
		if err != nil {
			return nil, fmt.Errorf("project %q: resolve lead_agent_slug: %w", d.Metadata.Slug, err)
		}
		leadID = id
	}
	body := d.postBody(leadID)

	items := make([]internalapi.PlanItem, 0, 2)
	if remote != nil {
		projectID := remote.ID
		items = append(items, internalapi.PlanItem{
			Kind:        projectKind,
			Slug:        d.Metadata.Slug,
			Action:      internalapi.ActionDelete,
			Description: fmt.Sprintf("delete project %q (--replace)", d.Metadata.Slug),
			Exec: func(ctx context.Context, c internalapi.Client) error {
				resp, err := c.Delete(ctx, "/api/v1/projects/"+projectID)
				if err != nil {
					return fmt.Errorf("DELETE project %q: %w", d.Metadata.Slug, err)
				}
				return projectExpectSuccess(resp, "delete project")
			},
		})
	}
	items = append(items, internalapi.PlanItem{
		Kind:        projectKind,
		Slug:        d.Metadata.Slug,
		Action:      internalapi.ActionCreate,
		Description: fmt.Sprintf("create project %q (--replace)", d.Metadata.Slug),
		Exec: func(ctx context.Context, c internalapi.Client) error {
			resp, err := c.Post(ctx, "/api/v1/projects", body)
			if err != nil {
				return fmt.Errorf("POST project %q: %w", d.Metadata.Slug, err)
			}
			return projectExpectSuccess(resp, "create project")
		},
	})
	return items, nil
}

// postBody assembles the JSON request body for both Create (full
// payload) and as the source of truth for Update diffs. Empty fields
// stay omitted so the server's defaults apply on first create — the
// project handler will fill in color="blue", status="backlog",
// priority="none" when we leave those blank.
//
// lead_id is included only when leadAgentSlug resolved to a non-empty
// id; lead_type is hardcoded to "agent" because the manifest's
// lead_agent_slug field can only target agents (workspace users are
// out of scope for declarative deploys).
func (d *ProjectDocument) postBody(leadID string) map[string]any {
	body := map[string]any{
		"name": d.Metadata.Name,
		"slug": d.Metadata.Slug,
	}
	if d.Metadata.Description != "" {
		body["description"] = d.Metadata.Description
	}
	if d.Spec.Color != "" {
		body["color"] = d.Spec.Color
	}
	if d.Spec.Status != "" {
		body["status"] = d.Spec.Status
	}
	if d.Spec.Priority != "" {
		body["priority"] = d.Spec.Priority
	}
	if d.Spec.Health != "" {
		body["health"] = d.Spec.Health
	}
	if d.Spec.TargetDate != "" {
		body["target_date"] = d.Spec.TargetDate
	}
	if leadID != "" {
		body["lead_id"] = leadID
		body["lead_type"] = "agent"
	}
	return body
}

// diffPatch returns ONLY the fields whose declared value differs
// from `remote`. Empty declared fields are skipped (they mean
// "leave server value alone"), so a manifest that omits `color`
// won't overwrite a color the user picked in the UI.
func (d *ProjectDocument) diffPatch(remote *ProjectRemote, leadID string) map[string]any {
	patch := map[string]any{}

	if d.Metadata.Name != "" && d.Metadata.Name != remote.Name {
		patch["name"] = d.Metadata.Name
	}
	if d.Metadata.Description != "" && d.Metadata.Description != remote.Description {
		patch["description"] = d.Metadata.Description
	}
	if d.Spec.Color != "" && d.Spec.Color != remote.Color {
		patch["color"] = d.Spec.Color
	}
	if d.Spec.Status != "" && d.Spec.Status != remote.Status {
		patch["status"] = d.Spec.Status
	}
	if d.Spec.Priority != "" && d.Spec.Priority != remote.Priority {
		patch["priority"] = d.Spec.Priority
	}
	if d.Spec.Health != "" && d.Spec.Health != remote.Health {
		patch["health"] = d.Spec.Health
	}
	if d.Spec.TargetDate != "" && d.Spec.TargetDate != remote.TargetDate {
		patch["target_date"] = d.Spec.TargetDate
	}

	if d.Spec.LeadAgentSlug != "" {
		if leadID != remote.LeadID || remote.LeadType != "agent" {
			patch["lead_id"] = leadID
			patch["lead_type"] = "agent"
		}
	}
	return patch
}

// ── Export ───────────────────────────────────────────────────────────────

// ExportProjects fetches every project the caller can see and renders
// each as a ProjectDocument suitable for re-applying. The function
// is the inverse of Plan/Create — fields the manifest doesn't model
// (issue_count, progress, timestamps) are dropped.
//
// Lead resolution is best-effort: if the agent list fetch fails or
// the lead_id isn't in the workspace's agent list (e.g. the lead is
// a workspace user, not an agent), we leave lead_agent_slug empty
// and continue. That keeps `crewship export workspace` from failing
// the whole pipeline because one project has a user-lead the
// manifest doesn't model.
func ExportProjects(ctx context.Context, c internalapi.Client) ([]*ProjectDocument, error) {
	rows, err := projectFetchProjects(ctx, c)
	if err != nil {
		return nil, err
	}

	agents, agentsErr := projectFetchAgents(ctx, c)
	agentSlugByID := map[string]string{}
	if agentsErr == nil {
		for _, a := range agents {
			agentSlugByID[a.ID] = a.Slug
		}
	}

	out := make([]*ProjectDocument, 0, len(rows))
	for _, r := range rows {
		doc := &ProjectDocument{
			APIVersion: projectAPIVersion,
			Kind:       projectKind,
			Metadata: internalapi.Metadata{
				Name: r.Name,
				Slug: r.Slug,
			},
			Spec: ProjectSpec{
				Color:    r.Color,
				Status:   r.Status,
				Priority: r.Priority,
				Health:   r.Health,
			},
		}
		if r.Description != nil && *r.Description != "" {
			doc.Metadata.Description = *r.Description
		}
		if r.TargetDate != nil {
			doc.Spec.TargetDate = *r.TargetDate
		}
		if r.LeadType != nil && *r.LeadType == "agent" && r.LeadID != nil {
			if slug, ok := agentSlugByID[*r.LeadID]; ok {
				doc.Spec.LeadAgentSlug = slug
			}
		}
		out = append(out, doc)
	}

	// Deterministic order keeps round-trip diffs stable.
	sort.Slice(out, func(i, j int) bool { return out[i].Metadata.Slug < out[j].Metadata.Slug })
	return out, nil
}

// ── HTTP helpers ─────────────────────────────────────────────────────────

// projectFetchProjects issues GET /api/v1/projects and decodes the
// body into the manifest's wire-type. The server returns a JSON
// array of projectResponse rows; we keep only the fields
// ProjectRemote / projectRow declares — extra fields decode and are
// dropped.
//
// The "project" prefix exists because the kinds package is shared
// across every per-kind file; a bare `fetchProjects` would still be
// unique today but starts to look ambiguous next to a label or
// milestone equivalent.
func projectFetchProjects(ctx context.Context, c internalapi.Client) ([]projectRow, error) {
	resp, err := c.Get(ctx, "/api/v1/projects")
	if err != nil {
		return nil, fmt.Errorf("GET /api/v1/projects: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("GET /api/v1/projects: status %d", resp.StatusCode)
	}
	var rows []projectRow
	if err := projectDecodeJSON(resp.Body, &rows); err != nil {
		return nil, fmt.Errorf("decode projects: %w", err)
	}
	return rows, nil
}

// projectFetchAgents pulls the workspace-wide agent list so
// ExportProjects can map lead_id → slug. The endpoint requires no
// query string when the caller wants every agent in the workspace;
// the server scopes to the auth'd workspace automatically.
func projectFetchAgents(ctx context.Context, c internalapi.Client) ([]projectAgentRow, error) {
	resp, err := c.Get(ctx, "/api/v1/agents")
	if err != nil {
		return nil, fmt.Errorf("GET /api/v1/agents: %w", err)
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("GET /api/v1/agents: status %d", resp.StatusCode)
	}
	var rows []projectAgentRow
	if err := projectDecodeJSON(resp.Body, &rows); err != nil {
		return nil, fmt.Errorf("decode agents: %w", err)
	}
	return rows, nil
}

// FetchProjectBySlug is a convenience helper the upstream apply.go
// will call to obtain the *ProjectRemote that gets passed into
// Plan. Returns (nil, nil) when no row matches — the caller treats
// that as "create" rather than as an error.
func FetchProjectBySlug(ctx context.Context, c internalapi.Client, slug string) (*ProjectRemote, error) {
	rows, err := projectFetchProjects(ctx, c)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		if r.Slug == slug {
			remote := &ProjectRemote{
				ID:          r.ID,
				WorkspaceID: r.WorkspaceID,
				Slug:        r.Slug,
				Name:        r.Name,
				Color:       r.Color,
				Status:      r.Status,
				Priority:    r.Priority,
				Health:      r.Health,
			}
			if r.Description != nil {
				remote.Description = *r.Description
			}
			if r.TargetDate != nil {
				remote.TargetDate = *r.TargetDate
			}
			if r.LeadType != nil {
				remote.LeadType = *r.LeadType
			}
			if r.LeadID != nil {
				remote.LeadID = *r.LeadID
			}
			return remote, nil
		}
	}
	return nil, nil
}

// projectResolveAgentSlugToID maps an agent slug to its server-
// assigned id for use in POST/PATCH project bodies. Returns an
// error when the slug isn't found so apply surfaces a clear "lead
// doesn't exist" failure instead of silently sending an empty
// lead_id (which the server would record as null).
func projectResolveAgentSlugToID(ctx context.Context, c internalapi.Client, slug string) (string, error) {
	agents, err := projectFetchAgents(ctx, c)
	if err != nil {
		return "", err
	}
	for _, a := range agents {
		if a.Slug == slug {
			return a.ID, nil
		}
	}
	return "", fmt.Errorf("agent with slug %q not found", slug)
}

// projectExpectSuccess turns a non-2xx response into an error
// carrying the response body (up to 4 KB) so apply's CLI output can
// show the server's RFC 7807 Problem Details. Larger bodies get
// truncated to keep terminal output sane.
func projectExpectSuccess(resp *internalapi.Response, op string) error {
	if resp == nil {
		return fmt.Errorf("%s: nil response", op)
	}
	if resp.StatusCode/100 == 2 {
		return nil
	}
	body := ""
	if resp.Body != nil {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		body = strings.TrimSpace(string(b))
	}
	if body == "" {
		return fmt.Errorf("%s: status %d", op, resp.StatusCode)
	}
	return fmt.Errorf("%s: status %d: %s", op, resp.StatusCode, body)
}

// projectDecodeJSON wraps json.NewDecoder so callers can hand it an
// io.Reader directly (Response.Body) without worrying about
// nil-Reader panics. A nil body short-circuits to "empty array /
// object decoded as zero value".
func projectDecodeJSON(r io.Reader, v any) error {
	if r == nil {
		return nil
	}
	return json.NewDecoder(r).Decode(v)
}
