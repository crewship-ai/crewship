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

// registerEscalationWaiter creates a buffered channel for the given escalation ID
// and stores it in the waiter map. Returns the channel to receive the result on.
// Only one waiter per escalation is supported; subsequent registrations overwrite.
func (h *QueryHandler) registerEscalationWaiter(id string) chan escalationResult {
	h.escalationMu.Lock()
	defer h.escalationMu.Unlock()
	ch := make(chan escalationResult, 1)
	h.escalationWaiters[id] = ch
	return ch
}

// notifyEscalationWaiter sends the result to the waiter channel (if one exists)
// and removes it from the map. Uses non-blocking send to prevent panic if the
// waiter has already timed out and the channel buffer is full or removed.
func (h *QueryHandler) notifyEscalationWaiter(id string, result escalationResult) {
	h.escalationMu.Lock()
	ch, ok := h.escalationWaiters[id]
	if ok {
		delete(h.escalationWaiters, id)
	}
	h.escalationMu.Unlock()
	if ok {
		select {
		case ch <- result:
		default:
			// Waiter already timed out or channel full — discard safely.
		}
	}
}

// removeEscalationWaiter removes a waiter channel from the map only if the
// stored channel matches the given instance. This prevents a timed-out waiter
// from accidentally removing a newer waiter registered for the same escalation.
func (h *QueryHandler) removeEscalationWaiter(id string, ch chan escalationResult) {
	h.escalationMu.Lock()
	defer h.escalationMu.Unlock()
	if h.escalationWaiters[id] == ch {
		delete(h.escalationWaiters, id)
	}
}

// WaitForEscalationResponse handles GET /api/v1/internal/escalations/{escalationId}/wait.
// It blocks until the escalation is resolved or the request context is cancelled (timeout).
// This is called by the sidecar to deliver the human's response back to the waiting agent.
func (h *QueryHandler) WaitForEscalationResponse(w http.ResponseWriter, r *http.Request) {
	escalationID := r.PathValue("escalationId")

	// Register waiter FIRST to avoid lost-wakeup race: if ResolveEscalation
	// runs between the DB check and the registration, the notification would
	// be lost. By registering first, the channel is in place before we check.
	ch := h.registerEscalationWaiter(escalationID)
	defer h.removeEscalationWaiter(escalationID, ch)

	// Now re-check if the escalation is already resolved.
	var status, escalationType string
	var resolution, action, redirectTo sql.NullString
	err := h.db.QueryRowContext(r.Context(), `
		SELECT status, type, resolution, action, redirect_to FROM escalations WHERE id = ?
	`, escalationID).Scan(&status, &escalationType, &resolution, &action, &redirectTo)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "escalation not found"})
			return
		}
		h.logger.Error("wait escalation lookup", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	if status == "RESOLVED" {
		// Already resolved — decrypt CREDENTIAL resolutions and return immediately.
		resolved := resolution.String
		if escalationType == "CREDENTIAL" && resolved != "" {
			dec, decErr := encryption.Decrypt(resolved)
			if decErr != nil {
				h.logger.Error("decrypt credential resolution", "error", decErr)
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
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
