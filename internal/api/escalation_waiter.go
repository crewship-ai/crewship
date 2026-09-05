package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/credname"
	"github.com/crewship-ai/crewship/internal/credpolicy"
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
	Resolution string `json:"resolution,omitempty"`
	Action     string `json:"action"`
	RedirectTo string `json:"redirect_to,omitempty"`
	Status     string `json:"status,omitempty"`
	Warning    string `json:"warning,omitempty"`
	// Credential is the answer to a CREDENTIAL escalation (#2376): the handle
	// the agent may use the credential by. It replaces Resolution entirely on
	// that type — the value is in the vault and reaches a command only through
	// /keeper/execute, never this channel.
	Credential *escalationCredentialHandle `json:"credential,omitempty"`
	// CredentialEscalation says the row is a CREDENTIAL escalation even when
	// there is no handle to give (a rejection), so the wire body can omit the
	// resolution key rather than send an empty one that reads like silence.
	CredentialEscalation bool `json:"-"`
}

// escalationCredentialHandle is what an agent receives in place of a secret:
// enough to USE the credential and nothing that would let it read one.
type escalationCredentialHandle struct {
	ID string `json:"id"`
	// Name is the credential_name /keeper/execute resolves — the grant's env
	// var name, which is also the file name it would have under /secrets/ if
	// it were ever delivered there.
	Name          string `json:"name"`
	Type          string `json:"type"`
	SecurityLevel int    `json:"security_level"`
	HandleOnly    bool   `json:"handle_only"`
	// Granted says an agent_credentials row exists for the asking agent. An
	// approved PROPOSAL on a workspace without auto-lease activates the
	// credential but grants nothing (see grantLeasedCredentialOnApprove); the
	// agent is told so rather than left to discover a 404 from /keeper/execute.
	Granted        bool   `json:"granted"`
	LeaseExpiresAt string `json:"lease_expires_at,omitempty"`
	// Use is the only way the agent may reach the value: "keeper_execute" for
	// a handle-only or Keeper-gated credential, "environment" for a type the
	// delivery policy hands over at boot.
	Use string `json:"use"`
}

// resolvedEscalationBody is the wire body for a RESOLVED answer, shared by the
// live wake-up and the two read-it-back paths so the three cannot disagree
// about what a CREDENTIAL answer looks like: it has a `credential` and no
// `resolution`, whatever the row's resolution column holds.
func resolvedEscalationBody(res escalationResult) map[string]interface{} {
	body := map[string]interface{}{
		"status":      escalationStatusResolved,
		"action":      res.Action,
		"redirect_to": res.RedirectTo,
	}
	switch {
	case res.Credential != nil:
		body["credential"] = res.Credential
	case res.CredentialEscalation:
		// A rejected or redirected ask: no handle, and no text either.
	default:
		body["resolution"] = res.Resolution
	}
	return body
}

// credentialHandleFor builds the handle for credentialID as agentID sees it,
// or nil when the credential is not ACTIVE (rejected, disposed, still
// waiting). Reads the grant so Name is the env var the agent must actually use.
func (h *QueryHandler) credentialHandleFor(ctx context.Context, workspaceID, credentialID, agentID string) *escalationCredentialHandle {
	if credentialID == "" {
		return nil
	}
	var (
		name, credType, status string
		level                  int
		handleOnly             bool
		envVar, leaseExpires   string
	)
	err := h.db.QueryRowContext(ctx, `
		SELECT c.name, c.type, COALESCE(c.security_level, 1), c.handle_only, c.status,
		       COALESCE(ac.env_var_name, ''), COALESCE(ac.expires_at, '')
		FROM credentials c
		LEFT JOIN agent_credentials ac ON ac.credential_id = c.id AND ac.agent_id = ?
		WHERE c.id = ? AND c.workspace_id = ? AND c.deleted_at IS NULL`,
		agentID, credentialID, workspaceID).
		Scan(&name, &credType, &level, &handleOnly, &status, &envVar, &leaseExpires)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			h.logger.Warn("credential handle: lookup failed", "error", err, "credential_id", credentialID)
		}
		return nil
	}
	if status != "ACTIVE" {
		return nil
	}
	granted := envVar != ""
	if !granted {
		envVar, _ = credname.Canonical(name)
	}
	use := "environment"
	if handleOnly || credpolicy.IsKeeperGated(credType) {
		use = "keeper_execute"
	}
	return &escalationCredentialHandle{
		ID: credentialID, Name: envVar, Type: credType, SecurityLevel: level,
		HandleOnly: handleOnly, Granted: granted, LeaseExpiresAt: leaseExpires, Use: use,
	}
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
			         workspace_id, crew_id, chat_id, from_agent_id, reason,
			         COALESCE(answer_deadline_at, ''), COALESCE(credential_id, '')
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
				&row.scope.workspaceID, &row.scope.crewID, &row.scope.chatID, &row.scope.fromAgentID, &row.scope.reason,
				&row.scope.deadlineAt, &row.scope.credentialID)
		row.scope.id = escalationID
		// row.deadline is the AGENT's window (what this poll waits on);
		// row.scope.deadlineAt is the HUMAN's (what an expiry would describe).
		// They are different columns and conflating them here is how the
		// regression got written in the first place.
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

	// Already terminal — an answer, or a terminal state that is not one. Either
	// way the row is what it is, and the agent is told which; a CREDENTIAL
	// answer comes back as a handle, never as text (#2376).
	if row.status != escalationStatusPending {
		h.replyWithSettledEscalation(w, r.Context(), escalationID, row)
		return
	}

	// The server's own deadline, not the client's, and specifically the AGENT's
	// deadline_at — never answer_deadline_at. Before this existed the only clock
	// in the picture was the sidecar's 300 s context: it expired, the agent
	// proceeded without an answer, and the row stayed PENDING forever because
	// nothing on this side ever learned that the question had been abandoned.
	//
	// Now the wait ends on a column, and the SAME event that ends the wait
	// records the give-up (agent_gave_up_at). What that event does NOT do any
	// more is close the question: the human's clock is a different column with
	// days on it, and the version of this file that expired the row here made
	// the approval queue unusable — 409 for questions nobody had answered.
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
		writeJSON(w, http.StatusOK, resolvedEscalationBody(result))
	case <-deadlineC:
		// The AGENT's window closed. Re-read FIRST: a human may have decided in
		// the same instant, and `select` picks at random between a ready channel
		// and a ready timer, so reporting "no answer" without looking would
		// throw away a decision that had already been made. If the row is
		// terminal, hand back whatever it actually holds.
		//
		// The write runs on a detached, bounded context: r.Context() may be
		// cancelled the moment we answer, and a half-committed stamp because the
		// client hung up is a failure mode nothing would clean up.
		bgCtx, bgCancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
		defer bgCancel()

		settled, rerr := scoped()
		if rerr != nil {
			replyInternalError(w, h.logger, "wait escalation deadline re-read", rerr)
			return
		}
		if settled.status != escalationStatusPending {
			h.replyWithSettledEscalation(w, r.Context(), escalationID, settled)
			return
		}

		// Still PENDING — but is it still ANSWERABLE? A row whose human
		// deadline has also passed is one the sweeper has not reached yet (it
		// ticks at 60 s and the read paths only sweep on a read). Telling the
		// agent the question is still open would be false, so settle it here on
		// the same CAS every other observer uses, and disposal of any staged
		// credential comes along with it.
		if answerDue(settled.scope.deadlineAt, time.Now()) {
			flipped, expErr := h.expireEscalationRow(bgCtx, settled.scope, time.Now())
			if expErr != nil {
				h.logger.Error("wait escalation: expire at answer deadline", "error", expErr, "escalation_id", escalationID)
			}
			if flipped {
				writeJSON(w, http.StatusOK, escalationNoAnswerBody(escalationStatusExpired))
				return
			}
			// Lost the CAS: somebody decided in the same instant. Their answer,
			// not our expiry.
			if again, aerr := scoped(); aerr == nil {
				h.replyWithSettledEscalation(w, r.Context(), escalationID, again)
				return
			}
		}

		// Still open. Record that this agent stopped waiting — a fact about the
		// run, not a decision about the question — and tell it so. The row stays
		// PENDING and stays in the operator's inbox; whoever comes back from the
		// password manager in seven minutes can still click Approve, and their
		// approval will still put the credential in the vault. It just will not
		// reach this run, which is what escalationUnansweredWarning says.
		//
		// The stamp is CAS-guarded on status='PENDING', so a human who resolved
		// between the re-read and here wins and no give-up is recorded for a
		// question that was in fact answered.
		if _, markErr := h.markAgentGaveUp(bgCtx, escalationID, time.Now()); markErr != nil {
			h.logger.Error("wait escalation: mark agent gave up", "error", markErr, "escalation_id", escalationID)
		}
		h.logger.Info("escalation wait window closed; question stays open",
			"escalation_id", escalationID, "crew_id", row.scope.crewID)
		writeJSON(w, http.StatusOK, escalationNoAnswerBody(escalationWireUnanswered))
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
//
// Three shapes, and the difference between them matters to the model reading
// it. EXPIRED and CANCELLED are terminal — the question is gone, nothing more
// is coming, and a later run asking again is asking something new. UNANSWERED
// is not terminal: the question is still in a human's inbox and may well be
// answered, just not to this run. `still_open` carries that so an agent (or a
// CLI rendering the response) does not have to infer it from the status string.
func escalationNoAnswerBody(status string) map[string]interface{} {
	warning := escalationExpiredWarning
	stillOpen := false
	switch status {
	case escalationStatusCancelled:
		warning = "The question was withdrawn by a human before it was decided. Continue without an answer and do not assume approval."
	case escalationWireUnanswered:
		warning = escalationUnansweredWarning
		stillOpen = true
	}
	return map[string]interface{}{
		"status":       status,
		"resolution":   "",
		"action":       "",
		"warning":      warning,
		"agent_action": escalationOutcomeContinuedWithWarning,
		"still_open":   stillOpen,
	}
}

// answerDue reports whether a human answer deadline has passed. An empty or
// unparseable value is NOT due: "no deadline" and "a deadline I cannot read"
// must both leave the question answerable, because the alternative is expiring
// a row on the strength of a string nobody could interpret.
func answerDue(answerDeadline string, now time.Time) bool {
	if answerDeadline == "" {
		return false
	}
	at, err := parseEscalationDeadline(answerDeadline)
	if err != nil {
		return false
	}
	return !at.After(now)
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
func (h *QueryHandler) replyWithSettledEscalation(w http.ResponseWriter, ctx context.Context, escalationID string, row escalationWaitRow) {
	if row.status != escalationStatusResolved {
		writeJSON(w, http.StatusOK, escalationNoAnswerBody(row.status))
		return
	}
	res := escalationResult{
		Action:               row.action.String,
		RedirectTo:           row.redir.String,
		CredentialEscalation: row.escalationType == "CREDENTIAL",
	}
	if res.CredentialEscalation {
		// The resolution column is never read for a CREDENTIAL row (#2376): a
		// historical one holds the "[credential submitted]" marker, a current
		// one NULL, and neither is something an agent should be handed. The
		// answer is the grant, read live so a lease issued since is reflected.
		if row.action.String == "approve" {
			res.Credential = h.credentialHandleFor(ctx, row.scope.workspaceID, row.scope.credentialID, row.scope.fromAgentID)
		}
	} else {
		res.Resolution = row.resolution.String
	}
	writeJSON(w, http.StatusOK, resolvedEscalationBody(res))
}
