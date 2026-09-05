package api

// inbox_act.go — acting on a run_needs_human card (PRD-ISSUES-AND-ROUTINES-2026
// §18 scenario 15, second half; work package B15, #2389).
//
// B6 raises exactly one card when a run reports outcome NEEDS_HUMAN and B10
// threads it. Until this file, the card could be read and its state flipped,
// but nothing a person did on it reached the session that asked. Act closes
// the loop, per §10.5 "Human approval → the exact waitpoint or session that
// asked. Resume that waitpoint; never mint a generic assignment":
//
//   - `answer` (needs `input`): the person's text becomes a comment on the
//     issue, is delivered to the SAME agent's session (B2 delivery, B3
//     one-turn rule, B5 context pack — the run resumes from its checkpoint
//     with the answer as the next unread delta) and the run is dispatched
//     through the mention door — one delivery, one run, the existing
//     exactly-once guarantees.
//   - `take_over`: the person continues the work themselves. The session
//     leaves awaiting_input for idle so it is not forever "waiting on a
//     human" who has already acted.
//   - `dismiss`: no further work now. Same session transition.
//
// Every action writes a receipt: an `inbox_acted` row on the issue's event
// log (B1, seq-ordered, the same log B11's forced-DONE receipt lives in)
// naming who acted, which action, which card, the session's agent_version
// (§11.6 — the routine_version analogue for an issue run) and, for answer,
// the delivery and run it produced. The card itself is resolved IN PLACE
// with the receipt merged into its payload — the same thread, not a new
// card.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/inbox"
	"github.com/crewship-ai/crewship/internal/journal"
)

// The closed vocabulary of actions a run_needs_human card accepts. Also the
// actions B6 offers on the card; a card written before B15 offers only
// take_over, but the vocabulary is the kind's, not the row's, so an old
// card can still be answered.
const (
	inboxActAnswer   = "answer"
	inboxActTakeOver = "take_over"
	inboxActDismiss  = "dismiss"
)

// runNeedsHumanActions is what B6 puts on every new run_needs_human card.
var runNeedsHumanActions = []inbox.Action{
	{ID: inboxActAnswer, Label: "Answer", Effect: "Delivers your input to the agent's session and resumes the run from its checkpoint", Irreversible: false},
	{ID: inboxActTakeOver, Label: "Take over", Effect: "Opens the issue for you to continue; the agent's session goes idle", Irreversible: false},
	{ID: inboxActDismiss, Label: "Dismiss", Effect: "No further work now; the agent's session goes idle", Irreversible: false},
}

// SetMentionDispatcher wires the door `answer` resumes a run through — the
// same AssignmentHandler the @mention path uses, so an answer is a
// delivery like any other. nil is supported (an answer then records
// "no dispatcher wired" and refuses, never silently drops).
func (h *InboxHandler) SetMentionDispatcher(d mentionDispatcher) { h.mentionDispatch = d }

// SetJournal wires the journal emitter the receipt's issue event is
// mirrored into, matching IssueHandler.SetJournal. Optional.
func (h *InboxHandler) SetJournal(j journal.Emitter) { h.journal = j }

func (h *InboxHandler) events() issueEvents {
	return issueEvents{db: h.db, hub: h.hub, logger: h.logger, journal: h.journal}
}

// inboxActReceipt is what the receipt names, on the event log and on the
// card.
type inboxActReceipt struct {
	Action       string `json:"action"`
	ActedBy      string `json:"acted_by"`
	ActedAt      string `json:"acted_at"`
	InboxItemID  string `json:"inbox_item_id"`
	SessionID    string `json:"session_id,omitempty"`
	AgentVersion *int64 `json:"agent_version,omitempty"`
	SourceRunID  string `json:"source_run_id"`
	// answer only
	CommentID     string `json:"comment_id,omitempty"`
	DeliveryID    string `json:"delivery_id,omitempty"`
	RunID         string `json:"run_id,omitempty"`
	DispatchState string `json:"dispatch_state,omitempty"`
	// receipt's own position on the issue's event log
	EventID string `json:"event_id,omitempty"`
	Seq     int    `json:"seq,omitempty"`
}

// Act — POST /api/v1/inbox/{id}/act — performs one of the card's actions.
// Body: {"action": "answer|take_over|dismiss", "input": "..."}.
func (h *InboxHandler) Act(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	user := UserFromContext(r.Context())
	role := RoleFromContext(r.Context())
	if workspaceID == "" || user == nil {
		replyError(w, http.StatusUnauthorized, "auth required")
		return
	}
	id := r.PathValue("id")
	if id == "" {
		replyError(w, http.StatusBadRequest, "id required")
		return
	}
	var body struct {
		Action string `json:"action"`
		Input  string `json:"input"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&body); err != nil {
		replyError(w, http.StatusBadRequest, "invalid body")
		return
	}
	switch body.Action {
	case inboxActAnswer, inboxActTakeOver, inboxActDismiss:
	default:
		replyError(w, http.StatusBadRequest, "action must be answer|take_over|dismiss")
		return
	}
	body.Input = strings.TrimSpace(body.Input)
	if body.Action == inboxActAnswer && body.Input == "" {
		replyError(w, http.StatusBadRequest, "answer needs a non-empty input")
		return
	}

	// The card, within the caller's visibility (same clause as Get/Patch).
	visClause, visArgs := inboxVisibilityClause(user.ID, role)
	var (
		kind, sourceID, state, payloadJSON string
		resolvedAction, resolvedBy         sql.NullString
	)
	err := h.db.QueryRowContext(r.Context(),
		`SELECT kind, source_id, state, payload_json, resolved_action, resolved_by_user_id
		   FROM inbox_items WHERE id = ? AND workspace_id = ?`+visClause,
		append([]interface{}{id, workspaceID}, visArgs...)...,
	).Scan(&kind, &sourceID, &state, &payloadJSON, &resolvedAction, &resolvedBy)
	if errors.Is(err, sql.ErrNoRows) {
		replyError(w, http.StatusNotFound, "not found")
		return
	}
	if err != nil {
		h.logger.Error("inbox act lookup", "error", err)
		replyError(w, http.StatusInternalServerError, "lookup failed")
		return
	}
	if kind != inbox.KindRunNeedsHuman {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "this kind has no server-side action; decide it through its source endpoint (waitpoints: /pipelines/waitpoints/{token}/approve, escalations: /escalations/{id}/resolve) or flip its state with PATCH",
			"kind":  kind,
		})
		return
	}
	if state == "resolved" {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":               "already acted on",
			"resolved_action":     resolvedAction.String,
			"resolved_by_user_id": resolvedBy.String,
		})
		return
	}

	// The run behind the card and the session it belongs to.
	var (
		missionID, agentID        string
		sessionID                 sql.NullString
		identifier, title, crewID sql.NullString
		sessionState              sql.NullString
		agentVersion              sql.NullInt64
	)
	err = h.db.QueryRowContext(r.Context(), `
		SELECT COALESCE(a.group_id, a.chat_id), a.assigned_to_id, a.session_id,
		       m.identifier, m.title, m.crew_id, s.state, s.agent_version
		  FROM assignments a
		  LEFT JOIN missions m ON m.id = COALESCE(a.group_id, a.chat_id)
		  LEFT JOIN issue_agent_sessions s ON s.id = a.session_id
		 WHERE a.id = ? AND a.workspace_id = ?`, sourceID, workspaceID,
	).Scan(&missionID, &agentID, &sessionID, &identifier, &title, &crewID, &sessionState, &agentVersion)
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "the run behind this card no longer exists", "source_id": sourceID})
		return
	}
	if err != nil {
		h.logger.Error("inbox act: load run", "error", err, "assignment_id", sourceID)
		replyError(w, http.StatusInternalServerError, "lookup failed")
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)
	receipt := inboxActReceipt{
		Action: body.Action, ActedBy: user.ID, ActedAt: now, InboxItemID: id,
		SessionID: sessionID.String, SourceRunID: sourceID,
	}
	if agentVersion.Valid {
		v := agentVersion.Int64
		receipt.AgentVersion = &v
	}

	switch body.Action {
	case inboxActAnswer:
		// The answer is a comment on the issue, authored by the person —
		// visible in the thread, and the unread delta the resumed run's
		// context pack carries (B5).
		commentID := generateCUID()
		if _, err := h.db.ExecContext(r.Context(), `
			INSERT INTO mission_comments (id, mission_id, author_type, author_id, body, created_at, updated_at)
			VALUES (?, ?, 'user', ?, ?, ?, ?)`,
			commentID, missionID, user.ID, body.Input, now, now); err != nil {
			h.logger.Error("inbox act: insert answer comment", "error", err)
			replyError(w, http.StatusInternalServerError, "could not record the answer")
			return
		}
		receipt.CommentID = commentID
		rec := mentionRecorder{db: h.db, logger: h.logger, events: h.events(), dispatcher: h.mentionDispatch}
		mc := mentionContext{
			WorkspaceID: workspaceID, MissionID: missionID, Identifier: identifier.String,
			IssueTitle: title.String, IssueCrewID: crewID.String,
			CommentID: commentID, CommentBody: body.Input,
			AuthorType: "user", AuthorID: user.ID, AuthorName: user.Name,
		}
		dstate, runID, deliveryID, detail := rec.deliverToAgent(r.Context(), mc, agentID)
		receipt.DispatchState, receipt.RunID, receipt.DeliveryID = dstate, runID, deliveryID
		if dstate != mentionDispatchDispatched && dstate != mentionDispatchQueued {
			// The answer is on the issue as a comment, but nothing will
			// pick it up. Say so and leave the card open so the person can
			// try again once the cause (a held agent, an unconnected crew,
			// no dispatcher) is fixed.
			writeJSON(w, http.StatusConflict, map[string]any{
				"error":          "the answer was recorded as a comment but could not be delivered to the agent's session",
				"dispatch_state": dstate,
				"detail":         detail,
				"comment_id":     commentID,
			})
			return
		}
	case inboxActTakeOver, inboxActDismiss:
		// The person has acted; a session still parked in awaiting_input
		// would be waiting for something that already happened. idle is
		// the state a delivery reopens from (§10.1).
		if sessionID.Valid && sessionID.String != "" {
			res, uerr := h.db.ExecContext(r.Context(), `
				UPDATE issue_agent_sessions
				   SET state = 'idle', last_activity_at = ?, updated_at = ?
				 WHERE id = ? AND state = 'awaiting_input'`, now, now, sessionID.String)
			if uerr != nil {
				h.logger.Warn("inbox act: settle session", "error", uerr, "session_id", sessionID.String)
			} else if n, _ := res.RowsAffected(); n > 0 {
				broadcastIssueSessionState(r.Context(), h.db, h.hub, workspaceID, sessionID.String, "idle")
			}
		}
	}

	// The receipt on the issue's event log — seq-ordered, the same log the
	// B11 forced-DONE receipt lives in.
	details := fmt.Sprintf("%s on NEEDS_HUMAN card %s (run %s)", body.Action, id, sourceID)
	if receipt.AgentVersion != nil {
		details += fmt.Sprintf(" agent_version %d", *receipt.AgentVersion)
	}
	if receipt.RunID != "" {
		details += " → run " + receipt.RunID
	}
	eventID, written := h.events().logEvent(r.Context(), issueEvent{
		MissionID: missionID, ActorType: "user", ActorID: user.ID,
		Action: actionInboxActed, Details: details, RunID: receipt.RunID,
	})
	receipt.EventID, receipt.Seq = eventID, written.Seq

	// The card is resolved IN PLACE with the receipt on it: the same thread
	// the condition was raised in, not a new card.
	if err := h.resolveCardWithReceipt(r.Context(), id, user.ID, body.Action, payloadJSON, receipt); err != nil {
		if errors.Is(err, errInboxCardActedConcurrently) {
			// Two people acted at once: both passed the open-card check, the
			// other's resolve landed first. What this request did (a comment
			// and a delivery, for an answer) is real and on the log; the
			// second answer queues behind the first run under B3's one-turn
			// rule rather than racing it. Say so instead of a bare 500.
			writeJSON(w, http.StatusConflict, map[string]any{
				"error":   "someone else acted on this card at the same time; your action ran but the card carries theirs",
				"receipt": receipt,
			})
			return
		}
		h.logger.Error("inbox act: resolve card", "error", err, "id", id)
		replyError(w, http.StatusInternalServerError, "acted, but the card could not be updated")
		return
	}
	if h.hub != nil {
		broadcastWorkspaceEvent(h.hub, workspaceID, "inbox.updated", map[string]string{"id": id, "state": "resolved"})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":      id,
		"state":   "resolved",
		"action":  body.Action,
		"receipt": receipt,
	})
}

// resolveCardWithReceipt flips the card to resolved and merges the receipt
// into payload_json under "receipt", keeping every other payload key
// (who_can_act, context, ...) as the producer wrote it.
func (h *InboxHandler) resolveCardWithReceipt(ctx context.Context, id, userID, action, payloadJSON string, receipt inboxActReceipt) error {
	payload := map[string]any{}
	if strings.TrimSpace(payloadJSON) != "" {
		_ = json.Unmarshal([]byte(payloadJSON), &payload)
	}
	payload["receipt"] = receipt
	merged, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	res, err := h.db.ExecContext(ctx, `
		UPDATE inbox_items
		   SET state = 'resolved', resolved_at = ?, resolved_by_user_id = ?, resolved_action = ?,
		       payload_json = ?, updated_at = ?
		 WHERE id = ? AND state != 'resolved'`,
		receipt.ActedAt, userID, action, string(merged), receipt.ActedAt, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errInboxCardActedConcurrently
	}
	return nil
}

var errInboxCardActedConcurrently = errors.New("inbox card was resolved concurrently")
