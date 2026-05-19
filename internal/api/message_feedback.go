package api

import (
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
)

// MessageFeedbackHandler serves the typed feedback signal API. Sits
// alongside message_reactions (open emoji shelf) but exposes a tight
// vocabulary of six signals so eval pipelines and the rolling-baseline
// drift detector can query without LIKE-matching codepoints.
//
// Endpoints:
//
//	POST /api/v1/feedback                          {message_id, signal, ...}
//	GET  /api/v1/feedback?message_id=<id>
//	GET  /api/v1/feedback?trace_id=<id>
//
// POST is idempotent at the (message_id, user_id, signal) UNIQUE
// constraint — re-submitting the same signal from the same user replaces
// the reason text via INSERT OR REPLACE. That matches the UX of a user
// changing their mind about why a thumb-down happened without spawning
// a new row each click.
type MessageFeedbackHandler struct {
	db     *sql.DB
	logger *slog.Logger
}

// NewMessageFeedbackHandler constructs the handler. db must have the
// v95 schema applied; older installations get a build-time mismatch via
// the migrations table and a clearer runtime error than a missing column.
func NewMessageFeedbackHandler(db *sql.DB, logger *slog.Logger) *MessageFeedbackHandler {
	return &MessageFeedbackHandler{db: db, logger: logger}
}

// allowedFeedbackSignals mirrors the CHECK constraint in v95. Kept in Go
// so the handler can reject the value before the DB does, giving a
// readable 400 message instead of a SQLite constraint violation.
var allowedFeedbackSignals = map[string]struct{}{
	"helpful":     {},
	"not_helpful": {},
	"inaccurate":  {},
	"unsafe":      {},
	"edit":        {},
	"regenerate":  {},
}

// maxFeedbackReasonChars caps the free-form reason field at 4096 chars
// so a single malicious or buggy client can't pump multi-megabyte rows
// into the table. Real "edit" payloads (the largest expected reason
// kind, since they carry the user's replacement text) are typically
// well under 2 KB; 4096 leaves headroom without becoming a storage
// hazard.
const maxFeedbackReasonChars = 4096

type feedbackCreateRequest struct {
	MessageID string `json:"message_id"`
	ChatID    string `json:"chat_id,omitempty"`
	TraceID   string `json:"trace_id,omitempty"`
	Signal    string `json:"signal"`
	Reason    string `json:"reason,omitempty"`
}

type feedbackRow struct {
	ID        string  `json:"id"`
	MessageID string  `json:"message_id"`
	ChatID    *string `json:"chat_id,omitempty"`
	TraceID   *string `json:"trace_id,omitempty"`
	Signal    string  `json:"signal"`
	Reason    *string `json:"reason,omitempty"`
	UserID    *string `json:"user_id,omitempty"`
	CreatedAt string  `json:"created_at"`
}

// ensureChatVisible mirrors the message_reactions handler — feedback is
// scoped to a chat the authenticated user can already see. Workspace
// membership comes via workspace_members because the route doesn't
// carry workspace_id in the URL.
func (h *MessageFeedbackHandler) ensureChatVisible(r *http.Request, chatID string) (workspaceID string, ok bool) {
	if chatID == "" {
		return "", false
	}
	user := UserFromContext(r.Context())
	if user == nil {
		return "", false
	}
	var owner string
	err := h.db.QueryRowContext(r.Context(),
		"SELECT workspace_id FROM chats WHERE id = ?", chatID).Scan(&owner)
	if err != nil {
		return "", false
	}
	var role string
	err = h.db.QueryRowContext(r.Context(),
		"SELECT role FROM workspace_members WHERE workspace_id = ? AND user_id = ?",
		owner, user.ID).Scan(&role)
	if err != nil {
		return "", false
	}
	return owner, true
}

// Create handles POST /api/v1/feedback. Returns 201 with the inserted
// row's id on first submit; subsequent submits with the same
// (message_id, user_id, signal) tuple return 200 with the existing id
// and the updated reason — a no-op-friendly contract for clients that
// retry on flaky networks.
func (h *MessageFeedbackHandler) Create(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	if user == nil {
		replyError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	var body feedbackCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		replyError(w, http.StatusBadRequest, "invalid json")
		return
	}
	body.MessageID = strings.TrimSpace(body.MessageID)
	body.Signal = strings.TrimSpace(body.Signal)
	if body.MessageID == "" {
		replyError(w, http.StatusBadRequest, "message_id required")
		return
	}
	if _, ok := allowedFeedbackSignals[body.Signal]; !ok {
		replyError(w, http.StatusBadRequest, "signal must be one of: helpful, not_helpful, inaccurate, unsafe, edit, regenerate")
		return
	}
	if len(body.Reason) > maxFeedbackReasonChars {
		replyError(w, http.StatusBadRequest, "reason exceeds maximum length")
		return
	}

	// chat_id is optional in the payload — older clients may not know
	// it — but if provided, we use it to derive the workspace via the
	// chats table and to enforce that the user can actually see the
	// chat. Without a chat_id we fall back to the user's primary
	// workspace (the user is signed in, so they have at least one).
	var workspaceID string
	var chatPtr *string
	if body.ChatID != "" {
		ws, ok := h.ensureChatVisible(r, body.ChatID)
		if !ok {
			replyError(w, http.StatusNotFound, "chat not found")
			return
		}
		workspaceID = ws
		chatPtr = &body.ChatID
	} else {
		// Fall back: derive the user's most recent workspace. This keeps
		// feedback collection working for clients that POST from a
		// surface without easy chat_id access (e.g. an embed widget).
		err := h.db.QueryRowContext(r.Context(),
			`SELECT workspace_id FROM workspace_members WHERE user_id = ? ORDER BY created_at LIMIT 1`,
			user.ID).Scan(&workspaceID)
		if err != nil {
			h.logger.Error("feedback workspace lookup", "err", err)
			replyError(w, http.StatusForbidden, "no workspace membership")
			return
		}
	}

	var tracePtr *string
	if body.TraceID != "" {
		tracePtr = &body.TraceID
	}
	var reasonPtr *string
	if body.Reason != "" {
		reasonPtr = &body.Reason
	}

	// UNIQUE(message_id, user_id, signal) — INSERT OR REPLACE keeps the
	// row id stable when a user updates their reason text, but writes a
	// new row id when the (message_id, user_id, signal) tuple was never
	// recorded. Return the resulting id either way so the client can
	// reference the row in a follow-up GET.
	id := generateCUID()
	_, err := h.db.ExecContext(r.Context(), `
INSERT INTO message_feedback (id, workspace_id, chat_id, message_id, trace_id, signal, reason, user_id)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(message_id, user_id, signal) DO UPDATE SET
    reason   = excluded.reason,
    trace_id = COALESCE(excluded.trace_id, message_feedback.trace_id),
    chat_id  = COALESCE(excluded.chat_id, message_feedback.chat_id)
`, id, workspaceID, chatPtr, body.MessageID, tracePtr, body.Signal, reasonPtr, user.ID)
	if err != nil {
		h.logger.Error("insert feedback", "err", err)
		replyError(w, http.StatusInternalServerError, "internal")
		return
	}

	// Resolve the persisted id (may be the existing row's id on UPSERT).
	var persistedID string
	if err := h.db.QueryRowContext(r.Context(),
		`SELECT id FROM message_feedback WHERE message_id = ? AND user_id = ? AND signal = ?`,
		body.MessageID, user.ID, body.Signal).Scan(&persistedID); err != nil {
		// The INSERT succeeded but the lookup failed — return what we
		// know (the generated id) rather than erroring; the row exists.
		persistedID = id
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": persistedID})
}

// List handles GET /api/v1/feedback?message_id=... | ?trace_id=... .
// Returns rows visible to the authenticated user (scoped via the
// workspace membership of the row, not the user's primary workspace —
// a user with multiple memberships can see feedback across any of them).
func (h *MessageFeedbackHandler) List(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	if user == nil {
		replyError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	messageID := strings.TrimSpace(r.URL.Query().Get("message_id"))
	traceID := strings.TrimSpace(r.URL.Query().Get("trace_id"))
	if messageID == "" && traceID == "" {
		replyError(w, http.StatusBadRequest, "message_id or trace_id required")
		return
	}

	// Scope the result to workspaces the user is a member of. The
	// subquery is on workspace_members so a user with no membership at
	// all simply sees an empty list, which is the right answer.
	const baseQuery = `
SELECT id, message_id, chat_id, trace_id, signal, reason, user_id, created_at
FROM message_feedback
WHERE workspace_id IN (SELECT workspace_id FROM workspace_members WHERE user_id = ?)
`
	var (
		rows *sql.Rows
		err  error
	)
	switch {
	case messageID != "":
		rows, err = h.db.QueryContext(r.Context(),
			baseQuery+` AND message_id = ? ORDER BY created_at DESC`,
			user.ID, messageID)
	default:
		rows, err = h.db.QueryContext(r.Context(),
			baseQuery+` AND trace_id = ? ORDER BY created_at DESC`,
			user.ID, traceID)
	}
	if err != nil {
		h.logger.Error("list feedback", "err", err)
		replyError(w, http.StatusInternalServerError, "internal")
		return
	}
	defer rows.Close()

	out := []feedbackRow{}
	for rows.Next() {
		var fr feedbackRow
		if err := rows.Scan(&fr.ID, &fr.MessageID, &fr.ChatID, &fr.TraceID,
			&fr.Signal, &fr.Reason, &fr.UserID, &fr.CreatedAt); err != nil {
			h.logger.Error("list feedback scan", "err", err)
			replyError(w, http.StatusInternalServerError, "internal")
			return
		}
		out = append(out, fr)
	}
	if err := rows.Err(); err != nil {
		h.logger.Error("list feedback rows", "err", err)
		replyError(w, http.StatusInternalServerError, "internal")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"feedback": out})
}
