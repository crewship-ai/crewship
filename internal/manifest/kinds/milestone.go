// Package kinds holds the per-kind implementations for the declarative
// manifest pipeline. Each kind owns its own *.go file plus a paired
// _test.go and documentation page under docs/manifest. This file
// implements kind: Milestone (project deliverable target).
//
// Milestones are child rows under a Project. The REST surface is
// asymmetric:
//
//	POST   /api/v1/projects/{projectId}/milestones    (nested)
//	GET    /api/v1/projects/{projectId}/milestones    (nested)
//	PATCH  /api/v1/milestones/{id}                    (flat)
//	DELETE /api/v1/milestones/{id}                    (flat)
//
// Apply therefore has to resolve `project_slug` → `projectId` BEFORE
// it can perform any operation, both for the create POST and for the
// nested list GET used to detect drift.
//
// All non-exported helpers in this file carry a `milestone` prefix so
// that the parallel agents implementing other kinds in this package
// can ship their own checkStatus/readAll/slugify variants without
// duplicate-symbol link errors.
package kinds

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/manifest/internalapi"
)

// MilestoneSpec is the shape under `spec:` for kind: Milestone.
//
// `project_slug` is the only required field and is the slug of a
// Project kind in the same workspace (declared earlier in the bundle
// or already present on the server). All other fields are optional
// and map directly onto the milestone_handler POST body.
type MilestoneSpec struct {
	// ProjectSlug is the parent project. Required. Resolved to a
	// concrete project_id at Plan time via GET /api/v1/projects.
	ProjectSlug string `yaml:"project_slug"          json:"project_slug"`

	// Description is the free-form human description.
	Description string `yaml:"description,omitempty" json:"description,omitempty"`

	// TargetDate is the milestone deadline as YYYY-MM-DD. Empty means
	// no deadline.
	TargetDate string `yaml:"target_date,omitempty" json:"target_date,omitempty"`

	// Status is the milestone lifecycle state: planned | active |
	// completed. Defaults to "planned" when empty in the YAML; the
	// handler itself defaults to "active" on POST but the manifest
	// stays explicit so re-export round-trips.
	Status string `yaml:"status,omitempty"      json:"status,omitempty"`
}

// MilestoneDocument is the YAML-level envelope produced/consumed by
// the manifest pipeline.
type MilestoneDocument struct {
	APIVersion string               `yaml:"apiVersion" json:"apiVersion"`
	Kind       string               `yaml:"kind"       json:"kind"`
	Metadata   internalapi.Metadata `yaml:"metadata"   json:"metadata"`
	Spec       MilestoneSpec        `yaml:"spec"       json:"spec"`
}

// MilestoneRemote is the snapshot of the server-side milestone row
// that Plan compares against. Fields mirror milestoneResponse from
// internal/api/milestone_handler.go but only carry what Plan actually
// diffs on.
type MilestoneRemote struct {
	ID          string  `json:"id"`
	ProjectID   string  `json:"project_id"`
	Name        string  `json:"name"`
	Description *string `json:"description"`
	TargetDate  *string `json:"target_date"`
	Status      string  `json:"status"`
}

// Allowed enum values. Kept as package-level vars so the Validate
// test can reflect on them rather than duplicating the list.
var (
	milestoneStatuses = map[string]struct{}{
		"planned":   {},
		"active":    {},
		"completed": {},
	}

	milestoneDateRE = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
)

// Validate enforces the structural rules from SPEC-2 section 3.
//
// Rules:
//   - metadata.name must be set (server requires `name`).
//   - metadata.slug must be set (manifest idempotency key).
//   - spec.project_slug must be set AND present in workspaceCtx
//     (declared or remote project).
//   - spec.target_date if set must parse as YYYY-MM-DD.
//   - spec.status if set must be one of {planned, active, completed}.
func (d *MilestoneDocument) Validate(workspaceCtx internalapi.WorkspaceContext) error {
	if strings.TrimSpace(d.Metadata.Name) == "" {
		return fmt.Errorf("milestone: metadata.name is required")
	}
	if strings.TrimSpace(d.Metadata.Slug) == "" {
		return fmt.Errorf("milestone %q: metadata.slug is required", d.Metadata.Name)
	}
	if strings.TrimSpace(d.Spec.ProjectSlug) == "" {
		return fmt.Errorf("milestone %q: spec.project_slug is required", d.Metadata.Slug)
	}
	if !workspaceCtx.HasProject(d.Spec.ProjectSlug) {
		return fmt.Errorf(
			"milestone %q: spec.project_slug %q does not reference any declared or remote project",
			d.Metadata.Slug, d.Spec.ProjectSlug,
		)
	}
	if d.Spec.TargetDate != "" {
		if !milestoneDateRE.MatchString(d.Spec.TargetDate) {
			return fmt.Errorf(
				"milestone %q: spec.target_date %q must be YYYY-MM-DD",
				d.Metadata.Slug, d.Spec.TargetDate,
			)
		}
		if _, err := time.Parse("2006-01-02", d.Spec.TargetDate); err != nil {
			return fmt.Errorf(
				"milestone %q: spec.target_date %q is not a valid calendar date: %w",
				d.Metadata.Slug, d.Spec.TargetDate, err,
			)
		}
	}
	if d.Spec.Status != "" {
		if _, ok := milestoneStatuses[d.Spec.Status]; !ok {
			return fmt.Errorf(
				"milestone %q: spec.status %q must be one of planned|active|completed",
				d.Metadata.Slug, d.Spec.Status,
			)
		}
	}
	return nil
}

// Plan diffs the declared milestone against the server. `remote` is
// nil when the milestone does not exist yet; callers use
// LookupMilestoneRemote to compute the remote snapshot up front.
//
// On a fresh apply Plan emits a single ActionCreate item whose Exec
// closure POSTs to the nested endpoint. Update goes to the flat
// /api/v1/milestones/{id}; Delete the same.
func (d *MilestoneDocument) Plan(ctx context.Context, c internalapi.Client, remote *MilestoneRemote) ([]internalapi.PlanItem, error) {
	// Always resolve the parent project so both Create and Update can
	// produce useful error messages if the project has gone missing
	// between Validate and Plan (e.g. a parallel admin deleted it).
	projectID, err := milestoneResolveProjectIDBySlug(ctx, c, d.Spec.ProjectSlug)
	if err != nil {
		return nil, fmt.Errorf("milestone %q: resolve project_slug %q: %w",
			d.Metadata.Slug, d.Spec.ProjectSlug, err)
	}

	if remote == nil {
		body := d.toCreateBody()
		return []internalapi.PlanItem{{
			Kind:        "milestone",
			Slug:        d.Metadata.Slug,
			Action:      internalapi.ActionCreate,
			Description: fmt.Sprintf("create milestone %q under project %q", d.Metadata.Name, d.Spec.ProjectSlug),
			Exec: func(ctx context.Context, c internalapi.Client) error {
				path := fmt.Sprintf("/api/v1/projects/%s/milestones", projectID)
				resp, err := c.Post(ctx, path, body)
				if err != nil {
					return fmt.Errorf("POST %s: %w", path, err)
				}
				return milestoneCheckStatus(resp, "create milestone "+d.Metadata.Slug)
			},
		}}, nil
	}

	// Diff: produce a PATCH body containing only the drifted fields.
	patch := d.diffPatch(remote)
	if len(patch) == 0 {
		return []internalapi.PlanItem{{
			Kind:        "milestone",
			Slug:        d.Metadata.Slug,
			Action:      internalapi.ActionUnchanged,
			Description: fmt.Sprintf("milestone %q is up to date", d.Metadata.Name),
		}}, nil
	}

	milestoneID := remote.ID
	return []internalapi.PlanItem{{
		Kind:        "milestone",
		Slug:        d.Metadata.Slug,
		Action:      internalapi.ActionUpdate,
		Description: fmt.Sprintf("update milestone %q (%d field(s))", d.Metadata.Name, len(patch)),
		Exec: func(ctx context.Context, c internalapi.Client) error {
			path := fmt.Sprintf("/api/v1/milestones/%s", milestoneID)
			resp, err := c.Patch(ctx, path, patch)
			if err != nil {
				return fmt.Errorf("PATCH %s: %w", path, err)
			}
			return milestoneCheckStatus(resp, "update milestone "+d.Metadata.Slug)
		},
	}}, nil
}

// toCreateBody builds the POST body for the nested create endpoint.
// Mirrors the milestoneHandler.Create reader struct exactly so any
// field renames must be made in lockstep.
func (d *MilestoneDocument) toCreateBody() map[string]any {
	body := map[string]any{
		"name": d.Metadata.Name,
	}
	if d.Spec.Description != "" {
		body["description"] = d.Spec.Description
	}
	if d.Spec.TargetDate != "" {
		body["target_date"] = d.Spec.TargetDate
	}
	if d.Spec.Status != "" {
		body["status"] = d.Spec.Status
	}
	return body
}

// diffPatch produces a sparse PATCH body containing only the keys
// that drift between the declared spec and the remote row. Returning
// an empty map signals "unchanged" to the caller.
func (d *MilestoneDocument) diffPatch(remote *MilestoneRemote) map[string]any {
	patch := map[string]any{}

	if remote.Name != d.Metadata.Name {
		patch["name"] = d.Metadata.Name
	}
	if milestoneDerefOrEmpty(remote.Description) != d.Spec.Description {
		patch["description"] = d.Spec.Description
	}
	if milestoneDerefOrEmpty(remote.TargetDate) != d.Spec.TargetDate {
		patch["target_date"] = d.Spec.TargetDate
	}
	// Status defaults to "active" on the server when omitted at
	// create time but the manifest carries the explicit value. Only
	// diff when the manifest spelled it out.
	if d.Spec.Status != "" && remote.Status != d.Spec.Status {
		patch["status"] = d.Spec.Status
	}
	return patch
}

// ExportMilestones walks every project in the workspace and pulls
// its milestones, producing one MilestoneDocument per row. The
// parent project's slug is folded back into spec.project_slug so the
// output is directly re-applyable.
func ExportMilestones(ctx context.Context, c internalapi.Client) ([]*MilestoneDocument, error) {
	projects, err := milestoneListProjects(ctx, c)
	if err != nil {
		return nil, fmt.Errorf("export milestones: list projects: %w", err)
	}

	var out []*MilestoneDocument
	for _, p := range projects {
		milestones, err := milestoneListForProject(ctx, c, p.ID)
		if err != nil {
			return nil, fmt.Errorf("export milestones for project %q: %w", p.Slug, err)
		}
		for _, m := range milestones {
			doc := &MilestoneDocument{
				APIVersion: "crewship/v1",
				Kind:       "Milestone",
				Metadata: internalapi.Metadata{
					Name: m.Name,
					Slug: milestoneSlugFromName(m.Name),
				},
				Spec: MilestoneSpec{
					ProjectSlug: p.Slug,
					Description: milestoneDerefOrEmpty(m.Description),
					TargetDate:  milestoneDerefOrEmpty(m.TargetDate),
					Status:      m.Status,
				},
			}
			out = append(out, doc)
		}
	}
	return out, nil
}

// LookupMilestoneRemote fetches the current remote state for one
// declared milestone document. Returns nil + nil when the project
// exists but no milestone with the declared name is present (i.e.
// ActionCreate territory). Apply uses this before calling Plan so
// the Plan method's `remote` argument is already populated.
func LookupMilestoneRemote(ctx context.Context, c internalapi.Client, d *MilestoneDocument) (*MilestoneRemote, error) {
	projectID, err := milestoneResolveProjectIDBySlug(ctx, c, d.Spec.ProjectSlug)
	if err != nil {
		// Project missing entirely → milestone obviously absent.
		// Surface the underlying error so callers can choose between
		// "wait for the project to be created earlier in the bundle"
		// and "abort". Apply orders phases such that the project
		// will already exist on the server by the time Milestone
		// runs.
		return nil, err
	}
	rows, err := milestoneListForProject(ctx, c, projectID)
	if err != nil {
		return nil, err
	}
	for i := range rows {
		if rows[i].Name == d.Metadata.Name {
			return &rows[i], nil
		}
	}
	return nil, nil
}

// ── helpers (all milestone-prefixed) ───────────────────────────────────────

// milestoneProjectStub is the minimal shape the milestone code needs
// from the project list endpoint. Defined locally so this file stays
// self-contained and does not couple to another kind's response
// struct (cross-kind imports inside `kinds` would create
// initialisation-ordering surprises and break test isolation).
type milestoneProjectStub struct {
	ID   string `json:"id"`
	Slug string `json:"slug"`
	Name string `json:"name"`
}

// milestoneListProjects returns every project in the workspace. The
// server has no slug filter so we fetch the full list and filter
// client-side; the list is workspace-scoped and small (< few hundred
// even on large instances) so this stays O(N) in memory but only one
// HTTP round-trip.
func milestoneListProjects(ctx context.Context, c internalapi.Client) ([]milestoneProjectStub, error) {
	resp, err := c.Get(ctx, "/api/v1/projects")
	if err != nil {
		return nil, fmt.Errorf("GET /api/v1/projects: %w", err)
	}
	if err := milestoneCheckStatus(resp, "list projects"); err != nil {
		return nil, err
	}
	body, err := milestoneReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read /api/v1/projects body: %w", err)
	}
	if len(body) == 0 {
		return nil, nil
	}
	var projects []milestoneProjectStub
	if err := json.Unmarshal(body, &projects); err != nil {
		return nil, fmt.Errorf("decode /api/v1/projects: %w", err)
	}
	return projects, nil
}

// milestoneResolveProjectIDBySlug walks the project list and returns
// the project_id whose slug matches. Returns a not-found-style error
// if no row matches; the caller wraps it with the milestone context.
func milestoneResolveProjectIDBySlug(ctx context.Context, c internalapi.Client, slug string) (string, error) {
	projects, err := milestoneListProjects(ctx, c)
	if err != nil {
		return "", err
	}
	for _, p := range projects {
		if p.Slug == slug {
			return p.ID, nil
		}
	}
	return "", fmt.Errorf("project with slug %q not found", slug)
}

// milestoneListForProject hits the nested list endpoint and decodes
// the result into MilestoneRemote slices. Used by both Plan (drift
// detection via LookupMilestoneRemote) and ExportMilestones.
func milestoneListForProject(ctx context.Context, c internalapi.Client, projectID string) ([]MilestoneRemote, error) {
	path := fmt.Sprintf("/api/v1/projects/%s/milestones", projectID)
	resp, err := c.Get(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", path, err)
	}
	if err := milestoneCheckStatus(resp, "list milestones"); err != nil {
		return nil, err
	}
	body, err := milestoneReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read %s body: %w", path, err)
	}
	if len(body) == 0 {
		return nil, nil
	}
	var rows []MilestoneRemote
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	return rows, nil
}

// milestoneSlugFromName produces a kebab-case slug for round-trip
// identity on export. The server table has no slug column for
// milestones, so on export we synthesise one from the human name.
// Two milestones with the same name in different projects would
// collide here, which is fine: the manifest already disallows
// duplicate slugs within a bundle and the user will be prompted.
func milestoneSlugFromName(name string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevDash = false
		case r == '-' || r == '_' || r == ' ' || r == '.':
			if !prevDash && b.Len() > 0 {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	out := b.String()
	out = strings.TrimRight(out, "-")
	if out == "" {
		return "milestone"
	}
	return out
}

// milestoneDerefOrEmpty unboxes a *string into its value or "" for
// nil. The milestone REST API returns sql.NullString-style pointers;
// the manifest treats absent and "" as equivalent for diffing
// purposes.
func milestoneDerefOrEmpty(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// milestoneCheckStatus returns an error if the response status code
// is outside 2xx, decorating the message with the operation name so
// a chain of wraps stays readable.
func milestoneCheckStatus(resp *internalapi.Response, op string) error {
	if resp == nil {
		return fmt.Errorf("%s: nil response", op)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := milestoneReadAll(resp.Body)
		return fmt.Errorf("%s: HTTP %d: %s", op, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

// milestoneReadAll consumes the body reader; tolerates nil to keep
// test mocks simple.
func milestoneReadAll(r io.Reader) ([]byte, error) {
	if r == nil {
		return nil, nil
	}
	return io.ReadAll(r)
}
