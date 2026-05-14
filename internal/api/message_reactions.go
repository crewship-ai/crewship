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
		replyError(w, http.StatusNotFound, "chat not found")
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
		replyError(w, http.StatusInternalServerError, "internal")
		return
	}
	defer rows.Close()
	out := []reactionRow{}
	for rows.Next() {
		var rr reactionRow
		var mineCnt int
		if err := rows.Scan(&rr.Emoji, &rr.Count, &mineCnt); err != nil {
			h.logger.Error("list reactions scan", "err", err)
			replyError(w, http.StatusInternalServerError, "internal")
			return
		}
		rr.Mine = mineCnt > 0
		out = append(out, rr)
	}
	if err := rows.Err(); err != nil {
		h.logger.Error("list reactions rows", "err", err)
		replyError(w, http.StatusInternalServerError, "internal")
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
		replyError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if !h.ensureChatVisible(r, chatID) {
		replyError(w, http.StatusNotFound, "chat not found")
		return
	}
	var body struct {
		Emoji string `json:"emoji"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Emoji == "" {
		replyError(w, http.StatusBadRequest, "emoji required")
		return
	}
	if !isValidEmojiReaction(body.Emoji) {
		replyError(w, http.StatusBadRequest, "emoji must be 1-8 emoji code points")
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
		replyError(w, http.StatusInternalServerError, "internal")
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
		replyError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if !h.ensureChatVisible(r, chatID) {
		replyError(w, http.StatusNotFound, "chat not found")
		return
	}
	_, err := h.db.ExecContext(r.Context(),
		`DELETE FROM message_reactions WHERE chat_id = ? AND message_id = ? AND emoji = ? AND user_id = ?`,
		chatID, messageID, emoji, user.ID)
	if err != nil {
		h.logger.Error("remove reaction", "err", err)
		replyError(w, http.StatusInternalServerError, "internal")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// isValidEmojiReaction enforces a tight allowlist on the emoji string an
// authenticated user can attach to a message. Pre-fix the only check
// was rune-length ≤ 8 — a payload like {"emoji":"<img onerror='x'>"}
// passed the length check (16 runes — fixed: 8 was actual count, but
// any HTML-special-char payload of length ≤ 8 also slipped through)
// and was stored verbatim, ready to render as HTML in any list-view
// that didn't escape it. Closes D2 from the chat/WS pentest agent.
//
// Allowed code points:
//   - Standard emoji blocks (U+1F300–U+1FAFF, U+2600–U+27BF,
//     U+2300–U+23FF, U+2B00–U+2BFF, U+1F000–U+1F02F, U+1F0A0–U+1F0FF)
//   - Regional-indicator letters (U+1F1E6–U+1F1FF) for flag composition
//   - ZWJ (U+200D) for compound emoji (👨‍💻, 🏳️‍🌈, etc.)
//   - Emoji variation selector (U+FE0F)
//   - Skin-tone modifiers (U+1F3FB–U+1F3FF)
//   - Keycap combiner (U+20E3) and ASCII digits/asterisk for keycap emoji
//   - U+00A9, U+00AE (© ®) and U+2122 (™) — common legacy text-symbol emoji
//
// Anything else (HTML metacharacters, ASCII letters, control chars, BiDi
// marks, RTL overrides) is rejected.
func isValidEmojiReaction(s string) bool {
	if s == "" {
		return false
	}
	count := 0
	for _, r := range s {
		count++
		if count > 8 {
			return false
		}
		if !isAllowedEmojiRune(r) {
			return false
		}
	}
	return count >= 1
}

func isAllowedEmojiRune(r rune) bool {
	switch r {
	case 0x200D, 0xFE0F, 0x20E3, 0x00A9, 0x00AE, 0x2122:
		return true
	case '0', '1', '2', '3', '4', '5', '6', '7', '8', '9', '#', '*':
		// keycap base characters — only meaningful when followed by U+20E3,
		// but rune-by-rune validation can't enforce sequence; accepting
		// these in isolation is a deliberate trade-off (keycap-prefix
		// reactions are unusual but not dangerous).
		return true
	}
	switch {
	case r >= 0x1F1E6 && r <= 0x1F1FF: // regional indicators (flags)
		return true
	case r >= 0x1F3FB && r <= 0x1F3FF: // skin-tone modifiers
		return true
	case r >= 0x1F300 && r <= 0x1FAFF: // miscellaneous emoji blocks (broadest)
		return true
	case r >= 0x2600 && r <= 0x27BF: // misc symbols + dingbats
		return true
	case r >= 0x2300 && r <= 0x23FF: // misc technical (incl. ⌚ ⏰)
		return true
	case r >= 0x2B00 && r <= 0x2BFF: // misc symbols and arrows
		return true
	case r >= 0x1F000 && r <= 0x1F02F: // mahjong tiles
		return true
	case r >= 0x1F0A0 && r <= 0x1F0FF: // playing cards
		return true
	}
	return false
}
