package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
)

// ChatParticipantsHandler manages the humans in a multi-user group chat.
// Adding the first extra participant promotes the chat to visibility='group',
// after which the agent responds only when @mentioned (see chatbridge).
//
// Endpoints (scoped via chats.workspace_id; cross-tenant returns 404):
//
//	GET    /api/v1/chats/{chatId}/participants
//	POST   /api/v1/chats/{chatId}/participants            {user_id, role?}
//	DELETE /api/v1/chats/{chatId}/participants/{userId}
type ChatParticipantsHandler struct {
	db     *sql.DB
	logger *slog.Logger
}

func NewChatParticipantsHandler(db *sql.DB, logger *slog.Logger) *ChatParticipantsHandler {
	return &ChatParticipantsHandler{db: db, logger: logger}
}

type participantRow struct {
	UserID   string `json:"user_id"`
	Email    string `json:"email,omitempty"`
	FullName string `json:"full_name,omitempty"`
	Role     string `json:"role"`
	JoinedAt string `json:"joined_at"`
}

// chatWorkspace returns the chat's workspace id and whether the authenticated
// caller is a member of it. Routes carry no workspace_id, so the handler
// enforces tenancy itself — same contract as the reactions handler.
func (h *ChatParticipantsHandler) chatWorkspace(r *http.Request, chatID string) (string, bool) {
	if chatID == "" {
		return "", false
	}
	user := UserFromContext(r.Context())
	if user == nil {
		return "", false
	}
	var ws string
	if err := h.db.QueryRowContext(r.Context(),
		"SELECT workspace_id FROM chats WHERE id = ?", chatID).Scan(&ws); err != nil {
		return "", false
	}
	var role string
	if err := h.db.QueryRowContext(r.Context(),
		"SELECT role FROM workspace_members WHERE workspace_id = ? AND user_id = ?",
		ws, user.ID).Scan(&role); err != nil {
		return "", false
	}
	return ws, true
}

// canManageChat reports whether userID may mutate the chat's roster: only the
// chat's creator or a workspace OWNER/ADMIN. Plain workspace members can SEE a
// chat (chatWorkspace) but must not be able to add/remove participants or flip
// it to a group — otherwise any member could reshape any chat in the workspace.
func (h *ChatParticipantsHandler) canManageChat(r *http.Request, chatID, ws, userID string) bool {
	var createdBy sql.NullString
	if err := h.db.QueryRowContext(r.Context(),
		"SELECT created_by FROM chats WHERE id = ?", chatID).Scan(&createdBy); err != nil {
		return false
	}
	if createdBy.Valid && createdBy.String == userID {
		return true
	}
	var role string
	if err := h.db.QueryRowContext(r.Context(),
		"SELECT role FROM workspace_members WHERE workspace_id = ? AND user_id = ?",
		ws, userID).Scan(&role); err != nil {
		return false
	}
	return role == "OWNER" || role == "ADMIN"
}

func (h *ChatParticipantsHandler) List(w http.ResponseWriter, r *http.Request) {
	chatID := r.PathValue("chatId")
	if _, ok := h.chatWorkspace(r, chatID); !ok {
		replyError(w, http.StatusNotFound, "chat not found")
		return
	}
	rows, err := h.db.QueryContext(r.Context(), `
SELECT p.user_id, COALESCE(u.email, ''), COALESCE(u.full_name, ''), p.role, p.joined_at
FROM chat_participants p
LEFT JOIN users u ON u.id = p.user_id
WHERE p.chat_id = ?
ORDER BY p.joined_at ASC, p.user_id ASC`, chatID)
	if err != nil {
		h.logger.Error("list participants", "err", err)
		replyError(w, http.StatusInternalServerError, "internal")
		return
	}
	defer rows.Close()
	out := []participantRow{}
	for rows.Next() {
		var p participantRow
		if err := rows.Scan(&p.UserID, &p.Email, &p.FullName, &p.Role, &p.JoinedAt); err != nil {
			h.logger.Error("list participants scan", "err", err)
			replyError(w, http.StatusInternalServerError, "internal")
			return
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		replyError(w, http.StatusInternalServerError, "internal")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"participants": out})
}

func (h *ChatParticipantsHandler) Add(w http.ResponseWriter, r *http.Request) {
	chatID := r.PathValue("chatId")
	user := UserFromContext(r.Context())
	if user == nil {
		replyError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	ws, ok := h.chatWorkspace(r, chatID)
	if !ok {
		replyError(w, http.StatusNotFound, "chat not found")
		return
	}
	if !h.canManageChat(r, chatID, ws, user.ID) {
		replyError(w, http.StatusForbidden, "only the chat owner or a workspace admin can manage participants")
		return
	}
	var body struct {
		UserID string `json:"user_id"`
		Role   string `json:"role"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.UserID == "" {
		replyError(w, http.StatusBadRequest, "user_id required")
		return
	}
	role := "member"
	if body.Role == "owner" {
		role = "owner"
	}
	// The target must be a member of the chat's workspace — you can only add
	// people who already belong to the workspace.
	var memberCheck string
	if err := h.db.QueryRowContext(r.Context(),
		"SELECT user_id FROM workspace_members WHERE workspace_id = ? AND user_id = ?",
		ws, body.UserID).Scan(&memberCheck); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			replyError(w, http.StatusBadRequest, "user is not a member of this workspace")
			return
		}
		h.logger.Error("participant member check", "err", err)
		replyError(w, http.StatusInternalServerError, "internal")
		return
	}

	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		replyError(w, http.StatusInternalServerError, "internal")
		return
	}
	defer func() { _ = tx.Rollback() }()

	// Make sure the chat creator is recorded as an owner participant too, so a
	// freshly promoted group lists everyone (the owner is otherwise implicit).
	if _, err := tx.ExecContext(r.Context(),
		`INSERT OR IGNORE INTO chat_participants (chat_id, user_id, role)
		 SELECT id, created_by, 'owner' FROM chats WHERE id = ? AND created_by IS NOT NULL`,
		chatID); err != nil {
		h.logger.Error("seed owner participant", "err", err)
		replyError(w, http.StatusInternalServerError, "internal")
		return
	}
	if _, err := tx.ExecContext(r.Context(),
		`INSERT OR IGNORE INTO chat_participants (chat_id, user_id, role) VALUES (?, ?, ?)`,
		chatID, body.UserID, role); err != nil {
		h.logger.Error("add participant", "err", err)
		replyError(w, http.StatusInternalServerError, "internal")
		return
	}
	// Promote the chat to a group — the agent now responds only on @mention.
	if _, err := tx.ExecContext(r.Context(),
		`UPDATE chats SET visibility = 'group' WHERE id = ?`, chatID); err != nil {
		h.logger.Error("promote chat to group", "err", err)
		replyError(w, http.StatusInternalServerError, "internal")
		return
	}
	if err := tx.Commit(); err != nil {
		replyError(w, http.StatusInternalServerError, "internal")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *ChatParticipantsHandler) Remove(w http.ResponseWriter, r *http.Request) {
	chatID := r.PathValue("chatId")
	userID := r.PathValue("userId")
	user := UserFromContext(r.Context())
	if user == nil {
		replyError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	ws, ok := h.chatWorkspace(r, chatID)
	if !ok {
		replyError(w, http.StatusNotFound, "chat not found")
		return
	}
	if !h.canManageChat(r, chatID, ws, user.ID) {
		replyError(w, http.StatusForbidden, "only the chat owner or a workspace admin can manage participants")
		return
	}
	// The chat creator is the implicit owner — removing them would orphan the
	// group roster, so reject it. (Add re-seeds created_by as an owner row.)
	var createdBy sql.NullString
	if err := h.db.QueryRowContext(r.Context(),
		"SELECT created_by FROM chats WHERE id = ?", chatID).Scan(&createdBy); err == nil &&
		createdBy.Valid && createdBy.String == userID {
		replyError(w, http.StatusBadRequest, "cannot remove the chat owner")
		return
	}
	if _, err := h.db.ExecContext(r.Context(),
		`DELETE FROM chat_participants WHERE chat_id = ? AND user_id = ?`,
		chatID, userID); err != nil {
		h.logger.Error("remove participant", "err", err)
		replyError(w, http.StatusInternalServerError, "internal")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
