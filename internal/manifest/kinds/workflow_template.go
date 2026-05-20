// Package kinds hosts the per-kind manifest implementations
// (one .go + one _test.go per Kind). Each file declares the
// Spec/Document/Remote types plus Validate, Plan, and Export
// for one Kind, exclusively against the leaf interfaces in
// internalapi so the top-level manifest package can wire them
// without an import cycle.
package kinds

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"

	"github.com/crewship-ai/crewship/internal/manifest/internalapi"
)

// Stage type enum — matches the Linear-style state machine the
// workflow_templates table encodes in the `template_json` column.
// Every issue/run flowing through the template sits in exactly one
// stage and transitions follow these semantics:
//
//   - open: the entry state (exactly one per template). Newly
//     created items land here.
//   - started: in-progress states. Multiple are allowed
//     (in_progress, in_review, blocked, ...).
//   - completed: terminal success states. At least one is required
//     so an item can ever finish.
//   - cancelled: terminal failure / abandon states. Optional.
const (
	StageTypeOpen      = "open"
	StageTypeStarted   = "started"
	StageTypeCompleted = "completed"
	StageTypeCancelled = "cancelled"
)

// workflowKindName is the lowercased kind string surfaced in plan
// items + CLI summary. Declared once so the file has a single
// source of truth.
const workflowKindName = "workflowtemplate"

// workflowHexColorPattern validates the optional `color` fields.
// Six-digit RGB with a leading hash. Kept file-local (prefixed) so
// it doesn't collide with the same pattern declared elsewhere in
// this package by other kind files. Three-digit shorthand is
// intentionally rejected: the UI assumes the long form when
// rendering swatches.
var workflowHexColorPattern = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)

// validWorkflowStageTypes is the set used by Validate. Kept as a
// map for O(1) membership checks; the slice form would be fine at
// four entries but the map matches the style used in the larger
// validators in this package.
var validWorkflowStageTypes = map[string]struct{}{
	StageTypeOpen:      {},
	StageTypeStarted:   {},
	StageTypeCompleted: {},
	StageTypeCancelled: {},
}

// WorkflowStage is one row in a workflow template's state machine.
// The shape mirrors the rows stored in `template_json` server-side
// — name + type + position + optional color. The order in the
// `stages` array is authoritative; `position` is the explicit
// stable sort key the UI honours when rendering the column board.
type WorkflowStage struct {
	Name     string `yaml:"name"            json:"name"`
	Type     string `yaml:"type"            json:"type"`
	Position int    `yaml:"position"        json:"position"`
	Color    string `yaml:"color,omitempty" json:"color,omitempty"`
}

// WorkflowTemplateSpec is the shape under `spec:` for kind:
// WorkflowTemplate. The three optional header fields (description,
// icon, color) are decoration; `stages` is the load-bearing part.
type WorkflowTemplateSpec struct {
	Description string          `yaml:"description,omitempty" json:"description,omitempty"`
	Icon        string          `yaml:"icon,omitempty"        json:"icon,omitempty"`
	Color       string          `yaml:"color,omitempty"       json:"color,omitempty"`
	Stages      []WorkflowStage `yaml:"stages"                json:"stages"`
}

// WorkflowTemplateDocument is the top-level YAML document shape
// (apiVersion + kind + metadata + spec) the parser produces and
// Export emits.
type WorkflowTemplateDocument struct {
	APIVersion string               `yaml:"apiVersion" json:"apiVersion"`
	Kind       string               `yaml:"kind"       json:"kind"`
	Metadata   internalapi.Metadata `yaml:"metadata"   json:"metadata"`
	Spec       WorkflowTemplateSpec `yaml:"spec"       json:"spec"`
}

// WorkflowTemplateRemote is the server-side row shape returned by
// `GET /api/v1/workflow-templates`. The handler stores the stage
// array as a JSON string in the `template_json` TEXT column;
// Export unmarshals it back into WorkflowStage entries.
//
// IsBuiltin is true for the rows seeded by SeedBuiltinTemplates at
// workspace bootstrap. Those are managed by the server, not the
// user, so Export filters them out — re-exporting + re-applying
// must not produce a "modify the builtin" plan item.
type WorkflowTemplateRemote struct {
	ID           string `json:"id"`
	WorkspaceID  string `json:"workspace_id"`
	Name         string `json:"name"`
	Description  string `json:"description"`
	TemplateJSON string `json:"template_json"`
	Icon         string `json:"icon"`
	Color        string `json:"color"`
	IsBuiltin    bool   `json:"is_builtin"`
}

// Validate enforces the structural rules documented in
// docs/manifest/workflow_template.md. The order matters: cheap
// scalar checks run first so the error a user sees points at the
// most local mistake.
//
// The workspaceCtx argument is unused — WorkflowTemplate has no
// cross-kind FK references — but kept in the signature so the
// validator slot in the manifest package can call every kind
// uniformly.
func (d *WorkflowTemplateDocument) Validate(_ internalapi.WorkspaceContext) error {
	if d.Metadata.Slug == "" {
		return errors.New("workflowtemplate: metadata.slug is required")
	}
	if d.Metadata.Name == "" {
		return errors.New("workflowtemplate: metadata.name is required")
	}

	if d.Spec.Color != "" && !workflowHexColorPattern.MatchString(d.Spec.Color) {
		return fmt.Errorf("workflowtemplate %q: color %q is not a valid hex code (#RRGGBB)", d.Metadata.Slug, d.Spec.Color)
	}

	if len(d.Spec.Stages) == 0 {
		return fmt.Errorf("workflowtemplate %q: stages array must not be empty", d.Metadata.Slug)
	}

	var (
		openCount      int
		completedCount int
		seenNames      = make(map[string]struct{}, len(d.Spec.Stages))
		seenPositions  = make(map[int]struct{}, len(d.Spec.Stages))
	)
	for i, st := range d.Spec.Stages {
		if st.Name == "" {
			return fmt.Errorf("workflowtemplate %q: stages[%d].name is required", d.Metadata.Slug, i)
		}
		if _, ok := validWorkflowStageTypes[st.Type]; !ok {
			return fmt.Errorf(
				"workflowtemplate %q: stages[%d] (%q) has invalid type %q (want one of: open, started, completed, cancelled)",
				d.Metadata.Slug, i, st.Name, st.Type,
			)
		}
		if _, dup := seenNames[st.Name]; dup {
			return fmt.Errorf("workflowtemplate %q: duplicate stage name %q", d.Metadata.Slug, st.Name)
		}
		seenNames[st.Name] = struct{}{}

		if _, dup := seenPositions[st.Position]; dup {
			return fmt.Errorf("workflowtemplate %q: duplicate stage position %d (used by %q)", d.Metadata.Slug, st.Position, st.Name)
		}
		seenPositions[st.Position] = struct{}{}

		if st.Color != "" && !workflowHexColorPattern.MatchString(st.Color) {
			return fmt.Errorf("workflowtemplate %q: stages[%d] (%q) color %q is not a valid hex code (#RRGGBB)", d.Metadata.Slug, i, st.Name, st.Color)
		}

		switch st.Type {
		case StageTypeOpen:
			openCount++
		case StageTypeCompleted:
			completedCount++
		}
	}

	if openCount != 1 {
		return fmt.Errorf("workflowtemplate %q: must have exactly one stage with type=open (have %d)", d.Metadata.Slug, openCount)
	}
	if completedCount < 1 {
		return fmt.Errorf("workflowtemplate %q: must have at least one stage with type=completed (have 0)", d.Metadata.Slug)
	}

	return nil
}

// Plan compares the declared document against the server's current
// state and returns the single plan item that will reconcile them.
//
// `remote` is the result of a kind-package-level GET-and-filter
// pass: nil means the template doesn't exist on the server yet
// (Create), non-nil means we compare desired vs actual and emit
// Update if any field drifts (Unchanged otherwise).
//
// The POST endpoint is `/api/v1/workflow-templates` — the handler
// is being built in parallel; tests use httptest fakes so they
// don't depend on the real implementation existing yet.
func (d *WorkflowTemplateDocument) Plan(_ context.Context, _ internalapi.Client, remote *WorkflowTemplateRemote) ([]internalapi.PlanItem, error) {
	desiredBody, err := d.buildPostBody()
	if err != nil {
		return nil, fmt.Errorf("workflowtemplate %q: build POST body: %w", d.Metadata.Slug, err)
	}

	if remote == nil {
		return []internalapi.PlanItem{{
			Kind:        workflowKindName,
			Slug:        d.Metadata.Slug,
			Action:      internalapi.ActionCreate,
			Description: fmt.Sprintf("create workflow template %q with %d stages", d.Metadata.Name, len(d.Spec.Stages)),
			Exec: func(ctx context.Context, c internalapi.Client) error {
				return workflowExec(ctx, c, "POST", "/api/v1/workflow-templates", desiredBody)
			},
		}}, nil
	}

	// Server has a row keyed on (workspace_id, name). Decode the
	// stages JSON so we can compare structured values rather than
	// string-equal JSON blobs (which would flag whitespace drift).
	var remoteStages []WorkflowStage
	if remote.TemplateJSON != "" {
		if err := json.Unmarshal([]byte(remote.TemplateJSON), &remoteStages); err != nil {
			return nil, fmt.Errorf("workflowtemplate %q: decode remote template_json: %w", d.Metadata.Slug, err)
		}
	}

	if workflowTemplateMatches(d, remote, remoteStages) {
		return []internalapi.PlanItem{{
			Kind:        workflowKindName,
			Slug:        d.Metadata.Slug,
			Action:      internalapi.ActionUnchanged,
			Description: fmt.Sprintf("workflow template %q already matches", d.Metadata.Name),
		}}, nil
	}

	remoteID := remote.ID
	return []internalapi.PlanItem{{
		Kind:        workflowKindName,
		Slug:        d.Metadata.Slug,
		Action:      internalapi.ActionUpdate,
		Description: fmt.Sprintf("update workflow template %q (%d stages)", d.Metadata.Name, len(d.Spec.Stages)),
		Exec: func(ctx context.Context, c internalapi.Client) error {
			return workflowExec(ctx, c, "PATCH", "/api/v1/workflow-templates/"+remoteID, desiredBody)
		},
	}}, nil
}

// buildPostBody assembles the JSON object the handler expects.
// `template_json` is a stringified JSON array because the DB column
// is TEXT — the handler does NOT re-parse user-supplied JSON, it
// passes the string through (the SeedBuiltin path uses the same
// shape).
func (d *WorkflowTemplateDocument) buildPostBody() (map[string]any, error) {
	stagesJSON, err := json.Marshal(d.Spec.Stages)
	if err != nil {
		return nil, fmt.Errorf("marshal stages: %w", err)
	}
	body := map[string]any{
		"name":          d.Metadata.Name,
		"description":   d.Spec.Description,
		"template_json": string(stagesJSON),
		"icon":          d.Spec.Icon,
		"color":         d.Spec.Color,
	}
	return body, nil
}

// workflowTemplateMatches reports whether the declared document is
// equivalent to the server's row. The comparison normalises stages
// by `position` because two arrays with the same set of stages in
// different order should still count as equal — the position field
// is the canonical ordering and YAML authors might reorder by
// hand.
func workflowTemplateMatches(d *WorkflowTemplateDocument, remote *WorkflowTemplateRemote, remoteStages []WorkflowStage) bool {
	if d.Metadata.Name != remote.Name {
		return false
	}
	if d.Spec.Description != remote.Description {
		return false
	}
	if d.Spec.Icon != remote.Icon {
		return false
	}
	if d.Spec.Color != remote.Color {
		return false
	}
	if len(d.Spec.Stages) != len(remoteStages) {
		return false
	}
	declared := append([]WorkflowStage(nil), d.Spec.Stages...)
	actual := append([]WorkflowStage(nil), remoteStages...)
	sort.Slice(declared, func(i, j int) bool { return declared[i].Position < declared[j].Position })
	sort.Slice(actual, func(i, j int) bool { return actual[i].Position < actual[j].Position })
	for i := range declared {
		if declared[i] != actual[i] {
			return false
		}
	}
	return true
}

// workflowExec runs a single mutating REST call and discards the
// body. Plan items don't need the response payload — apply.go
// tracks the result by counting Action types — so we just verify
// the status code and close the reader.
//
// File-local helper (prefixed name) to avoid collisions with the
// generic `execAndDiscard` other kind files may declare in this
// same package; keeping the helper local keeps this file
// self-contained and resilient to neighbouring agents shuffling
// their utilities around.
func workflowExec(ctx context.Context, c internalapi.Client, method, path string, body any) error {
	var (
		resp *internalapi.Response
		err  error
	)
	switch method {
	case "POST":
		resp, err = c.Post(ctx, path, body)
	case "PATCH":
		resp, err = c.Patch(ctx, path, body)
	case "PUT":
		resp, err = c.Put(ctx, path, body)
	case "DELETE":
		resp, err = c.Delete(ctx, path)
	default:
		return fmt.Errorf("unsupported HTTP method %q", method)
	}
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	if resp == nil {
		return fmt.Errorf("%s %s: nil response", method, path)
	}
	// Drain & close so the underlying connection can be reused. The
	// 4 KB cap is enough to surface a meaningful error message
	// without slurping a stray binary blob.
	var bodyBytes []byte
	if resp.Body != nil {
		bodyBytes, _ = io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s %s: HTTP %d: %s", method, path, resp.StatusCode, bodyBytes)
	}
	return nil
}

// ExportWorkflowTemplates fetches every workflow_template row in
// the workspace and converts each non-builtin row back into a
// WorkflowTemplateDocument suitable for emitting as YAML.
//
// Builtins are skipped: those are seeded by the server (see
// internal/database/seed_templates.go's SeedBuiltinTemplates) and
// re-applying them via manifest would either be a no-op
// (Action=Unchanged) or — worse — drift them if the user edited
// the exported file. Treating them as server-owned keeps the round
// trip idempotent.
func ExportWorkflowTemplates(ctx context.Context, c internalapi.Client) ([]*WorkflowTemplateDocument, error) {
	resp, err := c.Get(ctx, "/api/v1/workflow-templates")
	if err != nil {
		return nil, fmt.Errorf("GET workflow-templates: %w", err)
	}
	if resp == nil {
		return nil, errors.New("export workflow templates: nil response")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var body []byte
		if resp.Body != nil {
			body, _ = io.ReadAll(io.LimitReader(resp.Body, 4096))
		}
		return nil, fmt.Errorf("export workflow templates: HTTP %d: %s", resp.StatusCode, string(body))
	}

	var raw []byte
	if resp.Body != nil {
		raw, err = io.ReadAll(io.LimitReader(resp.Body, 10<<20))
		if err != nil {
			return nil, fmt.Errorf("read workflow-templates response: %w", err)
		}
	}

	// Accept both `[...]` and `{templates: [...]}` to absorb whatever
	// envelope the handler ships with — matches the conventions
	// other list endpoints in this codebase use.
	var rows []WorkflowTemplateRemote
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &rows); err != nil {
			var wrapped struct {
				Templates []WorkflowTemplateRemote `json:"templates"`
			}
			if werr := json.Unmarshal(raw, &wrapped); werr != nil {
				return nil, fmt.Errorf("decode workflow templates: %w", err)
			}
			rows = wrapped.Templates
		}
	}

	out := make([]*WorkflowTemplateDocument, 0, len(rows))
	for _, row := range rows {
		if row.IsBuiltin {
			continue
		}
		var stages []WorkflowStage
		if row.TemplateJSON != "" {
			if err := json.Unmarshal([]byte(row.TemplateJSON), &stages); err != nil {
				return nil, fmt.Errorf("decode template_json for %q: %w", row.Name, err)
			}
		}
		doc := &WorkflowTemplateDocument{
			APIVersion: "crewship/v1",
			Kind:       "WorkflowTemplate",
			Metadata: internalapi.Metadata{
				Name: row.Name,
				// The DB has no slug column — derive a deterministic slug
				// from the name so re-export → re-apply round-trips.
				// Apply keys on metadata.slug, so the slug just has to
				// be stable; humans can rewrite it before committing.
				Slug:        workflowSlugify(row.Name),
				Description: row.Description,
			},
			Spec: WorkflowTemplateSpec{
				Description: row.Description,
				Icon:        row.Icon,
				Color:       row.Color,
				Stages:      stages,
			},
		}
		out = append(out, doc)
	}
	return out, nil
}

// workflowSlugify lower-cases a name and replaces runs of non-
// alphanumerics with single hyphens. File-local (prefixed) to keep
// this kind self-contained: other kind files in the same package
// own their own slug helpers and we don't want to depend on a
// shared symbol whose definition may move.
func workflowSlugify(name string) string {
	b := make([]byte, 0, len(name))
	prevDash := false
	for i := 0; i < len(name); i++ {
		ch := name[i]
		switch {
		case ch >= 'A' && ch <= 'Z':
			b = append(b, ch+('a'-'A'))
			prevDash = false
		case (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9'):
			b = append(b, ch)
			prevDash = false
		default:
			if !prevDash && len(b) > 0 {
				b = append(b, '-')
				prevDash = true
			}
		}
	}
	for len(b) > 0 && b[len(b)-1] == '-' {
		b = b[:len(b)-1]
	}
	if len(b) == 0 {
		return "workflow-template"
	}
	return string(b)
}
