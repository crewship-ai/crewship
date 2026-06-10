package api

import (
	"database/sql"
	"errors"
	"net/http"
	"time"
)

// CreateChat creates a new chat session record on behalf of the sidecar.
// POST /api/v1/internal/chats
func (h *InternalHandler) CreateChat(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ChatID      string  `json:"chat_id"`
		AgentID     string  `json:"agent_id"`
		WorkspaceID string  `json:"workspace_id"`
		UserID      *string `json:"user_id"`
		Title       *string `json:"title"`
	}
	if err := readJSON(r, &body); err != nil {
		replyError(w, http.StatusBadRequest, "Invalid JSON")
		return
	}
	if body.ChatID == "" || body.AgentID == "" || body.WorkspaceID == "" {
		replyError(w, http.StatusBadRequest, "chat_id, agent_id, workspace_id required")
		return
	}

	var existingID string
	if err := h.db.QueryRowContext(r.Context(), "SELECT id FROM chats WHERE id = ?", body.ChatID).Scan(&existingID); err == nil {
		writeJSON(w, http.StatusOK, map[string]string{"id": existingID, "status": "already_exists"})
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)
	_, err := h.db.ExecContext(r.Context(), `
		INSERT INTO chats (id, agent_id, workspace_id, created_by, title, mode, status, started_at, created_at)
		VALUES (?, ?, ?, ?, ?, 'CHAT', 'ACTIVE', ?, ?)`,
		body.ChatID, body.AgentID, body.WorkspaceID, body.UserID, body.Title, now, now)
	if err != nil {
		h.logger.Error("create chat", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	writeJSON(w, http.StatusCreated, map[string]string{"id": body.ChatID, "status": "created"})
}

// ResolveChat looks up a chat's agent and returns the full agent configuration.
// GET /api/v1/internal/chats/{chatId}/resolve
func (h *InternalHandler) ResolveChat(w http.ResponseWriter, r *http.Request) {
	chatID := r.PathValue("chatId")

	// PR-E F6: along with agent_id we read created_by so the resolver
	// can inject the right peer card at session start. created_by is
	// NULL for system-initiated chats (routine dispatch) — empty
	// OpenedByUserID downstream means "no peer card injection",
	// which is what we want for non-human-opened sessions.
	var (
		agentID  string
		openedBy sql.NullString
	)
	// Tenant scope (PR-F24 F-2). A workspace-bound token constrains the
	// chat lookup to its workspace; a foreign chat id then yields the
	// same ErrNoRows → 404 as a missing one (don't leak cross-tenant
	// existence). Master-token (host-side) callers have an empty scope
	// and keep the id-only behavior.
	chatQuery := "SELECT agent_id, created_by FROM chats WHERE id = ?"
	chatArgs := []any{chatID}
	if scope := InternalTokenWorkspaceFromContext(r.Context()); scope != "" {
		chatQuery += " AND workspace_id = ?"
		chatArgs = append(chatArgs, scope)
	}
	err := h.db.QueryRowContext(r.Context(), chatQuery, chatArgs...).Scan(&agentID, &openedBy)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			replyError(w, http.StatusNotFound, "Chat not found")
			return
		}
		h.logger.Error("resolve chat lookup", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	h.resolveAgentConfigWithOpener(w, r, agentID, openedBy.String)
}

// ResolveAgent returns the full configuration for a given agent ID.
// GET /api/v1/internal/agents/{agentId}/resolve
func (h *InternalHandler) ResolveAgent(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agentId")
	h.resolveAgentConfig(w, r, agentID)
}

// IncrementMessageCount increases the message_count on a chat by the given delta.
// POST /api/v1/internal/chats/{chatId}/messages
func (h *InternalHandler) IncrementMessageCount(w http.ResponseWriter, r *http.Request) {
	chatID := r.PathValue("chatId")
	var body struct {
		Delta int `json:"delta"`
	}
	if err := readJSON(r, &body); err != nil || body.Delta <= 0 {
		replyError(w, http.StatusBadRequest, "Invalid delta")
		return
	}
	// Tenant scope (PR-F24 F-5): a bound token may only bump its own
	// workspace's chats. A foreign chat id matches zero rows → 404 below.
	mcQuery := "UPDATE chats SET message_count = message_count + ? WHERE id = ?"
	mcArgs := []any{body.Delta, chatID}
	if scope := InternalTokenWorkspaceFromContext(r.Context()); scope != "" {
		mcQuery += " AND workspace_id = ?"
		mcArgs = append(mcArgs, scope)
	}
	res, err := h.db.ExecContext(r.Context(), mcQuery, mcArgs...)
	if err != nil {
		h.logger.Error("increment message count", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	// A non-existent chat ID still returned 200 OK — silent no-op masking
	// caller bugs (typo'd ID, race against deletion). Surface it as 404
	// so the caller can either retry resolution or log the broken
	// reference instead of trusting a phantom success.
	n, err := res.RowsAffected()
	if err != nil {
		h.logger.Error("increment message count rows affected", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	if n == 0 {
		replyError(w, http.StatusNotFound, "chat not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": chatID})
}

// UpdateChatTitle sets the title on a chat if it has not been set yet.
// PATCH /api/v1/internal/chats/{chatId}/title
func (h *InternalHandler) UpdateChatTitle(w http.ResponseWriter, r *http.Request) {
	chatID := r.PathValue("chatId")
	var body struct {
		Title string `json:"title"`
	}
	if err := readJSON(r, &body); err != nil || body.Title == "" {
		replyError(w, http.StatusBadRequest, "title required")
		return
	}
	// Tenant scope (PR-F24 F-5): a bound token may only title its own
	// workspace's chats; a foreign id matches zero rows → 404 below.
	titleQuery := "UPDATE chats SET title = ? WHERE id = ? AND (title IS NULL OR title = '')"
	titleArgs := []any{body.Title, chatID}
	if scope := InternalTokenWorkspaceFromContext(r.Context()); scope != "" {
		titleQuery += " AND workspace_id = ?"
		titleArgs = append(titleArgs, scope)
	}
	res, err := h.db.ExecContext(r.Context(), titleQuery, titleArgs...)
	if err != nil {
		h.logger.Error("update chat title", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	n, err := res.RowsAffected()
	if err != nil {
		h.logger.Error("update chat title rows affected", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	if n == 0 {
		replyError(w, http.StatusNotFound, "Chat not found or already titled")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": chatID, "title": body.Title})
}
