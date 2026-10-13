package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/crewship-ai/crewship/internal/chatentities"
)

// Human retries are scoped to the authenticated person, never a client actor.
// Reservation, mutation and response are committed in the same transaction.
func (h *IssueHandler) beginHumanIssueOperation(w http.ResponseWriter, r *http.Request, tx *sql.Tx, verb, entityID string, payload any) (*chatentities.Operation, bool) {
	user := UserFromContext(r.Context())
	if user == nil {
		replyError(w, 401, "Authentication required")
		return nil, true
	}
	allowed, err := issueRequesterAllowed(r.Context(), tx, user.ID, WorkspaceIDFromContext(r.Context()), verb)
	if err != nil {
		internalError(w, r, h.logger, "authorize human issue operation", err)
		return nil, true
	}
	var active bool
	if err = tx.QueryRowContext(r.Context(), `SELECT EXISTS(SELECT 1 FROM users WHERE id=? AND (suspended_at IS NULL OR suspended_at=''))`, user.ID).Scan(&active); err != nil {
		internalError(w, r, h.logger, "authorize human account", err)
		return nil, true
	}
	if !allowed || !active {
		replyError(w, 403, "requester_forbidden")
		return nil, true
	}
	key := r.Header.Get("Idempotency-Key")
	if key == "" {
		return nil, false
	}
	if strings.TrimSpace(key) == "" || len(key) > 200 {
		replyError(w, 400, "operation_invalid")
		return nil, true
	}
	if verb == "updated" {
		raw, e := json.Marshal(payload)
		if e != nil {
			internalError(w, r, h.logger, "hash issue patch", e)
			return nil, true
		}
		var fields map[string]any
		dec := json.NewDecoder(strings.NewReader(string(raw)))
		dec.UseNumber()
		if e = dec.Decode(&fields); e != nil {
			internalError(w, r, h.logger, "hash issue patch", e)
			return nil, true
		}
		fields["force"] = r.URL.Query().Get("force") == "true"
		payload = fields
	}
	hash, err := chatIssueRequestHash(verb, entityID, payload)
	if err != nil {
		internalError(w, r, h.logger, "hash human issue operation", err)
		return nil, true
	}
	op, replay, err := chatentities.BeginOperation(r.Context(), tx, chatentities.OperationSpec{ID: generateCUID(), WorkspaceID: WorkspaceIDFromContext(r.Context()), Key: key, RequestHash: hash, ActorType: "human", ActorID: user.ID, Provenance: "human", Kind: "issue", Verb: verb})
	if err != nil {
		writeChatIssueError(w, err)
		return nil, true
	}
	if replay {
		// The caller resolved the target in this transaction and rechecked current
		// membership above. A stored response is never a substitute for live access.
		if op.EntityID != entityID {
			writeChatIssueError(w, chatentities.ErrKeyConflict)
			return nil, true
		}
		w.Header().Set("Idempotency-Replayed", "true")
		w.Header().Set("Operation-ID", op.ID)
		status := http.StatusOK
		if verb == "commented" {
			status = http.StatusCreated
		}
		writeJSON(w, status, json.RawMessage(op.Result))
		return nil, true
	}
	return op, false
}

func completeHumanIssueOperation(r *http.Request, tx *sql.Tx, op *chatentities.Operation, entityID string, response any) error {
	if op == nil {
		return nil
	}
	var revision, aggregate int64
	if err := tx.QueryRowContext(r.Context(), `SELECT revision,aggregate_revision FROM missions WHERE id=? AND workspace_id=?`, entityID, WorkspaceIDFromContext(r.Context())).Scan(&revision, &aggregate); err != nil {
		return err
	}
	result, err := json.Marshal(response)
	if err != nil {
		return err
	}
	return chatentities.CompleteOperation(r.Context(), tx, op.ID, chatentities.ResultSpec{EntityID: entityID, Revision: revision, AggregateRevision: aggregate, Result: result})
}

// A missing precondition keeps existing clients compatible. Card editors send
// expected_revision (or If-Match) and a stable Idempotency-Key on every retry.
func issueExpectedRevision(r *http.Request, expected *int64) (*int64, error) {
	raw := r.Header.Get("If-Match")
	if raw == "" {
		if expected != nil && *expected < 1 {
			return nil, errors.New("revision_invalid")
		}
		return expected, nil
	}
	raw = strings.Trim(raw, "\"")
	var n int64
	if err := json.Unmarshal([]byte(raw), &n); err != nil || n < 1 {
		return nil, errors.New("revision_invalid")
	}
	if expected != nil && *expected != n {
		return nil, errors.New("revision_invalid")
	}
	return &n, nil
}
