package api

import (
	"database/sql"
	"errors"
	"net/http"

	"github.com/crewship-ai/crewship/internal/encryption"
)

// escalationResult is the response delivered to a waiting sidecar when a human resolves an escalation.
type escalationResult struct {
	Resolution string `json:"resolution"`
	Action     string `json:"action"`
	RedirectTo string `json:"redirect_to,omitempty"`
}

// registerEscalationWaiter adds a buffered channel for the given escalation
// ID and returns it.
//
// The map holds a SLICE of channels, not one. It used to hold one and
// overwrite — "only one waiter per escalation is supported" — which meant any
// second request for the same id silently stole the first one's wakeup: the
// incumbent then blocked until its context expired and returned TIMEOUT for an
// escalation a human had already answered. A sidecar retrying its long poll is
// enough to trigger that, and so is a caller the authorization predicate is
// about to refuse.
func (h *QueryHandler) registerEscalationWaiter(id string) chan escalationResult {
	h.escalationMu.Lock()
	defer h.escalationMu.Unlock()
	ch := make(chan escalationResult, 1)
	h.escalationWaiters[id] = append(h.escalationWaiters[id], ch)
	return ch
}

// notifyEscalationWaiter delivers the result to every waiter registered for
// the escalation and clears them. Non-blocking sends, so a waiter that has
// already timed out cannot stall the resolve path.
func (h *QueryHandler) notifyEscalationWaiter(id string, result escalationResult) {
	h.escalationMu.Lock()
	chans := h.escalationWaiters[id]
	delete(h.escalationWaiters, id)
	h.escalationMu.Unlock()
	for _, ch := range chans {
		select {
		case ch <- result:
		default:
			// Waiter already timed out or its buffer is full — discard.
		}
	}
}

// removeEscalationWaiter drops one specific channel, leaving any other waiters
// on the same escalation registered. Identity comparison rather than id alone
// is what stops a departing waiter from cancelling its siblings.
func (h *QueryHandler) removeEscalationWaiter(id string, ch chan escalationResult) {
	h.escalationMu.Lock()
	defer h.escalationMu.Unlock()
	chans := h.escalationWaiters[id]
	for i, c := range chans {
		if c == ch {
			chans = append(chans[:i], chans[i+1:]...)
			break
		}
	}
	if len(chans) == 0 {
		delete(h.escalationWaiters, id)
		return
	}
	h.escalationWaiters[id] = chans
}

// WaitForEscalationResponse handles GET /api/v1/internal/escalations/{escalationId}/wait.
// It blocks until the escalation is resolved or the request context is cancelled (timeout).
// This is called by the sidecar to deliver the human's response back to the waiting agent.
//
// Authorization follows the PR-F24 / #1159 token-binding pattern its sibling
// CreateEscalation already uses: the lookup is scoped to whatever the caller's
// X-Internal-Token is cryptographically bound to, never to a caller-supplied
// id. A crew-bound (crwv1) sidecar may only wait on its OWN crew's
// escalations — a sibling crew in the same workspace is as foreign as another
// tenant, since crwv1 tokens exist precisely to isolate crews from each other.
// A workspace-bound (wsv1) caller is limited to its workspace; the unbound
// master token (host-side trusted services) is unrestricted, as everywhere
// else. This matters more here than on most internal routes because a
// resolved CREDENTIAL escalation is DECRYPTED below and handed back in the
// clear: without the predicate, one captured sidecar token could poll ids
// until it harvested every tenant's secrets.
//
// A refusal is the same 404 as a genuinely unknown id, deliberately: a 403
// would confirm that the id exists in someone else's tenant, turning the
// endpoint into an existence oracle.
func (h *QueryHandler) WaitForEscalationResponse(w http.ResponseWriter, r *http.Request) {
	escalationID := r.PathValue("escalationId")

	// Authorize BEFORE registering. Registration used to come first, to close
	// the lost-wakeup window between the status read and the channel being in
	// place — but once the lookup can REFUSE, a refused caller was registering
	// a channel and then tearing it down on its way out, which took a
	// legitimate waiter's wakeup with it. A cross-tenant probe, or the same
	// sidecar's own retry, was enough.
	//
	// The window is closed by re-reading after registration instead: authorize
	// and check once, register only if the answer is "still pending", then
	// check again. The second read costs one query on the blocking path only,
	// which is the path already prepared to wait.
	scoped := func() (status, escalationType string, resolution, action, redirectTo sql.NullString, err error) {
		query := `SELECT status, type, resolution, action, redirect_to FROM escalations WHERE id = ?`
		args := []interface{}{escalationID}
		if boundCrew := InternalTokenCrewFromContext(r.Context()); boundCrew != "" {
			query += ` AND crew_id = ?`
			args = append(args, boundCrew)
		} else if boundWS := InternalTokenWorkspaceFromContext(r.Context()); boundWS != "" {
			query += ` AND workspace_id = ?`
			args = append(args, boundWS)
		}
		err = h.db.QueryRowContext(r.Context(), query, args...).
			Scan(&status, &escalationType, &resolution, &action, &redirectTo)
		return
	}

	status, escalationType, resolution, action, redirectTo, err := scoped()
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			replyError(w, http.StatusNotFound, "escalation not found")
			return
		}
		replyInternalError(w, h.logger, "wait escalation lookup", err)
		return
	}

	var ch chan escalationResult
	if status != "RESOLVED" {
		ch = h.registerEscalationWaiter(escalationID)
		defer h.removeEscalationWaiter(escalationID, ch)

		// Re-read: a resolve that landed between the first read and the
		// registration would otherwise never reach us.
		status, escalationType, resolution, action, redirectTo, err = scoped()
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				replyError(w, http.StatusNotFound, "escalation not found")
				return
			}
			replyInternalError(w, h.logger, "wait escalation re-check", err)
			return
		}
	}

	if status == "RESOLVED" {
		// Already resolved — decrypt CREDENTIAL resolutions and return immediately.
		resolved := resolution.String
		if escalationType == "CREDENTIAL" && resolved != "" {
			dec, decErr := encryption.Decrypt(resolved)
			if decErr != nil {
				h.logger.Error("decrypt credential resolution", "error", decErr)
				replyError(w, http.StatusInternalServerError, "Internal server error")
				return
			}
			resolved = dec
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"status":      "RESOLVED",
			"resolution":  resolved,
			"action":      action.String,
			"redirect_to": redirectTo.String,
		})
		return
	}

	// Block until resolved or timeout.
	select {
	case result := <-ch:
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"status":      "RESOLVED",
			"resolution":  result.Resolution,
			"action":      result.Action,
			"redirect_to": result.RedirectTo,
		})
	case <-r.Context().Done():
		writeJSON(w, http.StatusRequestTimeout, map[string]string{
			"status": "TIMEOUT",
			"error":  "escalation not resolved in time",
		})
	}
}
