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
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid JSON"})
		return
	}
	if body.ChatID == "" || body.AgentID == "" || body.WorkspaceID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "chat_id, agent_id, workspace_id required"})
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
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	writeJSON(w, http.StatusCreated, map[string]string{"id": body.ChatID, "status": "created"})
}

// ResolveChat looks up a chat's agent and returns the full agent configuration.
// GET /api/v1/internal/chats/{chatId}/resolve
func (h *InternalHandler) ResolveChat(w http.ResponseWriter, r *http.Request) {
	chatID := r.PathValue("chatId")

	var agentID string
	err := h.db.QueryRowContext(r.Context(), "SELECT agent_id FROM chats WHERE id = ?", chatID).Scan(&agentID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "Chat not found"})
			return
		}
		h.logger.Error("resolve chat lookup", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	h.resolveAgentConfig(w, r, agentID)
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
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid delta"})
		return
	}
	_, err := h.db.ExecContext(r.Context(),
		"UPDATE chats SET message_count = message_count + ? WHERE id = ?",
		body.Delta, chatID)
	if err != nil {
		h.logger.Error("increment message count", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
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
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "title required"})
		return
	}
	res, err := h.db.ExecContext(r.Context(),
		"UPDATE chats SET title = ? WHERE id = ? AND (title IS NULL OR title = '')",
		body.Title, chatID)
	if err != nil {
		h.logger.Error("update chat title", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Chat not found or already titled"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": chatID, "title": body.Title})
}
