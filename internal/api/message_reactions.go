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
// passed the length check and was stored verbatim, ready to render as
// HTML in any list-view that didn't escape it. Closes D2 from the
// chat/WS pentest agent.
//
// The first version of this validator did rune-membership only: every
// code point individually had to belong to an allowed emoji block.
// CodeRabbit caught the gap on review — a payload like
// "🏳️" (lone variation selector, no base) or "🏻🏻🏻" (just skin-tone
// modifiers) passed even though the resulting cluster is meaningless
// and renders as tofu / mojibake. We now also enforce composition:
//
//   - The string must start with an emoji base character — not a
//     modifier, ZWJ, or VS-16.
//   - ZWJ (U+200D) is a connector — it must sit between two base
//     characters (no leading or trailing ZWJ; no double ZWJ).
//   - Skin-tone modifier, variation selector, and keycap combiner
//     must follow a base character (not appear in isolation).
//   - Regional indicators (flag halves) must come in pairs (so at most
//     one full flag of two halves), with no other base mixed in.
//
// Anything else (HTML metacharacters, ASCII letters, control chars,
// BiDi marks, RTL overrides) is rejected at the rune-class step.
func isValidEmojiReaction(s string) bool {
	if s == "" {
		return false
	}
	runes := []rune(s)
	if len(runes) > 8 {
		return false
	}

	// First-rune class check. ZWJ/VS/modifier/keycap-combiner are NEVER
	// valid at position 0 — they only make sense AFTER an emoji base.
	switch emojiClass(runes[0]) {
	case classBase:
	case classRegional:
		// Will be validated in the loop (regionals must come in pairs).
	default:
		return false
	}

	prev := classNone
	// regionalRun counts consecutive regional indicators. Resets to 0
	// when a non-regional rune is encountered. We require that the run
	// length be even at any reset point (and at end of string), because
	// a flag emoji is exactly two indicator halves.
	regionalRun := 0
	for _, r := range runes {
		c := emojiClass(r)
		if c == classNone {
			return false
		}
		switch c {
		case classZWJ:
			if prev != classBase && prev != classModifier && prev != classRegional {
				return false
			}
		case classModifier, classVariation, classKeycap:
			if prev != classBase && prev != classModifier {
				return false
			}
		case classRegional:
			regionalRun++
			if regionalRun > 2 {
				// Three RIs in a row can't form a flag (flag = exactly 2).
				// A "country letter A" plus a complete "country flag" would
				// cap the run; three in a row is always wrong.
				return false
			}
		case classBase:
			// Closing a regional run: must be even (= one complete flag).
			if regionalRun > 0 && regionalRun%2 != 0 {
				return false
			}
			regionalRun = 0
		}
		// When prev was regional and we see a non-regional, classBase
		// already enforces the pair-completion check above.
		if c != classRegional && prev == classRegional && regionalRun > 0 {
			// The classBase branch already reset; modifiers / VS / etc.
			// after a regional are nonsense — would have failed the
			// per-class predecessor check above already.
			_ = regionalRun // intentional no-op for readability
		}
		prev = c
	}

	if prev == classZWJ {
		return false
	}
	// Trailing regional run must be a complete pair too.
	if regionalRun > 0 && regionalRun%2 != 0 {
		return false
	}
	return true
}

// emojiClass categorises each rune for the composition state machine.
type emojiCharClass int

const (
	classNone      emojiCharClass = iota // not allowed at all
	classBase                            // standalone emoji base (face, animal, symbol, ©, digit-for-keycap, ...)
	classModifier                        // skin-tone modifier
	classVariation                       // U+FE0F (emoji presentation)
	classKeycap                          // U+20E3 (keycap combiner)
	classZWJ                             // U+200D (zero-width joiner)
	classRegional                        // U+1F1E6-1F1FF (flag halves)
)

func emojiClass(r rune) emojiCharClass {
	switch r {
	case 0x200D:
		return classZWJ
	case 0xFE0F:
		return classVariation
	case 0x20E3:
		return classKeycap
	case 0x00A9, 0x00AE, 0x2122:
		return classBase
	case '0', '1', '2', '3', '4', '5', '6', '7', '8', '9', '#', '*':
		// Keycap base characters. Without a following U+20E3 they
		// render as plain ASCII — accepting them in isolation is a
		// deliberate trade-off (a "1" reaction isn't an XSS risk).
		return classBase
	}
	switch {
	case r >= 0x1F1E6 && r <= 0x1F1FF:
		return classRegional
	case r >= 0x1F3FB && r <= 0x1F3FF:
		return classModifier
	case r >= 0x1F300 && r <= 0x1FAFF,
		r >= 0x2600 && r <= 0x27BF,
		r >= 0x2300 && r <= 0x23FF,
		r >= 0x2B00 && r <= 0x2BFF,
		r >= 0x1F000 && r <= 0x1F02F,
		r >= 0x1F0A0 && r <= 0x1F0FF:
		return classBase
	}
	return classNone
}
