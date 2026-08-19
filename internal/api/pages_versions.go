package api

// Pages — versions and rollback (docs/prd/pages.md §10b.1).
//
// "Several agents may rewrite one page, and the one who breaks it is rarely
// the one who notices — so `crewship page rollback <slug> --to <seq>` is not a
// nicety." Every save is already a version (pages_handler.go writes one on
// create and on update, and trims to pages.MaxVersionsPerPage); this file is
// the half that reads them back.
//
// ── THE RULE THAT SHAPES THIS FILE ─────────────────────────────────────────
//
// ROLLBACK RESTORES STRUCTURE, NEVER NUMBERS. §10b.1, and it is not a
// preference:
//
//	"A panel brought back by a rollback renders dimmed, in a 'waiting for
//	 first data' state, even if rows for it survive in the ring. Old payloads
//	 are never resurrected and shown as current — that is precisely the lie §4
//	 exists to prevent, and a rollback is exactly when someone is most likely
//	 to believe what they see."
//
// So a rollback restores the SPEC and, for every panel it brings back or
// redefines, clears that panel's ring so the server's own verdict is
// `never_produced` — the fourth state, which §11b decision 8 makes the
// SERVER's to send. The panel is dimmed because it genuinely has no data, not
// because the renderer was asked to pretend.
//
// Which panels count as "brought back or redefined" is decided against the
// live rows, before reconciliation:
//
//   - ABSENT from the live page — the panel is literally brought back. (Its
//     ring is usually gone anyway: page_panel_data cascades from page_panels.
//     The delete is still issued, because an invariant that holds only as long
//     as a cascade elsewhere keeps holding is not an invariant.)
//   - SCHEMA changed — the surviving payloads are shaped for a different
//     schema. Rendering a table payload in a metric panel is at best a blank
//     panel and at worst a false number.
//   - PRODUCER changed — the panel footer would credit the restored producer
//     for a payload a different one pushed (§4 rule 5).
//   - OWNER CREW changed — the payload was produced for a different crew's
//     eyes, and the restored owner is a different ACL (§7.1 rule 2).
//
// A panel the rollback does not touch keeps its data, and that is the honest
// answer for it: its payload was produced under exactly the definition the
// rollback restored, and blanking it would be destroying data the rollback had
// no quarrel with.
//
// Rollback is itself a SAVE and appends a new version rather than truncating
// the history back to the target. Forward history is what lets somebody roll
// back a rollback, and a version log that rewrites itself is not a log.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/pages"
)

// pageVersionWire is one row of the version log.
//
// Author is rendered as a reference ("user/<id>", "agent/<slug>") rather than
// as a raw column, because the two arcs are both nullable — a version whose
// author was erased is still a version worth keeping (the migration says so),
// and a client reading two empty columns cannot tell that from a bug.
type pageVersionWire struct {
	Seq         int64  `json:"seq"`
	CreatedAt   string `json:"created_at"`
	Author      string `json:"author,omitempty"`
	AuthorLabel string `json:"author_label,omitempty"`
	Name        string `json:"name,omitempty"`
	PanelCount  int    `json:"panel_count"`
	Current     bool   `json:"current"`
}

// ListVersions returns the retained version history, newest first.
//
// GET /api/v1/pages/{slug}/versions
//
// Gated like Export and for the same reason: a version row carries the page's
// whole arrangement, including panels this caller would receive sealed on the
// ordinary read path (§7.1 rule 2). Owner, workspace admin, or a `write`
// grantee — the principals who can already see the entire spec.
func (h *PageHandler) ListVersions(w http.ResponseWriter, r *http.Request) {
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
		replyInternalError(w, h.logger, "load page for versions", err)
		return
	}
	if !h.mayEditSpec(r.Context(), wsID, user.ID, RoleFromContext(r.Context()), rec) {
		replyError(w, http.StatusForbidden,
			"a version carries the page's whole arrangement, including panels sealed to you; "+
				"only the page owner, a workspace admin, or a write grantee may read the history")
		return
	}

	rows, err := h.db.QueryContext(r.Context(), `
		SELECT v.seq, v.spec_json, v.created_at,
		       COALESCE(v.author_user_id, ''), COALESCE(u.email, ''),
		       COALESCE(v.author_agent_id, ''), COALESCE(a.slug, '')
		FROM page_versions v
		LEFT JOIN users u  ON u.id = v.author_user_id
		LEFT JOIN agents a ON a.id = v.author_agent_id
		WHERE v.page_id = ?
		ORDER BY v.seq DESC`, rec.ID)
	if err != nil {
		replyInternalError(w, h.logger, "list page versions", err)
		return
	}
	defer rows.Close()

	out := make([]pageVersionWire, 0, pages.MaxVersionsPerPage)
	for rows.Next() {
		var (
			seq                                   int64
			specJSON, createdAt                   string
			userID, userEmail, agentID, agentSlug string
		)
		if err := rows.Scan(&seq, &specJSON, &createdAt, &userID, &userEmail, &agentID, &agentSlug); err != nil {
			replyInternalError(w, h.logger, "scan page version", err)
			return
		}
		row := pageVersionWire{Seq: seq, CreatedAt: createdAt}
		var doc pages.Document
		if json.Unmarshal([]byte(specJSON), &doc) == nil {
			row.Name = doc.Metadata.Name
			row.PanelCount = len(doc.Spec.Panels)
		}
		switch {
		case agentID != "":
			row.Author = "agent/" + pageFirstNonEmpty(agentSlug, agentID)
			row.AuthorLabel = pageFirstNonEmpty(agentSlug, agentID)
		case userID != "":
			row.Author = "user/" + userID
			row.AuthorLabel = pageFirstNonEmpty(userEmail, userID)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		replyInternalError(w, h.logger, "rows iteration (page versions)", err)
		return
	}
	if len(out) > 0 {
		out[0].Current = true
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"page":     rec.Slug,
		"retained": pages.MaxVersionsPerPage,
		"versions": out,
	})
}

func pageFirstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// pageRollbackRequest names the version to restore.
type pageRollbackRequest struct {
	To *int64 `json:"to"`
}

// livePanelShape is one live panel row, as much of it as the dimming decision
// needs.
type livePanelShape struct {
	Schema       string
	OwnerCrewID  string
	ProducerKind string
	ProducerRef  string
}

// Rollback restores a retained version's spec.
//
// POST /api/v1/pages/{slug}/rollback  {"to": <seq>}
func (h *PageHandler) Rollback(w http.ResponseWriter, r *http.Request) {
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
		replyInternalError(w, h.logger, "load page for rollback", err)
		return
	}
	// A rollback is an edit of the arrangement, so it is the `write` verb —
	// the same gate PATCH runs (§7.1b rule 2).
	if !h.mayEditSpec(r.Context(), wsID, user.ID, RoleFromContext(r.Context()), rec) {
		replyError(w, http.StatusForbidden,
			"only the page owner, a workspace admin, or a write grantee may roll this page back")
		return
	}

	body, ok := readCapped(w, r, 4<<10, "rollback request")
	if !ok {
		return
	}
	var req pageRollbackRequest
	if len(strings.TrimSpace(string(body))) > 0 {
		if err := json.Unmarshal(body, &req); err != nil {
			replyError(w, http.StatusBadRequest, "invalid JSON body")
			return
		}
	}
	if req.To == nil {
		replyError(w, http.StatusBadRequest,
			"to is required: rollback names the version to restore — see crewship page versions "+slug)
		return
	}
	target := *req.To

	var specJSON string
	err = h.db.QueryRowContext(r.Context(),
		`SELECT spec_json FROM page_versions WHERE page_id = ? AND seq = ?`, rec.ID, target).Scan(&specJSON)
	if errors.Is(err, sql.ErrNoRows) {
		// Say why it is missing. §10b.3 retains 50 versions, so "there is no
		// version 3" and "version 3 has aged out" are different sentences and
		// the operator needs the second one.
		var lowest, highest int64
		_ = h.db.QueryRowContext(r.Context(),
			`SELECT COALESCE(MIN(seq), 0), COALESCE(MAX(seq), 0) FROM page_versions WHERE page_id = ?`,
			rec.ID).Scan(&lowest, &highest)
		if target > 0 && target < lowest {
			replyError(w, http.StatusNotFound, fmt.Sprintf(
				"version %d of %q is no longer retained; the last %d versions are kept and the oldest is %d",
				target, rec.Slug, pages.MaxVersionsPerPage, lowest))
			return
		}
		replyError(w, http.StatusNotFound, fmt.Sprintf(
			"page %q has no version %d; the retained range is %d..%d", rec.Slug, target, lowest, highest))
		return
	}
	if err != nil {
		replyInternalError(w, h.logger, "load page version", err)
		return
	}

	var doc pages.Document
	if err := json.Unmarshal([]byte(specJSON), &doc); err != nil {
		replyInternalError(w, h.logger, "decode stored page version", err)
		return
	}
	doc.APIVersion = pages.DocumentAPIVersion
	doc.Kind = pages.DocumentKind
	// The slug is the page's address and a rollback never moves it — the same
	// rule PATCH enforces, for the same reason: every producer script pushes to
	// this slug.
	doc.Metadata.Slug = rec.Slug
	if err := doc.Validate(); err != nil {
		// A retained spec that no longer validates is possible: the schema set
		// is closed and can lose a value between the save and the rollback.
		// Refuse rather than write it — and say which rule, so the operator can
		// edit the old spec by hand.
		writeSpecError(w, err)
		return
	}
	// The stored version resolved when it was saved; the workspace has moved
	// since. resolveReferences is the authoring gate and this IS authoring —
	// unlike an import, which §10b.1 puts outside it — so a rollback to a spec
	// naming a routine that has since been deleted is refused, naming the
	// panel and the routine, rather than restoring a grid of dead panels.
	resolved, ok := h.resolveReferences(w, r, wsID, &doc)
	if !ok {
		return
	}
	// The sensor half of the same gate. A rollback is a SAVE (§10b.1 says so in
	// as many words, and this handler appends a version rather than truncating
	// one), so it owes the same reconcile every other save does: the compiled
	// `automations` rows are DERIVED from the spec, and a rollback that restored
	// the spec without them would leave the page wired to the gates and refreshes
	// of a version nobody can see any more. `refresh:` made that visible —
	// restoring a spec that declares one produced no rule at all — but the same
	// was already true of `wake:`.
	gates, ok := h.resolveGates(w, r, wsID, &doc)
	if !ok {
		return
	}

	// The arrangement being replaced, read before anything is written.
	current, ok := h.currentDocument(w, rec)
	if !ok {
		return
	}
	beforeArrangement := pageArrangementFingerprint(current)

	live, err := h.livePanelShapes(r.Context(), rec.ID)
	if err != nil {
		replyInternalError(w, h.logger, "read live page panels", err)
		return
	}
	dim := panelsToDim(&doc, resolved, live)

	restored, err := json.Marshal(&doc)
	if err != nil {
		replyInternalError(w, h.logger, "marshal restored page spec", err)
		return
	}
	now := h.evaluator().Now().UTC().Format(time.RFC3339)

	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		replyInternalError(w, h.logger, "begin page rollback", err)
		return
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(r.Context(),
		`UPDATE pages SET name = ?, description = NULLIF(?, ''), spec_json = ?, updated_at = ? WHERE id = ?`,
		doc.Metadata.Name, doc.Metadata.Description, string(restored), now, rec.ID); err != nil {
		replyInternalError(w, h.logger, "update page for rollback", err)
		return
	}
	if err := reconcilePanels(r.Context(), tx, rec.ID, &doc, resolved, now); err != nil {
		replyInternalError(w, h.logger, "reconcile page panels for rollback", err)
		return
	}
	// §10b.1's rule, in one statement: the restored panels have no numbers.
	if err := clearPanelRings(r.Context(), tx, rec.ID, dim); err != nil {
		replyInternalError(w, h.logger, "clear restored panel rings", err)
		return
	}
	if err := reconcileWakeAutomations(r.Context(), tx, wsID, rec.ID, rec.Slug, gates, user.ID, now); err != nil {
		replyInternalError(w, h.logger, "compile page rules for rollback", err)
		return
	}

	var seq int64
	if err := tx.QueryRowContext(r.Context(),
		`SELECT COALESCE(MAX(seq), 0) + 1 FROM page_versions WHERE page_id = ?`, rec.ID).Scan(&seq); err != nil {
		replyInternalError(w, h.logger, "next page version", err)
		return
	}
	if err := insertPageVersion(r.Context(), tx, rec.ID, seq, string(restored), user.ID, now); err != nil {
		replyInternalError(w, h.logger, "insert rollback version", err)
		return
	}
	if err := trimPageVersions(r.Context(), tx, rec.ID, seq); err != nil {
		replyInternalError(w, h.logger, "trim page versions", err)
		return
	}
	if err := tx.Commit(); err != nil {
		replyInternalError(w, h.logger, "commit page rollback", err)
		return
	}

	updated, err := h.loadPage(r.Context(), wsID, rec.Slug)
	if err != nil {
		replyInternalError(w, h.logger, "reload rolled-back page", err)
		return
	}
	panels, err := h.loadPanels(r.Context(), wsID, updated.ID)
	if err != nil {
		replyInternalError(w, h.logger, "reload rolled-back panels", err)
		return
	}
	h.refreshAutomations(r.Context())
	// A rollback that restores a different panel list IS an arrangement change,
	// and the fingerprint decides: rolling back to a version whose panels are
	// identical (a title was edited and edited back) emits nothing.
	if after := pageArrangementFingerprint(&doc); after != beforeArrangement {
		h.emitPageSpecChanged(r.Context(), wsID, updated, &doc, false, after)
	}
	broadcastWorkspaceEvent(h.hub, wsID, "page.updated",
		map[string]any{"page_id": updated.ID, "slug": updated.Slug})

	writeJSON(w, http.StatusOK, map[string]any{
		"page":           h.pageDocument(r.Context(), updated, panels, nil),
		"rolled_back_to": target,
		"version":        seq,
		// The panels a viewer is about to find dimmed, named — so the operator
		// who ran the rollback learns it from the response rather than from a
		// blank panel five minutes later.
		"awaiting_data": dim,
	})
}

// livePanelShapes reads the page's panels as they stand right now.
func (h *PageHandler) livePanelShapes(ctx context.Context, pageID string) (map[string]livePanelShape, error) {
	rows, err := h.db.QueryContext(ctx, `
		SELECT panel_id, schema, owner_crew_id, producer_kind, producer_ref
		FROM page_panels WHERE page_id = ?`, pageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]livePanelShape{}
	for rows.Next() {
		var id string
		var s livePanelShape
		if err := rows.Scan(&id, &s.Schema, &s.OwnerCrewID, &s.ProducerKind, &s.ProducerRef); err != nil {
			return nil, err
		}
		out[id] = s
	}
	return out, rows.Err()
}

// panelsToDim decides which panels the rollback brings back or redefines. See
// the file header for why each case is on the list.
func panelsToDim(doc *pages.Document, resolved map[string]resolvedPanel, live map[string]livePanelShape) []string {
	out := make([]string, 0)
	for i := range doc.Spec.Panels {
		p := &doc.Spec.Panels[i]
		res := resolved[p.ID]
		was, ok := live[p.ID]
		switch {
		case !ok,
			was.Schema != string(p.Schema),
			was.ProducerKind != res.Kind || was.ProducerRef != res.Ref,
			was.OwnerCrewID != res.OwnerCrewID:
			out = append(out, p.ID)
		}
	}
	return out
}

// clearPanelRings deletes the payload ring of the named panels.
//
// Keyed through page_panels rather than by the author-chosen panel id, because
// page_panel_data.panel_id is the ROW id — the same distinction that lets a
// panel keep its ring across an ordinary edit.
func clearPanelRings(ctx context.Context, tx *sql.Tx, pageID string, panelIDs []string) error {
	if len(panelIDs) == 0 {
		return nil
	}
	args := make([]any, 0, len(panelIDs)+1)
	args = append(args, pageID)
	for _, id := range panelIDs {
		args = append(args, id)
	}
	_, err := tx.ExecContext(ctx, `
		DELETE FROM page_panel_data
		WHERE panel_id IN (
			SELECT id FROM page_panels
			WHERE page_id = ? AND panel_id IN (`+sqlPlaceholders(len(panelIDs))+`)
		)`, args...)
	return err
}

// trimPageVersions enforces §10b.3's "versions per page: 50".
//
// pages.MaxVersionsPerPage is a contract number and a Go constant, not a
// tunable: a page whose history depth varies per deployment is a page whose
// `rollback --to` means something different on every install.
func trimPageVersions(ctx context.Context, tx *sql.Tx, pageID string, newest int64) error {
	_, err := tx.ExecContext(ctx, `
		DELETE FROM page_versions
		WHERE page_id = ? AND seq <= ?`, pageID, newest-int64(pages.MaxVersionsPerPage))
	return err
}
