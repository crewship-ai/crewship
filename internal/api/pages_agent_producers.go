package api

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"sort"
	"strings"
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
// producer designation has ended.
const pageAgentProducerRetiredFault = "producer agent %q was not named by the page's current owner (or its designator stopped " +
	"owning the page), so it may not push; only the owner names an agent producer, and the owner must grant the agent produce"

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
