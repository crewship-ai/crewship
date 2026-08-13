package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/encryption"
)

// escalationResult is the response delivered to a waiting sidecar when an
// escalation reaches a terminal state.
//
// Status carries WHICH terminal state, because "the wait ended" and "a human
// answered" stopped being the same thing when EXPIRED and CANCELLED arrived. An
// empty Status means RESOLVED — the only outcome that existed when the resolve
// path was written, so its call site needs no change to keep meaning what it
// always meant.
//
// Warning is the text handed to the agent IN PLACE OF an answer. It is empty
// on a real resolution and non-empty on every other terminal state, which is
// the wire-level expression of the product decision in escalation_lifecycle.go:
// an agent may continue without an answer, but never without being told.
type escalationResult struct {
	Resolution string `json:"resolution"`
	Action     string `json:"action"`
	RedirectTo string `json:"redirect_to,omitempty"`
	Status     string `json:"status,omitempty"`
	Warning    string `json:"warning,omitempty"`
}

// terminalStatus normalises the zero value to RESOLVED. See the Status field.
func (r escalationResult) terminalStatus() string {
	if r.Status == "" {
		return escalationStatusResolved
	}
	return r.Status
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
	scoped := func() (escalationWaitRow, error) {
		var row escalationWaitRow
		query := `SELECT status, type, resolution, action, redirect_to, deadline_at,
			         workspace_id, crew_id, chat_id, from_agent_id, reason
			  FROM escalations WHERE id = ?`
		args := []interface{}{escalationID}
		if boundCrew := InternalTokenCrewFromContext(r.Context()); boundCrew != "" {
			query += ` AND crew_id = ?`
			args = append(args, boundCrew)
		} else if boundWS := InternalTokenWorkspaceFromContext(r.Context()); boundWS != "" {
			query += ` AND workspace_id = ?`
			args = append(args, boundWS)
		}
		err := h.db.QueryRowContext(r.Context(), query, args...).
			Scan(&row.status, &row.escalationType, &row.resolution, &row.action, &row.redir, &row.deadline,
				&row.scope.workspaceID, &row.scope.crewID, &row.scope.chatID, &row.scope.fromAgentID, &row.scope.reason)
		row.scope.id = escalationID
		row.scope.deadlineAt = row.deadline.String
		return row, err
	}

	row, err := scoped()
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			replyError(w, http.StatusNotFound, "escalation not found")
			return
		}
		replyInternalError(w, h.logger, "wait escalation lookup", err)
		return
	}

	var ch chan escalationResult
	if row.status == escalationStatusPending {
		ch = h.registerEscalationWaiter(escalationID)
		defer h.removeEscalationWaiter(escalationID, ch)

		// Re-read: a resolve that landed between the first read and the
		// registration would otherwise never reach us.
		row, err = scoped()
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				replyError(w, http.StatusNotFound, "escalation not found")
				return
			}
			replyInternalError(w, h.logger, "wait escalation re-check", err)
			return
		}
	}

	status, escalationType, resolution, action, redirectTo := row.status, row.escalationType, row.resolution, row.action, row.redir

	// A terminal state that is not an answer. The agent is told which one and
	// why, never left to infer silence.
	if status == escalationStatusExpired || status == escalationStatusCancelled {
		writeJSON(w, http.StatusOK, escalationNoAnswerBody(status))
		return
	}

	if status == escalationStatusResolved {
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

	// The server's own deadline, not the client's. Before this, the only clock
	// in the picture was the sidecar's 300 s context: it expired, the agent
	// proceeded without an answer, and the row stayed PENDING forever because
	// nothing on this side ever learned that the question had been abandoned.
	//
	// Now the deadline is a column, the wait ends on it, and the SAME event
	// that ends the wait writes the terminal status. The agent's belief and the
	// database cannot disagree because there is only one event.
	//
	// A row with no deadline (raised before the column existed) keeps the old
	// shape: block until the client gives up, and answer TIMEOUT. That is not a
	// terminal state and is not claimed to be one.
	var deadlineC <-chan time.Time
	if row.deadline.Valid && row.deadline.String != "" {
		if at, perr := parseEscalationDeadline(row.deadline.String); perr == nil {
			timer := time.NewTimer(time.Until(at))
			defer timer.Stop()
			deadlineC = timer.C
		} else {
			h.logger.Warn("wait escalation: unparseable deadline_at, falling back to the client's timeout",
				"escalation_id", escalationID, "deadline_at", row.deadline.String, "error", perr)
		}
	}

	select {
	case result := <-ch:
		// Someone reached a terminal state while we waited. It may be the
		// human's answer, or it may be an expiry/cancellation raced in by
		// another observer — either way the result says which.
		if st := result.terminalStatus(); st != escalationStatusResolved {
			writeJSON(w, http.StatusOK, escalationNoAnswerBody(st))
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"status":      escalationStatusResolved,
			"resolution":  result.Resolution,
			"action":      result.Action,
			"redirect_to": result.RedirectTo,
		})
	case <-deadlineC:
		// Flip the row, then answer whatever actually happened. Losing the CAS
		// means a human decided in the same instant, and reporting an expiry
		// for a question that WAS answered would corrupt the trail in exactly
		// the direction this change exists to fix — so re-read and hand back
		// their decision instead.
		//
		// The write runs on a detached, bounded context: r.Context() may be
		// cancelled the moment we answer, and a transition that half-committed
		// because the client hung up is the failure mode the sweeper would
		// then have to clean up.
		bgCtx, bgCancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
		defer bgCancel()
		flipped, expErr := h.expireEscalationRow(bgCtx, row.scope, time.Now())
		if expErr != nil {
			h.logger.Error("wait escalation: expire at deadline", "error", expErr, "escalation_id", escalationID)
		}
		if flipped {
			writeJSON(w, http.StatusOK, escalationNoAnswerBody(escalationStatusExpired))
			return
		}
		settled, rerr := scoped()
		if rerr != nil {
			replyInternalError(w, h.logger, "wait escalation deadline re-read", rerr)
			return
		}
		h.replyWithSettledEscalation(w, escalationID, settled)
	case <-r.Context().Done():
		writeJSON(w, http.StatusRequestTimeout, map[string]string{
			"status": "TIMEOUT",
			"error":  "escalation not resolved in time",
		})
	}
}

// escalationNoAnswerBody is what an agent gets instead of an answer. The
// warning is mandatory: continuing without a human decision is allowed, doing
// so silently is the defect.
func escalationNoAnswerBody(status string) map[string]interface{} {
	warning := escalationExpiredWarning
	if status == escalationStatusCancelled {
		warning = "The question was withdrawn by a human before it was decided. Continue without an answer and do not assume approval."
	}
	return map[string]interface{}{
		"status":       status,
		"resolution":   "",
		"action":       "",
		"warning":      warning,
		"agent_action": escalationOutcomeContinuedWithWarning,
	}
}

// parseEscalationDeadline accepts both timestamp shapes this table carries:
// the RFC3339 this package writes, and the space-separated form SQLite's
// datetime() produces. Same reason escalationDuePredicate normalises with
// datetime() — a deadline the server cannot read is a deadline that never
// fires.
func parseEscalationDeadline(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	return time.Parse("2006-01-02 15:04:05", s)
}

// escalationWaitRow is one escalation as the long poll needs it: the state and
// answer it may already carry, its deadline, and the scope a deadline
// transition has to journal itself with. Read once under the caller's token
// binding, so the expiry path never has to make a second authorization
// decision.
type escalationWaitRow struct {
	status, escalationType    string
	resolution, action, redir sql.NullString
	deadline                  sql.NullString
	scope                     expirableEscalation
}

// replyWithSettledEscalation answers with whatever terminal state the row
// actually holds, for the deadline path that lost its compare-and-swap.
func (h *QueryHandler) replyWithSettledEscalation(w http.ResponseWriter, escalationID string, row escalationWaitRow) {
	if row.status != escalationStatusResolved {
		writeJSON(w, http.StatusOK, escalationNoAnswerBody(row.status))
		return
	}
	resolved := row.resolution.String
	if row.escalationType == "CREDENTIAL" && resolved != "" {
		dec, decErr := encryption.Decrypt(resolved)
		if decErr != nil {
			h.logger.Error("decrypt credential resolution", "error", decErr, "escalation_id", escalationID)
			replyError(w, http.StatusInternalServerError, "Internal server error")
			return
		}
		resolved = dec
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":      escalationStatusResolved,
		"resolution":  resolved,
		"action":      row.action.String,
		"redirect_to": row.redir.String,
	})
}
