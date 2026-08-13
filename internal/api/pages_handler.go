package api

// Pages — the HTTP surface (docs/prd/pages.md §11, §11b).
//
// A page holds no query, no datasource, no connection string and no
// credentials: it renders the last payload a producer pushed, plus the
// metadata the SERVER attached to that push. That single property is what
// shapes this file — there is nothing here that fetches anything, and every
// decision that could be a lie (who may see a panel, how old its data is, who
// produced it) is made server-side.
//
// Wire decisions this file implements, all pinned in §11b:
//
//	 1. Routes are workspace-unscoped (/api/v1/pages/...), with wsCtx supplying
//	    the workspace, following saved-views rather than pipelines.
//	 2. Create/update carry the PARSED spec as JSON, never YAML verbatim — the
//	    server has to validate it, and it cannot validate an opaque string.
//	 3. SLA is `sla_seconds` (integer) on the wire; `sla: 30s` is YAML sugar the
//	    CLI converts.
//	 4. Provenance is a NESTED object {producer, run_id, produced_at}, because
//	    flat fields collide with payload keys the moment a producer emits
//	    `produced_at` itself.
//	 8. There are FOUR panel states and the SERVER sends the fourth: it knows
//	    there is no page_panel_data row, and letting the client infer
//	    `never_produced` from an absent field is how two clients end up
//	    disagreeing.
//
// The error envelope is {"error": …} throughout (replyError), which is what
// hooks/use-pages.ts reads and what internal/cli/client.go CheckError surfaces
// as the CLI's message. The one exception is the oversize rejection, which is
// the richer 422 envelope from internal/sidecar/memory_write.go — see
// pages_data.go.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/pages"
	"github.com/crewship-ai/crewship/internal/ws"
)

// PageHandler serves the Pages surface.
//
// The clock is injected for the same reason internal/pages injects one: the
// freshness verdict is arithmetic against it, and "a panel goes stale exactly
// at its SLA" is only testable if the test owns the clock.
type PageHandler struct {
	db      *sql.DB
	hub     *ws.Hub
	logger  *slog.Logger
	journal journal.Emitter
	clock   pages.Clock
	// pushLimits is §10b.3's push rate, layer 1: per-panel and per-workspace
	// token buckets over the values in internal/ratelimitcfg. Held on the
	// handler rather than in a package global so a test owns its own buckets,
	// and so the numbers are read once per process instead of per request.
	// Layer 2 — the floor that survives more than one replica — is in the push
	// transaction itself (pages_data.go).
	pushLimits *pages.PushLimiter
	// automationRefresh reloads the in-memory automation registry after this
	// handler has written wake-gate rules. Nil in tests and in any process
	// that runs no registry; see SetAutomationRefresh in pages_wake.go for why
	// the alternative is a gate that does nothing for up to a minute after it
	// is authored.
	automationRefresh func(context.Context)
}

// NewPageHandler builds the handler with the production clock.
func NewPageHandler(db *sql.DB, hub *ws.Hub, logger *slog.Logger) *PageHandler {
	return &PageHandler{
		db:         db,
		hub:        hub,
		logger:     logger,
		clock:      pages.SystemClock{},
		pushLimits: pages.NewConfiguredPushLimiter(),
	}
}

// SetJournal wires the journal emitter. An unauthorised push is a signal, not
// noise (§7.1b rule 3), and the journal entry is half of that signal.
func (h *PageHandler) SetJournal(e journal.Emitter) *PageHandler {
	h.journal = e
	return h
}

// SetClockForTesting swaps the time source. Returns the handler so a test can
// chain it onto the constructor.
func (h *PageHandler) SetClockForTesting(c pages.Clock) *PageHandler {
	if c != nil {
		h.clock = c
	}
	return h
}

func (h *PageHandler) evaluator() *pages.Evaluator { return pages.NewEvaluator(h.clock) }

// ── The wire ───────────────────────────────────────────────────────────────

// pageProvenance is §11b decision 4: nested, never flat.
type pageProvenance struct {
	Producer   string `json:"producer"`
	RunID      string `json:"run_id"`
	ProducedAt string `json:"produced_at"`
}

// pagePanelWire is one panel as the API sends and accepts it.
//
// The first block is the SPEC (what a human or an agent authored); the second
// is the SNAPSHOT (what the server computed and attached). Nothing in the
// second block is writable: a request body that carries `state` or
// `provenance` has those fields ignored, because §4 rules 2 and 5 make them
// the server's to write and §7.1b makes identity a property of the token.
type pagePanelWire struct {
	ID         string `json:"id"`
	Schema     string `json:"schema"`
	Title      string `json:"title,omitempty"`
	Owner      string `json:"owner"`
	Producer   string `json:"producer"`
	SLASeconds int    `json:"sla_seconds"`
	Span       int    `json:"span"`
	Public     bool   `json:"public,omitempty"`

	// Icon is the author's glyph for this panel, from the closed set in
	// internal/pages/icons.go. It rides on BOTH halves of this struct, unlike
	// the actions and the gates below: the client draws the header, so a
	// document that carried the icon only on the write path would render every
	// panel with its schema's icon and lose the distinction on the next read.
	// A panel that declares none sends nothing, and the client falls back to
	// the schema's own icon.
	Icon string `json:"icon,omitempty"`

	// Tab is the name on the tab bar this panel appears under
	// (internal/pages/tabs.go). It rides on BOTH halves of this struct for the
	// same reason the icon does — the client draws the bar, and a document that
	// carried the tab only on the write path would render every page as one
	// long scroll and lose the author's grouping on the next read.
	//
	// A page where no panel declares one sends nothing anywhere, and the client
	// draws no bar.
	Tab string `json:"tab,omitempty"`

	// Actions are the buttons this panel declares (§8b.1). They ride on the
	// WRITE half of this struct: a human authoring the page sends them, and the
	// server stores them in spec_json, which is the allow-list a click is
	// resolved against (§8b.2). They are not echoed on the read path here — a
	// panel's actions are served by GET …/panels/{id}/actions, which reads that
	// same stored spec (pages_actions.go), so there is one reader of it and a
	// sealed panel cannot leak its buttons through the page document.
	Actions []pages.PanelAction `json:"actions,omitempty"`

	// Wake and OnFailure are the sensor half of the panel (§5, §4 rule 4) and
	// ride on the WRITE half of this struct, like Actions: a human authors
	// them, the server stores them in spec_json, and spec_json is what the
	// gate compiler and the freshness sweeper read. They are not echoed on the
	// read path — `crewship page export` serves the stored spec, which is
	// where the authored form belongs, and a panel document that carried
	// half the spec back would invite a client to treat it as the whole of it.
	Wake      []pages.PanelWake     `json:"wake,omitempty"`
	OnFailure *pages.PanelOnFailure `json:"on_failure,omitempty"`

	State      string          `json:"state,omitempty"`
	Reason     string          `json:"reason,omitempty"`
	Data       json.RawMessage `json:"data,omitempty"`
	Provenance *pageProvenance `json:"provenance,omitempty"`
}

// pageSealedPanelWire is §11b decision 14: a panel the viewer may not see is
// serialised as EXACTLY this — no schema, no payload, no producer, no SLA.
//
// The placeholder rather than an omission is what keeps the page the same
// shape for everyone (§2.3): the grid does not reflow into a different layout
// depending on who is looking, and an agent assembling a cross-crew page can
// see that a slot is filled without seeing what fills it (§7.1b rule 2).
//
// `sealed` is present and true rather than inferred from missing fields,
// because the renderer must never mistake a serialisation bug for a permission
// decision — those are opposite failures and only one of them is safe.
type pageSealedPanelWire struct {
	PanelID       string `json:"panel_id"`
	Span          int    `json:"span"`
	Sealed        bool   `json:"sealed"`
	OwnerCrewName string `json:"owner_crew_name"`

	// Tab is the one field §11b decision 14's list has gained, and it is here
	// for the decision's OWN reason rather than in spite of it.
	//
	// The placeholder exists so the page has the same shape for everyone: the
	// grid does not reflow depending on who is looking. A tab bar is part of
	// that shape. Without this field a sealed panel would have no tab, would
	// fall onto the first one, and a tab whose panels are all foreign would
	// vanish from that reader's bar — which both reflows the page per viewer
	// and discloses, by the tab's absence, that everything on it belongs to a
	// crew they are not in. The tab name is authored page structure, exactly
	// like `span`; it says nothing about the panel's data, its producer or its
	// health, which is what the placeholder withholds.
	Tab string `json:"tab,omitempty"`
}

// pageWire is the page document returned by get/create/update.
//
// Panels is []any because it is heterogeneous by design: a full panel or a
// sealed placeholder, decided per panel and per viewer.
type pageWire struct {
	ID          string `json:"id"`
	Slug        string `json:"slug"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Owner       string `json:"owner"`
	Panels      []any  `json:"panels"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

// pageListWire is one row of the index. It carries counts rather than panels:
// a workspace's whole page set is a nav surface, and shipping every payload on
// it is not what a list costs.
//
// §11b decision 15 pins the freshness rollup, and it is the whole reason the
// index is useful: without panel_states the overview band (PAGES / STALE NOW /
// UPDATED TODAY / NOT REPORTING) and the STATUS facet have nothing to count
// and render as em dashes.
//
// LastProducedAt is deliberately NOT UpdatedAt. §10 defines updated_at as the
// SPEC's modification time, so a page whose spec was edited an hour ago but
// whose data last arrived a week ago would read as "updated today" if the two
// were conflated. They answer different questions.
type pageListWire struct {
	ID            string `json:"id"`
	Slug          string `json:"slug"`
	Name          string `json:"name"`
	Description   string `json:"description,omitempty"`
	Owner         string `json:"owner"`
	OwnerCrewSlug string `json:"owner_crew_slug,omitempty"`
	// PanelCount counts EVERY panel on the page, including sealed ones: the
	// grid renders a placeholder for those, so a count that skipped them would
	// disagree with what the page draws.
	PanelCount int `json:"panel_count"`
	// PanelStates always carries all four states, zeros included — a client
	// reading a missing key cannot tell "none in this state" from "this build
	// does not send that state".
	PanelStates    map[string]int `json:"panel_states"`
	State          string         `json:"state,omitempty"`
	LastProducedAt string         `json:"last_produced_at,omitempty"`
	CreatedAt      string         `json:"created_at"`
	UpdatedAt      string         `json:"updated_at"`
}

// zeroPanelStates is the rollup's fixed shape (§11b decision 15).
func zeroPanelStates() map[string]int {
	return map[string]int{
		string(pages.StateFresh):         0,
		string(pages.StateStale):         0,
		string(pages.StateFailed):        0,
		string(pages.StateNeverProduced): 0,
	}
}

// pageWriteRequest is the parsed spec a client sends to create or update.
//
// It is deliberately the FLAT shape rather than the manifest envelope: the CLI
// parses the YAML document (internal/pages.ParseDocument) and sends what it
// parsed, so apiVersion/kind have already done their job by the time this
// arrives. The server rebuilds a pages.Document from it and validates again —
// the client's validation is a courtesy, this one is the gate (§10b.1).
type pageWriteRequest struct {
	Slug        *string          `json:"slug"`
	Name        *string          `json:"name"`
	Description *string          `json:"description"`
	Panels      []pagePanelWire  `json:"panels"`
	Owner       *string          `json:"owner"`
	Spec        *json.RawMessage `json:"spec"`
}

// ── Internal records ───────────────────────────────────────────────────────

type pageRecord struct {
	ID          string
	Slug        string
	Name        string
	Description string
	OwnerUserID string
	OwnerCrewID string
	CreatedAt   string
	UpdatedAt   string
}

type panelRecord struct {
	RowID         string
	PanelID       string
	Schema        string
	Title         string
	OwnerCrewID   string
	OwnerCrew     string
	OwnerCrewName string
	ProducerKind  string
	ProducerRef   string
	SLASeconds    int
	Span          int
	// Icon is the author's chosen glyph. It has no column: it is presentation,
	// it is validated on the way into spec_json, and page_panels carries the
	// contract (schema, owner, producer, SLA) rather than the styling. It is
	// attached from the parsed spec the read path already loads, next to the
	// wake gates — see panelsFor.
	Icon string
	// Tab is the bar this panel renders under (internal/pages/tabs.go). Like
	// the icon it has no column and is attached from the parsed spec: it is
	// layout, and page_panels carries the contract.
	Tab string
	// Fault is §10b.4's stated reason: the producer was deleted, the owning
	// crew removed. It outranks the clock — no amount of recent data makes a
	// panel whose producer is gone current.
	Fault string

	// Gates are the panel's compiled `wake:` thresholds and its on_failure
	// crew (§5, §4 rule 4), attached from the spec loadPanels already parses.
	// A panel that declares neither leaves this zero, and the push path spends
	// one length check on it. See pages_wake.go.
	Gates panelGates

	// Snapshot, filled from the newest page_panel_data row when there is one.
	HasData    bool
	Seq        int64
	Payload    string
	ProducedAt time.Time
	RunID      string
	PushState  string
}

func (p *panelRecord) producerRef() string { return p.ProducerKind + "/" + p.ProducerRef }
func (p *panelRecord) ownerRef() string    { return "crew/" + p.OwnerCrew }

// ── 1. List — GET /api/v1/pages ────────────────────────────────────────────

// List returns every page in the workspace the caller may see, newest edit
// first. Panels the caller may not see are counted out of the rollup as well
// as out of the document: a state count that includes a panel the viewer will
// never be shown is a leak of that panel's existence and its health.
func (h *PageHandler) List(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	if user == nil {
		replyError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	wsID := WorkspaceIDFromContext(r.Context())

	rows, err := h.db.QueryContext(r.Context(), `
		SELECT p.id, p.slug, p.name, COALESCE(p.description, ''),
		       COALESCE(p.owner_user_id, ''), COALESCE(p.owner_crew_id, ''),
		       p.created_at, p.updated_at
		FROM pages p
		WHERE p.workspace_id = ?
		ORDER BY p.updated_at DESC, p.slug ASC`, wsID)
	if err != nil {
		replyInternalError(w, h.logger, "list pages", err)
		return
	}
	defer rows.Close()

	var records []pageRecord
	for rows.Next() {
		var p pageRecord
		if err := rows.Scan(&p.ID, &p.Slug, &p.Name, &p.Description,
			&p.OwnerUserID, &p.OwnerCrewID, &p.CreatedAt, &p.UpdatedAt); err != nil {
			replyInternalError(w, h.logger, "scan page", err)
			return
		}
		records = append(records, p)
	}
	if err := rows.Err(); err != nil {
		replyInternalError(w, h.logger, "rows iteration (pages)", err)
		return
	}

	viewer, err := h.loadViewer(r.Context(), wsID, user.ID)
	if err != nil {
		replyInternalError(w, h.logger, "load page viewer", err)
		return
	}

	out := make([]pageListWire, 0, len(records))
	for i := range records {
		rec := &records[i]
		panels, err := h.loadPanels(r.Context(), wsID, rec.ID)
		if err != nil {
			replyInternalError(w, h.logger, "load page panels", err)
			return
		}
		row := pageListWire{
			ID:          rec.ID,
			Slug:        rec.Slug,
			Name:        rec.Name,
			Description: rec.Description,
			Owner:       h.ownerRef(r.Context(), rec),
			PanelCount:  len(panels),
			PanelStates: zeroPanelStates(),
			CreatedAt:   rec.CreatedAt,
			UpdatedAt:   rec.UpdatedAt,
		}
		if rec.OwnerCrewID != "" {
			row.OwnerCrewSlug = strings.TrimPrefix(row.Owner, "crew/")
		}
		// The rollup counts only the panels this viewer may see. A sealed
		// panel contributes to panel_count (the grid draws it) and to nothing
		// else: reporting its state would disclose the health of data the
		// viewer is not entitled to read.
		var newest time.Time
		for _, panel := range panels {
			if !h.canSeePanel(viewer, panel) {
				continue
			}
			v := h.verdict(panel)
			row.PanelStates[string(v.State)]++
			if panel.HasData && panel.ProducedAt.After(newest) {
				newest = panel.ProducedAt
			}
		}
		row.State = string(worstPanelState(row.PanelStates))
		if !newest.IsZero() {
			row.LastProducedAt = newest.UTC().Format(time.RFC3339)
		}
		out = append(out, row)
	}

	writeJSON(w, http.StatusOK, out)
}

// worstPanelState mirrors hooks/use-pages.ts worstPanelState: the page reports
// its worst panel, and `never_produced` outranks `fresh` because a page with an
// empty panel is not finished being set up.
func worstPanelState(counts map[string]int) pages.State {
	order := []pages.State{pages.StateFailed, pages.StateStale, pages.StateNeverProduced, pages.StateFresh}
	for _, s := range order {
		if counts[string(s)] > 0 {
			return s
		}
	}
	return ""
}

// ── 2. Get — GET /api/v1/pages/{slug} ──────────────────────────────────────

// Get returns one page with its panels, their last payload, the server's
// freshness verdict and the server-attached provenance.
//
// Panels the viewer may not see are OMITTED (§7.1 rule 2) — filtered here,
// before serialisation, never hidden client-side, because a hidden-but-
// delivered panel is a data leak (§7.1 rule 5).
func (h *PageHandler) Get(w http.ResponseWriter, r *http.Request) {
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
		replyInternalError(w, h.logger, "load page", err)
		return
	}
	panels, err := h.loadPanels(r.Context(), wsID, rec.ID)
	if err != nil {
		replyInternalError(w, h.logger, "load page panels", err)
		return
	}
	viewer, err := h.loadViewer(r.Context(), wsID, user.ID)
	if err != nil {
		replyInternalError(w, h.logger, "load page viewer", err)
		return
	}

	// A caller who may edit the spec gets the authored half back, so the
	// document the editor renders is the whole page rather than a copy that
	// loses its gates on save.
	authored := h.mayEditSpec(r.Context(), wsID, user.ID, RoleFromContext(r.Context()), rec)
	writeJSON(w, http.StatusOK, h.pageDocumentFor(r.Context(), rec, panels, viewer, authored))
}

// ── 3. Create — POST /api/v1/pages ─────────────────────────────────────────

// Create validates and stores a page spec.
//
// The authoring gate is §10b.1's cheap half: validate the spec against the
// schema, then check that every declared owner and producer RESOLVES. It stops
// an agent saving a page that names a routine which does not exist — the page
// would render a grid of dead panels and nobody would know why.
func (h *PageHandler) Create(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	if user == nil {
		replyError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	wsID := WorkspaceIDFromContext(r.Context())

	// page.create is the v109 capability layer: a MEMBER an admin has trusted
	// with page authoring passes without being promoted to MANAGER.
	if !requireRoleOrCapabilityOrForbid(w, r, h.logger, h.db, wsID, user.ID,
		RoleFromContext(r.Context()), CapabilityPageCreate, "page.create", "page:new", "create") {
		return
	}

	req, ok := h.decodeWrite(w, r)
	if !ok {
		return
	}
	doc, ok := h.documentFrom(w, req, "")
	if !ok {
		return
	}

	// §10b.3: pages per workspace is a soft, admin-raisable cap that exists to
	// stop an agent loop producing thousands.
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

	resolved, ok := h.resolveReferences(w, r, wsID, doc)
	if !ok {
		return
	}
	// The sensor's half of the same gate: every `wake:` predicate has to be
	// readable against its panel's schema, and every crew it names has to
	// exist (§5). Before the transaction, so a bad gate is a 400 rather than a
	// rollback.
	gates, ok := h.resolveGates(w, r, wsID, doc)
	if !ok {
		return
	}

	// §7.1 rule 1: a page has exactly one owner, and it is either a user or a
	// crew — owner_user_id XOR owner_crew_id, which the schema enforces. The
	// creator is the default; `owner: crew/<slug>` hands the page to the crew,
	// which is the natural home for a crew's own status board and needs no
	// personal owner at all.
	ownerUserID, ownerCrewID := user.ID, ""
	if req.Owner != nil && strings.TrimSpace(*req.Owner) != "" {
		kind, ref, _ := strings.Cut(strings.TrimSpace(*req.Owner), "/")
		switch kind {
		case "crew":
			var crewID string
			err := h.db.QueryRowContext(r.Context(),
				`SELECT id FROM crews WHERE workspace_id = ? AND slug = ? AND deleted_at IS NULL`, wsID, ref).Scan(&crewID)
			if errors.Is(err, sql.ErrNoRows) {
				replyError(w, http.StatusBadRequest, fmt.Sprintf("owner crew/%s does not exist in this workspace", ref))
				return
			}
			if err != nil {
				replyInternalError(w, h.logger, "resolve page owner crew", err)
				return
			}
			ownerUserID, ownerCrewID = "", crewID
		case "user":
			if ref != user.ID {
				// Assigning a page to somebody else is a transfer, not a
				// creation, and §7.1 rule 1b gives transfer its own rules.
				replyError(w, http.StatusBadRequest,
					"a page is created owned by its creator or by a crew; transferring it to another user is a separate action")
				return
			}
		default:
			replyError(w, http.StatusBadRequest, `owner must be "crew/<slug>" or omitted`)
			return
		}
	}

	specJSON, err := json.Marshal(doc)
	if err != nil {
		replyInternalError(w, h.logger, "marshal page spec", err)
		return
	}

	pageID := generateCUID()
	// The handler's clock, not the wall clock: every timestamp Pages writes has to
	// come from one source, or a test that owns the clock still gets a spec mtime
	// it cannot predict.
	now := h.evaluator().Now().UTC().Format(time.RFC3339)

	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		replyInternalError(w, h.logger, "begin page create", err)
		return
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(r.Context(), `
		INSERT INTO pages (id, workspace_id, slug, name, description, owner_user_id, owner_crew_id, spec_json, created_at, updated_at)
		VALUES (?, ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''), NULLIF(?, ''), ?, ?, ?)`,
		pageID, wsID, doc.Metadata.Slug, doc.Metadata.Name, doc.Metadata.Description,
		ownerUserID, ownerCrewID, string(specJSON), now, now); err != nil {
		if isUniqueViolation(err) {
			replyError(w, http.StatusConflict, fmt.Sprintf("a page with slug %q already exists in this workspace", doc.Metadata.Slug))
			return
		}
		replyInternalError(w, h.logger, "insert page", err)
		return
	}
	for i := range doc.Spec.Panels {
		if err := insertPanel(r.Context(), tx, pageID, &doc.Spec.Panels[i], resolved[doc.Spec.Panels[i].ID], now); err != nil {
			replyInternalError(w, h.logger, "insert page panel", err)
			return
		}
	}
	// §5: each wake gate compiles to an `automations` row, in the SAME
	// transaction as the page. A page whose spec says it wakes devops, saved
	// next to a rule set that failed to write, is a page that lies about what
	// it does.
	if err := reconcileWakeAutomations(r.Context(), tx, wsID, pageID, doc.Metadata.Slug, gates, user.ID, now); err != nil {
		replyInternalError(w, h.logger, "compile page wake gates", err)
		return
	}
	// §10b.1: every save is a version, following the pipeline_versions
	// precedent — several agents may rewrite one page and the one who breaks it
	// is rarely the one who notices.
	if err := insertPageVersion(r.Context(), tx, pageID, 1, string(specJSON), user.ID, now); err != nil {
		replyInternalError(w, h.logger, "insert page version", err)
		return
	}
	if err := tx.Commit(); err != nil {
		replyInternalError(w, h.logger, "commit page create", err)
		return
	}

	rec, err := h.loadPage(r.Context(), wsID, doc.Metadata.Slug)
	if err != nil {
		replyInternalError(w, h.logger, "reload created page", err)
		return
	}
	panels, err := h.loadPanels(r.Context(), wsID, rec.ID)
	if err != nil {
		replyInternalError(w, h.logger, "reload created panels", err)
		return
	}
	// A gate authored in this save must fire on the NEXT push, not up to a
	// minute later (§5).
	h.refreshAutomations(r.Context())
	broadcastWorkspaceEvent(h.hub, wsID, "page.updated", map[string]any{"page_id": rec.ID, "slug": rec.Slug})
	writeJSON(w, http.StatusCreated, h.pageDocument(r.Context(), rec, panels, nil))
}

// ── 4. Update — PATCH /api/v1/pages/{slug} ─────────────────────────────────

// Update replaces the page's spec. Panels are reconciled by panel_id rather
// than rebuilt: a panel that survives an edit keeps its payload ring, because
// deleting the row would cascade the history away and the panel would come
// back reading "never produced" — a sentence that would not be true.
func (h *PageHandler) Update(w http.ResponseWriter, r *http.Request) {
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
		replyInternalError(w, h.logger, "load page for update", err)
		return
	}
	// `write` is authority over ARRANGEMENT, never over content (§7.1b rule 2).
	if !h.mayEditSpec(r.Context(), wsID, user.ID, RoleFromContext(r.Context()), rec) {
		replyError(w, http.StatusForbidden, "only the page owner, a workspace admin, or a write grantee may edit this page")
		return
	}

	req, ok := h.decodeWrite(w, r)
	if !ok {
		return
	}
	// The slug is the page's identity and its URL; renaming it through a PATCH
	// would silently break every producer script that pushes to it.
	if req.Slug != nil && *req.Slug != "" && *req.Slug != rec.Slug {
		replyError(w, http.StatusBadRequest,
			"a page's slug is its address; create a new page rather than renaming this one")
		return
	}
	base, ok := h.currentDocument(w, rec)
	if !ok {
		return
	}
	if req.Name != nil {
		base.Metadata.Name = *req.Name
	}
	if req.Description != nil {
		base.Metadata.Description = *req.Description
	}
	if req.Panels != nil {
		panels, ok := panelSpecsFrom(w, req.Panels)
		if !ok {
			return
		}
		base.Spec.Panels = panels
	}
	if err := base.Validate(); err != nil {
		writeSpecError(w, err)
		return
	}
	resolved, ok := h.resolveReferences(w, r, wsID, base)
	if !ok {
		return
	}
	gates, ok := h.resolveGates(w, r, wsID, base)
	if !ok {
		return
	}

	specJSON, err := json.Marshal(base)
	if err != nil {
		replyInternalError(w, h.logger, "marshal page spec", err)
		return
	}
	// The handler's clock, not the wall clock: every timestamp Pages writes has to
	// come from one source, or a test that owns the clock still gets a spec mtime
	// it cannot predict.
	now := h.evaluator().Now().UTC().Format(time.RFC3339)

	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		replyInternalError(w, h.logger, "begin page update", err)
		return
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(r.Context(),
		`UPDATE pages SET name = ?, description = NULLIF(?, ''), spec_json = ?, updated_at = ? WHERE id = ?`,
		base.Metadata.Name, base.Metadata.Description, string(specJSON), now, rec.ID); err != nil {
		replyInternalError(w, h.logger, "update page", err)
		return
	}
	if err := reconcilePanels(r.Context(), tx, rec.ID, base, resolved, now); err != nil {
		replyInternalError(w, h.logger, "reconcile page panels", err)
		return
	}
	// A gate removed from the spec loses its rule here, in the same
	// transaction that removed it. The rules are derived state; the spec is
	// the source of truth (§5).
	if err := reconcileWakeAutomations(r.Context(), tx, wsID, rec.ID, rec.Slug, gates, user.ID, now); err != nil {
		replyInternalError(w, h.logger, "compile page wake gates", err)
		return
	}
	var seq int64
	if err := tx.QueryRowContext(r.Context(),
		`SELECT COALESCE(MAX(seq), 0) + 1 FROM page_versions WHERE page_id = ?`, rec.ID).Scan(&seq); err != nil {
		replyInternalError(w, h.logger, "next page version", err)
		return
	}
	if err := insertPageVersion(r.Context(), tx, rec.ID, seq, string(specJSON), user.ID, now); err != nil {
		replyInternalError(w, h.logger, "insert page version", err)
		return
	}
	// §10b.3 keeps the last 50 versions. Through trimPageVersions, not an
	// inline DELETE: rollback appends versions too, and this rule having two
	// implementations is how the two start disagreeing about what 50 means.
	// The grants resolver already had to be de-duplicated for the same reason.
	if err := trimPageVersions(r.Context(), tx, rec.ID, seq); err != nil {
		replyInternalError(w, h.logger, "trim page versions", err)
		return
	}
	if err := tx.Commit(); err != nil {
		replyInternalError(w, h.logger, "commit page update", err)
		return
	}

	updated, err := h.loadPage(r.Context(), wsID, rec.Slug)
	if err != nil {
		replyInternalError(w, h.logger, "reload updated page", err)
		return
	}
	panels, err := h.loadPanels(r.Context(), wsID, updated.ID)
	if err != nil {
		replyInternalError(w, h.logger, "reload updated panels", err)
		return
	}
	h.refreshAutomations(r.Context())
	broadcastWorkspaceEvent(h.hub, wsID, "page.updated", map[string]any{"page_id": updated.ID, "slug": updated.Slug})
	writeJSON(w, http.StatusOK, h.pageDocument(r.Context(), updated, panels, nil))
}

// ── 5. Delete — DELETE /api/v1/pages/{slug} ────────────────────────────────

// Delete removes the page and everything that cascades from it.
func (h *PageHandler) Delete(w http.ResponseWriter, r *http.Request) {
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
		replyInternalError(w, h.logger, "load page for delete", err)
		return
	}
	// Deleting is not editing: a `write` grant rearranges the page, it does not
	// remove it. Owner or workspace ADMIN/OWNER only.
	if !h.isPageOwner(r.Context(), wsID, user.ID, rec) && !canRole(RoleFromContext(r.Context()), "manage") {
		replyError(w, http.StatusForbidden, "only the page owner or a workspace admin may delete this page")
		return
	}
	if _, err := h.db.ExecContext(r.Context(), `DELETE FROM pages WHERE id = ?`, rec.ID); err != nil {
		replyInternalError(w, h.logger, "delete page", err)
		return
	}
	// Panels, data, versions and grants cascade from the page row; the wake
	// gates' `automations` rows belong to another feature's table and do not.
	// A rule left behind would sit in `automation list` matching an event no
	// surviving panel can emit.
	if err := deletePageWakeAutomations(r.Context(), h.db, wsID, rec.ID); err != nil {
		// The page is gone and the response is a 204 either way; a rule the
		// delete could not remove is an orphan to log, not a reason to tell
		// the caller their delete failed.
		h.logger.Warn("pages: deleting the page's wake automations failed",
			"page", rec.Slug, "page_id", rec.ID, "error", err)
	}
	h.refreshAutomations(r.Context())
	broadcastWorkspaceEvent(h.hub, wsID, "page.deleted", map[string]any{"page_id": rec.ID, "slug": rec.Slug})
	w.WriteHeader(http.StatusNoContent)
}

// ── Loading ────────────────────────────────────────────────────────────────

func (h *PageHandler) loadPage(ctx context.Context, wsID, slug string) (*pageRecord, error) {
	var p pageRecord
	err := h.db.QueryRowContext(ctx, `
		SELECT id, slug, name, COALESCE(description, ''),
		       COALESCE(owner_user_id, ''), COALESCE(owner_crew_id, ''),
		       created_at, updated_at
		FROM pages WHERE workspace_id = ? AND slug = ?`, wsID, slug).Scan(
		&p.ID, &p.Slug, &p.Name, &p.Description, &p.OwnerUserID, &p.OwnerCrewID,
		&p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// loadPanels returns the page's panels in SPEC order with their newest payload
// attached.
//
// Spec order, not row order: page_panels has no position column (§10), and the
// authored spec is the thing that knows how the grid reads. Reconciliation
// keeps the rows in step with it, so the spec is a safe index.
//
// The two LEFT JOINs are §10b.4 — "when the ground moves". If a panel's
// producer routine or agent has been deleted, or its owning crew removed, the
// panel does NOT disappear and does not go on rendering its last number as if
// nothing had happened: it switches to failed with a stated reason and stays
// on the page. A page is a fixed structure, and silently shrinking it would
// mean the page lies about what it is supposed to show.
func (h *PageHandler) loadPanels(ctx context.Context, wsID, pageID string) ([]*panelRecord, error) {
	rows, err := h.db.QueryContext(ctx, `
		SELECT pp.id, pp.panel_id, pp.schema, COALESCE(pp.title, ''),
		       pp.owner_crew_id, c.slug, c.name, c.deleted_at IS NOT NULL,
		       pp.producer_kind, pp.producer_ref, pp.sla_seconds, pp.span,
		       CASE pp.producer_kind
		            WHEN 'routine' THEN pl.id IS NOT NULL
		            WHEN 'agent'   THEN ag.id IS NOT NULL
		            ELSE 1
		       END AS producer_alive
		FROM page_panels pp
		JOIN crews c ON c.id = pp.owner_crew_id
		LEFT JOIN pipelines pl
		       ON pp.producer_kind = 'routine' AND pl.workspace_id = ?
		      AND pl.slug = pp.producer_ref AND pl.deleted_at IS NULL
		LEFT JOIN agents ag
		       ON pp.producer_kind = 'agent' AND ag.workspace_id = ?
		      AND ag.slug = pp.producer_ref AND ag.deleted_at IS NULL
		WHERE pp.page_id = ?`, wsID, wsID, pageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	byPanelID := map[string]*panelRecord{}
	var order []*panelRecord
	for rows.Next() {
		var p panelRecord
		var crewGone, producerAlive bool
		if err := rows.Scan(&p.RowID, &p.PanelID, &p.Schema, &p.Title, &p.OwnerCrewID, &p.OwnerCrew, &p.OwnerCrewName,
			&crewGone, &p.ProducerKind, &p.ProducerRef, &p.SLASeconds, &p.Span, &producerAlive); err != nil {
			return nil, err
		}
		switch {
		case crewGone:
			p.Fault = fmt.Sprintf("the owning crew %q no longer exists", p.OwnerCrew)
		case !producerAlive:
			p.Fault = fmt.Sprintf("producer %s %q no longer exists", p.ProducerKind, p.ProducerRef)
		}
		rec := p
		byPanelID[p.PanelID] = &rec
		order = append(order, &rec)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(order) == 0 {
		return nil, nil
	}

	// Newest payload per panel. One statement rather than one per panel: the
	// page read is the hot path and a page carries up to 24 panels.
	ids := make([]any, 0, len(order))
	for _, p := range order {
		ids = append(ids, p.RowID)
	}
	dataRows, err := h.db.QueryContext(ctx, `
		SELECT d.panel_id, d.seq, d.payload_json, d.produced_at,
		       COALESCE(d.producer_run_id, ''), d.state
		FROM page_panel_data d
		JOIN (
			SELECT panel_id, MAX(seq) AS seq
			FROM page_panel_data
			WHERE panel_id IN (`+sqlPlaceholders(len(ids))+`)
			GROUP BY panel_id
		) newest ON newest.panel_id = d.panel_id AND newest.seq = d.seq`, ids...)
	if err != nil {
		return nil, err
	}
	defer dataRows.Close()
	byRowID := map[string]*panelRecord{}
	for _, p := range order {
		byRowID[p.RowID] = p
	}
	for dataRows.Next() {
		var rowID, payload, producedAt, runID, state string
		var seq int64
		if err := dataRows.Scan(&rowID, &seq, &payload, &producedAt, &runID, &state); err != nil {
			return nil, err
		}
		p, ok := byRowID[rowID]
		if !ok {
			continue
		}
		p.HasData = true
		p.Seq = seq
		p.Payload = payload
		p.ProducedAt = parsePageTime(producedAt)
		p.RunID = runID
		p.PushState = state
	}
	if err := dataRows.Err(); err != nil {
		return nil, err
	}

	// Spec order. A panel present in the table but absent from the spec (a
	// reconciliation that raced an edit) still renders, at the end, rather than
	// disappearing — §10b.4: a panel never disappears quietly.
	var specDoc pages.Document
	var specJSON string
	if err := h.db.QueryRowContext(ctx, `SELECT spec_json FROM pages WHERE id = ?`, pageID).Scan(&specJSON); err == nil {
		_ = json.Unmarshal([]byte(specJSON), &specDoc)
	}
	// The same spec carries each panel's wake gates and on_failure block
	// (§5, §4 rule 4). Attached here, off a document that is already parsed,
	// so the sensor costs the read path nothing — see pages_wake.go.
	attachPanelGates(&specDoc, byPanelID)
	ordered := make([]*panelRecord, 0, len(order))
	seen := map[string]bool{}
	for _, ps := range specDoc.Spec.Panels {
		if p, ok := byPanelID[ps.ID]; ok && !seen[ps.ID] {
			// The icon comes off the spec for the same reason the gates do:
			// it is authored, it is not part of the panel's contract, and the
			// document is already parsed here. A panel in the table but not in
			// the spec (the racing-edit case below) simply keeps its schema's
			// icon, which is what it had before it declared one.
			p.Icon = string(ps.Icon)
			// The tab travels the same road, for the same reason. A panel in
			// the table but not in the spec keeps no tab and therefore lands on
			// the first one — it is already rendering at the end of the page
			// rather than disappearing (§10b.4), and a visible panel on the
			// wrong tab is a better failure than a panel on no tab at all.
			p.Tab = ps.Tab
			ordered = append(ordered, p)
			seen[ps.ID] = true
		}
	}
	rest := make([]*panelRecord, 0)
	for _, p := range order {
		if !seen[p.PanelID] {
			rest = append(rest, p)
		}
	}
	sort.SliceStable(rest, func(i, j int) bool { return rest[i].PanelID < rest[j].PanelID })
	return append(ordered, rest...), nil
}

// parsePageTime reads a stored timestamp. Everything Pages writes is RFC 3339
// UTC; the fallbacks cover rows written by SQLite's own datetime('now')
// default, which has no zone marker.
func parsePageTime(s string) time.Time {
	s = strings.TrimSpace(s)
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05", "2006-01-02T15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

// ── Serialising ────────────────────────────────────────────────────────────

func (h *PageHandler) verdict(p *panelRecord) pages.Verdict {
	state := pages.PanelState{SLA: time.Duration(p.SLASeconds) * time.Second, Fault: p.Fault}
	if p.HasData {
		push := pages.PushOK
		if p.PushState == string(pages.PushFailed) {
			push = pages.PushFailed
		}
		state.Last = &pages.Observation{ProducedAt: p.ProducedAt, Push: push}
	}
	return h.evaluator().Evaluate(state)
}

// panelWire renders one panel, spec plus snapshot.
//
// The provenance object is built from stored columns only. There is no branch
// here through which a producer-supplied value could reach it — that is the
// whole point of §4 rule 5, and it is enforced upstream, in pages_data.go,
// by never reading identity out of the request body.
func (h *PageHandler) panelWire(p *panelRecord) pagePanelWire {
	v := h.verdict(p)
	out := pagePanelWire{
		ID:         p.PanelID,
		Schema:     p.Schema,
		Title:      p.Title,
		Icon:       p.Icon,
		Tab:        p.Tab,
		Owner:      p.ownerRef(),
		Producer:   p.producerRef(),
		SLASeconds: p.SLASeconds,
		Span:       p.Span,
		State:      string(v.State),
		Reason:     v.Reason,
	}
	if p.HasData {
		out.Data = json.RawMessage(p.Payload)
		out.Provenance = &pageProvenance{
			Producer:   p.producerRef(),
			RunID:      pushReference(p),
			ProducedAt: p.ProducedAt.UTC().Format(time.RFC3339),
		}
	}
	return out
}

// pushReference is the `run_id` half of §4 rule 5's provenance triple.
//
// A routine push carries a real pipeline_runs id and that is what is shown. A
// script or webhook producer has no run — §10 says as much, and
// page_panel_data.producer_run_id is a foreign key into pipeline_runs, so
// there is nowhere to invent one. Leaving it blank would mean a panel footer
// that cannot answer "which push produced this", which §4 rule 5 requires of
// EVERY panel, so the fallback is the push's own server-side identity: the
// panel row and the ring sequence that together name exactly one accepted
// push. It is prefixed so it can never be mistaken for a run id and looked up
// as one.
func pushReference(p *panelRecord) string {
	if p.RunID != "" {
		return p.RunID
	}
	return fmt.Sprintf("push:%s:%d", p.RowID, p.Seq)
}

// pageDocument renders the page for one viewer: full panels where they are
// entitled, sealed placeholders everywhere else (§7.1 rule 2, §11b decision
// 14). A nil viewer sees everything and is only ever passed on the create and
// update paths, where the caller has just authored the spec.
func (h *PageHandler) pageDocument(ctx context.Context, rec *pageRecord, panels []*panelRecord, viewer *pageViewer) pageWire {
	return h.pageDocumentFor(ctx, rec, panels, viewer, false)
}

// pageDocumentFor is pageDocument with one extra decision: whether to echo the
// AUTHORED half of each panel — actions, wake gates and on_failure.
//
// The original design withheld them from every read so that spec_json had one
// reader and a sealed panel could not leak its buttons. That was right about
// the leak and wrong about the consequence: the in-app editor renders its YAML
// from this document, so a document without them is a lossy copy of the page,
// and saving that copy back deleted the gates and their compiled automation
// rows. A page could be disarmed by renaming it.
//
// So the withholding narrows rather than disappears. `authored` is true only
// for a caller who may already edit the spec — who can therefore read the
// whole of it through export anyway — and a sealed panel never reaches this
// branch, because it was replaced by the placeholder above. A reader who
// cannot edit still sees exactly what they saw before.
func (h *PageHandler) pageDocumentFor(ctx context.Context, rec *pageRecord, panels []*panelRecord, viewer *pageViewer, authored bool) pageWire {
	out := pageWire{
		ID:          rec.ID,
		Slug:        rec.Slug,
		Name:        rec.Name,
		Description: rec.Description,
		Owner:       h.ownerRef(ctx, rec),
		Panels:      make([]any, 0, len(panels)),
		CreatedAt:   rec.CreatedAt,
		UpdatedAt:   rec.UpdatedAt,
	}
	for _, p := range panels {
		if viewer != nil && !h.canSeePanel(viewer, p) {
			out.Panels = append(out.Panels, pageSealedPanelWire{
				PanelID:       p.PanelID,
				Span:          p.Span,
				Sealed:        true,
				OwnerCrewName: p.OwnerCrewName,
				Tab:           p.Tab,
			})
			continue
		}
		wire := h.panelWire(p)
		if authored {
			h.attachAuthoredHalf(ctx, rec.ID, p.PanelID, &wire)
		}
		out.Panels = append(out.Panels, wire)
	}
	return out
}

// attachAuthoredHalf copies a panel's declared actions, wake gates and
// on_failure onto the wire.
//
// It reads through storedPanelSpec, which is the one reader of spec_json, so
// "what the author saved" still has a single implementation and this cannot
// drift from what a click is resolved against.
//
// A read failure is not fatal and deliberately so: the panel's DATA is already
// on the wire and correct, and refusing the whole page because its authored
// half could not be re-read would turn a lossy document into no document.
// The editor's own banner covers the remaining case.
func (h *PageHandler) attachAuthoredHalf(ctx context.Context, pageID, panelID string, wire *pagePanelWire) {
	spec, err := h.storedPanelSpec(ctx, pageID, panelID)
	if err != nil || spec == nil {
		if err != nil && h.logger != nil {
			h.logger.Warn("pages: authored half not echoed", "panel", panelID, "error", err)
		}
		return
	}
	wire.Actions = spec.Actions
	wire.Wake = spec.Wake
	wire.OnFailure = spec.OnFailure
}

// ownerRef renders the page's owner as `user/<id>` or `crew/<slug>` — exactly
// one of the two exists (§10's XOR CHECK).
func (h *PageHandler) ownerRef(ctx context.Context, rec *pageRecord) string {
	if rec.OwnerCrewID != "" {
		var slug string
		if err := h.db.QueryRowContext(ctx, `SELECT slug FROM crews WHERE id = ?`, rec.OwnerCrewID).Scan(&slug); err == nil {
			return "crew/" + slug
		}
		return "crew/" + rec.OwnerCrewID
	}
	return "user/" + rec.OwnerUserID
}

// ── Request decoding and validation ────────────────────────────────────────

// decodeWrite reads a create/update body under the §10b.3 spec cap.
//
// The cap is enforced HERE and never as a DB CHECK (§10): a CHECK cannot
// produce the rejection envelope the API owes the caller, and cannot be raised
// without a migration.
func (h *PageHandler) decodeWrite(w http.ResponseWriter, r *http.Request) (*pageWriteRequest, bool) {
	body, ok := readCapped(w, r, pages.MaxSpecBytes, "page spec")
	if !ok {
		return nil, false
	}
	var req pageWriteRequest
	if err := json.Unmarshal(body, &req); err != nil {
		replyError(w, http.StatusBadRequest, "invalid JSON body")
		return nil, false
	}
	// `spec: {panels: [...]}` is accepted as well as a flat `panels`, because
	// an agent that has a pages.Document in hand should not have to flatten it.
	if len(req.Panels) == 0 && req.Spec != nil {
		var inner struct {
			Panels []pagePanelWire `json:"panels"`
		}
		if err := json.Unmarshal(*req.Spec, &inner); err == nil {
			req.Panels = inner.Panels
		}
	}
	return &req, true
}

// documentFrom turns a request into a validated pages.Document.
func (h *PageHandler) documentFrom(w http.ResponseWriter, req *pageWriteRequest, slug string) (*pages.Document, bool) {
	doc := &pages.Document{
		APIVersion: pages.DocumentAPIVersion,
		Kind:       pages.DocumentKind,
	}
	if req.Name != nil {
		doc.Metadata.Name = *req.Name
	}
	if req.Description != nil {
		doc.Metadata.Description = *req.Description
	}
	doc.Metadata.Slug = slug
	if req.Slug != nil && *req.Slug != "" {
		doc.Metadata.Slug = *req.Slug
	}
	panelSpecs, ok := panelSpecsFrom(w, req.Panels)
	if !ok {
		return nil, false
	}
	doc.Spec.Panels = panelSpecs
	if err := doc.Validate(); err != nil {
		writeSpecError(w, err)
		return nil, false
	}
	return doc, true
}

// panelSpecsFrom converts the wire panels into spec panels.
//
// SLA crosses the wire as `sla_seconds` (§11b decision 3). A string form is
// accepted from a hand-written client, but the integer is canonical and is
// what the database stores.
func panelSpecsFrom(w http.ResponseWriter, in []pagePanelWire) ([]pages.PanelSpec, bool) {
	out := make([]pages.PanelSpec, 0, len(in))
	for i := range in {
		p := &in[i]
		if p.SLASeconds < 0 {
			replyError(w, http.StatusBadRequest,
				fmt.Sprintf("panel %q declares a negative sla_seconds", p.ID))
			return nil, false
		}
		out = append(out, pages.PanelSpec{
			ID:     p.ID,
			Schema: pages.PanelSchema(p.Schema),
			Title:  p.Title,
			// Untrusted, and refused by name in Validate below: an icon the
			// client cannot draw must never reach spec_json, because from
			// there it reaches a header that renders blank.
			Icon: pages.PanelIcon(p.Icon),
			// Untrusted in the same way, and normalised and refused by
			// validatePageTabs: a blank or unreadable tab name would draw
			// nothing on the bar while still hiding the panels under it.
			Tab:       p.Tab,
			Owner:     p.Owner,
			Producer:  p.Producer,
			SLA:       fmt.Sprintf("%ds", p.SLASeconds),
			Span:      p.Span,
			Public:    p.Public,
			Actions:   p.Actions,
			Wake:      p.Wake,
			OnFailure: p.OnFailure,
		})
	}
	return out, true
}

// currentDocument reads the stored spec so a PATCH can be applied to it.
func (h *PageHandler) currentDocument(w http.ResponseWriter, rec *pageRecord) (*pages.Document, bool) {
	var specJSON string
	if err := h.db.QueryRow(`SELECT spec_json FROM pages WHERE id = ?`, rec.ID).Scan(&specJSON); err != nil {
		replyInternalError(w, h.logger, "read stored page spec", err)
		return nil, false
	}
	var doc pages.Document
	if err := json.Unmarshal([]byte(specJSON), &doc); err != nil {
		replyInternalError(w, h.logger, "decode stored page spec", err)
		return nil, false
	}
	doc.APIVersion = pages.DocumentAPIVersion
	doc.Kind = pages.DocumentKind
	doc.Metadata.Slug = rec.Slug
	return &doc, true
}

// writeSpecError maps a pages.ValidationError onto HTTP.
//
// A spec over the cap is the 422 rejection envelope; everything else is a 400
// naming the rule that was broken, because a producer reading this is fixing
// its YAML, not retrying.
func writeSpecError(w http.ResponseWriter, err error) {
	var ve *pages.ValidationError
	if errors.As(err, &ve) {
		if ve.Code == pages.CodeTooLarge {
			writeRejection(w, pageRejection{
				Kind:    "cap",
				Message: ve.Detail,
				Detail:  map[string]any{"bytes_limit": pages.MaxSpecBytes},
			})
			return
		}
		replyError(w, http.StatusBadRequest, ve.Detail)
		return
	}
	replyError(w, http.StatusBadRequest, err.Error())
}

// ── Reference resolution (§10b.1) ──────────────────────────────────────────

// resolvedPanel is what a panel's declared references resolved to.
type resolvedPanel struct {
	OwnerCrewID string
	Kind        string
	Ref         string
}

// resolveReferences is the second half of the authoring gate: every declared
// owner and producer must EXIST. Cheap, synchronous, no render run.
//
// A `script` or `webhook` producer resolves to nothing by design — there is no
// table of scripts, and inventing one would be the datasource a page is not
// allowed to have. Their authority is checked at push time instead
// (pages_data.go).
func (h *PageHandler) resolveReferences(w http.ResponseWriter, r *http.Request, wsID string, doc *pages.Document) (map[string]resolvedPanel, bool) {
	out := make(map[string]resolvedPanel, len(doc.Spec.Panels))
	for i := range doc.Spec.Panels {
		p := &doc.Spec.Panels[i]
		crewSlug, err := p.OwnerCrewSlug()
		if err != nil {
			replyError(w, http.StatusBadRequest, fmt.Sprintf("panel %q: %v", p.ID, err))
			return nil, false
		}
		var crewID string
		err = h.db.QueryRowContext(r.Context(),
			`SELECT id FROM crews WHERE workspace_id = ? AND slug = ? AND deleted_at IS NULL`, wsID, crewSlug).Scan(&crewID)
		if errors.Is(err, sql.ErrNoRows) {
			replyError(w, http.StatusBadRequest, fmt.Sprintf(
				"panel %q is owned by crew/%s, which does not exist in this workspace — "+
					"the owner is the panel's ACL, so it cannot be a name nobody answers to", p.ID, crewSlug))
			return nil, false
		}
		if err != nil {
			replyInternalError(w, h.logger, "resolve panel owner crew", err)
			return nil, false
		}

		kind, ref, err := p.ProducerParts()
		if err != nil {
			replyError(w, http.StatusBadRequest, fmt.Sprintf("panel %q: %v", p.ID, err))
			return nil, false
		}
		switch kind {
		case pages.ProducerRoutine:
			var one int
			err := h.db.QueryRowContext(r.Context(),
				`SELECT 1 FROM pipelines WHERE workspace_id = ? AND slug = ? AND deleted_at IS NULL`, wsID, ref).Scan(&one)
			if errors.Is(err, sql.ErrNoRows) {
				replyError(w, http.StatusBadRequest, fmt.Sprintf(
					"panel %q names routine/%s as its producer, and no such routine exists here", p.ID, ref))
				return nil, false
			}
			if err != nil {
				replyInternalError(w, h.logger, "resolve panel producer routine", err)
				return nil, false
			}
		case pages.ProducerAgent:
			var one int
			err := h.db.QueryRowContext(r.Context(),
				`SELECT 1 FROM agents WHERE workspace_id = ? AND slug = ? AND deleted_at IS NULL`, wsID, ref).Scan(&one)
			if errors.Is(err, sql.ErrNoRows) {
				replyError(w, http.StatusBadRequest, fmt.Sprintf(
					"panel %q names agent/%s as its producer, and no such agent exists here", p.ID, ref))
				return nil, false
			}
			if err != nil {
				replyInternalError(w, h.logger, "resolve panel producer agent", err)
				return nil, false
			}
		}
		out[p.ID] = resolvedPanel{OwnerCrewID: crewID, Kind: string(kind), Ref: ref}
	}
	// The same gate applied to the routines a `call` action names — see
	// resolveActionRoutines in pages_actions.go for why a button that resolves
	// only at click time is the worse half of this failure.
	if !h.resolveActionRoutines(w, r, wsID, doc) {
		return nil, false
	}
	return out, true
}

// ── Writing panels ─────────────────────────────────────────────────────────

func insertPanel(ctx context.Context, tx *sql.Tx, pageID string, spec *pages.PanelSpec, res resolvedPanel, now string) error {
	sla, err := spec.SLADuration()
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO page_panels (id, page_id, panel_id, schema, title, owner_crew_id,
		                         producer_kind, producer_ref, sla_seconds, span, config_json,
		                         created_at, updated_at)
		VALUES (?, ?, ?, ?, NULLIF(?, ''), ?, ?, ?, ?, ?, '{}', ?, ?)`,
		generateCUID(), pageID, spec.ID, string(spec.Schema), spec.Title, res.OwnerCrewID,
		res.Kind, res.Ref, int(sla.Seconds()), spec.Span, now, now)
	return err
}

// reconcilePanels brings page_panels in line with the new spec: update what
// survived, insert what is new, delete what the spec dropped.
//
// A surviving panel is UPDATED rather than replaced so its payload ring
// survives the edit (page_panel_data cascades from page_panels).
func reconcilePanels(ctx context.Context, tx *sql.Tx, pageID string, doc *pages.Document, resolved map[string]resolvedPanel, now string) error {
	rows, err := tx.QueryContext(ctx, `SELECT panel_id FROM page_panels WHERE page_id = ?`, pageID)
	if err != nil {
		return err
	}
	existing := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		existing[id] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	wanted := map[string]bool{}
	for i := range doc.Spec.Panels {
		spec := &doc.Spec.Panels[i]
		wanted[spec.ID] = true
		res := resolved[spec.ID]
		sla, err := spec.SLADuration()
		if err != nil {
			return err
		}
		if existing[spec.ID] {
			if _, err := tx.ExecContext(ctx, `
				UPDATE page_panels
				   SET schema = ?, title = NULLIF(?, ''), owner_crew_id = ?,
				       producer_kind = ?, producer_ref = ?, sla_seconds = ?, span = ?, updated_at = ?
				 WHERE page_id = ? AND panel_id = ?`,
				string(spec.Schema), spec.Title, res.OwnerCrewID, res.Kind, res.Ref,
				int(sla.Seconds()), spec.Span, now, pageID, spec.ID); err != nil {
				return err
			}
			continue
		}
		if err := insertPanel(ctx, tx, pageID, spec, res, now); err != nil {
			return err
		}
	}
	for id := range existing {
		if wanted[id] {
			continue
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM page_panels WHERE page_id = ? AND panel_id = ?`, pageID, id); err != nil {
			return err
		}
	}
	return nil
}

func insertPageVersion(ctx context.Context, tx *sql.Tx, pageID string, seq int64, specJSON, userID, now string) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO page_versions (page_id, seq, spec_json, author_user_id, created_at)
		VALUES (?, ?, ?, ?, ?)`, pageID, seq, specJSON, userID, now)
	return err
}
