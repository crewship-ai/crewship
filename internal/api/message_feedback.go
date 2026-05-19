package api

import (
	"database/sql"
	"encoding/json"
	"errors"
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

// maxFeedbackIDChars bounds id-shaped fields (message_id, chat_id,
// trace_id). Internal Crewship ids are CUIDs (~25 chars); OTel
// trace_ids are 32 hex chars. Allowing 256 here gives 10× headroom
// for future longer-prefixed schemes while keeping a 1 KB
// abuse-prevention ceiling well below the reason cap.
const maxFeedbackIDChars = 256

// maxFeedbackSignalChars caps the `signal` field beyond the enum check.
// The enum lookup itself runs in O(n) over the input string (Go map
// hashing iterates every byte); without a length cap, a 10 MB
// `?signal=...` query param would force a 10 MB hash before the
// expected miss. 64 chars is comfortably above the longest legal
// value ("regenerate" = 10) and well below abuse territory.
const maxFeedbackSignalChars = 64

// maxFeedbackBodyBytes caps the HTTP body BEFORE json.Decode runs.
// The field-level limits (maxFeedbackReasonChars + maxFeedbackIDChars
// × 3) cap memory at the application layer, but a malicious client
// could still force Decode to allocate a multi-MB string for `reason`
// before our trim/length check fires. http.MaxBytesReader bounds the
// parsing pass itself: anything over the cap fails the read and
// returns *http.MaxBytesError without touching the JSON decoder's
// allocator.
//
// 16 KiB = 4 × maxFeedbackReasonChars headroom for JSON overhead
// + the three id fields + signal + chat_id. Real payloads are well
// under 5 KiB.
const maxFeedbackBodyBytes = 16 * 1024

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
//
// Return shape distinguishes three states the caller must handle
// separately:
//
//   - (ws, true, nil)   — chat exists and the caller is a member
//   - ("", false, nil)  — chat doesn't exist OR caller isn't a member
//     (404 — we don't leak which one to avoid an
//     existence-probing oracle on chat ids)
//   - ("", false, err)  — DB outage / schema mismatch (500)
//
// An earlier version collapsed every error into the (false, nil)
// branch, hiding real outages behind false 404s. The eyes-only fix
// (treat sql.ErrNoRows as the only expected miss) is below.
func (h *MessageFeedbackHandler) ensureChatVisible(r *http.Request, chatID string) (workspaceID string, ok bool, err error) {
	if chatID == "" {
		return "", false, nil
	}
	user := UserFromContext(r.Context())
	if user == nil {
		return "", false, nil
	}
	var owner string
	err = h.db.QueryRowContext(r.Context(),
		"SELECT workspace_id FROM chats WHERE id = ?", chatID).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	var role string
	err = h.db.QueryRowContext(r.Context(),
		"SELECT role FROM workspace_members WHERE workspace_id = ? AND user_id = ?",
		owner, user.ID).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return owner, true, nil
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

	// Cap the request body BEFORE JSON parsing. MaxBytesReader returns
	// *http.MaxBytesError on overflow, which we surface as 413; any
	// other Decode error (malformed JSON, type mismatch) collapses to
	// 400 since they're equivalent client bugs from the API contract's
	// perspective.
	r.Body = http.MaxBytesReader(w, r.Body, maxFeedbackBodyBytes)
	var body feedbackCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			replyError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}
		replyError(w, http.StatusBadRequest, "invalid json")
		return
	}
	body.MessageID = strings.TrimSpace(body.MessageID)
	body.ChatID = strings.TrimSpace(body.ChatID)
	body.TraceID = strings.TrimSpace(body.TraceID)
	body.Signal = strings.TrimSpace(body.Signal)
	if body.MessageID == "" {
		replyError(w, http.StatusBadRequest, "message_id required")
		return
	}
	// Cap id-shaped fields at maxFeedbackIDChars. Without this, an
	// attacker can POST a 10 MB trace_id and exercise the partial index
	// every query. The cap is generous (10× longest realistic id) so
	// no legitimate caller trips it.
	if len(body.MessageID) > maxFeedbackIDChars ||
		len(body.ChatID) > maxFeedbackIDChars ||
		len(body.TraceID) > maxFeedbackIDChars {
		replyError(w, http.StatusBadRequest, "id field exceeds maximum length")
		return
	}
	if len(body.Signal) > maxFeedbackSignalChars {
		replyError(w, http.StatusBadRequest, "signal exceeds maximum length")
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

	// chat_id is optional in the payload — older clients (eval widgets,
	// CLI feedback) may not have one — but providing it is strongly
	// preferred. When present, we derive the workspace from the chat
	// row and verify the user is a member of that workspace.
	//
	// Message↔chat ownership is NOT validated here because messages
	// live in JSONL files (chats.jsonl_path), not a SQL table — a
	// per-POST file read would slow the path enough to discourage
	// real-time feedback collection. The threat model is acceptable:
	// workspaces are trust boundaries, so a workspace member filing
	// feedback against any chat in their workspace is by design.
	// Cross-tenant probes are blocked by ensureChatVisible. Forged
	// fabricated message_ids are visible at eval-mining time (the
	// grader joins back to the JSONL store and orphans are dropped).
	//
	// When absent we fall back to the user's MOST RECENT workspace
	// (ORDER BY created_at DESC). The previous version sorted ASC,
	// picking the oldest membership — defensible for "primary" but a
	// surprising default for a user whose primary membership has moved.
	// DESC matches the implicit "current active org" mental model.
	var workspaceID string
	var chatPtr *string
	if body.ChatID != "" {
		ws, ok, err := h.ensureChatVisible(r, body.ChatID)
		if err != nil {
			// DB outage / schema drift — surface as 500 instead of a
			// false 404 that would mask the real issue from the
			// operator (and confuse the client into retrying against
			// a non-existent chat).
			h.logger.Error("feedback chat visibility check", "err", err, "chat_id", body.ChatID)
			replyError(w, http.StatusInternalServerError, "internal")
			return
		}
		if !ok {
			replyError(w, http.StatusNotFound, "chat not found")
			return
		}
		workspaceID = ws
		chatPtr = &body.ChatID
	} else {
		err := h.db.QueryRowContext(r.Context(),
			`SELECT workspace_id FROM workspace_members WHERE user_id = ? ORDER BY created_at DESC LIMIT 1`,
			user.ID).Scan(&workspaceID)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			// User has no workspace memberships at all. 403 is honest
			// here — the call shape is valid, the caller just isn't
			// authorized to write anywhere.
			replyError(w, http.StatusForbidden, "no workspace membership")
			return
		case err != nil:
			h.logger.Error("feedback workspace lookup", "err", err)
			replyError(w, http.StatusInternalServerError, "internal")
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

	// UNIQUE(message_id, user_id, signal) — UPSERT keeps the row id
	// stable when a user updates their reason text and re-anchors the
	// workspace + chat references to the latest POST. Re-anchoring
	// matters when an earlier POST came in without chat_id (workspace
	// fallback) and a later one carries the real chat: without the
	// workspace_id overwrite the row stays parked in the fallback
	// workspace and List queries scoped to the actual workspace miss
	// it.
	id := generateCUID()
	_, err := h.db.ExecContext(r.Context(), `
INSERT INTO message_feedback (id, workspace_id, chat_id, message_id, trace_id, signal, reason, user_id)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(message_id, user_id, signal) DO UPDATE SET
    workspace_id = excluded.workspace_id,
    reason       = excluded.reason,
    trace_id     = COALESCE(excluded.trace_id, message_feedback.trace_id),
    chat_id      = COALESCE(excluded.chat_id, message_feedback.chat_id)
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

// Delete handles DELETE /api/v1/feedback?message_id=...&signal=... .
// Removes the authenticated user's feedback for the given (message_id,
// signal) tuple. Other users' rows on the same message are untouched.
// The route is the under-undo path for the chat UI: a thumb-down that
// gets toggled off must actually remove the row so the eval pipeline
// doesn't keep counting a retracted signal.
//
// Scoped via the workspace membership of the row, not chat ownership
// — a user can only delete their OWN feedback (user_id = current
// user), which is the strictest reasonable rule. We return 204 on
// successful delete AND on "row didn't exist" so the client can call
// DELETE freely without checking first.
func (h *MessageFeedbackHandler) Delete(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	if user == nil {
		replyError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	messageID := strings.TrimSpace(r.URL.Query().Get("message_id"))
	signal := strings.TrimSpace(r.URL.Query().Get("signal"))
	if messageID == "" || signal == "" {
		replyError(w, http.StatusBadRequest, "message_id and signal required")
		return
	}
	// Cap id-shaped query params at the same ceiling as the POST body's
	// equivalent fields. Without this, a workspace member could DoS-amplify
	// by spamming DELETE with a 6-8 KB message_id (URL limit) — each call
	// would drive an indexed lookup against an oversized key. The POST
	// path already enforces this via maxFeedbackIDChars; DELETE + List
	// inherited the gap.
	if len(messageID) > maxFeedbackIDChars {
		replyError(w, http.StatusBadRequest, "message_id exceeds maximum length")
		return
	}
	if len(signal) > maxFeedbackSignalChars {
		replyError(w, http.StatusBadRequest, "signal exceeds maximum length")
		return
	}
	if _, ok := allowedFeedbackSignals[signal]; !ok {
		replyError(w, http.StatusBadRequest, "unknown signal")
		return
	}
	if _, err := h.db.ExecContext(r.Context(),
		`DELETE FROM message_feedback WHERE message_id = ? AND user_id = ? AND signal = ?`,
		messageID, user.ID, signal); err != nil {
		h.logger.Error("delete feedback", "err", err)
		replyError(w, http.StatusInternalServerError, "internal")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// List handles GET /api/v1/feedback?message_id=... | ?trace_id=... .
// Returns ONLY the caller's own feedback rows for the requested
// message/trace. Feedback is private to the user who wrote it — even
// a workspace owner shouldn't be able to read another member's
// thumb-downs or "edit" reasons by polling this endpoint, both for
// the same reason a Slack reaction or a Google Docs comment isn't
// publicly enumerable: those signals carry candid feedback the user
// expects only the eval pipeline (server-side, not API-exposed) to
// see.
//
// An earlier version scoped only by workspace membership, which let
// any workspace member fetch every other member's rows. The bug was
// caught in PR review; the fix is the additional user_id = ? clause
// below. Workspace scope is kept as a defense-in-depth predicate so
// a cross-workspace probe still returns 0 rows.
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
	// Same DoS-amplification protection as POST + DELETE: cap both
	// id-shaped query params at maxFeedbackIDChars before they reach
	// the partial-index lookup.
	if len(messageID) > maxFeedbackIDChars || len(traceID) > maxFeedbackIDChars {
		replyError(w, http.StatusBadRequest, "id field exceeds maximum length")
		return
	}

	// Two predicates: the caller is the row's author AND the row's
	// workspace_id is one the caller belongs to. The second clause is
	// belt-and-suspenders — if a row's workspace ever gets corrupted
	// or the user is removed mid-query we still don't surface stale
	// cross-tenant data.
	const baseQuery = `
SELECT id, message_id, chat_id, trace_id, signal, reason, user_id, created_at
FROM message_feedback
WHERE user_id = ?
  AND workspace_id IN (SELECT workspace_id FROM workspace_members WHERE user_id = ?)
`
	var (
		rows *sql.Rows
		err  error
	)
	switch {
	case messageID != "":
		rows, err = h.db.QueryContext(r.Context(),
			baseQuery+` AND message_id = ? ORDER BY created_at DESC`,
			user.ID, user.ID, messageID)
	default:
		rows, err = h.db.QueryContext(r.Context(),
			baseQuery+` AND trace_id = ? ORDER BY created_at DESC`,
			user.ID, user.ID, traceID)
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
