package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/crewship-ai/crewship/internal/journal"
)

// #5260, R71 §0.14/1: naming `agent/<slug>` as a panel's producer lets that
// agent push the panel's data, which is Edit of the artifact's data, so only
// the page's owner (pageOwnerForDelegation) designates one. page_panels keeps
// who designated each producer; migration 20261109152536 retires an agent
// designation the owner did not make, for good and with an audit entry, and
// the loader reports it as the panel's fault. The handlers below refuse it
// before it is stored, where they can say so.

const pageAgentProducerOwnerOnlyRefusal = "only the artifact's owner can name an agent as a panel's producer (%s); " +
	"keep the panel's current producer, or ask the owner to name the agent or grant it produce"

// pageAgentProducerRetiredFault is the visible reason on a panel whose agent
// producer designation has ended: the state in plain words first, then who
// can fix it and how. The owner's way out is AuthorizePanelProducer.
const pageAgentProducerRetiredFault = "Producer not authorized by the owner: agent %q may not push this panel's data, " +
	"because the page's current owner did not name it as the producer (or whoever named it no longer owns the page). " +
	"Only the page's owner can authorize it: Authorize producer on this panel, or `crewship page authorize-producer <page> <panel>`."

const pageAgentProducerPrivateRefusal = "private artifact audience delegates nothing to agents; " +
	"an agent producer on a private artifact stays unauthorized"

const pageAgentProducerAuthorizeRefusal = "only the artifact's owner can authorize an agent as a panel's producer (R71 §0.14/1); " +
	"ask the page's owner to authorize it"

// producerDesignator is the human a request designates producers for: the
// authenticated person, or nobody when an agent makes the request — an agent
// is never the owner, whatever human it acts for.
func producerDesignator(r *http.Request, userID string) string {
	if agent, _ := pageGrantCallerIsAgent(r); agent {
		return ""
	}
	return userID
}

// retiredAgentProducers maps each of this page's panels whose agent producer
// designation is retired to that producer and its retirement stamp, read
// inside the write's transaction.
func retiredAgentProducers(ctx context.Context, tx *sql.Tx, pageID string) (map[string]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT panel_id, producer_ref || '@' || producer_retired_at FROM page_panels WHERE page_id=? AND producer_kind='agent' AND producer_retired_at IS NOT NULL`, pageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, state string
		if err := rows.Scan(&id, &state); err != nil {
			return nil, err
		}
		out[id] = state
	}
	return out, rows.Err()
}

// refuseUndelegatedAgentProducers answers 403 when this write designated an
// agent producer the owner did not: the triggers retired it as it landed, so
// any agent designation retired now that was not retired before the write, or
// was retired under another producer or at another moment (re-pointed, then
// retired again), is this request's. It reports whether it replied; the
// caller's deferred rollback discards the write.
func (h *PageHandler) refuseUndelegatedAgentProducers(w http.ResponseWriter, r *http.Request, tx *sql.Tx, pageID string, before map[string]string) bool {
	after, err := retiredAgentProducers(r.Context(), tx, pageID)
	if err != nil {
		replyInternalError(w, h.logger, "read agent producer designations", err)
		return true
	}
	var fresh []string
	for id, state := range after {
		if before[id] != state {
			fresh = append(fresh, id)
		}
	}
	if len(fresh) == 0 {
		return false
	}
	sort.Strings(fresh)
	replyError(w, http.StatusForbidden, fmt.Sprintf(pageAgentProducerOwnerOnlyRefusal, "panel "+strings.Join(fresh, ", ")))
	return true
}

// agentProducerDelegatedSQL is the use-time half, against page_panels pp and
// pages p: true unless the panel's producer is an agent whose designation is
// retired or was not made by the page's owner now. The triggers make the
// first permanent; the second covers the instant before.
const agentProducerDelegatedSQL = `(pp.producer_kind <> 'agent' OR (pp.producer_retired_at IS NULL AND (
		(COALESCE(p.owner_user_id, '') <> '' AND p.owner_user_id = pp.producer_designated_by_user_id)
	 OR (COALESCE(p.owner_user_id, '') = '' AND COALESCE(p.owner_crew_id, '') <> ''
	     AND EXISTS (SELECT 1 FROM crew_members ocm JOIN crews oc ON oc.id = ocm.crew_id AND oc.deleted_at IS NULL AND oc.workspace_id = p.workspace_id
	                 WHERE ocm.crew_id = p.owner_crew_id AND ocm.user_id = pp.producer_designated_by_user_id)))))`

// pageProducerAuthorizationWire rides on a panel whose agent producer the
// owner has not authorized: which producer, and whether THIS viewer is the
// one who can authorize it. It is a hint for the button; the endpoint
// decides again.
type pageProducerAuthorizationWire struct {
	Producer     string `json:"producer"`
	CanAuthorize bool   `json:"can_authorize"`
}

// AuthorizePanelProducer handles
// POST /api/v1/pages/{slug}/panels/{panelId}/producer/authorize: the page's
// owner, a human, explicitly names the panel's current agent producer as
// theirs. It is a new designation by the owner, so it is the owner's for as
// long as they own the page (the triggers end it with their ownership like
// any other), and it is audited. It never widens anything else: the producer
// stays the one the spec declares, and every other caller is refused.
func (h *PageHandler) AuthorizePanelProducer(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	if user == nil {
		replyError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if agent, _ := pageGrantCallerIsAgent(r); agent {
		replyError(w, http.StatusForbidden, pageAgentProducerAuthorizeRefusal+"; an agent is never the owner")
		return
	}
	wsID := WorkspaceIDFromContext(r.Context())
	slug := r.PathValue("slug")
	panelID := strings.TrimSpace(r.PathValue("panelId"))

	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		replyInternalError(w, h.logger, "begin producer authorization", err)
		return
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(r.Context(), `UPDATE workspaces SET updated_at=updated_at WHERE id=?`, wsID); err != nil {
		replyInternalError(w, h.logger, "fence producer authorization", err)
		return
	}
	r = r.WithContext(context.WithValue(r.Context(), pageAuthReaderKey{}, tx))
	rec, err := h.loadPage(r.Context(), wsID, slug)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			replyError(w, http.StatusNotFound, fmt.Sprintf("page %q not found", slug))
			return
		}
		replyInternalError(w, h.logger, "load page for producer authorization", err)
		return
	}
	if !h.pageOwnerForDelegation(r.Context(), wsID, user.ID, rec) {
		// A caller the page is not reachable for gets the 404 an unknown
		// slug gets, so the endpoint cannot be walked to find pages.
		reachable, err := h.producerAuthorizationReachable(r.Context(), wsID, user.ID, rec)
		if err != nil {
			replyInternalError(w, h.logger, "resolve page reach for producer authorization", err)
			return
		}
		if !reachable {
			replyError(w, http.StatusNotFound, fmt.Sprintf("page %q not found", slug))
			return
		}
		replyError(w, http.StatusForbidden, pageAgentProducerAuthorizeRefusal)
		return
	}
	// A private artifact delegates nothing to agents (mayDelegateAgentEdit);
	// authorizing a producer would hand an agent standing Edit of its data.
	if rec.PrivateAudience {
		replyError(w, http.StatusConflict, pageAgentProducerPrivateRefusal)
		return
	}

	var kind, ref, designator string
	var wasRetired, delegated bool
	err = tx.QueryRowContext(r.Context(), `SELECT pp.producer_kind, pp.producer_ref, COALESCE(pp.producer_designated_by_user_id, ''),
		pp.producer_retired_at IS NOT NULL, `+agentProducerDelegatedSQL+`
		FROM page_panels pp JOIN pages p ON p.id = pp.page_id WHERE pp.page_id = ? AND pp.panel_id = ?`,
		rec.ID, panelID).Scan(&kind, &ref, &designator, &wasRetired, &delegated)
	if errors.Is(err, sql.ErrNoRows) {
		replyError(w, http.StatusNotFound, fmt.Sprintf("page %q has no panel %q", slug, panelID))
		return
	}
	if err != nil {
		replyInternalError(w, h.logger, "read panel producer", err)
		return
	}
	if kind != "agent" {
		replyError(w, http.StatusConflict, fmt.Sprintf(
			"panel %q is produced by %s/%s, not by an agent; only an agent producer needs the owner's authorization", panelID, kind, ref))
		return
	}
	out := map[string]any{"page": rec.Slug, "panel": panelID, "producer": "agent/" + ref, "authorized": true, "changed": false}
	// Already live under the page's owner (this person, or another member of
	// the owning crew): nothing to do, and nothing is re-attributed.
	if delegated {
		writeJSON(w, http.StatusOK, out)
		return
	}
	if _, err := tx.ExecContext(r.Context(), `UPDATE page_panels SET producer_designated_by_user_id = ?, producer_retired_at = NULL
		WHERE page_id = ? AND panel_id = ?`, user.ID, rec.ID, panelID); err != nil {
		replyInternalError(w, h.logger, "authorize panel producer", err)
		return
	}
	var retiredNow bool
	// The insert/update triggers retire a designation the owner did not make;
	// read it back so a race with an ownership change cannot answer 200.
	if err := tx.QueryRowContext(r.Context(), `SELECT producer_retired_at IS NOT NULL FROM page_panels WHERE page_id = ? AND panel_id = ?`,
		rec.ID, panelID).Scan(&retiredNow); err != nil {
		replyInternalError(w, h.logger, "read authorized producer", err)
		return
	}
	if retiredNow {
		replyError(w, http.StatusForbidden, pageAgentProducerAuthorizeRefusal)
		return
	}
	metadata := map[string]any{
		"panel_id": panelID, "producer": "agent/" + ref,
		"previous_designated_by_user_id": designator, "previously_retired": wasRetired,
		"reason": "the artifact owner authorized the agent producer explicitly (R71 0.14/1)",
	}
	meta, err := json.Marshal(metadata)
	if err != nil {
		replyInternalError(w, h.logger, "encode producer authorization audit", err)
		return
	}
	if _, err := tx.ExecContext(r.Context(), `INSERT INTO audit_logs(id, workspace_id, user_id, action, entity_type, entity_id, metadata, created_at)
		VALUES (lower(hex(randomblob(16))), ?, ?, 'page.agent_producer.authorized', 'page', ?, ?, strftime('%Y-%m-%dT%H:%M:%fZ','now'))`,
		wsID, user.ID, rec.ID, string(meta)); err != nil {
		replyInternalError(w, h.logger, "audit producer authorization", err)
		return
	}
	// An authorization delegates data Edit to an agent: an access change, so
	// it is also appended to the chained journal in the same transaction. The
	// page is never private here (refused above), so the journal's
	// workspace-wide audience reveals nothing the audit listing would not.
	notify, err := appendAuthorizationAudit(r.Context(), tx, h.journal, journal.Entry{
		WorkspaceID: wsID, Type: journal.EntryAuditEntityUpdated, Severity: journal.SeverityNotice,
		ActorType: journal.ActorUser, ActorID: user.ID,
		Summary: fmt.Sprintf("Page owner authorized agent/%s as the producer of panel %s", ref, panelID),
		Payload: map[string]any{"action": "page.agent_producer.authorized", "entity_type": "page", "entity_id": rec.ID, "metadata": metadata},
		Refs:    map[string]any{"entity_type": "page", "entity_id": rec.ID},
	})
	if err != nil {
		replyInternalError(w, h.logger, "journal producer authorization", err)
		return
	}
	if err := tx.Commit(); err != nil {
		replyInternalError(w, h.logger, "commit producer authorization", err)
		return
	}
	if notify != nil {
		notify()
	}
	out["changed"] = true
	writeJSON(w, http.StatusOK, out)
}

func (h *PageHandler) producerAuthorizationReachable(ctx context.Context, wsID, userID string, rec *pageRecord) (bool, error) {
	panels, err := h.loadPanels(ctx, wsID, rec.ID)
	if err != nil {
		return false, err
	}
	viewer, err := h.loadViewer(ctx, wsID, userID)
	if err != nil {
		return false, err
	}
	return h.canSeePage(ctx, wsID, rec, panels, viewer)
}

// producerAuthorizationCallerKey marks a page read made by an agent, so the
// document never offers it the owner's authorize button.
type producerAuthorizationCallerKey struct{}

// producerAuthorizationContext is the request's context, marked when an
// agent makes the request, for every page document a request renders.
func producerAuthorizationContext(r *http.Request) context.Context {
	if agent, _ := pageGrantCallerIsAgent(r); agent {
		return context.WithValue(r.Context(), producerAuthorizationCallerKey{}, true)
	}
	return r.Context()
}

// mayAuthorizeProducersFor is can_authorize on the wire: a person who owns
// the page as delegation reads it, on a page that is not private.
func (h *PageHandler) mayAuthorizeProducersFor(ctx context.Context, viewer *pageViewer, rec *pageRecord) bool {
	if agent, _ := ctx.Value(producerAuthorizationCallerKey{}).(bool); agent {
		return false
	}
	if viewer == nil || viewer.UserID == "" || rec.PrivateAudience {
		return false
	}
	return h.pageOwnerForDelegation(ctx, WorkspaceIDFromContext(ctx), viewer.UserID, rec)
}
