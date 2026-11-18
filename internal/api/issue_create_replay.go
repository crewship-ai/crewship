package api

import (
	"database/sql"
	"encoding/json"
	"net/http"

	"github.com/crewship-ai/crewship/internal/chatentities"
)

// A task composer can lose the response after a successful create. Reuse the
// existing operation ledger in the SAME transaction as the issue, rather than
// treating a stable client id as a suggestion and creating duplicate work.
// This is called after normal current-role, crew and target validation.
func (h *IssueHandler) beginHumanIssueCreate(w http.ResponseWriter, r *http.Request, tx *sql.Tx, crew string, payload any) (*chatentities.Operation, bool) {
	key := r.Header.Get("Idempotency-Key")
	if key == "" {
		return nil, false
	}
	user := UserFromContext(r.Context())
	if user == nil {
		replyError(w, 401, "authentication required")
		return nil, true
	}
	allowed, err := issueRequesterAllowed(r.Context(), tx, user.ID, WorkspaceIDFromContext(r.Context()), "created")
	if err != nil {
		replyError(w, 503, "create authority unavailable")
		return nil, true
	}
	var active bool
	if err := tx.QueryRowContext(r.Context(), `SELECT EXISTS(SELECT 1 FROM users u JOIN workspace_members wm ON wm.user_id=u.id JOIN workspaces w ON w.id=wm.workspace_id WHERE u.id=? AND wm.workspace_id=? AND u.suspended_at IS NULL AND w.deleted_at IS NULL)`, user.ID, WorkspaceIDFromContext(r.Context())).Scan(&active); err != nil {
		replyError(w, 503, "create authority unavailable")
		return nil, true
	}
	if !allowed || !active {
		replyError(w, 403, "requester_forbidden")
		return nil, true
	}
	hash, err := chatIssueRequestHash("created", crew, payload)
	if err != nil {
		writeChatIssueError(w, err)
		return nil, true
	}
	op, replay, err := chatentities.BeginOperation(r.Context(), tx, chatentities.OperationSpec{ID: generateCUID(), WorkspaceID: WorkspaceIDFromContext(r.Context()), Key: key, RequestHash: hash, ActorType: "human", ActorID: user.ID, Provenance: "human", Kind: "issue", Verb: "created"})
	if err != nil {
		writeChatIssueError(w, err)
		return nil, true
	}
	if !replay {
		return op, false
	}
	// An operation receipt is not permission to read a deleted or foreign issue.
	var exists bool
	if err := tx.QueryRowContext(r.Context(), `SELECT EXISTS(SELECT 1 FROM missions WHERE id=? AND workspace_id=? AND crew_id=?)`, op.EntityID, WorkspaceIDFromContext(r.Context()), crew).Scan(&exists); err != nil {
		replyError(w, 503, "create receipt unavailable")
		return nil, true
	}
	if !exists {
		replyError(w, 404, "Issue not found")
		return nil, true
	}
	w.Header().Set("Idempotency-Replayed", "true")
	w.Header().Set("Operation-ID", op.ID)
	writeJSON(w, http.StatusCreated, json.RawMessage(op.Result))
	return nil, true
}
