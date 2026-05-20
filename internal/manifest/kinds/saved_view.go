// Package kinds holds one Go file per declarative manifest kind. Each
// file defines the YAML-facing struct shape (Spec + Document), the
// remote-state shape returned by the server (Remote), and the three
// per-kind verbs Validate / Plan / Export that the top-level
// internal/manifest package wires together via internalapi.Client.
//
// This file implements kind: SavedView — per-user (or workspace-shared)
// filter+sort presets over issues/missions/runs. The server stores the
// filter and sort as opaque JSON TEXT columns; this layer keeps a
// structured Go representation so manifest authors get type-checked
// fields and diff-by-field plans instead of fighting opaque strings.
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

// ── YAML-facing shapes ─────────────────────────────────────────────────────

// SavedViewFilter is the structured filter clause under spec.filter.
// All fields are optional. An empty filter object means "match
// everything" — useful for inbox-style views ordered purely by sort.
//
// Cross-kind references use slugs (LabelSlugs, AssigneeAgentSlug,
// ProjectSlug); Validate resolves them against WorkspaceContext so
// typos surface at parse time rather than as a confused empty board
// the next time someone opens the view.
type SavedViewFilter struct {
	// Status accepts any issue/mission/run status string the server
	// recognises (e.g. "todo", "in_progress", "done"). The manifest
	// layer doesn't enforce the per-entity_type enum here because the
	// set differs between entity types — the server is authoritative.
	Status []string `yaml:"status,omitempty" json:"status,omitempty"`

	// LabelSlugs filters to issues that carry ALL of the named labels
	// (AND semantics — matches the existing board filter behavior).
	// Each slug must resolve via WorkspaceContext.HasLabel at validate
	// time.
	LabelSlugs []string `yaml:"label_slugs,omitempty" json:"label_slugs,omitempty"`

	// AssigneeAgentSlug filters to items assigned to a specific agent.
	// Empty string = no assignee filter. Resolution to an agent id is
	// the server's job; manifest layer just passes the slug through.
	AssigneeAgentSlug string `yaml:"assignee_agent_slug,omitempty" json:"assignee_agent_slug,omitempty"`

	// ProjectSlug narrows to a single project. Empty = all projects.
	ProjectSlug string `yaml:"project_slug,omitempty" json:"project_slug,omitempty"`
}

// SavedViewSort is the structured sort clause under spec.sort. The
// server treats `field` opaquely (any column name on the underlying
// table is fair game), so the manifest layer doesn't enumerate the
// allowed values — only `direction` is enum-checked.
type SavedViewSort struct {
	// Field is the column name to sort by (e.g. "created_at",
	// "priority", "updated_at"). Server validates it; an unknown
	// column comes back as a 400 at apply time.
	Field string `yaml:"field" json:"field"`

	// Direction is "asc" or "desc". Validate enforces the enum.
	Direction string `yaml:"direction" json:"direction"`
}

// SavedViewSpec is the shape under `spec:` for a kind: SavedView
// document. EntityType picks the underlying domain (issues vs. missions
// vs. runs); the filter+sort then apply to that domain.
type SavedViewSpec struct {
	// Shared, when true, makes the view visible to every workspace
	// member. Default false = visible only to the creating user.
	// Manifest authors typically set this true for "team standard"
	// views committed to a manifest, since per-user state is usually
	// not what you want versioned.
	Shared bool `yaml:"shared" json:"shared"`

	// EntityType picks the underlying record kind the view filters
	// over. One of: issue, mission, run.
	EntityType string `yaml:"entity_type" json:"entity_type"`

	// Filter is the structured WHERE clause (see SavedViewFilter).
	// Marshalled to a JSON string when sent over the wire because the
	// server stores it as opaque TEXT.
	Filter SavedViewFilter `yaml:"filter,omitempty" json:"filter,omitempty"`

	// Sort is the structured ORDER BY clause. Marshalled to JSON like
	// Filter. Required — even an empty view needs deterministic
	// ordering for stable UX.
	Sort SavedViewSort `yaml:"sort" json:"sort"`
}

// SavedViewDocument is the top-level document shape (apiVersion+kind+
// metadata+spec). One Go file per kind, one Document type per kind —
// the parser dispatches on Kind and decodes into the right shape.
type SavedViewDocument struct {
	APIVersion string               `yaml:"apiVersion" json:"apiVersion"`
	Kind       string               `yaml:"kind"       json:"kind"`
	Metadata   internalapi.Metadata `yaml:"metadata"   json:"metadata"`
	Spec       SavedViewSpec        `yaml:"spec"       json:"spec"`
}

// ── Remote (server-side) shape ─────────────────────────────────────────────

// SavedViewRemote mirrors the row shape GET /api/v1/saved-views
// returns. Filter and Sort arrive as raw JSON strings; reverse helpers
// (toSpec) lift them back into structured form for Plan diffing and
// Export round-tripping.
type SavedViewRemote struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Shared      bool    `json:"shared"`
	EntityType  string  `json:"entity_type"`
	FilterJSON  string  `json:"filter_json"`
	SortJSON    *string `json:"sort_json"`
	CreatedAt   string  `json:"created_at"`
	Description string  `json:"description,omitempty"`
}

// toSpec converts a remote row into a SavedViewSpec (filter+sort
// unmarshalled). Used by Plan to diff and by Export to round-trip.
// Defensive: a malformed JSON string yields an empty struct + error so
// the caller can decide whether to surface it.
func (r *SavedViewRemote) toSpec() (SavedViewSpec, error) {
	spec := SavedViewSpec{
		Shared:     r.Shared,
		EntityType: r.EntityType,
	}
	if r.FilterJSON != "" {
		if err := json.Unmarshal([]byte(r.FilterJSON), &spec.Filter); err != nil {
			return spec, fmt.Errorf("unmarshal filter_json: %w", err)
		}
	}
	if r.SortJSON != nil && *r.SortJSON != "" {
		if err := json.Unmarshal([]byte(*r.SortJSON), &spec.Sort); err != nil {
			return spec, fmt.Errorf("unmarshal sort_json: %w", err)
		}
	}
	return spec, nil
}

// ── Validate ───────────────────────────────────────────────────────────────

// savedViewEntityTypes lists the entity_type enum values. Kept as a
// slice (not a map) so error messages render in declared order. Name-
// prefixed because every kind in this package would otherwise want a
// "validEntityTypes" symbol and Go has no per-file scoping.
var savedViewEntityTypes = []string{"issue", "mission", "run"}

// savedViewSortDirections lists the direction enum values. Same
// rationale as savedViewEntityTypes.
var savedViewSortDirections = []string{"asc", "desc"}

// Validate checks structural rules:
//   - entity_type must be one of {issue, mission, run}
//   - sort.direction must be one of {asc, desc}
//   - sort.field must be non-empty (server defaults are not assumed
//     here — manifests should be explicit)
//   - every filter.label_slugs entry must resolve via ctx.HasLabel
//   - filter.project_slug (if set) must resolve via ctx.HasProject
//
// Returns nil on success. The error string is human-facing and goes
// directly into the CLI's "manifest invalid" output.
func (d *SavedViewDocument) Validate(ctx internalapi.WorkspaceContext) error {
	if d.Metadata.Slug == "" {
		return fmt.Errorf("saved_view %q: metadata.slug is required", d.Metadata.Name)
	}

	if !savedViewContains(savedViewEntityTypes, d.Spec.EntityType) {
		return fmt.Errorf(
			"saved_view %q: spec.entity_type %q is not one of {%s}",
			d.Metadata.Slug, d.Spec.EntityType, strings.Join(savedViewEntityTypes, ", "),
		)
	}

	if d.Spec.Sort.Field == "" {
		return fmt.Errorf("saved_view %q: spec.sort.field is required", d.Metadata.Slug)
	}
	if !savedViewContains(savedViewSortDirections, d.Spec.Sort.Direction) {
		return fmt.Errorf(
			"saved_view %q: spec.sort.direction %q is not one of {%s}",
			d.Metadata.Slug, d.Spec.Sort.Direction, strings.Join(savedViewSortDirections, ", "),
		)
	}

	for _, slug := range d.Spec.Filter.LabelSlugs {
		if !ctx.HasLabel(slug) {
			return fmt.Errorf(
				"saved_view %q: spec.filter.label_slugs references unknown label %q",
				d.Metadata.Slug, slug,
			)
		}
	}

	if d.Spec.Filter.ProjectSlug != "" && !ctx.HasProject(d.Spec.Filter.ProjectSlug) {
		return fmt.Errorf(
			"saved_view %q: spec.filter.project_slug %q does not exist",
			d.Metadata.Slug, d.Spec.Filter.ProjectSlug,
		)
	}

	return nil
}

// ── Plan ───────────────────────────────────────────────────────────────────

// Plan compares the declared SavedView against the remote list and
// emits one PlanItem. The manifest layer's saved-view list is small
// (typically <50 per workspace) so list-and-filter is cheaper than
// any clever indexing scheme.
//
// remote is the matching remote row by metadata.name (saved_views have
// no slug column on the server — see spec note), or nil if not found.
// Caller resolves this; Plan stays oblivious to the lookup strategy so
// a future schema migration to slug-keyed lookups doesn't require
// changing every kind in lockstep.
func (d *SavedViewDocument) Plan(
	_ context.Context,
	_ internalapi.Client,
	remote *SavedViewRemote,
) ([]internalapi.PlanItem, error) {
	postBody, err := d.toPostBody()
	if err != nil {
		return nil, fmt.Errorf("saved_view %q: build post body: %w", d.Metadata.Slug, err)
	}

	// Create case: no remote row exists for this name.
	if remote == nil {
		item := internalapi.PlanItem{
			Kind:        "saved_view",
			Slug:        d.Metadata.Slug,
			Action:      internalapi.ActionCreate,
			Description: fmt.Sprintf("create saved view %q (%s)", d.Metadata.Name, d.Spec.EntityType),
			Exec: func(ctx context.Context, c internalapi.Client) error {
				_, err := c.Post(ctx, "/api/v1/saved-views", postBody)
				return err
			},
		}
		return []internalapi.PlanItem{item}, nil
	}

	// Compare declared vs remote. Filter and sort are compared by
	// re-marshalling to JSON so field-order differences don't trigger
	// spurious updates.
	remoteSpec, err := remote.toSpec()
	if err != nil {
		return nil, fmt.Errorf("saved_view %q: parse remote: %w", d.Metadata.Slug, err)
	}

	if savedViewSpecsEqual(d.Spec, remoteSpec) && d.Metadata.Name == remote.Name {
		return []internalapi.PlanItem{{
			Kind:        "saved_view",
			Slug:        d.Metadata.Slug,
			Action:      internalapi.ActionUnchanged,
			Description: fmt.Sprintf("saved view %q unchanged", d.Metadata.Name),
		}}, nil
	}

	// Drift exists — emit an Update. PATCH body covers every mutable
	// field (server accepts partial PATCHes but sending the full set
	// keeps Apply's behavior identical regardless of which field
	// drifted).
	patchBody := map[string]any{
		"name":         d.Metadata.Name,
		"filters_json": postBody["filter_json"],
		"sort_json":    postBody["sort_json"],
		"shared":       d.Spec.Shared,
	}
	viewID := remote.ID

	item := internalapi.PlanItem{
		Kind:        "saved_view",
		Slug:        d.Metadata.Slug,
		Action:      internalapi.ActionUpdate,
		Description: fmt.Sprintf("update saved view %q", d.Metadata.Name),
		Exec: func(ctx context.Context, c internalapi.Client) error {
			_, err := c.Patch(ctx, "/api/v1/saved-views/"+viewID, patchBody)
			return err
		},
	}
	return []internalapi.PlanItem{item}, nil
}

// toPostBody marshals the structured filter+sort into JSON strings and
// assembles the POST body the saved-views handler expects. Returns an
// error if either side fails to marshal — shouldn't happen for valid
// inputs (the structs are JSON-clean), but defensive in case a future
// field carries something unmarshallable.
func (d *SavedViewDocument) toPostBody() (map[string]any, error) {
	filterJSON, err := json.Marshal(d.Spec.Filter)
	if err != nil {
		return nil, fmt.Errorf("marshal filter: %w", err)
	}
	sortJSON, err := json.Marshal(d.Spec.Sort)
	if err != nil {
		return nil, fmt.Errorf("marshal sort: %w", err)
	}
	return map[string]any{
		"name":        d.Metadata.Name,
		"shared":      d.Spec.Shared,
		"entity_type": d.Spec.EntityType,
		"filter_json": string(filterJSON),
		"sort_json":   string(sortJSON),
	}, nil
}

// savedViewSpecsEqual reports whether two SavedViewSpec values are
// semantically equal. Compares filter and sort by JSON-canonicalising
// (sort slices, re-marshal) so {labels: [a,b]} == {labels: [b,a]}.
func savedViewSpecsEqual(a, b SavedViewSpec) bool {
	if a.Shared != b.Shared || a.EntityType != b.EntityType {
		return false
	}
	if !savedViewSortEqual(a.Sort, b.Sort) {
		return false
	}
	return savedViewFilterEqual(a.Filter, b.Filter)
}

func savedViewSortEqual(a, b SavedViewSort) bool {
	return a.Field == b.Field && a.Direction == b.Direction
}

func savedViewFilterEqual(a, b SavedViewFilter) bool {
	if a.AssigneeAgentSlug != b.AssigneeAgentSlug || a.ProjectSlug != b.ProjectSlug {
		return false
	}
	return savedViewStringSliceEqualSorted(a.Status, b.Status) &&
		savedViewStringSliceEqualSorted(a.LabelSlugs, b.LabelSlugs)
}

func savedViewStringSliceEqualSorted(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	ac := append([]string(nil), a...)
	bc := append([]string(nil), b...)
	sort.Strings(ac)
	sort.Strings(bc)
	for i := range ac {
		if ac[i] != bc[i] {
			return false
		}
	}
	return true
}

// ── Export ─────────────────────────────────────────────────────────────────

// ExportSavedViews fetches every saved view in the workspace and
// converts each row into a SavedViewDocument suitable for re-emission
// via `crewship export`. The filter_json and sort_json TEXT columns
// are unmarshalled back into structured spec form so the emitted YAML
// is human-editable, not a wall of escaped JSON.
//
// The metadata.slug field is derived from the row's `name` (lowercased,
// non-alnum collapsed to `-`) because the server's saved_views table
// has no slug column. Callers needing strict round-trip preservation
// of names with unusual characters should keep the original name and
// re-derive the slug on the next apply — the server keys on `name`
// anyway, so slug churn is cosmetic only.
func ExportSavedViews(ctx context.Context, c internalapi.Client) ([]*SavedViewDocument, error) {
	resp, err := c.Get(ctx, "/api/v1/saved-views")
	if err != nil {
		return nil, fmt.Errorf("list saved views: %w", err)
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("list saved views: HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	if err != nil {
		return nil, fmt.Errorf("read saved views body: %w", err)
	}

	// Accept both a flat array and a {saved_views: [...]} wrapper —
	// the current handler returns a flat array but normalising here
	// future-proofs the export against a wrapper-shape migration.
	var rows []SavedViewRemote
	if len(body) > 0 {
		if err := json.Unmarshal(body, &rows); err != nil {
			var wrapped struct {
				SavedViews []SavedViewRemote `json:"saved_views"`
			}
			if err2 := json.Unmarshal(body, &wrapped); err2 != nil {
				return nil, fmt.Errorf("decode saved views: %w", err)
			}
			rows = wrapped.SavedViews
		}
	}

	docs := make([]*SavedViewDocument, 0, len(rows))
	for i := range rows {
		row := rows[i]
		spec, err := row.toSpec()
		if err != nil {
			return nil, fmt.Errorf("saved view %q: %w", row.Name, err)
		}
		docs = append(docs, &SavedViewDocument{
			APIVersion: "crewship/v1",
			Kind:       "SavedView",
			Metadata: internalapi.Metadata{
				Name:        row.Name,
				Slug:        savedViewSlugify(row.Name),
				Description: row.Description,
			},
			Spec: spec,
		})
	}
	return docs, nil
}

// ── Helpers ────────────────────────────────────────────────────────────────

// savedViewSlugify derives a kebab-case slug from a human-readable
// view name. Lowercase ASCII, non-alnum collapsed to `-`, trimmed.
// Not stable across rename — Export consumers that need stable slugs
// should set them explicitly in the emitted YAML.
//
// Name-prefixed because the kinds package already has sibling
// slugifiers (triage_rule, workflow_template) that operate on
// different inputs; a shared helper isn't worth the coupling.
func savedViewSlugify(name string) string {
	var b strings.Builder
	b.Grow(len(name))
	prevDash := false
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash && b.Len() > 0 {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	out := strings.TrimRight(b.String(), "-")
	if out == "" {
		return "view"
	}
	return out
}

// savedViewContains reports whether needle is in haystack. Small
// helper used by Validate's enum checks; name-prefixed to avoid
// collisions with whatever sibling kinds may define.
func savedViewContains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
