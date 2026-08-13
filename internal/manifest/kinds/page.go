// Package kinds holds one Go file per declarative manifest kind. This
// file implements kind: Page — the 21st kind (docs/prd/pages.md §6,
// §12 v1): a named grid of typed, permissioned panels that producers
// push payloads into.
//
// The one thing this file deliberately does NOT do is re-declare the
// page document shape. internal/pages already owns it (spec.go), it is
// what `crewship page create --file` parses, and the PRD's §6 promise is
// that a manifest page and a CLI-authored page are the SAME document.
// Two Go structs for one YAML shape is how they drift, so PageSpec
// carries []pages.PanelSpec verbatim and Validate delegates the
// structural half to pages.Document.Validate. What this layer adds is
// the part internal/pages cannot do: resolving every declared crew,
// agent and routine against the rest of the bundle before anything is
// sent (docs/prd/pages.md §10b.1, the authoring gate's second half).
package kinds

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/crewship-ai/crewship/internal/manifest/internalapi"
	"github.com/crewship-ai/crewship/internal/pages"
)

// ── Constants ──────────────────────────────────────────────────────────────

// pageAPIVersion is the only apiVersion a Page document accepts.
const pageAPIVersion = pages.DocumentAPIVersion

// pageDocKind is the literal `kind:` value in YAML, and the spelling the
// plan.go rank table is written in.
const pageDocKind = pages.DocumentKind

// pagePlanKind is what PlanItem.Kind carries. Lowercase, per the
// internalapi.PlanItem contract ("the lowercased kind name").
//
// The rank table in plan.go is capitalised while kinds emit lowercase;
// snakeToDocKind (plan.go) bridges the two, and the mismatch has already
// caused one production ordering bug (plan.go:272-280). "page" is a
// single word, so snakeToDocKind("page") == "Page" == pageDocKind — the
// rank entry is found on the second lookup rather than falling through
// to the 99 fallback. Both spellings resolving to the same rank is the
// property under test in the manifest package's ordering test; do not
// change one of these three constants without the other two.
const pagePlanKind = "page"

// pagesEndpoint is the workspace-UNSCOPED collection route (PRD §11b
// decision 1: /api/v1/pages, with the workspace coming from wsCtx —
// the saved-views shape, not the pipelines one).
const pagesEndpoint = "/api/v1/pages"

// ── YAML-facing shapes ─────────────────────────────────────────────────────

// PageSpec is the shape under `spec:` for a kind: Page document.
//
// Panels is []pages.PanelSpec — the authoring type itself, not a copy.
// A panel therefore declares exactly what it declares to the CLI:
// id, schema, title, owner, producer, sla (a duration STRING, "30s"),
// span, public, and the authored half the surface is for — `actions:`
// (§8b.1), `wake:` (§5) and `on_failure:` (§4 rule 4). Every one of
// those is a field on pages.PanelSpec, is validated by
// pages.Document.Validate, and is SENT by writeBody. That last clause is
// the one that was false: actions validated here and were dropped on the
// way out, so a manifest declaring buttons applied with exit 0 and
// produced a page with none. `refresh:` is still v1.1 (§12) and is not a
// field on the type; a manifest naming it fails at validate rather than
// being silently ignored, which is what §6 asks for.
type PageSpec struct {
	Panels []pages.PanelSpec `yaml:"panels" json:"panels"`
}

// PageDocument is the top-level YAML shape for kind: Page. The envelope
// is the manifest's (internalapi.Metadata, so metadata.labels works the
// way it does for every other kind); the spec below it is the pages
// package's.
type PageDocument struct {
	APIVersion string               `yaml:"apiVersion" json:"apiVersion"`
	Kind       string               `yaml:"kind"       json:"kind"`
	Metadata   internalapi.Metadata `yaml:"metadata"   json:"metadata"`
	Spec       PageSpec             `yaml:"spec"       json:"spec"`
}

// ── Remote (server-side) shape ─────────────────────────────────────────────

// PageRemote mirrors the document GET /api/v1/pages/{slug} returns.
//
// Keyed BY SLUG, not by name. Pages are slug-addressable from the first
// migration (`UNIQUE(workspace_id, slug)`, PRD §10) and the slug is the
// page's URL, so drift detection has a real identity to match on. This
// is the one thing §13 obstacle 10 asks this file not to get wrong:
// SavedView matches on `name` only because saved_views has no slug
// column (saved_view.go:216-220), and inheriting that shape here would
// mean a page rename silently forking into a second page.
type PageRemote struct {
	ID          string            `json:"id"`
	Slug        string            `json:"slug"`
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	Owner       string            `json:"owner"`
	Panels      []PagePanelRemote `json:"panels"`
}

// PagePanelRemote is one entry of the remote `panels` array, which is
// heterogeneous by design (PRD §11b decision 14): either a full panel or
// a sealed placeholder for a panel this viewer may not see. One struct
// covers both because the two shapes share no required field — `id` vs
// `panel_id` — and Sealed says which set is populated.
//
// The snapshot half of the wire panel (state, reason, data, provenance)
// is deliberately absent: it is server-computed and no manifest declares
// it, so modelling it here would invite a diff against it.
//
// `public`, `actions`, `wake` and `on_failure` are absent for the
// opposite reason: they ARE declared, and the read path does not send
// them back (pages_handler.go:132-149). Adding fields for them would
// model them as always-empty and every page that declares one would read
// as drifted. The consequence is stated where it costs something —
// pagePanelsDiffer, and pageDocumentFromRemote below.
type PagePanelRemote struct {
	// Full-panel fields.
	ID     string `json:"id"`
	Schema string `json:"schema"`
	Title  string `json:"title,omitempty"`
	// Icon is echoed by the read path (unlike `public`, `actions` and the
	// gates below), so it is one of the fields drift detection can actually
	// compare. A field the applier can see is a field the applier checks.
	Icon string `json:"icon,omitempty"`
	// Tab is echoed for the same reason, and on the SEALED placeholder too —
	// which is why it is compared above the `Sealed` check below, exactly like
	// `span`. A tab is the page's shape, not the panel's data: a page must have
	// the same bar for everyone, so moving a panel this account cannot see onto
	// another tab is still a change the applier can see and plan.
	Tab        string `json:"tab,omitempty"`
	Owner      string `json:"owner"`
	Producer   string `json:"producer"`
	SLASeconds int    `json:"sla_seconds"`
	Span       int    `json:"span"`

	// Sealed-placeholder fields (§11b decision 14). `sealed` is present
	// and true rather than inferred from a missing field, so a
	// serialisation bug can never be read as a permission decision.
	PanelID       string `json:"panel_id,omitempty"`
	Sealed        bool   `json:"sealed,omitempty"`
	OwnerCrewName string `json:"owner_crew_name,omitempty"`
}

// panelID returns the panel's address whichever shape the row arrived in.
func (p *PagePanelRemote) panelID() string {
	if p.Sealed {
		return p.PanelID
	}
	return p.ID
}

// ── Validate ───────────────────────────────────────────────────────────────

// Validate checks a Page document in two passes.
//
// Pass 1 is structural and is delegated to pages.Document.Validate: the
// panel-id slug shape, duplicate ids, the closed schema set, owner
// being crew/<slug>, producer being <known-kind>/<ref>, a parseable and
// non-zero SLA, span within the 12-column grid, and the per-page panel
// cap. Delegating rather than reimplementing is what keeps `crewship
// apply` and `crewship page create --file` from disagreeing about the
// same file.
//
// Pass 2 is the manifest layer's own contribution: every crew, agent and
// routine a panel names has to exist somewhere the applier can see —
// either declared in this bundle or already on the server. A page whose
// producer does not resolve renders a grid of dead panels and nobody can
// tell why (PRD §10b.1), and the server would refuse it anyway; catching
// it here costs no round trip.
//
// Empty WorkspaceContext slices mean "skip", following the Issue kind
// (issue.go:339-360): a page-only manifest applied against a workspace
// whose crews already exist must not fail because this process never
// fetched them.
func (d *PageDocument) Validate(ctx internalapi.WorkspaceContext) error {
	if d.APIVersion != pageAPIVersion {
		return fmt.Errorf("page %q: unsupported apiVersion %q (want %q)",
			d.Metadata.Slug, d.APIVersion, pageAPIVersion)
	}
	if d.Kind != pageDocKind {
		return fmt.Errorf("page %q: kind must be %q, got %q",
			d.Metadata.Slug, pageDocKind, d.Kind)
	}

	// Structural pass. The pages validator owns metadata.name /
	// metadata.slug too, so there is no second copy of those rules here.
	if err := d.pagesDocument().Validate(); err != nil {
		return fmt.Errorf("page %q: %s", d.Metadata.Slug, pageErrorDetail(err))
	}

	// FK pass. Every failure is collected so a manifest with three bad
	// references reports three, not one per apply.
	var errs []string
	for i := range d.Spec.Panels {
		p := &d.Spec.Panels[i]

		// Owner. Already known to parse as crew/<slug> — pass 1 checked
		// the shape, this checks the crew is real.
		crewSlug, err := p.OwnerCrewSlug()
		if err != nil {
			// Unreachable after pass 1; kept so a future change to the
			// pages validator cannot turn this into a nil-slug lookup.
			errs = append(errs, fmt.Sprintf("panel %q: %v", p.ID, err))
			continue
		}
		if pageCtxKnowsCrews(ctx) && !ctx.HasCrew(crewSlug) {
			errs = append(errs, fmt.Sprintf(
				"panel %q: owner crew/%s does not reference any declared or remote crew", p.ID, crewSlug))
		}

		// Producer. Only two of the four producer kinds name something
		// the manifest models: `script` is a path inside a crew
		// container and `webhook` is a token minted after the fact
		// (PRD §10b.5c), so neither is resolvable here and neither is
		// checked — silence on those is correct, not an omission.
		kind, ref, err := p.ProducerParts()
		if err != nil {
			errs = append(errs, fmt.Sprintf("panel %q: %v", p.ID, err))
			continue
		}
		switch kind {
		case pages.ProducerRoutine:
			if pageCtxKnowsRoutines(ctx) && !ctx.HasRoutine(ref) {
				errs = append(errs, fmt.Sprintf(
					"panel %q: producer routine/%s does not reference any declared or remote routine", p.ID, ref))
			}
		case pages.ProducerAgent:
			if pageCtxKnowsAgents(ctx) && !ctx.HasAgent(ref) {
				errs = append(errs, fmt.Sprintf(
					"panel %q: producer agent/%s does not reference any declared or remote agent", p.ID, ref))
			}
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("page %q: %s", d.Metadata.Slug, strings.Join(errs, "; "))
	}
	return nil
}

// pageCtxKnowsCrews / pageCtxKnowsAgents / pageCtxKnowsRoutines report
// whether the workspace context carries any entry of that kind at all.
// A context that knows nothing about crews cannot be used to prove a
// crew is missing — see the Issue precedent quoted in Validate.
func pageCtxKnowsCrews(ctx internalapi.WorkspaceContext) bool {
	return len(ctx.DeclaredCrews) > 0 || len(ctx.RemoteCrews) > 0
}

func pageCtxKnowsAgents(ctx internalapi.WorkspaceContext) bool {
	return len(ctx.DeclaredAgents) > 0 || len(ctx.RemoteAgents) > 0
}

func pageCtxKnowsRoutines(ctx internalapi.WorkspaceContext) bool {
	return len(ctx.DeclaredRoutines) > 0 || len(ctx.RemoteRoutines) > 0
}

// pagesDocument projects the manifest document onto the authoring type
// the pages package validates.
//
// The panel slice is COPIED because pages.Document.Validate mutates it:
// a panel that declares no span is defaulted to the full 12 columns in
// place. Letting that land on the manifest's own slice would make
// Validate a prerequisite for a correct Plan, and a kind whose diff
// depends on whether somebody validated it first is a kind that reports
// spurious drift in tests. Plan applies the same default explicitly
// instead (pageSpanOrDefault).
func (d *PageDocument) pagesDocument() *pages.Document {
	panels := make([]pages.PanelSpec, len(d.Spec.Panels))
	copy(panels, d.Spec.Panels)
	return &pages.Document{
		APIVersion: pageAPIVersion,
		Kind:       pageDocKind,
		Metadata: pages.Metadata{
			Name:        d.Metadata.Name,
			Slug:        d.Metadata.Slug,
			Description: d.Metadata.Description,
		},
		Spec: pages.Spec{Panels: panels},
	}
}

// pageErrorDetail unwraps a pages.ValidationError down to its human
// half. The full Error() string is "pages: invalid_spec: …", and
// prefixing that with "page %q:" reads as two error systems arguing.
func pageErrorDetail(err error) string {
	var ve *pages.ValidationError
	if errors.As(err, &ve) {
		return ve.Detail
	}
	return err.Error()
}

// ── Plan ───────────────────────────────────────────────────────────────────

// Plan compares the declared page against the remote one and emits a
// single PlanItem.
//
// `remote` is the page matched BY SLUG (LookupPageRemoteBySlug), or nil
// when the workspace has no page under that slug. Create posts the whole
// document; update PATCHes the same body minus the slug — the full panel
// list included, because PATCH /pages/{slug} replaces the panel set
// wholesale and a partial list would delete the panels it left out, and
// each panel complete with its actions and its gates, because a panel
// sent without them is a panel whose buttons and wake rules are dropped
// in the same transaction (pages_wake.go reconciles against what it was
// sent).
//
// The slug is never sent on update: the server refuses a PATCH that
// changes it ("a page's slug is its address"), so including it can only
// turn a no-op into a 400.
func (d *PageDocument) Plan(
	_ context.Context,
	_ internalapi.Client,
	remote *PageRemote,
) ([]internalapi.PlanItem, error) {
	body, err := d.writeBody()
	if err != nil {
		return nil, fmt.Errorf("page %q: build request body: %w", d.Metadata.Slug, err)
	}

	if remote == nil {
		return []internalapi.PlanItem{{
			Kind:        pagePlanKind,
			Slug:        d.Metadata.Slug,
			Action:      internalapi.ActionCreate,
			Description: fmt.Sprintf("create page %q (%d panel(s))", d.Metadata.Slug, len(d.Spec.Panels)),
			Exec: func(ctx context.Context, c internalapi.Client) error {
				resp, err := c.Post(ctx, pagesEndpoint, body)
				if err != nil {
					return fmt.Errorf("POST page %q: %w", d.Metadata.Slug, err)
				}
				return checkStatus(resp, "create page "+d.Metadata.Slug)
			},
		}}, nil
	}

	changed := d.driftFields(remote)
	if len(changed) == 0 {
		return []internalapi.PlanItem{{
			Kind:        pagePlanKind,
			Slug:        d.Metadata.Slug,
			Action:      internalapi.ActionUnchanged,
			Description: fmt.Sprintf("page %q unchanged", d.Metadata.Slug),
		}}, nil
	}

	// The update body is the create body MINUS the slug, derived rather
	// than re-listed. A hand-written subset here is the same mistake as a
	// hand-written panel: the day writeBody learns a new top-level key,
	// create would send it and update would silently not, and the two
	// doors would disagree about what a page is. The slug is the one
	// exclusion and it is named, with its reason, in the doc comment.
	patch := make(map[string]any, len(body))
	for k, v := range body {
		if k == "slug" {
			continue
		}
		patch[k] = v
	}
	slug := remote.Slug
	return []internalapi.PlanItem{{
		Kind:        pagePlanKind,
		Slug:        d.Metadata.Slug,
		Action:      internalapi.ActionUpdate,
		Description: fmt.Sprintf("update page %q (%s)", d.Metadata.Slug, strings.Join(changed, ", ")),
		Exec: func(ctx context.Context, c internalapi.Client) error {
			resp, err := c.Patch(ctx, pagesEndpoint+"/"+slug, patch)
			if err != nil {
				return fmt.Errorf("PATCH page %q: %w", d.Metadata.Slug, err)
			}
			return checkStatus(resp, "update page "+d.Metadata.Slug)
		},
	}}, nil
}

// writeBody assembles the create/update body.
//
// SLA crosses the wire as `sla_seconds` (PRD §11b decision 3): one
// representation in the database, one on the wire, one for humans. The
// conversion happens here, exactly as cmd_page.go's pageWriteFrom does
// it for the CLI, so the two authoring doors send identical JSON.
func (d *PageDocument) writeBody() (map[string]any, error) {
	panels := make([]map[string]any, 0, len(d.Spec.Panels))
	for i := range d.Spec.Panels {
		p := &d.Spec.Panels[i]
		sla, err := p.SLADuration()
		if err != nil {
			return nil, fmt.Errorf("panel %q: %w", p.ID, err)
		}
		panel := map[string]any{
			"id":          p.ID,
			"schema":      string(p.Schema),
			"owner":       strings.TrimSpace(p.Owner),
			"producer":    strings.TrimSpace(p.Producer),
			"sla_seconds": int(sla.Seconds()),
			"span":        pageSpanOrDefault(p.Span),
		}
		if p.Title != "" {
			panel["title"] = p.Title
		}
		// Omitted when unset rather than sent empty: an absent icon means
		// "the schema's own", and sending "" would say the same thing in a
		// second way the server would have to know to read.
		if icon := strings.TrimSpace(string(p.Icon)); icon != "" {
			panel["icon"] = icon
		}
		// The tab, on the same terms: absent means "no tab", and a page where
		// no panel declares one has no bar at all.
		if tab := strings.TrimSpace(p.Tab); tab != "" {
			panel["tab"] = tab
		}
		if p.Public {
			panel["public"] = true
		}
		// The buttons (§8b.1) and the sensor half (§5, §4 rule 4). Sent
		// verbatim, exactly as cmd_page.go's pageWritePanelJSON sends them:
		// the server parses `when:` against the panel's schema, resolves the
		// crews, and stores the action list a later click is resolved against.
		// That is where those rules belong — a manifest that declared a gate or
		// a button the applier dropped on the floor would be a page that looks
		// monitored, or operable, and is not.
		//
		// Like `public`, none of the three is diffable from here (the read path
		// does not echo them), so a change to a gate or a button ALONE is only
		// applied when something else on the page changes too; see
		// pagePanelsDiffer.
		if len(p.Actions) > 0 {
			panel["actions"] = p.Actions
		}
		if len(p.Wake) > 0 {
			panel["wake"] = p.Wake
		}
		if p.OnFailure != nil {
			panel["on_failure"] = p.OnFailure
		}
		panels = append(panels, panel)
	}
	body := map[string]any{
		"slug":   d.Metadata.Slug,
		"name":   d.Metadata.Name,
		"panels": panels,
	}
	if d.Metadata.Description != "" {
		body["description"] = d.Metadata.Description
	}
	return body, nil
}

// driftFields returns the names of the field groups that differ between
// the declared page and the remote one. Empty means unchanged.
//
// Description follows the house rule for optional metadata (see
// project.go's diffPatch): a manifest that omits it means "leave the
// server value alone", not "clear it". Name and the panel list are
// always authoritative — a page's grid is the thing the manifest exists
// to declare.
func (d *PageDocument) driftFields(remote *PageRemote) []string {
	var changed []string
	if d.Metadata.Name != remote.Name {
		changed = append(changed, "name")
	}
	if d.Metadata.Description != "" && d.Metadata.Description != remote.Description {
		changed = append(changed, "description")
	}
	if pagePanelsDiffer(d.Spec.Panels, remote.Panels) {
		changed = append(changed, "panels")
	}
	return changed
}

// pagePanelsDiffer compares the declared panel list against the remote
// one, position by position. Order is significant: the panel list IS the
// grid layout (PRD §6, "layout is declared, never dragged"), so two
// pages with the same panels in a different order are two different
// pages.
//
// Sealed panels are compared on the only two fields a placeholder
// carries — id and span. That is a real limitation and it is the honest
// one: an applier who is not in the owning crew cannot see the panel's
// schema, producer or SLA, and treating "cannot see" as "must differ"
// would PATCH the page on every single apply, minting a page_versions
// row nobody asked for. That is exactly the bug LookupRoutineRemoteBySlug
// exists to have fixed for routines, and it is not worth reintroducing
// here for a case an admin-run apply does not hit. Documented in
// docs/manifest/page.md.
//
// `public`, `actions`, `wake` and `on_failure` are not compared at all:
// the read path serialises none of them (pages_handler.go panelWire
// builds the spec fields it can echo and stops there), so the remote
// value is always the zero one on the wire and diffing it would report
// drift forever on any page that declares a button or a gate. They are
// still SENT on create and update — the write path reads all four — so
// the declared value lands; it just cannot be verified from here, which
// means a manifest whose ONLY change is to a gate or a button plans as
// "unchanged". That is a known hole with one honest fix (echo the
// authored half to a caller who may edit the spec) and it lives on the
// server side of the wire, not here.
func pagePanelsDiffer(declared []pages.PanelSpec, remote []PagePanelRemote) bool {
	if len(declared) != len(remote) {
		return true
	}
	for i := range declared {
		d := &declared[i]
		r := &remote[i]
		if d.ID != r.panelID() {
			return true
		}
		if pageSpanOrDefault(d.Span) != r.Span {
			return true
		}
		// Above the sealed check, like the span: the tab bar is the page's
		// shape and is the same for every viewer, so it is comparable even for
		// a panel whose contents this account cannot see.
		if strings.TrimSpace(d.Tab) != r.Tab {
			return true
		}
		if r.Sealed {
			continue
		}
		if string(d.Schema) != r.Schema {
			return true
		}
		if d.Title != r.Title {
			return true
		}
		// The icon IS comparable — the read path echoes it — so removing
		// `icon:` from a manifest plans as a change and restores the schema's
		// default, rather than leaving the old glyph on the page forever.
		if strings.TrimSpace(string(d.Icon)) != r.Icon {
			return true
		}
		if strings.TrimSpace(d.Owner) != r.Owner {
			return true
		}
		if strings.TrimSpace(d.Producer) != r.Producer {
			return true
		}
		sla, err := d.SLADuration()
		if err != nil {
			// Unparseable SLA never reaches Plan through Validate; if it
			// somehow does, "differs" is the safe answer — the update
			// will fail loudly at the server rather than being reported
			// as converged.
			return true
		}
		if int(sla.Seconds()) != r.SLASeconds {
			return true
		}
	}
	return false
}

// pageSpanOrDefault applies the grid default a panel that declares no
// span gets (pages.DefaultSpan — the full 12 columns; zero would render
// a panel with no width). Kept explicit here rather than relying on
// pages.Document.Validate's in-place mutation, so Plan gives the same
// answer whether or not Validate ran first.
func pageSpanOrDefault(span int) int {
	if span == 0 {
		return pages.DefaultSpan
	}
	return span
}

// ── Lookup ─────────────────────────────────────────────────────────────────

// LookupPageRemoteBySlug returns the page with this slug, or (nil, nil)
// when the workspace has none.
//
// One GET by slug rather than a list-and-filter: pages are addressed by
// slug on the server (`GET /api/v1/pages/{slug}`), so there is a direct
// route and no reason to pull every page's panel set to find one.
//
// A transport or server error is propagated, never folded into "absent".
// A lookup that cannot tell "not there" from "could not look" plans a
// create for a page that already exists — which here means a 409 at
// apply time, and a plan nobody should trust.
func LookupPageRemoteBySlug(ctx context.Context, c internalapi.Client, slug string) (*PageRemote, error) {
	resp, err := c.Get(ctx, pagesEndpoint+"/"+slug)
	if err != nil {
		return nil, fmt.Errorf("GET page %q: %w", slug, err)
	}
	if resp != nil && resp.StatusCode == 404 {
		return nil, nil
	}
	if err := checkStatus(resp, "get page "+slug); err != nil {
		return nil, err
	}
	data, err := readAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read page %q: %w", slug, err)
	}
	if len(data) == 0 {
		return nil, nil
	}
	var remote PageRemote
	if err := json.Unmarshal(data, &remote); err != nil {
		return nil, fmt.Errorf("decode page %q: %w", slug, err)
	}
	return &remote, nil
}

// ── Export ─────────────────────────────────────────────────────────────────

// ExportPages fetches every page in the workspace and renders each as a
// PageDocument suitable for re-applying — the inverse of Plan.
//
// NOTE ON REACHABILITY: this function has no non-test caller today, and
// that is a known and recorded state rather than an oversight.
// `crewship export` knows only Crew and Workspace
// (cmd/crewship/cmd_export_manifest.go:89,122), so every per-kind
// Export* in this package — ExportSavedViews, ExportRoutines,
// ExportProjects — is in the same position; PRD §13 obstacle 6 names it
// and schedules the CLI's kinds path for v1.2. It ships written and
// tested so that the day the CLI grows that path, Page is not the one
// kind missing from it.
//
// Two round trips per page (the index, then the document) because the
// index carries counts, not panels — a page list that shipped every
// panel of every page is not what a list costs (pages_handler.go).
func ExportPages(ctx context.Context, c internalapi.Client) ([]*PageDocument, error) {
	rows, err := pageList(ctx, c)
	if err != nil {
		return nil, err
	}
	out := make([]*PageDocument, 0, len(rows))
	for _, row := range rows {
		remote, err := LookupPageRemoteBySlug(ctx, c, row.Slug)
		if err != nil {
			return nil, fmt.Errorf("export page %q: %w", row.Slug, err)
		}
		if remote == nil {
			// Deleted between the list and the fetch. Skipping is right:
			// exporting a page that no longer exists would emit a
			// document that recreates it on the next apply.
			continue
		}
		doc, err := pageDocumentFromRemote(remote)
		if err != nil {
			return nil, err
		}
		out = append(out, doc)
	}
	// Deterministic order keeps round-trip diffs stable. The index is
	// sorted by updated_at, which is not a property anyone wants their
	// exported YAML to be ordered by.
	sort.Slice(out, func(i, j int) bool { return out[i].Metadata.Slug < out[j].Metadata.Slug })
	return out, nil
}

// pageDocumentFromRemote rebuilds an authored document from a fetched
// page.
//
// A sealed panel is a hard error rather than a skipped entry. The
// exported document is a complete declaration of the page: dropping a
// panel the exporter could not see would produce YAML that silently
// DELETES that panel the next time anyone applies it. Refusing, and
// naming the panel, is the only outcome that cannot lose somebody's
// work.
//
// KNOWN GAP, same class, no fix available at this layer: the fields
// below are enumerated by hand because PageRemote is all this function
// has, and PageRemote cannot carry `public`, `actions`, `wake` or
// `on_failure` — the read path does not send them. An exported document
// is therefore complete in its panels and INCOMPLETE in their authored
// halves, and re-applying it would delete a page's buttons and gates for
// exactly the reason the sealed-panel refusal exists. It is not refused
// the way a sealed panel is, because the exporter cannot tell a page
// that has none (the overwhelming majority) from one that has some:
// there is no evidence to refuse on, only an absent field. Closing this
// needs the read path to echo the authored half to a caller who may edit
// the spec; until then ExportPages has no non-test caller (see the note
// on ExportPages), so nothing ships on top of the gap.
func pageDocumentFromRemote(remote *PageRemote) (*PageDocument, error) {
	panels := make([]pages.PanelSpec, 0, len(remote.Panels))
	for i := range remote.Panels {
		p := &remote.Panels[i]
		if p.Sealed {
			return nil, fmt.Errorf(
				"export page %q: panel %q is owned by another crew and is sealed to this account; "+
					"export it as a member of that crew, or the emitted document would drop the panel",
				remote.Slug, p.panelID())
		}
		panels = append(panels, pages.PanelSpec{
			ID:       p.ID,
			Schema:   pages.PanelSchema(p.Schema),
			Title:    p.Title,
			Icon:     pages.PanelIcon(p.Icon),
			Tab:      p.Tab,
			Owner:    p.Owner,
			Producer: p.Producer,
			SLA:      pageFormatSLA(p.SLASeconds),
			Span:     p.Span,
		})
	}
	return &PageDocument{
		APIVersion: pageAPIVersion,
		Kind:       pageDocKind,
		Metadata: internalapi.Metadata{
			Name:        remote.Name,
			Slug:        remote.Slug,
			Description: remote.Description,
		},
		Spec: PageSpec{Panels: panels},
	}, nil
}

// pageListRow is the subset of the index row (pageListWire) that export
// needs. The rollup counts and freshness fields on that row are runtime
// state and have no place in an authored document.
type pageListRow struct {
	Slug string `json:"slug"`
	Name string `json:"name"`
}

// pageList fetches the page index. The handler returns a flat array; the
// wrapped {pages: [...]} shape is accepted too, on the same
// future-proofing grounds as ExportSavedViews' wrapper fallback.
func pageList(ctx context.Context, c internalapi.Client) ([]pageListRow, error) {
	resp, err := c.Get(ctx, pagesEndpoint)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", pagesEndpoint, err)
	}
	if err := checkStatus(resp, "list pages"); err != nil {
		return nil, err
	}
	data, err := readAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read pages list: %w", err)
	}
	if len(data) == 0 {
		return nil, nil
	}
	var rows []pageListRow
	if err := json.Unmarshal(data, &rows); err != nil {
		var wrapped struct {
			Pages []pageListRow `json:"pages"`
		}
		if werr := json.Unmarshal(data, &wrapped); werr != nil {
			return nil, fmt.Errorf("decode pages list: %w", err)
		}
		rows = wrapped.Pages
	}
	return rows, nil
}

// pageFormatSLA renders a whole number of seconds back into the duration
// string a human authored.
//
// time.Duration.String() would emit "1h0m0s" for an hour, which
// round-trips correctly but reads like machine output in a file people
// edit. The largest exact unit is what the PRD's own examples use
// ("30s", "1h").
func pageFormatSLA(seconds int) string {
	if seconds <= 0 {
		// Zero never validates (an SLA of zero is "the default that
		// means never mind", and §4 says there is not one). Emitting it
		// verbatim keeps export honest: the document fails validation on
		// the way back in, naming the panel, instead of being quietly
		// repaired into a number nobody chose.
		return fmt.Sprintf("%ds", seconds)
	}
	switch {
	case seconds%3600 == 0:
		return fmt.Sprintf("%dh", seconds/3600)
	case seconds%60 == 0:
		return fmt.Sprintf("%dm", seconds/60)
	default:
		return fmt.Sprintf("%ds", seconds)
	}
}
