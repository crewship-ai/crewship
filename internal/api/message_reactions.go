package api

import (
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
)

// MessageReactionsHandler serves CRUD for emoji reactions on individual
// chat messages. Reactions are scoped per (chat, message, emoji, user)
// so a user cannot stack the same emoji on the same message twice.
//
// Endpoints:
//
//	GET    /api/v1/chats/{chatId}/messages/{messageId}/reactions
//	POST   /api/v1/chats/{chatId}/messages/{messageId}/reactions   {emoji}
//	DELETE /api/v1/chats/{chatId}/messages/{messageId}/reactions/{emoji}
type MessageReactionsHandler struct {
	db     *sql.DB
	logger *slog.Logger
}

func NewMessageReactionsHandler(db *sql.DB, logger *slog.Logger) *MessageReactionsHandler {
	return &MessageReactionsHandler{db: db, logger: logger}
}

type reactionRow struct {
	Emoji string `json:"emoji"`
	Count int    `json:"count"`
	Mine  bool   `json:"mine"`
}

// ensureChatVisible derives the chat's workspace from the chat row and
// confirms the authenticated user is a member. Routes are mounted as
// `/api/v1/chats/{chatId}/...` (no workspace_id in path/query), so we
// can't rely on RequireWorkspace — the handler enforces tenancy itself.
func (h *MessageReactionsHandler) ensureChatVisible(r *http.Request, chatID string) bool {
	if chatID == "" {
		return false
	}
	user := UserFromContext(r.Context())
	if user == nil {
		return false
	}
	var owner string
	err := h.db.QueryRowContext(r.Context(),
		"SELECT workspace_id FROM chats WHERE id = ?", chatID).Scan(&owner)
	if err != nil {
		return false
	}
	var role string
	err = h.db.QueryRowContext(r.Context(),
		"SELECT role FROM workspace_members WHERE workspace_id = ? AND user_id = ?",
		owner, user.ID).Scan(&role)
	return err == nil
}

func (h *MessageReactionsHandler) List(w http.ResponseWriter, r *http.Request) {
	chatID := r.PathValue("chatId")
	messageID := r.PathValue("messageId")
	if !h.ensureChatVisible(r, chatID) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "chat not found"})
		return
	}
	var userID string
	if u := UserFromContext(r.Context()); u != nil {
		userID = u.ID
	}
	rows, err := h.db.QueryContext(r.Context(), `
SELECT emoji,
       COUNT(*) AS cnt,
       SUM(CASE WHEN user_id = ? THEN 1 ELSE 0 END) AS mine_cnt
FROM message_reactions
WHERE chat_id = ? AND message_id = ?
GROUP BY emoji
ORDER BY cnt DESC, emoji ASC`, userID, chatID, messageID)
	if err != nil {
		h.logger.Error("list reactions", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal"})
		return
	}
	defer rows.Close()
	out := []reactionRow{}
	for rows.Next() {
		var rr reactionRow
		var mineCnt int
		if err := rows.Scan(&rr.Emoji, &rr.Count, &mineCnt); err != nil {
			h.logger.Error("list reactions scan", "err", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal"})
			return
		}
		rr.Mine = mineCnt > 0
		out = append(out, rr)
	}
	if err := rows.Err(); err != nil {
		h.logger.Error("list reactions rows", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"reactions": out})
}

func (h *MessageReactionsHandler) Add(w http.ResponseWriter, r *http.Request) {
	chatID := r.PathValue("chatId")
	messageID := r.PathValue("messageId")
	// Auth check first — ensureChatVisible itself reads the user from
	// context and returns false on missing user, which would surface as
	// 404 instead of the correct 401.
	user := UserFromContext(r.Context())
	if user == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	if !h.ensureChatVisible(r, chatID) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "chat not found"})
		return
	}
	var body struct {
		Emoji string `json:"emoji"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Emoji == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "emoji required"})
		return
	}
	if l := len([]rune(body.Emoji)); l == 0 || l > 8 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "emoji length invalid"})
		return
	}
	id := generateCUID()
	// UNIQUE(chat_id, message_id, emoji, user_id) — INSERT OR IGNORE
	// makes the operation idempotent (one user, one emoji, one row).
	_, err := h.db.ExecContext(r.Context(),
		`INSERT OR IGNORE INTO message_reactions (id, chat_id, message_id, emoji, user_id) VALUES (?, ?, ?, ?, ?)`,
		id, chatID, messageID, body.Emoji, user.ID)
	if err != nil {
		h.logger.Error("add reaction", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *MessageReactionsHandler) Remove(w http.ResponseWriter, r *http.Request) {
	chatID := r.PathValue("chatId")
	messageID := r.PathValue("messageId")
	emoji := r.PathValue("emoji")
	// Auth check first — see comment on Add().
	user := UserFromContext(r.Context())
	if user == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	if !h.ensureChatVisible(r, chatID) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "chat not found"})
		return
	}
	_, err := h.db.ExecContext(r.Context(),
		`DELETE FROM message_reactions WHERE chat_id = ? AND message_id = ? AND emoji = ? AND user_id = ?`,
		chatID, messageID, emoji, user.ID)
	if err != nil {
		h.logger.Error("remove reaction", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal"})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
