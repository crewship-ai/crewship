// Package kinds holds the per-kind implementations for the declarative
// manifest pipeline. This file implements kind: CrewTemplate — a
// REFERENCE kind whose manifest entry DEPLOYS an existing template
// (creating a new crew + agents from it) but cannot author the
// template itself. Authoring lives in `kind: Crew` or in the seeded
// built-in catalog under internal/database; the manifest pipeline only
// invokes the existing deploy endpoint.
//
// One-shot semantics
// ------------------
//
// The endpoint backing this kind is POST
// /api/v1/crew-templates/{slug}/deploy. It writes the crew + agents in
// a single transaction and rejects re-deploying onto an existing crew
// (errCrewSlugConflict, HTTP 409). There is no PATCH /deploy and no
// "diff & re-apply" path: once a crew with the override slug exists,
// the template kind has nothing left to do.
//
// As a consequence Plan only emits two actions:
//
//   - ActionCreate   — no crew with the override slug exists yet
//   - ActionUnchanged — the override slug already names a crew (we
//     cannot tell whether it was actually deployed
//     from THIS template; idempotency is by slug
//     alone)
//
// Setting `deploy: false` doesn't trigger an undeploy (the API has no
// such verb) — it's a no-op + warning, surfaced via PlanItem.Description
// so dry-run output explains why nothing happened.
package kinds

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/crewship-ai/crewship/internal/manifest/internalapi"
)

// crewTemplateAPIVersion / crewTemplateKind are the only literal
// envelope values this kind accepts; declaring them as constants keeps
// the parse/dispatch loop in internal/manifest/parse.go from
// drifting away from the per-kind file.
const (
	crewTemplateAPIVersion = "crewship/v1"
	crewTemplateKind       = "CrewTemplate"
)

// crewSlugPattern is the kebab-case slug shape the crews handler
// itself enforces (lowercase letters, digits, hyphens; cannot start
// or end with a hyphen). We re-validate here so an obviously invalid
// override slug is caught at manifest Validate() — before any HTTP
// round-trip — rather than surfacing as a server-side 400 deep inside
// Apply.
var crewSlugPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$`)

// ── Types ────────────────────────────────────────────────────────────────

// CrewTemplateSpec is the shape under `spec:` for kind: CrewTemplate.
//
// `crew_slug_override` is REQUIRED because a single template can
// deploy many crews — the override is what distinguishes "deploy
// engineering-team for project A" from "deploy engineering-team for
// project B". Without it Plan would have no idempotency key and
// every apply would either no-op or double-create.
//
// `inputs` is currently a forward-compatibility hook: the existing
// deploy handler ignores unknown keys via readJSON's tolerant
// decoding, so future template variables (devcontainer_image,
// llm_model overrides, …) can land without a manifest schema bump.
// We forward the map verbatim in the POST body.
type CrewTemplateSpec struct {
	// Deploy gates the action. true (default) means "ensure the crew
	// exists; create it if missing". false means "no-op" — kept
	// explicit so a user can pin a CrewTemplate document in the
	// bundle without re-deploying on every apply.
	Deploy bool `yaml:"deploy"                       json:"deploy"`

	// CrewSlugOverride is the slug the newly created crew should
	// carry. REQUIRED. Validated as kebab-case at Validate time;
	// uniqueness within the workspace is enforced server-side at
	// Apply (the deploy endpoint returns 409 on collision).
	CrewSlugOverride string `yaml:"crew_slug_override"           json:"crew_slug_override"`

	// Inputs are optional template parameters forwarded verbatim to
	// the deploy endpoint. Currently ignored server-side; kept for
	// forward compatibility.
	Inputs map[string]any `yaml:"inputs,omitempty"             json:"inputs,omitempty"`
}

// CrewTemplateDocument is the YAML envelope produced/consumed by the
// manifest pipeline. metadata.slug names the SOURCE template (must
// exist in the workspace's crew_templates catalog — either as a
// built-in or a workspace-authored entry).
type CrewTemplateDocument struct {
	APIVersion string               `yaml:"apiVersion" json:"apiVersion"`
	Kind       string               `yaml:"kind"       json:"kind"`
	Metadata   internalapi.Metadata `yaml:"metadata"   json:"metadata"`
	Spec       CrewTemplateSpec     `yaml:"spec"       json:"spec"`
}

// CrewTemplateRemote captures what Plan needs to know about the
// state of the world for one declared CrewTemplate document:
//
//   - TemplateExists / Template — does the SOURCE template (named by
//     metadata.slug) exist on the server? Plan returns an error if
//     not; we can't deploy what isn't there.
//   - DeployedCrew — the crew (if any) currently occupying the
//     override slug. Plan uses presence/absence of this field to
//     decide Create vs Unchanged. We can't tell whether the crew was
//     ACTUALLY deployed from this template (no provenance column on
//     `crews`), so any pre-existing crew under the override slug
//     blocks Create — by design, since the server would 409 anyway.
//   - DeployedCrews — list of crews whose slug suggests they were
//     deployed from this template's slug. Populated by export only;
//     Plan ignores it. With no provenance column it's a best-effort
//     prefix heuristic.
type CrewTemplateRemote struct {
	TemplateExists bool
	Template       *crewTemplateStub
	DeployedCrew   *crewStub
	DeployedCrews  []crewStub
}

// crewTemplateStub is the minimal shape this kind needs from the
// /api/v1/crew-templates list endpoint. Defined locally so the file
// stays self-contained.
type crewTemplateStub struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Slug      string `json:"slug"`
	IsBuiltin bool   `json:"is_builtin"`
}

// crewStub is the minimal shape this kind needs from the
// /api/v1/crews list endpoint.
type crewStub struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
}

// ── Validate ─────────────────────────────────────────────────────────────

// Validate enforces structural rules without any HTTP round-trip:
//
//   - apiVersion must equal "crewship/v1" (Parse already routes on
//     `kind`, but the envelope check belongs here for completeness)
//   - kind must equal "CrewTemplate"
//   - metadata.name set (human readable label sent to the deploy
//     handler as `crew_name` — server 400s if empty)
//   - metadata.slug set (the SOURCE template's slug; existence is
//     checked at Plan time via GET /api/v1/crew-templates)
//   - spec.crew_slug_override REQUIRED, non-empty, kebab-case
//
// We deliberately do NOT check template-slug existence here because
// Validate runs offline (no Client argument); template existence is
// re-asserted at Plan time where the Client is available.
func (d *CrewTemplateDocument) Validate(_ internalapi.WorkspaceContext) error {
	if d.APIVersion != "" && d.APIVersion != crewTemplateAPIVersion {
		return fmt.Errorf("crew_template %q: apiVersion %q must be %q",
			d.Metadata.Slug, d.APIVersion, crewTemplateAPIVersion)
	}
	if d.Kind != "" && d.Kind != crewTemplateKind {
		return fmt.Errorf("crew_template %q: kind %q must be %q",
			d.Metadata.Slug, d.Kind, crewTemplateKind)
	}
	if strings.TrimSpace(d.Metadata.Name) == "" {
		return fmt.Errorf("crew_template: metadata.name is required (sent to deploy handler as crew_name)")
	}
	if strings.TrimSpace(d.Metadata.Slug) == "" {
		return fmt.Errorf("crew_template %q: metadata.slug is required (names the source template)", d.Metadata.Name)
	}
	if strings.TrimSpace(d.Spec.CrewSlugOverride) == "" {
		return fmt.Errorf("crew_template %q: spec.crew_slug_override is required (one template can deploy many crews; the override disambiguates)",
			d.Metadata.Slug)
	}
	if !crewSlugPattern.MatchString(d.Spec.CrewSlugOverride) {
		return fmt.Errorf("crew_template %q: spec.crew_slug_override %q must be kebab-case (lowercase letters, digits, hyphens; no leading/trailing hyphen)",
			d.Metadata.Slug, d.Spec.CrewSlugOverride)
	}
	return nil
}

// ── Plan ─────────────────────────────────────────────────────────────────

// Plan diffs the declared deployment against remote state and emits
// at most one PlanItem.
//
// `remote` is the snapshot returned by LookupCrewTemplateRemote — it
// holds both the source-template presence and the candidate-crew
// presence. Plan callers MUST pass a non-nil remote (the function
// errors otherwise); the typical Apply wiring is to call
// LookupCrewTemplateRemote first and short-circuit on missing source
// templates BEFORE invoking Plan.
//
// Decision matrix:
//
//	deploy=true,  source missing → error (cannot deploy unknown template)
//	deploy=true,  crew missing   → ActionCreate (POST /deploy)
//	deploy=true,  crew present   → ActionUnchanged
//	deploy=false, crew missing   → ActionUnchanged + warning
//	deploy=false, crew present   → ActionUnchanged + warning ("cannot undeploy")
func (d *CrewTemplateDocument) Plan(ctx context.Context, c internalapi.Client, remote *CrewTemplateRemote) ([]internalapi.PlanItem, error) {
	if remote == nil {
		return nil, fmt.Errorf("crew_template %q: Plan requires a non-nil remote snapshot (call LookupCrewTemplateRemote first)",
			d.Metadata.Slug)
	}
	if !remote.TemplateExists {
		// We can't make this a Validate-time check because Validate is
		// offline; surfacing it here keeps the user-facing error at
		// the right phase (Plan, not Apply — the operator can dry-run
		// and see the failure before any mutation).
		return nil, fmt.Errorf("crew_template %q: source template not found in workspace catalog (no built-in or workspace template with slug=%q)",
			d.Metadata.Slug, d.Metadata.Slug)
	}

	if !d.Spec.Deploy {
		// `deploy: false` is intentionally inert. There is no
		// inverse endpoint to "undeploy" — deleting the crew is the
		// caller's responsibility via `kind: Crew` or the crew DELETE
		// route. We emit Unchanged with a warning so dry-run output
		// makes the no-op visible.
		desc := fmt.Sprintf("crew_template %q: deploy=false — no action (this kind cannot undeploy)", d.Metadata.Slug)
		if remote.DeployedCrew != nil {
			desc = fmt.Sprintf("crew_template %q: deploy=false but crew %q already exists — no action (this kind cannot undeploy; delete the crew via kind: Crew if intended)",
				d.Metadata.Slug, remote.DeployedCrew.Slug)
		}
		return []internalapi.PlanItem{{
			Kind:        "crew_template",
			Slug:        d.Metadata.Slug,
			Action:      internalapi.ActionUnchanged,
			Description: desc,
		}}, nil
	}

	if remote.DeployedCrew != nil {
		// One-shot semantics: a crew already occupies the override
		// slug. We don't second-guess whether it came from THIS
		// template (the server has no provenance column); slug
		// collision alone signals "deployment already done".
		return []internalapi.PlanItem{{
			Kind:   "crew_template",
			Slug:   d.Metadata.Slug,
			Action: internalapi.ActionUnchanged,
			Description: fmt.Sprintf("crew_template %q already deployed as crew %q (one-shot kind; re-deploy is not supported)",
				d.Metadata.Slug, remote.DeployedCrew.Slug),
		}}, nil
	}

	// Fresh deploy. Capture the bits the Exec closure needs by value;
	// the closure runs much later during Apply and the document/
	// remote pointers must not be aliased between iterations.
	templateSlug := d.Metadata.Slug
	body := d.toDeployBody()
	return []internalapi.PlanItem{{
		Kind:        "crew_template",
		Slug:        templateSlug,
		Action:      internalapi.ActionCreate,
		Description: fmt.Sprintf("deploy crew_template %q as crew %q", templateSlug, d.Spec.CrewSlugOverride),
		Exec: func(ctx context.Context, c internalapi.Client) error {
			path := fmt.Sprintf("/api/v1/crew-templates/%s/deploy", templateSlug)
			resp, err := c.Post(ctx, path, body)
			if err != nil {
				return fmt.Errorf("POST %s: %w", path, err)
			}
			return checkStatus(resp, "deploy crew_template "+templateSlug)
		},
	}}, nil
}

// toDeployBody renders the POST body the deploy handler expects. The
// real server reads `crew_name` (required) and `crew_slug` (optional —
// derived from name if missing); we always send both for determinism.
// `inputs` is forwarded as a sibling field so future template
// parameters land without a schema bump (the handler tolerates extra
// keys today).
func (d *CrewTemplateDocument) toDeployBody() map[string]any {
	body := map[string]any{
		"crew_name": d.Metadata.Name,
		"crew_slug": d.Spec.CrewSlugOverride,
	}
	if len(d.Spec.Inputs) > 0 {
		body["inputs"] = d.Spec.Inputs
	}
	return body
}

// ── Remote lookup ────────────────────────────────────────────────────────

// LookupCrewTemplateRemote fetches the state Plan needs in one place:
//
//  1. GET /api/v1/crew-templates → does the source template exist?
//  2. GET /api/v1/crews          → is the override slug already taken?
//
// Returns a non-nil remote even when the template is missing
// (TemplateExists=false) — Plan distinguishes that case from "missing
// crew" explicitly.
func LookupCrewTemplateRemote(ctx context.Context, c internalapi.Client, d *CrewTemplateDocument) (*CrewTemplateRemote, error) {
	templates, err := listCrewTemplates(ctx, c)
	if err != nil {
		return nil, fmt.Errorf("crew_template %q: list templates: %w", d.Metadata.Slug, err)
	}
	out := &CrewTemplateRemote{}
	for i := range templates {
		if templates[i].Slug == d.Metadata.Slug {
			out.TemplateExists = true
			tmpl := templates[i]
			out.Template = &tmpl
			break
		}
	}

	crews, err := listCrews(ctx, c)
	if err != nil {
		return nil, fmt.Errorf("crew_template %q: list crews: %w", d.Metadata.Slug, err)
	}
	for i := range crews {
		if crews[i].Slug == d.Spec.CrewSlugOverride {
			cr := crews[i]
			out.DeployedCrew = &cr
			break
		}
	}
	return out, nil
}

// listCrewTemplates pulls /api/v1/crew-templates and decodes the
// minimal shape this kind cares about. The list is workspace-scoped
// and small (built-ins + per-workspace) so we don't paginate.
func listCrewTemplates(ctx context.Context, c internalapi.Client) ([]crewTemplateStub, error) {
	resp, err := c.Get(ctx, "/api/v1/crew-templates")
	if err != nil {
		return nil, fmt.Errorf("GET /api/v1/crew-templates: %w", err)
	}
	if err := checkStatus(resp, "list crew templates"); err != nil {
		return nil, err
	}
	body, err := readAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read /api/v1/crew-templates body: %w", err)
	}
	var rows []crewTemplateStub
	if len(body) > 0 {
		if err := json.Unmarshal(body, &rows); err != nil {
			return nil, fmt.Errorf("decode /api/v1/crew-templates: %w", err)
		}
	}
	return rows, nil
}

// listCrews pulls /api/v1/crews and decodes the minimal shape this
// kind cares about (id, name, slug). Used by both Plan (slug
// presence) and Export (heuristic match against template names).
func listCrews(ctx context.Context, c internalapi.Client) ([]crewStub, error) {
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
	var rows []crewStub
	if len(body) > 0 {
		if err := json.Unmarshal(body, &rows); err != nil {
			return nil, fmt.Errorf("decode /api/v1/crews: %w", err)
		}
	}
	return rows, nil
}

// ── Export ───────────────────────────────────────────────────────────────

// ExportCrewTemplates produces one CrewTemplateDocument for each
// existing crew that we can identify as a template deployment.
//
// Caveat — provenance gap
// -----------------------
//
// The current `crews` schema has NO `template_slug` column (see
// internal/database/migrate.go). The deploy handler at
// internal/api/crew_templates.go writes the crew row without
// recording the source template. There is therefore no authoritative
// way to round-trip a deployed crew back into a `kind: CrewTemplate`
// document.
//
// We make a best-effort heuristic match: a crew whose slug exactly
// equals a known template's slug is reported as a CrewTemplate
// deployment with `crew_slug_override` = the crew slug. Crews whose
// slugs diverge from the template slug (the common case — operators
// usually rename) are NOT exported as CrewTemplate; they round-trip
// via `kind: Crew` instead. This means an export → wipe → re-apply
// cycle re-creates such crews as raw Crew documents, not template
// deployments — losing the "deployed from X" semantic but preserving
// the actual state of the world.
//
// When/if the server grows a `template_slug` column the heuristic
// here can switch to an exact provenance check; the public Export*
// signature stays the same.
func ExportCrewTemplates(ctx context.Context, c internalapi.Client) ([]*CrewTemplateDocument, error) {
	templates, err := listCrewTemplates(ctx, c)
	if err != nil {
		return nil, fmt.Errorf("export crew_templates: list templates: %w", err)
	}
	if len(templates) == 0 {
		// No templates → nothing could have been deployed from one.
		// Returning nil (not error) so the export pipeline keeps
		// going for other kinds.
		return nil, nil
	}

	crews, err := listCrews(ctx, c)
	if err != nil {
		return nil, fmt.Errorf("export crew_templates: list crews: %w", err)
	}

	// Index templates by slug for O(1) lookup.
	templateBySlug := make(map[string]crewTemplateStub, len(templates))
	for _, t := range templates {
		templateBySlug[t.Slug] = t
	}

	var out []*CrewTemplateDocument
	for _, cr := range crews {
		tmpl, ok := templateBySlug[cr.Slug]
		if !ok {
			// No template with matching slug — heuristic miss. This
			// crew is either authored directly (kind: Crew) or was
			// deployed from a template under a different override
			// slug. Either way we skip; kind: Crew export will
			// pick it up.
			continue
		}
		doc := &CrewTemplateDocument{
			APIVersion: crewTemplateAPIVersion,
			Kind:       crewTemplateKind,
			Metadata: internalapi.Metadata{
				Name: tmpl.Name,
				Slug: tmpl.Slug,
			},
			Spec: CrewTemplateSpec{
				Deploy:           true,
				CrewSlugOverride: cr.Slug,
			},
		}
		out = append(out, doc)
	}
	return out, nil
}
