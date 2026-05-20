// Package kinds holds one Go file per manifest kind (Project, Label,
// Milestone, …). Each kind owns its YAML schema, Validate/Plan/Export
// surface, and the request/response shapes the backend handler
// expects. The package only depends on internalapi so the parent
// `internal/manifest` package can import kinds without an import
// cycle.
//
// This file implements `kind: Label`. The labels table has no slug
// column, so the backend keys uniqueness on `name`. The manifest
// still requires `metadata.slug` for cross-kind FK references
// (TriageRule, SavedView, RecurringIssue all reference labels by
// slug); to keep that uniform the spec mandates
// metadata.slug == metadata.name. Validate enforces that invariant.
package kinds

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"

	"github.com/crewship-ai/crewship/internal/manifest/internalapi"
)

// labelHexColorPattern matches the canonical six-digit lowercase or
// uppercase hex color (`#RRGGBB`). The same regex appears in
// project.go and milestone.go; keeping it per-kind avoids a shared
// helper file that would force cross-kind coupling.
var labelHexColorPattern = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)

// LabelSpec is the shape under `spec:` for kind: Label.
//
// Color is required by the backend (`color is required` 400 from
// CreateLabel). Description is included per spec — the current
// labels table doesn't persist it, but the field is forward-
// compatible: the POST body carries it, the server silently ignores
// it via readJSON's tolerant decoding, and a future migration can
// add the column without changing the manifest schema.
type LabelSpec struct {
	Color       string `yaml:"color"                 json:"color"`
	Description string `yaml:"description,omitempty" json:"description,omitempty"`
}

// LabelDocument is the top-level document for kind: Label.
type LabelDocument struct {
	APIVersion string               `yaml:"apiVersion" json:"apiVersion"`
	Kind       string               `yaml:"kind"       json:"kind"`
	Metadata   internalapi.Metadata `yaml:"metadata"   json:"metadata"`
	Spec       LabelSpec            `yaml:"spec"       json:"spec"`
}

// LabelRemote mirrors one row of `GET /api/v1/labels`. The backend
// emits `id`, `name`, `color`, `label_group` (see
// internal/api/issue_handler.go labelResponse). Description is
// included for forward-compat once the column lands.
type LabelRemote struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Color       string  `json:"color"`
	LabelGroup  *string `json:"label_group,omitempty"`
	Description string  `json:"description,omitempty"`
}

// Validate enforces the structural rules from SPEC-2 §2 (Label):
//
//   - metadata.name is required
//   - metadata.slug must equal metadata.name (manifest convention to
//     keep cross-kind references uniform; the server keys on `name`
//     but every other kind references labels by slug)
//   - color matches `^#[0-9A-Fa-f]{6}$` if set
//
// The workspace context is unused for Label — labels have no FK
// dependencies — but the parameter is kept on the signature so the
// validate-phase dispatcher in internal/manifest/validate.go can
// invoke every kind through one interface.
func (d *LabelDocument) Validate(_ internalapi.WorkspaceContext) error {
	if d.Metadata.Name == "" {
		return fmt.Errorf("label: metadata.name is required")
	}
	if d.Metadata.Slug == "" {
		return fmt.Errorf("label %q: metadata.slug is required", d.Metadata.Name)
	}
	// The slug==name invariant is the load-bearing rule that lets
	// other kinds reference labels by slug even though the backend
	// keys on name. Surface it with both values in the error so the
	// user can fix either side without guessing which one drifted.
	if d.Metadata.Slug != d.Metadata.Name {
		return fmt.Errorf(
			"label %q: metadata.slug must equal metadata.name (got slug=%q, name=%q)",
			d.Metadata.Name, d.Metadata.Slug, d.Metadata.Name,
		)
	}
	// Color is required by the backend (POST /api/v1/labels returns
	// 400 "color is required" when missing — see
	// internal/api/issue_handler_labels.go). Catching this at
	// validate time means the operator sees one consolidated
	// "missing color" message in the same pass that lists other
	// validation issues, instead of getting a surprise 400 partway
	// through Apply when half the manifest has already been written.
	if d.Spec.Color == "" {
		return fmt.Errorf("label %q: spec.color is required (hex pattern ^#[0-9A-Fa-f]{6}$)", d.Metadata.Name)
	}
	if !labelHexColorPattern.MatchString(d.Spec.Color) {
		return fmt.Errorf(
			"label %q: spec.color %q must match pattern ^#[0-9A-Fa-f]{6}$",
			d.Metadata.Name, d.Spec.Color,
		)
	}
	return nil
}

// Plan diffs the declared label against the remote row (nil if the
// label doesn't exist yet) and returns a single PlanItem. Plan never
// performs network I/O itself; the caller (apply.go) is responsible
// for fetching the remote via ExportLabels / a direct GET. The
// signature mirrors every other kind so the dispatcher can stay
// generic.
//
// Behavior:
//
//   - remote == nil  → Action=Create, POST /api/v1/labels
//   - drifted        → Action=Update, PATCH /api/v1/labels/{id}
//     (body carries only the drifted fields)
//   - matches        → Action=Unchanged (Exec is nil)
//
// Delete is emitted by apply.go's ApplyReplace pass before Plan runs
// the create branch — not by Plan itself — so each PlanItem stays a
// single REST call.
func (d *LabelDocument) Plan(
	_ context.Context,
	_ internalapi.Client,
	remote *LabelRemote,
) ([]internalapi.PlanItem, error) {
	if remote == nil {
		body := map[string]any{
			"name":  d.Metadata.Name,
			"color": d.Spec.Color,
		}
		// The current backend ignores `description`, but we send it
		// so the round-trip becomes lossless the moment the column
		// lands (see SPEC-2 §2 POST body shape).
		if d.Spec.Description != "" {
			body["description"] = d.Spec.Description
		}
		return []internalapi.PlanItem{{
			Kind:        "label",
			Slug:        d.Metadata.Slug,
			Action:      internalapi.ActionCreate,
			Description: fmt.Sprintf("create label %q", d.Metadata.Name),
			Exec: func(ctx context.Context, c internalapi.Client) error {
				return execAndDiscard(ctx, c, "POST", "/api/v1/labels", body)
			},
		}}, nil
	}

	// Compute the field-level diff. PATCH bodies are pointer-style
	// on the backend (Name/Color/LabelGroup are all *string in
	// UpdateLabel) so an omitted field is a true no-op rather than a
	// blank-out. We mirror that by only emitting keys that actually
	// changed.
	patch := map[string]any{}
	if d.Spec.Color != "" && d.Spec.Color != remote.Color {
		patch["color"] = d.Spec.Color
	}
	if d.Spec.Description != "" && d.Spec.Description != remote.Description {
		patch["description"] = d.Spec.Description
	}
	// Name drift shouldn't be reachable under the slug==name
	// invariant (Validate already rejects it), but emit a rename
	// PATCH if we ever see it — silently dropping the diff would
	// leave the manifest disagreeing with reality.
	if d.Metadata.Name != remote.Name {
		patch["name"] = d.Metadata.Name
	}

	if len(patch) == 0 {
		return []internalapi.PlanItem{{
			Kind:        "label",
			Slug:        d.Metadata.Slug,
			Action:      internalapi.ActionUnchanged,
			Description: fmt.Sprintf("label %q is up to date", d.Metadata.Name),
			Exec:        nil,
		}}, nil
	}

	id := remote.ID
	return []internalapi.PlanItem{{
		Kind:        "label",
		Slug:        d.Metadata.Slug,
		Action:      internalapi.ActionUpdate,
		Description: fmt.Sprintf("update label %q (%d field(s))", d.Metadata.Name, len(patch)),
		Exec: func(ctx context.Context, c internalapi.Client) error {
			return execAndDiscard(ctx, c, "PATCH", "/api/v1/labels/"+id, patch)
		},
	}}, nil
}

// DeletePlanItem produces an Action=Delete item for the given remote
// label. Used by ApplyReplace and by the workspace-level "drop
// undeclared rows" pass in sync mode. Kept as a package-level helper
// rather than a method so apply.go can construct it without a
// LabelDocument in hand (you only have the remote row when you're
// pruning).
func DeletePlanItem(remote LabelRemote) internalapi.PlanItem {
	id := remote.ID
	name := remote.Name
	return internalapi.PlanItem{
		Kind:        "label",
		Slug:        name, // slug == name for Label
		Action:      internalapi.ActionDelete,
		Description: fmt.Sprintf("delete label %q", name),
		Exec: func(ctx context.Context, c internalapi.Client) error {
			return execAndDiscard(ctx, c, "DELETE", "/api/v1/labels/"+id, nil)
		},
	}
}

// ExportLabels fetches every label in the workspace and converts
// each row into a LabelDocument suitable for `crewship export`.
// Output is sorted by name for stable diffs across runs (the API
// already returns labels ordered by name ASC, so we just preserve
// that ordering instead of re-sorting).
func ExportLabels(ctx context.Context, c internalapi.Client) ([]*LabelDocument, error) {
	resp, err := c.Get(ctx, "/api/v1/labels")
	if err != nil {
		return nil, fmt.Errorf("list labels: %w", err)
	}
	if resp == nil {
		return nil, fmt.Errorf("list labels: nil response")
	}
	rows, err := decodeLabelList(resp)
	if err != nil {
		return nil, err
	}
	docs := make([]*LabelDocument, 0, len(rows))
	for _, row := range rows {
		// slug must equal name on export so a round-trip
		// (export → apply) survives Validate.
		doc := &LabelDocument{
			APIVersion: "crewship/v1",
			Kind:       "Label",
			Metadata: internalapi.Metadata{
				Name: row.Name,
				Slug: row.Name,
			},
			Spec: LabelSpec{
				Color:       row.Color,
				Description: row.Description,
			},
		}
		docs = append(docs, doc)
	}
	return docs, nil
}

// ---- internal helpers ----

// decodeLabelList tolerates both a flat array (`[{...}, {...}]`) and
// a wrapped `{"labels": [...]}` shape. The current handler returns
// the flat shape; the tolerance is cheap insurance against a future
// pagination wrapper.
func decodeLabelList(resp *internalapi.Response) ([]LabelRemote, error) {
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Best-effort body slurp for the error message; ignore read
		// errors since we already have a status code to surface.
		var preview string
		if resp.Body != nil {
			data, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
			preview = string(data)
		}
		return nil, fmt.Errorf("list labels: HTTP %d: %s", resp.StatusCode, preview)
	}
	if resp.Body == nil {
		return nil, nil
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return nil, fmt.Errorf("read labels body: %w", err)
	}
	if len(data) == 0 {
		return nil, nil
	}
	var flat []LabelRemote
	if err := json.Unmarshal(data, &flat); err == nil {
		return flat, nil
	}
	var wrapped struct {
		Labels []LabelRemote `json:"labels"`
	}
	if err := json.Unmarshal(data, &wrapped); err == nil {
		return wrapped.Labels, nil
	}
	return nil, fmt.Errorf("decode labels: unrecognized response shape")
}

// execAndDiscard runs a single mutating REST call and discards the
// body. Plan items don't need the response payload — apply.go tracks
// the result by counting Action types — so we just verify the
// status code and close the reader. Centralising the dispatch keeps
// every Plan exec closure to one line.
func execAndDiscard(
	ctx context.Context,
	c internalapi.Client,
	method, path string,
	body any,
) error {
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
