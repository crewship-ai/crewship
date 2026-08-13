package api

// Pages — portability (docs/prd/pages.md §10b.2).
//
// A "page template" is NOT a new noun. §10b.2 was rewritten once an earlier
// draft proposed a built-in catalog kind: the right precedent is routines,
// which already ship the marketplace mechanism this file copies —
// `POST …/pipelines/{slug}/export` produces a bundle with workspace-specific
// ids stripped "so a marketplace consumer can import via POST …/import"
// (internal/api/pipelines_crud.go:163-178), and import is "marketplace install
// flows + cross-workspace transfer" (:240). The same document is a page here
// and a template there.
//
// §10b.2 fixes exactly three properties for 1.0, and this file exists to hold
// them:
//
//  1. THE BUNDLE CARRIES NO WORKSPACE IDS. This is where Pages deliberately
//     diverges from the pipeline bundle, which keeps `source_workspace_id` in
//     its metadata for audit. §10b.2 says "the export bundle carries no
//     workspace ids", full stop, so there is no metadata field here that could
//     carry one and no branch that writes one. The page's owner is stripped
//     too when it is a USER (owner_user_id is an id of exactly that kind); a
//     crew owner survives as a slug, which is a name the receiving workspace
//     can bind.
//
//  2. EVERY EXTERNAL REFERENCE IS DECLARED. The importer must be able to see
//     what it will have to bind BEFORE installing, so the bundle carries a
//     `references` array alongside the spec: one entry per distinct crew,
//     routine or agent the page names, with the panels that need it. A page
//     whose producer routine does not exist locally must not become a page
//     full of dead panels.
//
//  3. IMPORT IS ONE TRANSACTION THAT BINDS EVERYTHING OR REFUSES. Every
//     reference is resolved against the receiving workspace before a single
//     row is written, and a refusal NAMES the reference it could not resolve —
//     all of them, not the first, because an operator fixing bindings one
//     round trip at a time is the same failure at a slower speed.
//
// And one property §10b.1 fixes: IMPORT SKIPS THE AUTHORING GATE, exactly as
// routines do ("Imports skip the test_run gate by design — a marketplace…",
// pipelines_crud.go:336). Concretely: import does not go through
// resolveReferences, whose errors are written for an author fixing their own
// YAML one panel at a time. References are checked at BIND time instead, in
// one pass, against the bind map the importer supplied — which is the whole
// point of a bundle arriving from somewhere else.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/pages"
)

// pageBundleFormat versions the document. It is checked on import for the same
// reason the pipeline bundle checks its own: a bundle from a future build is
// refused with its format named, rather than silently half-read.
const pageBundleFormat = "crewship-page-bundle/v1"

// pageImportEnvelopeSlack is the headroom the import body gets over the spec
// cap.
//
// The spec cap (§10b.3, 256 KiB) bounds the DOCUMENT. An import body carries
// the document plus the declared references and the caller's bind map, none of
// which is spec, so capping the read at exactly MaxSpecBytes would refuse a
// legal 256 KiB page for the crime of travelling with its own bindings.
const pageImportEnvelopeSlack = 32 << 10

// ── The bundle ─────────────────────────────────────────────────────────────

// pageBundle is the portable document `page export` produces and `page import`
// consumes.
type pageBundle struct {
	Format string             `json:"format"`
	Page   pageBundlePage     `json:"page"`
	Refs   []pageBundleRef    `json:"references"`
	Meta   pageBundleMetadata `json:"metadata"`
}

// pageBundlePage is the spec, stripped.
//
// Owner is "crew/<slug>" or empty. A user-owned page exports with NO owner:
// owner_user_id is a workspace-specific id, and the importer becomes the owner
// of what they installed — which is also the only answer §7.1 rule 1 allows,
// since "a page is created owned by its creator or by a crew".
type pageBundlePage struct {
	Name        string            `json:"name"`
	Slug        string            `json:"slug"`
	Description string            `json:"description,omitempty"`
	Owner       string            `json:"owner,omitempty"`
	Panels      []pageBundlePanel `json:"panels"`
}

// pageBundlePanel is one panel as it travels.
//
// `public` is absent by design and is not a field this struct has. §7.3.2
// rule 2 makes publication default-deny, per panel, human-only — "publishing
// must never be a bulk action over panels the author has not looked at". A
// bundle that could carry `public: true` would publish panels in the receiving
// workspace that nobody there has ever seen. Publication is a property of the
// install, not of the document.
//
// `icon` IS carried, and the contrast with `public` is the point: an icon is a
// property of the document — what the panel is about — and travels with it,
// while publication is a property of the install. A bundle that lost the icon
// would install a page whose headers all look alike, which is the state this
// field exists to end.
// `tab` travels for the same reason as the icon and as `span`: it is the
// page's shape, authored into the document, and a bundle that dropped it would
// install one long scroll where the author drew four screens.
type pageBundlePanel struct {
	ID         string `json:"id"`
	Schema     string `json:"schema"`
	Title      string `json:"title,omitempty"`
	Icon       string `json:"icon,omitempty"`
	Tab        string `json:"tab,omitempty"`
	Owner      string `json:"owner"`
	Producer   string `json:"producer"`
	SLASeconds int    `json:"sla_seconds"`
	Span       int    `json:"span,omitempty"`
}

// pageBundleRef is one declared placeholder: something outside the page that
// the page needs, named as "<kind>/<slug>" exactly as the spec names it.
//
// Bindable is false for `script` and `webhook` producers, and that is not an
// oversight. There is no table of scripts — inventing one would be the
// datasource a page is not allowed to have (resolveReferences says so on the
// authoring path), so `script/watch-services.sh` is a name, not a principal,
// and there is nothing local to resolve it against. It is still DECLARED,
// because the importer is owed the complete list of what the page expects to
// be fed by; it is simply not something the import can gate on.
type pageBundleRef struct {
	Ref      string   `json:"ref"`
	Kind     string   `json:"kind"`
	Bindable bool     `json:"bindable"`
	UsedBy   []string `json:"used_by"`
}

// pageBundleMetadata is deliberately thin. There is no source_workspace_id
// here — see the file header.
type pageBundleMetadata struct {
	ExportedAt string `json:"exported_at"`
	PanelCount int    `json:"panel_count"`
}

// pageBundleOwnerUse is the `used_by` entry for the page's own owner, as
// opposed to a panel id. A bundle reader has to be able to tell "this crew
// owns the page" from "this crew owns a panel called page".
const pageBundleOwnerUse = "page (owner)"

// ── Export ─────────────────────────────────────────────────────────────────

// Export returns the page as a portable bundle.
//
// GET /api/v1/pages/{slug}/export
//
// The gate is mayEditSpec — the `write` verb — and not plain readership, which
// is the one authorization decision in this file that is not copied from
// pipelines. The reason is §7.1 rule 2: an ordinary reader receives panels
// they may not see as SEALED placeholders (pageDocument), carrying no schema,
// no producer and no SLA. An export carries the whole arrangement by
// definition — a bundle with holes in it is not portable — so exporting under
// a reader's authority would hand out exactly the fields sealing exists to
// withhold. Owner, workspace admin, or a `write` grantee: the three principals
// who can already see the entire spec by editing it.
func (h *PageHandler) Export(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	if user == nil {
		replyError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	wsID := WorkspaceIDFromContext(r.Context())
	slug := r.PathValue("slug")

	rec, err := h.loadPage(r.Context(), wsID, slug)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			replyError(w, http.StatusNotFound, fmt.Sprintf("page %q not found", slug))
			return
		}
		replyInternalError(w, h.logger, "load page for export", err)
		return
	}
	if !h.mayEditSpec(r.Context(), wsID, user.ID, RoleFromContext(r.Context()), rec) {
		replyError(w, http.StatusForbidden,
			"exporting a page carries every panel on it, including panels sealed to you; "+
				"only the page owner, a workspace admin, or a write grantee may export it")
		return
	}
	doc, ok := h.currentDocument(w, rec)
	if !ok {
		return
	}

	owner := ""
	if rec.OwnerCrewID != "" {
		// A crew owner is a SLUG in the bundle. The id it resolves to here is
		// meaningless in any other workspace, which is the whole reason the
		// reference is declared rather than carried.
		owner = h.ownerRef(r.Context(), rec)
	}

	bundle := pageBundle{
		Format: pageBundleFormat,
		Page: pageBundlePage{
			Name:        doc.Metadata.Name,
			Slug:        rec.Slug,
			Description: doc.Metadata.Description,
			Owner:       owner,
			Panels:      make([]pageBundlePanel, 0, len(doc.Spec.Panels)),
		},
		Meta: pageBundleMetadata{
			ExportedAt: h.evaluator().Now().UTC().Format(time.RFC3339),
			PanelCount: len(doc.Spec.Panels),
		},
	}
	for i := range doc.Spec.Panels {
		p := &doc.Spec.Panels[i]
		sla, err := p.SLADuration()
		if err != nil {
			// A stored spec that no longer parses is a server-side fault, not a
			// caller error: it was validated on the way in.
			replyInternalError(w, h.logger, "export page panel sla", err)
			return
		}
		bundle.Page.Panels = append(bundle.Page.Panels, pageBundlePanel{
			ID:         p.ID,
			Schema:     string(p.Schema),
			Title:      p.Title,
			Icon:       string(p.Icon),
			Tab:        p.Tab,
			Owner:      strings.TrimSpace(p.Owner),
			Producer:   strings.TrimSpace(p.Producer),
			SLASeconds: int(sla.Seconds()),
			Span:       p.Span,
		})
	}
	bundle.Refs = pageBundleReferences(doc, owner)

	writeJSON(w, http.StatusOK, bundle)
}

// pageBundleReferences collects every external reference the page needs, once
// each, with the panels that need it.
//
// Order is by reference string rather than by appearance: a bundle is a
// document people diff, and a list whose order depends on map iteration is a
// document that changes when nothing changed.
func pageBundleReferences(doc *pages.Document, owner string) []pageBundleRef {
	byRef := map[string]*pageBundleRef{}
	add := func(ref, kind string, bindable bool, usedBy string) {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			return
		}
		got, ok := byRef[ref]
		if !ok {
			got = &pageBundleRef{Ref: ref, Kind: kind, Bindable: bindable}
			byRef[ref] = got
		}
		for _, u := range got.UsedBy {
			if u == usedBy {
				return
			}
		}
		got.UsedBy = append(got.UsedBy, usedBy)
	}

	if owner != "" {
		add(owner, "crew", true, pageBundleOwnerUse)
	}
	for i := range doc.Spec.Panels {
		p := &doc.Spec.Panels[i]
		add(strings.TrimSpace(p.Owner), "crew", true, p.ID)
		kind, _, err := p.ProducerParts()
		if err != nil {
			continue
		}
		add(strings.TrimSpace(p.Producer), string(kind), pageRefBindable(string(kind)), p.ID)
	}

	out := make([]pageBundleRef, 0, len(byRef))
	for _, ref := range byRef {
		out = append(out, *ref)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Ref < out[j].Ref })
	return out
}

// pageRefBindable reports whether a reference of this kind resolves to a row
// the receiving workspace can be checked against. See pageBundleRef.
func pageRefBindable(kind string) bool {
	switch kind {
	case "crew", string(pages.ProducerRoutine), string(pages.ProducerAgent):
		return true
	default:
		return false
	}
}

// ── Import ─────────────────────────────────────────────────────────────────

// pageImportRequest is a bundle plus the two things only the importer knows:
// the slug it should land under here, and the bindings.
//
// Bind is a MAP and the CLI flag behind it is repeatable rather than
// comma-separated (§11b.13): a slug may plausibly contain a comma, and a
// repeated flag cannot be mis-split. One key may therefore appear only once —
// two bindings for one reference is an operator error the CLI refuses before
// the request is sent, and which JSON could not represent anyway.
type pageImportRequest struct {
	Format string          `json:"format"`
	Page   pageBundlePage  `json:"page"`
	Refs   []pageBundleRef `json:"references"`

	Slug string            `json:"slug"`
	Bind map[string]string `json:"bind"`
}

// pageUnresolvedRef is one reference the import could not bind.
type pageUnresolvedRef struct {
	Ref    string   `json:"ref"`
	Kind   string   `json:"kind"`
	UsedBy []string `json:"used_by"`
	Reason string   `json:"reason"`
}

// Import installs a bundle as a page in the receiving workspace.
//
// POST /api/v1/pages/import
//
// Nothing is written until every reference has resolved. The refusal is a 422
// naming EVERY unresolved reference: "importing a page whose producer routine
// does not exist locally must not create a page full of dead panels" (§10b.2),
// and an import that reported one missing reference per attempt would take as
// many round trips as the page has panels to discover it is not installable.
func (h *PageHandler) Import(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	if user == nil {
		replyError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	wsID := WorkspaceIDFromContext(r.Context())

	// Importing creates a page, which is the same effective privilege as
	// Create — so it is the same gate, including the v109 capability layer: a
	// MEMBER an admin has trusted with page authoring installs a bundle
	// without being promoted.
	if !requireRoleOrCapabilityOrForbid(w, r, h.logger, h.db, wsID, user.ID,
		RoleFromContext(r.Context()), CapabilityPageCreate, "page.create", "page:import", "create") {
		return
	}

	body, ok := readCapped(w, r, pages.MaxSpecBytes+pageImportEnvelopeSlack, "page bundle")
	if !ok {
		return
	}
	var req pageImportRequest
	if err := json.Unmarshal(body, &req); err != nil {
		replyError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if strings.TrimSpace(req.Format) != pageBundleFormat {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error":  fmt.Sprintf("unsupported bundle format %q; this build reads %s", req.Format, pageBundleFormat),
			"format": req.Format,
		})
		return
	}

	// The document as it will be authored here. The slug is the importer's
	// choice (`--slug`), falling back to the bundle's own — a page's slug is
	// its address, and two workspaces may well already have a `weekly-close`.
	doc := &pages.Document{
		APIVersion: pages.DocumentAPIVersion,
		Kind:       pages.DocumentKind,
		Metadata: pages.Metadata{
			Name:        req.Page.Name,
			Slug:        strings.TrimSpace(req.Slug),
			Description: req.Page.Description,
		},
	}
	if doc.Metadata.Slug == "" {
		doc.Metadata.Slug = strings.TrimSpace(req.Page.Slug)
	}
	for i := range req.Page.Panels {
		p := &req.Page.Panels[i]
		if p.SLASeconds < 0 {
			replyError(w, http.StatusBadRequest,
				fmt.Sprintf("panel %q declares a negative sla_seconds", p.ID))
			return
		}
		doc.Spec.Panels = append(doc.Spec.Panels, pages.PanelSpec{
			ID:     p.ID,
			Schema: pages.PanelSchema(p.Schema),
			Title:  p.Title,
			// Validated by Validate below, like every other field off a
			// bundle: an icon this build does not know is a refused import,
			// not a panel that renders a blank header here.
			Icon:     pages.PanelIcon(strings.TrimSpace(p.Icon)),
			Tab:      p.Tab,
			Owner:    strings.TrimSpace(p.Owner),
			Producer: strings.TrimSpace(p.Producer),
			SLA:      fmt.Sprintf("%ds", p.SLASeconds),
			Span:     p.Span,
			// Public is not read from the bundle. See pageBundlePanel: an
			// imported panel is never published, and the importer publishes it
			// themselves after looking at it.
		})
	}
	owner := strings.TrimSpace(req.Page.Owner)

	// 1. Bind. A binding that names nothing in the bundle is refused rather
	//    than ignored: a typo'd `--bind` that silently did nothing would leave
	//    the operator believing they had rebound a reference that is still
	//    pointing at a name this workspace has never heard of.
	if err := applyPageBinds(doc, &owner, req.Bind); err != nil {
		replyError(w, http.StatusBadRequest, err.Error())
		return
	}

	// 2. Shape. The bundle came from somewhere else and is not trusted to be
	//    valid — a spec that fails here never reaches a resolution query.
	if err := doc.Validate(); err != nil {
		writeSpecError(w, err)
		return
	}

	// 3. Bind-time reference checking (§10b.1: import skips the AUTHORING
	//    gate, and its references are checked here instead). Every reference,
	//    in one pass, before anything is written.
	resolved, ownerCrewID, unresolved, err := h.bindPageReferences(r, wsID, doc, owner)
	if err != nil {
		replyInternalError(w, h.logger, "resolve bundle references", err)
		return
	}
	if len(unresolved) > 0 {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"error":      pageUnresolvedMessage(unresolved),
			"unresolved": unresolved,
			"hint": "bind each one to something that exists here: " +
				"crewship page import <file> --bind <bundle-ref>=<local-ref> --bind …",
		})
		return
	}

	// 4. The write, in one transaction.
	specJSON, err := json.Marshal(doc)
	if err != nil {
		replyInternalError(w, h.logger, "marshal imported page spec", err)
		return
	}
	// §10b.3: the same soft cap Create enforces. An import is a create.
	var count int
	if err := h.db.QueryRowContext(r.Context(),
		`SELECT COUNT(*) FROM pages WHERE workspace_id = ?`, wsID).Scan(&count); err != nil {
		replyInternalError(w, h.logger, "count pages", err)
		return
	}
	if count >= pages.MaxPagesPerWorkspace {
		writeRejection(w, pageRejection{
			Kind:    "cap",
			Message: fmt.Sprintf("this workspace holds %d pages; the limit is %d", count, pages.MaxPagesPerWorkspace),
			Detail: map[string]any{
				"pages_existing": count,
				"pages_limit":    pages.MaxPagesPerWorkspace,
			},
		})
		return
	}

	pageID := generateCUID()
	now := h.evaluator().Now().UTC().Format(time.RFC3339)
	ownerUserID := user.ID
	if ownerCrewID != "" {
		ownerUserID = ""
	}

	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		replyInternalError(w, h.logger, "begin page import", err)
		return
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(r.Context(), `
		INSERT INTO pages (id, workspace_id, slug, name, description, owner_user_id, owner_crew_id, spec_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), ?, ?, ?)`,
		pageID, wsID, doc.Metadata.Slug, doc.Metadata.Name, doc.Metadata.Description,
		ownerUserID, ownerCrewID, string(specJSON), now, now); err != nil {
		if isUniqueViolation(err) {
			replyError(w, http.StatusConflict, fmt.Sprintf(
				"a page with slug %q already exists in this workspace; import under another slug with --slug",
				doc.Metadata.Slug))
			return
		}
		replyInternalError(w, h.logger, "insert imported page", err)
		return
	}
	for i := range doc.Spec.Panels {
		if err := insertPanel(r.Context(), tx, pageID, &doc.Spec.Panels[i], resolved[doc.Spec.Panels[i].ID], now); err != nil {
			replyInternalError(w, h.logger, "insert imported page panel", err)
			return
		}
	}
	// §10b.1: every save is a version, and an import is the first one.
	if err := insertPageVersion(r.Context(), tx, pageID, 1, string(specJSON), user.ID, now); err != nil {
		replyInternalError(w, h.logger, "insert imported page version", err)
		return
	}
	if err := tx.Commit(); err != nil {
		replyInternalError(w, h.logger, "commit page import", err)
		return
	}

	rec, err := h.loadPage(r.Context(), wsID, doc.Metadata.Slug)
	if err != nil {
		replyInternalError(w, h.logger, "reload imported page", err)
		return
	}
	panels, err := h.loadPanels(r.Context(), wsID, rec.ID)
	if err != nil {
		replyInternalError(w, h.logger, "reload imported panels", err)
		return
	}
	broadcastWorkspaceEvent(h.hub, wsID, "page.updated", map[string]any{"page_id": rec.ID, "slug": rec.Slug})
	writeJSON(w, http.StatusCreated, h.pageDocument(r.Context(), rec, panels, nil))
}

// applyPageBinds rewrites the document's declared references through the bind
// map, in place.
//
// A binding is matched on the WHOLE reference ("crew/ucetni", not "ucetni"):
// the kind is half the meaning, and a bind keyed on the bare slug could not
// distinguish a crew named `close` from a routine named `close`. For the same
// reason a binding may not change the kind — rebinding `routine/x` to
// `crew/y` would produce a spec that validates and means something nobody
// asked for.
func applyPageBinds(doc *pages.Document, owner *string, bind map[string]string) error {
	if len(bind) == 0 {
		return nil
	}
	// Normalised copy, and the set of references the bundle actually declares.
	binds := make(map[string]string, len(bind))
	for from, to := range bind {
		from, to = strings.TrimSpace(from), strings.TrimSpace(to)
		if from == "" || to == "" {
			return fmt.Errorf("a binding needs both halves: --bind <bundle-ref>=<local-ref>")
		}
		fromKind, _, ok := strings.Cut(from, "/")
		if !ok {
			return fmt.Errorf("binding %q: a reference is \"<kind>/<slug>\" — crew/ucetni, routine/uzaverka", from)
		}
		toKind, _, ok := strings.Cut(to, "/")
		if !ok {
			return fmt.Errorf("binding %q=%q: the target is \"<kind>/<slug>\" too", from, to)
		}
		if fromKind != toKind {
			return fmt.Errorf(
				"binding %q=%q rebinds a %s to a %s; a binding replaces a reference, it does not change what kind of thing the panel names",
				from, to, fromKind, toKind)
		}
		binds[from] = to
	}

	present := map[string]bool{}
	if *owner != "" {
		present[*owner] = true
	}
	for i := range doc.Spec.Panels {
		present[strings.TrimSpace(doc.Spec.Panels[i].Owner)] = true
		present[strings.TrimSpace(doc.Spec.Panels[i].Producer)] = true
	}
	unknown := make([]string, 0)
	for from := range binds {
		if !present[from] {
			unknown = append(unknown, from)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return fmt.Errorf(
			"nothing in this bundle references %s; a binding that matches nothing is a typo, and ignoring it would leave the page pointing at a name this workspace does not know",
			strings.Join(unknown, ", "))
	}

	if to, ok := binds[*owner]; ok {
		*owner = to
	}
	for i := range doc.Spec.Panels {
		p := &doc.Spec.Panels[i]
		if to, ok := binds[strings.TrimSpace(p.Owner)]; ok {
			p.Owner = to
		}
		if to, ok := binds[strings.TrimSpace(p.Producer)]; ok {
			p.Producer = to
		}
	}
	return nil
}

// bindPageReferences resolves every reference in the bound document against
// the receiving workspace, collecting ALL failures rather than stopping at the
// first.
//
// It is the import-time twin of resolveReferences and deliberately not a call
// into it: that function is the AUTHORING gate, it answers with an HTTP 400
// per panel as soon as one reference misses, and §10b.1 puts imports outside
// it. What an importer needs is the complete list of what they must bind,
// which is a different answer to a different question.
//
// The returned error is reserved for a database failure — a reference that
// does not exist is not an error, it is the answer.
func (h *PageHandler) bindPageReferences(r *http.Request, wsID string, doc *pages.Document, owner string) (map[string]resolvedPanel, string, []pageUnresolvedRef, error) {
	resolved := make(map[string]resolvedPanel, len(doc.Spec.Panels))
	unresolved := map[string]*pageUnresolvedRef{}
	var order []string
	miss := func(ref, kind, usedBy, reason string) {
		got, ok := unresolved[ref]
		if !ok {
			got = &pageUnresolvedRef{Ref: ref, Kind: kind, Reason: reason}
			unresolved[ref] = got
			order = append(order, ref)
		}
		got.UsedBy = append(got.UsedBy, usedBy)
	}

	crewIDs := map[string]string{}
	lookupCrew := func(ref string) (string, string, error) {
		if id, ok := crewIDs[ref]; ok {
			return id, "", nil
		}
		kind, slug, _ := strings.Cut(ref, "/")
		if kind != "crew" {
			return "", fmt.Sprintf("%q is not a crew reference; an owner is crew/<slug>", ref), nil
		}
		var id string
		err := h.db.QueryRowContext(r.Context(),
			`SELECT id FROM crews WHERE workspace_id = ? AND slug = ? AND deleted_at IS NULL`, wsID, slug).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) {
			return "", fmt.Sprintf("no crew %q exists in this workspace", slug), nil
		}
		if err != nil {
			return "", "", err
		}
		crewIDs[ref] = id
		return id, "", nil
	}

	ownerCrewID := ""
	if owner != "" {
		id, reason, err := lookupCrew(owner)
		if err != nil {
			return nil, "", nil, err
		}
		if reason != "" {
			miss(owner, "crew", pageBundleOwnerUse, reason)
		}
		ownerCrewID = id
	}

	for i := range doc.Spec.Panels {
		p := &doc.Spec.Panels[i]
		ownerRef := strings.TrimSpace(p.Owner)
		crewID, reason, err := lookupCrew(ownerRef)
		if err != nil {
			return nil, "", nil, err
		}
		if reason != "" {
			miss(ownerRef, "crew", p.ID, reason)
		}

		producerRef := strings.TrimSpace(p.Producer)
		kind, ref, err := p.ProducerParts()
		if err != nil {
			// Validate() has already run, so this cannot happen for a document
			// that got this far; treat it as unresolvable rather than panicking
			// the request.
			miss(producerRef, "unknown", p.ID, err.Error())
			continue
		}
		switch kind {
		case pages.ProducerRoutine:
			var one int
			err := h.db.QueryRowContext(r.Context(),
				`SELECT 1 FROM pipelines WHERE workspace_id = ? AND slug = ? AND deleted_at IS NULL`, wsID, ref).Scan(&one)
			if errors.Is(err, sql.ErrNoRows) {
				miss(p.Producer, string(kind), p.ID,
					fmt.Sprintf("no routine %q exists in this workspace", ref))
			} else if err != nil {
				return nil, "", nil, err
			}
		case pages.ProducerAgent:
			var one int
			err := h.db.QueryRowContext(r.Context(),
				`SELECT 1 FROM agents WHERE workspace_id = ? AND slug = ? AND deleted_at IS NULL`, wsID, ref).Scan(&one)
			if errors.Is(err, sql.ErrNoRows) {
				miss(p.Producer, string(kind), p.ID,
					fmt.Sprintf("no agent %q exists in this workspace", ref))
			} else if err != nil {
				return nil, "", nil, err
			}
		}
		resolved[p.ID] = resolvedPanel{OwnerCrewID: crewID, Kind: string(kind), Ref: ref}
	}

	if len(order) == 0 {
		return resolved, ownerCrewID, nil, nil
	}
	sort.Strings(order)
	out := make([]pageUnresolvedRef, 0, len(order))
	for _, ref := range order {
		out = append(out, *unresolved[ref])
	}
	return nil, "", out, nil
}

// pageUnresolvedMessage is the sentence a plain {"error": …} reader gets. It
// NAMES the references, because the CLI, the UI and a curl all read that field
// and only one of them will also read the structured list.
func pageUnresolvedMessage(unresolved []pageUnresolvedRef) string {
	names := make([]string, 0, len(unresolved))
	for _, u := range unresolved {
		names = append(names, u.Ref+" ("+u.Reason+")")
	}
	noun := "reference"
	if len(names) > 1 {
		noun = "references"
	}
	return fmt.Sprintf(
		"import refused, nothing was created: %d %s do not resolve in this workspace — %s",
		len(names), noun, strings.Join(names, "; "))
}
