// Package kinds holds the per-kind manifest documents — one .go file
// per top-level YAML `kind:`. Each kind owns its YAML shape, its
// Validate rules, its Plan logic (declared vs. remote diff), and its
// Export reverse-mapping.
//
// This file implements kind: RecurringIssue.
//
// A RecurringIssue is a workspace-scoped, crew-owned schedule that
// stamps out a fresh issue every time its cron expression fires. The
// template fields (title, description, labels, project, priority,
// assignee, crew) live under spec.template so the surrounding spec
// can grow non-template knobs (cron, timezone, enabled) without
// reshuffling the YAML.
//
// The server stores the rendered template as a single JSON TEXT
// column (template_json). The kind resolves slug-based FKs (labels,
// project, assignee, crew) to IDs at exec-time — the Plan step holds
// onto the declared slugs, then the exec closure walks the Client
// caches to translate them right before POST/PATCH.
package kinds

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"sort"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/crewship-ai/crewship/internal/manifest/internalapi"
)

// recurringIssueCronParser is the standard 5-field parser (minute
// hour dom month dow). Mirrors what
// internal/api/recurring_issue_handler.go uses, so a manifest that
// validates here also validates on the server. The parser is cheap
// to construct but lives at package scope so we don't pay the cost
// on every Validate call.
var recurringIssueCronParser = cron.NewParser(
	cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow,
)

// validRecurringIssuePriorities is the closed enum the server
// accepts on the priority column. "none" is allowed because the
// schema default is 'none' — a manifest that omits priority lands
// on the server default and round-trips back as "none".
var validRecurringIssuePriorities = map[string]struct{}{
	"none":   {},
	"low":    {},
	"medium": {},
	"high":   {},
	"urgent": {},
}

// RecurringIssueTemplate is the issue shape that gets stamped out on
// every cron fire. Crew is required (recurring issues are crew-
// scoped); the other slug-FKs are optional and validated against
// the surrounding WorkspaceContext.
type RecurringIssueTemplate struct {
	Title             string   `yaml:"title"                          json:"title"`
	Description       string   `yaml:"description,omitempty"          json:"description,omitempty"`
	Labels            []string `yaml:"labels,omitempty"               json:"labels,omitempty"`
	ProjectSlug       string   `yaml:"project_slug,omitempty"         json:"project_slug,omitempty"`
	Priority          string   `yaml:"priority,omitempty"             json:"priority,omitempty"`
	AssigneeAgentSlug string   `yaml:"assignee_agent_slug,omitempty"  json:"assignee_agent_slug,omitempty"`
	// CrewSlug is REQUIRED — every recurring issue is owned by a
	// specific crew. The handler-side equivalent column is crew_id.
	CrewSlug string `yaml:"crew_slug" json:"crew_slug"`
}

// RecurringIssueSpec is the `spec:` shape under kind: RecurringIssue.
//
// Enabled defaults to true when omitted, since a manifest authored
// without `enabled:` is almost certainly meant to be on. The
// existing handler also defaults to enabled=1 on create.
type RecurringIssueSpec struct {
	Enabled  *bool                  `yaml:"enabled,omitempty"  json:"enabled,omitempty"`
	Cron     string                 `yaml:"cron"               json:"cron"`
	Timezone string                 `yaml:"timezone"           json:"timezone"`
	Template RecurringIssueTemplate `yaml:"template"           json:"template"`
}

// EnabledOrDefault returns Enabled if set, otherwise true. Apply
// uses this so a manifest authored without enabled lands on the
// server with the same default as the handler.
func (s *RecurringIssueSpec) EnabledOrDefault() bool {
	if s.Enabled == nil {
		return true
	}
	return *s.Enabled
}

// RecurringIssueDocument is the top-level YAML/JSON document shape.
type RecurringIssueDocument struct {
	APIVersion string               `yaml:"apiVersion" json:"apiVersion"`
	Kind       string               `yaml:"kind"       json:"kind"`
	Metadata   internalapi.Metadata `yaml:"metadata"   json:"metadata"`
	Spec       RecurringIssueSpec   `yaml:"spec"       json:"spec"`
}

// RecurringIssueRemote is the server-side shape this kind talks to.
// It mirrors the spec's POST body
// ({name, enabled, cron, timezone, template_json}) — Plan diffs by
// unmarshalling template_json into a RecurringIssueTemplate (with
// resolved IDs) so the diff compares apples to apples regardless
// of which fields drifted.
type RecurringIssueRemote struct {
	ID           string                        `json:"id"`
	Name         string                        `json:"name"`
	Slug         string                        `json:"slug"`
	Enabled      bool                          `json:"enabled"`
	Cron         string                        `json:"cron"`
	Timezone     string                        `json:"timezone"`
	TemplateJSON string                        `json:"template_json"`
	Template     *RecurringIssueRemoteTemplate `json:"-"`
}

// RecurringIssueRemoteTemplate is the unmarshalled template_json
// payload. Fields use IDs (not slugs) — Export reverses these back
// to slugs by looking each one up via the Client.
type RecurringIssueRemoteTemplate struct {
	Title           string   `json:"title"`
	Description     string   `json:"description,omitempty"`
	LabelIDs        []string `json:"label_ids,omitempty"`
	ProjectID       string   `json:"project_id,omitempty"`
	Priority        string   `json:"priority,omitempty"`
	AssigneeAgentID string   `json:"assignee_agent_id,omitempty"`
	CrewID          string   `json:"crew_id,omitempty"`
}

// Validate checks structural rules. Returns the first failure as an
// error — the top-level manifest validator already aggregates errors
// across kinds, so per-kind Validate returning a single error keeps
// the call site simple.
//
// The order of checks matches the order an author would notice
// failures in the file: required fields first, then enum bounds,
// then external references. cron / timezone parse last because they
// involve a more expensive call than the cheap field checks.
func (d *RecurringIssueDocument) Validate(wsCtx internalapi.WorkspaceContext) error {
	if d.Metadata.Slug == "" {
		return fmt.Errorf("recurring_issue: metadata.slug is required")
	}
	if d.Spec.Cron == "" {
		return fmt.Errorf("recurring_issue %q: spec.cron is required", d.Metadata.Slug)
	}
	if d.Spec.Timezone == "" {
		return fmt.Errorf("recurring_issue %q: spec.timezone is required", d.Metadata.Slug)
	}
	if d.Spec.Template.Title == "" {
		return fmt.Errorf("recurring_issue %q: spec.template.title is required", d.Metadata.Slug)
	}
	if d.Spec.Template.CrewSlug == "" {
		return fmt.Errorf("recurring_issue %q: spec.template.crew_slug is required (recurring issues are crew-scoped)", d.Metadata.Slug)
	}

	// Priority is optional; when set must be in the enum.
	if d.Spec.Template.Priority != "" {
		if _, ok := validRecurringIssuePriorities[d.Spec.Template.Priority]; !ok {
			return fmt.Errorf("recurring_issue %q: spec.template.priority %q must be one of none|low|medium|high|urgent",
				d.Metadata.Slug, d.Spec.Template.Priority)
		}
	}

	// Cron must parse. The handler enforces the same 5-field parser
	// so a manifest that fails here would also fail at apply time.
	if _, err := recurringIssueCronParser.Parse(d.Spec.Cron); err != nil {
		return fmt.Errorf("recurring_issue %q: spec.cron %q is invalid: %w", d.Metadata.Slug, d.Spec.Cron, err)
	}

	// Timezone must be a valid IANA zone. LoadLocation is the source
	// of truth: anything it accepts the cron scheduler can use.
	if _, err := time.LoadLocation(d.Spec.Timezone); err != nil {
		return fmt.Errorf("recurring_issue %q: spec.timezone %q is not a valid IANA timezone: %w",
			d.Metadata.Slug, d.Spec.Timezone, err)
	}

	// FK checks against the workspace context. Each missing slug is
	// reported with a distinct error so the author knows which
	// reference to fix.
	if !wsCtx.HasCrew(d.Spec.Template.CrewSlug) {
		return fmt.Errorf("recurring_issue %q: spec.template.crew_slug %q does not refer to a declared or remote crew",
			d.Metadata.Slug, d.Spec.Template.CrewSlug)
	}
	if d.Spec.Template.ProjectSlug != "" && !wsCtx.HasProject(d.Spec.Template.ProjectSlug) {
		return fmt.Errorf("recurring_issue %q: spec.template.project_slug %q does not refer to a declared or remote project",
			d.Metadata.Slug, d.Spec.Template.ProjectSlug)
	}
	if d.Spec.Template.AssigneeAgentSlug != "" && !wsCtx.HasAgent(d.Spec.Template.AssigneeAgentSlug) {
		return fmt.Errorf("recurring_issue %q: spec.template.assignee_agent_slug %q does not refer to a declared or remote agent",
			d.Metadata.Slug, d.Spec.Template.AssigneeAgentSlug)
	}
	for _, lbl := range d.Spec.Template.Labels {
		if !wsCtx.HasLabel(lbl) {
			return fmt.Errorf("recurring_issue %q: spec.template.labels references unknown label %q",
				d.Metadata.Slug, lbl)
		}
	}
	return nil
}

// Plan compares the declared document against the remote state and
// produces zero or one PlanItem. remote is nil for "doesn't exist
// on server yet" — the typical first-apply case.
//
// The exec closures resolve slug → id by calling the Client at
// apply time. We don't capture IDs at plan-time because the
// referenced entities may have been created in the same plan (e.g.
// the crew is a new Crew document a few phases earlier); their IDs
// don't exist until apply runs.
func (d *RecurringIssueDocument) Plan(
	ctx context.Context,
	c internalapi.Client,
	remote *RecurringIssueRemote,
) ([]internalapi.PlanItem, error) {
	desc := d.Metadata.Slug
	specCopy := d.Spec
	meta := d.Metadata

	if remote == nil {
		// CREATE
		return []internalapi.PlanItem{{
			Kind:        "recurring_issue",
			Slug:        desc,
			Action:      internalapi.ActionCreate,
			Description: fmt.Sprintf("recurring_issue/%s (cron %q in %s)", desc, specCopy.Cron, specCopy.Timezone),
			Exec: func(ctx context.Context, client internalapi.Client) error {
				body, err := buildRecurringIssueBody(ctx, client, meta, &specCopy)
				if err != nil {
					return fmt.Errorf("build recurring_issue body for %q: %w", desc, err)
				}
				resp, err := client.Post(ctx, "/api/v1/recurring-issues", body)
				if err != nil {
					return fmt.Errorf("create recurring_issue %q: %w", desc, err)
				}
				if resp.StatusCode >= 300 {
					return fmt.Errorf("create recurring_issue %q: server returned %d: %s",
						desc, resp.StatusCode, readBodyForError(resp))
				}
				return nil
			},
		}}, nil
	}

	// Compare drift. Resolve the declared slugs to IDs once so the
	// diff matches what the server stored. If a referenced entity
	// can't be resolved at plan-time (because it's being created in
	// this same apply), treat the value as "drifted" so we emit an
	// Update — the exec closure will resolve it at apply-time.
	declaredTemplate, resolveErr := resolveRecurringIssueTemplateToIDs(ctx, c, &specCopy.Template)
	remoteTemplate := remote.Template
	if remoteTemplate == nil && remote.TemplateJSON != "" {
		var t RecurringIssueRemoteTemplate
		if err := json.Unmarshal([]byte(remote.TemplateJSON), &t); err == nil {
			remoteTemplate = &t
		}
	}

	differs := remote.Name != metaName(meta) ||
		remote.Cron != specCopy.Cron ||
		remote.Timezone != specCopy.Timezone ||
		remote.Enabled != specCopy.EnabledOrDefault() ||
		!templatesEqual(declaredTemplate, remoteTemplate)

	if !differs && resolveErr == nil {
		return []internalapi.PlanItem{{
			Kind:        "recurring_issue",
			Slug:        desc,
			Action:      internalapi.ActionUnchanged,
			Description: fmt.Sprintf("recurring_issue/%s (in sync)", desc),
		}}, nil
	}

	existingID := remote.ID
	return []internalapi.PlanItem{{
		Kind:        "recurring_issue",
		Slug:        desc,
		Action:      internalapi.ActionUpdate,
		Description: fmt.Sprintf("recurring_issue/%s (updating)", desc),
		Exec: func(ctx context.Context, client internalapi.Client) error {
			body, err := buildRecurringIssueBody(ctx, client, meta, &specCopy)
			if err != nil {
				return fmt.Errorf("build recurring_issue body for %q: %w", desc, err)
			}
			resp, err := client.Patch(ctx, "/api/v1/recurring-issues/"+existingID, body)
			if err != nil {
				return fmt.Errorf("update recurring_issue %q: %w", desc, err)
			}
			if resp.StatusCode >= 300 {
				return fmt.Errorf("update recurring_issue %q: server returned %d: %s",
					desc, resp.StatusCode, readBodyForError(resp))
			}
			return nil
		},
	}}, nil
}

// buildRecurringIssueBody renders the POST/PATCH body the handler
// expects. The slug-FKs are resolved to IDs by calling the Client
// — at apply-time the referenced entities exist (earlier phases
// ran first per the topological order in apply.go).
func buildRecurringIssueBody(
	ctx context.Context,
	c internalapi.Client,
	meta internalapi.Metadata,
	spec *RecurringIssueSpec,
) (map[string]any, error) {
	resolved, err := resolveRecurringIssueTemplateToIDs(ctx, c, &spec.Template)
	if err != nil {
		return nil, err
	}
	templateBytes, err := json.Marshal(resolved)
	if err != nil {
		return nil, fmt.Errorf("marshal template_json: %w", err)
	}
	body := map[string]any{
		"name":          metaName(meta),
		"slug":          meta.Slug,
		"enabled":       spec.EnabledOrDefault(),
		"cron":          spec.Cron,
		"timezone":      spec.Timezone,
		"template_json": string(templateBytes),
	}
	if meta.Description != "" {
		body["description"] = meta.Description
	}
	return body, nil
}

// resolveRecurringIssueTemplateToIDs walks every slug-FK on the
// declared template and asks the Client for the matching ID. The
// crew is mandatory; the others are optional and skipped silently
// when blank. If a referenced entity is missing at apply time we
// return an error rather than silently sending an empty FK — a
// dangling reference would write a row the runtime can't satisfy
// later when the cron fires.
func resolveRecurringIssueTemplateToIDs(
	ctx context.Context,
	c internalapi.Client,
	t *RecurringIssueTemplate,
) (*RecurringIssueRemoteTemplate, error) {
	out := &RecurringIssueRemoteTemplate{
		Title:       t.Title,
		Description: t.Description,
		Priority:    t.Priority,
	}
	// Crew (required).
	crewID, err := lookupSlugID(ctx, c, "/api/v1/crews", t.CrewSlug)
	if err != nil {
		return nil, fmt.Errorf("resolve crew %q: %w", t.CrewSlug, err)
	}
	if crewID == "" {
		return nil, fmt.Errorf("crew %q not found in workspace", t.CrewSlug)
	}
	out.CrewID = crewID

	// Project (optional).
	if t.ProjectSlug != "" {
		id, err := lookupSlugID(ctx, c, "/api/v1/projects", t.ProjectSlug)
		if err != nil {
			return nil, fmt.Errorf("resolve project %q: %w", t.ProjectSlug, err)
		}
		if id == "" {
			return nil, fmt.Errorf("project %q not found in workspace", t.ProjectSlug)
		}
		out.ProjectID = id
	}

	// Assignee agent (optional).
	if t.AssigneeAgentSlug != "" {
		id, err := lookupSlugID(ctx, c, "/api/v1/agents", t.AssigneeAgentSlug)
		if err != nil {
			return nil, fmt.Errorf("resolve agent %q: %w", t.AssigneeAgentSlug, err)
		}
		if id == "" {
			return nil, fmt.Errorf("agent %q not found in workspace", t.AssigneeAgentSlug)
		}
		out.AssigneeAgentID = id
	}

	// Labels (optional, but every declared slug must resolve).
	if len(t.Labels) > 0 {
		// Snapshot the labels list once, then walk it for each
		// declared slug — avoids N GETs when the manifest declares
		// many labels.
		labels, err := listLabels(ctx, c)
		if err != nil {
			return nil, fmt.Errorf("list labels: %w", err)
		}
		labelIDs := make([]string, 0, len(t.Labels))
		for _, slug := range t.Labels {
			id, ok := labels[slug]
			if !ok {
				return nil, fmt.Errorf("label %q not found in workspace", slug)
			}
			labelIDs = append(labelIDs, id)
		}
		// Sort so the marshalled template_json is stable across
		// applies regardless of authoring order — keeps round-trip
		// diffing honest.
		sort.Strings(labelIDs)
		out.LabelIDs = labelIDs
	}
	return out, nil
}

// lookupSlugID fetches the list at `path` and returns the id of the
// row whose slug matches `slug`. Returns "" + nil when not found
// (so the caller can distinguish a transport error from a missing
// reference). The response is expected to be a JSON array of
// {id, slug} objects — the shape every list endpoint we hit uses.
func lookupSlugID(ctx context.Context, c internalapi.Client, path, slug string) (string, error) {
	if slug == "" {
		return "", nil
	}
	resp, err := c.Get(ctx, path)
	if err != nil {
		return "", err
	}
	body, err := readResponseBody(resp)
	if err != nil {
		return "", err
	}
	var rows []struct {
		ID   string `json:"id"`
		Slug string `json:"slug"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(body, &rows); err != nil {
		// Some endpoints wrap the list — best-effort second decode.
		var wrapped struct {
			Items []struct {
				ID   string `json:"id"`
				Slug string `json:"slug"`
				Name string `json:"name"`
			} `json:"items"`
		}
		if err2 := json.Unmarshal(body, &wrapped); err2 == nil {
			for _, r := range wrapped.Items {
				if r.Slug == slug || r.Name == slug {
					return r.ID, nil
				}
			}
			return "", nil
		}
		return "", fmt.Errorf("decode list at %s: %w", path, err)
	}
	for _, r := range rows {
		// Labels use `name` as the unique key (no slug column); fall
		// back to name when slug isn't returned so the same helper
		// works for both kinds.
		if r.Slug == slug || (r.Slug == "" && r.Name == slug) {
			return r.ID, nil
		}
	}
	return "", nil
}

// listLabels returns a slug→id map for every label in the
// workspace. Labels key on `name` server-side (no slug column),
// so we accept either slug or name as the lookup key. A single
// GET amortises across all label refs on a recurring issue.
func listLabels(ctx context.Context, c internalapi.Client) (map[string]string, error) {
	resp, err := c.Get(ctx, "/api/v1/labels")
	if err != nil {
		return nil, err
	}
	body, err := readResponseBody(resp)
	if err != nil {
		return nil, err
	}
	var rows []struct {
		ID   string `json:"id"`
		Slug string `json:"slug"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, fmt.Errorf("decode labels list: %w", err)
	}
	out := make(map[string]string, len(rows))
	for _, r := range rows {
		if r.Slug != "" {
			out[r.Slug] = r.ID
		}
		if r.Name != "" {
			out[r.Name] = r.ID
		}
	}
	return out, nil
}

// templatesEqual compares declared vs. remote template payloads
// field-by-field. nil-vs-empty-slice is treated as equal because
// the JSON round-trip can drop empty slices entirely.
func templatesEqual(a, b *RecurringIssueRemoteTemplate) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	if a.Title != b.Title || a.Description != b.Description ||
		a.Priority != b.Priority || a.ProjectID != b.ProjectID ||
		a.AssigneeAgentID != b.AssigneeAgentID || a.CrewID != b.CrewID {
		return false
	}
	if len(a.LabelIDs) != len(b.LabelIDs) {
		return false
	}
	// Sort copies so label order in the template_json doesn't
	// create false drift.
	ax := append([]string{}, a.LabelIDs...)
	bx := append([]string{}, b.LabelIDs...)
	sort.Strings(ax)
	sort.Strings(bx)
	return reflect.DeepEqual(ax, bx)
}

// ExportRecurringIssues fetches every recurring issue from the
// workspace and returns one Document per row, reverse-mapping IDs
// back to slugs. The function is the inverse of Plan→Apply:
// `crewship export workspace` calls it and emits the documents
// into the multi-doc YAML stream so a round-trip apply produces an
// identical workspace.
//
// The inverse mapping calls the Client for crews/projects/agents/
// labels once each — we cache the lookups locally rather than
// per-row to keep export O(1) GETs regardless of how many
// recurring issues live in the workspace.
func ExportRecurringIssues(ctx context.Context, c internalapi.Client) ([]*RecurringIssueDocument, error) {
	resp, err := c.Get(ctx, "/api/v1/recurring-issues")
	if err != nil {
		return nil, fmt.Errorf("list recurring_issues: %w", err)
	}
	body, err := readResponseBody(resp)
	if err != nil {
		return nil, fmt.Errorf("read recurring_issues list: %w", err)
	}
	if len(body) == 0 || string(body) == "null" {
		return nil, nil
	}
	var rows []RecurringIssueRemote
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, fmt.Errorf("decode recurring_issues list: %w", err)
	}
	if len(rows) == 0 {
		return nil, nil
	}

	// Reverse lookups: id → slug for each kind. Built once and
	// reused across every row.
	crewByID, err := listIDToSlug(ctx, c, "/api/v1/crews")
	if err != nil {
		return nil, fmt.Errorf("build crew lookup: %w", err)
	}
	projByID, err := listIDToSlug(ctx, c, "/api/v1/projects")
	if err != nil {
		return nil, fmt.Errorf("build project lookup: %w", err)
	}
	agentByID, err := listIDToSlug(ctx, c, "/api/v1/agents")
	if err != nil {
		return nil, fmt.Errorf("build agent lookup: %w", err)
	}
	labelByID, err := listIDToSlugLabels(ctx, c)
	if err != nil {
		return nil, fmt.Errorf("build label lookup: %w", err)
	}

	out := make([]*RecurringIssueDocument, 0, len(rows))
	for i := range rows {
		row := rows[i]
		var tmpl RecurringIssueRemoteTemplate
		if row.TemplateJSON != "" {
			if err := json.Unmarshal([]byte(row.TemplateJSON), &tmpl); err != nil {
				return nil, fmt.Errorf("decode template_json for %q: %w", row.Slug, err)
			}
		}
		labelSlugs := make([]string, 0, len(tmpl.LabelIDs))
		for _, id := range tmpl.LabelIDs {
			if s, ok := labelByID[id]; ok {
				labelSlugs = append(labelSlugs, s)
			}
		}
		// Deterministic ordering so successive exports of the same
		// state produce byte-identical YAML.
		sort.Strings(labelSlugs)

		enabled := row.Enabled
		doc := &RecurringIssueDocument{
			APIVersion: "crewship/v1",
			Kind:       "RecurringIssue",
			Metadata: internalapi.Metadata{
				Name: row.Name,
				Slug: row.Slug,
			},
			Spec: RecurringIssueSpec{
				Enabled:  &enabled,
				Cron:     row.Cron,
				Timezone: row.Timezone,
				Template: RecurringIssueTemplate{
					Title:             tmpl.Title,
					Description:       tmpl.Description,
					Labels:            labelSlugs,
					ProjectSlug:       projByID[tmpl.ProjectID],
					Priority:          tmpl.Priority,
					AssigneeAgentSlug: agentByID[tmpl.AssigneeAgentID],
					CrewSlug:          crewByID[tmpl.CrewID],
				},
			},
		}
		out = append(out, doc)
	}
	return out, nil
}

// listIDToSlug returns id → slug for a list endpoint. Used by
// Export to reverse the slug-FK resolution Plan does.
func listIDToSlug(ctx context.Context, c internalapi.Client, path string) (map[string]string, error) {
	resp, err := c.Get(ctx, path)
	if err != nil {
		return nil, err
	}
	body, err := readResponseBody(resp)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	if len(body) == 0 || string(body) == "null" {
		return out, nil
	}
	var rows []struct {
		ID   string `json:"id"`
		Slug string `json:"slug"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, fmt.Errorf("decode list at %s: %w", path, err)
	}
	for _, r := range rows {
		if r.ID == "" {
			continue
		}
		// Prefer slug when present; fall back to name (labels).
		if r.Slug != "" {
			out[r.ID] = r.Slug
		} else if r.Name != "" {
			out[r.ID] = r.Name
		}
	}
	return out, nil
}

// listIDToSlugLabels is a labels-specific variant: labels key on
// `name`, so we use that as the manifest-side slug.
func listIDToSlugLabels(ctx context.Context, c internalapi.Client) (map[string]string, error) {
	return listIDToSlug(ctx, c, "/api/v1/labels")
}

// readResponseBody buffers the body so callers can decode it more
// than once if multiple shapes need to be tried.
func readResponseBody(resp *internalapi.Response) ([]byte, error) {
	if resp == nil || resp.Body == nil {
		return nil, nil
	}
	if resp.StatusCode >= 300 {
		// Drain the body so the caller has it in the error message
		// rather than getting an opaque 4xx/5xx.
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, string(bytes.TrimSpace(raw)))
	}
	return io.ReadAll(io.LimitReader(resp.Body, 10<<20))
}

// readBodyForError returns a short snippet of the response body
// suitable for inclusion in an error message. It never returns "".
func readBodyForError(resp *internalapi.Response) string {
	if resp == nil || resp.Body == nil {
		return "(no body)"
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	s := string(bytes.TrimSpace(raw))
	if s == "" {
		return "(empty body)"
	}
	return s
}

// metaName returns the human-readable name from metadata, falling
// back to the slug when name is omitted. The server's recurring
// issue handler requires a non-empty `name` on create.
func metaName(meta internalapi.Metadata) string {
	if meta.Name != "" {
		return meta.Name
	}
	return meta.Slug
}
