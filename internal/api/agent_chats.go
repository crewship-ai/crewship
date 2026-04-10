package api

import (
	"database/sql"
	"encoding/json"
	"net/http"
)

// ListChats returns all chat sessions for a given agent.
// GET /api/v1/agents/{agentId}/chats
func (h *AgentHandler) ListChats(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agentId")
	workspaceID := WorkspaceIDFromContext(r.Context())

	rows, err := h.db.QueryContext(r.Context(), `
		SELECT id, agent_id, workspace_id, title, mode, status,
			message_count, started_at, ended_at, created_at
		FROM chats
		WHERE agent_id = ? AND workspace_id = ?
		ORDER BY created_at DESC
		LIMIT 100
	`, agentID, workspaceID)
	if err != nil {
		h.logger.Error("list agent chats", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	defer rows.Close()

	type chatResponse struct {
		ID           string  `json:"id"`
		AgentID      string  `json:"agent_id"`
		WorkspaceID  string  `json:"workspace_id"`
		Title        *string `json:"title"`
		Mode         string  `json:"mode"`
		Status       string  `json:"status"`
		MessageCount int     `json:"message_count"`
		StartedAt    string  `json:"started_at"`
		EndedAt      *string `json:"ended_at"`
		CreatedAt    string  `json:"created_at"`
	}

	var result []chatResponse
	for rows.Next() {
		var c chatResponse
		if err := rows.Scan(&c.ID, &c.AgentID, &c.WorkspaceID, &c.Title,
			&c.Mode, &c.Status, &c.MessageCount,
			&c.StartedAt, &c.EndedAt, &c.CreatedAt); err != nil {
			h.logger.Error("scan chat", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
			return
		}
		result = append(result, c)
	}
	if err := rows.Err(); err != nil {
		h.logger.Error("rows iteration (chats)", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	if result == nil {
		result = []chatResponse{}
	}
	writeJSON(w, http.StatusOK, result)
}

// CreateChat starts a new chat session with the specified agent.
// POST /api/v1/agents/{agentId}/chats
func (h *AgentHandler) CreateChat(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agentId")
	workspaceID := WorkspaceIDFromContext(r.Context())
	userID := UserFromContext(r.Context()).ID

	var body struct {
		SessionID string `json:"session_id"`
	}
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid request"})
		return
	}

	chatID := body.SessionID
	if chatID == "" {
		chatID = generateCUID()
	}

	// Check agent exists
	found, err := agentExists(r.Context(), h.db, agentID, workspaceID)
	if err != nil {
		h.logger.Error("check agent exists", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Agent not found"})
		return
	}

	// Atomic upsert: insert only if agent is still active (prevents TOCTOU with soft-delete)
	_, err = h.db.ExecContext(r.Context(),
		`INSERT OR IGNORE INTO chats (id, agent_id, workspace_id, created_by, status)
		 SELECT ?, ?, ?, ?, 'ACTIVE'
		 WHERE EXISTS (SELECT 1 FROM agents WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL)`,
		chatID, agentID, workspaceID, userID, agentID, workspaceID)
	if err != nil {
		h.logger.Error("create chat", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	// Check outcome: either inserted, already existed (IGNORE), or agent was deleted
	var ownerAgentID string
	if err := h.db.QueryRowContext(r.Context(),
		"SELECT agent_id FROM chats WHERE id = ?", chatID).Scan(&ownerAgentID); err != nil {
		if err == sql.ErrNoRows {
			// No row: agent was deleted between preflight and INSERT (WHERE EXISTS failed)
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "Agent not found"})
			return
		}
		h.logger.Error("verify chat owner", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	if ownerAgentID != agentID {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "Chat belongs to a different agent"})
		return
	}

	writeJSON(w, http.StatusCreated, map[string]string{"id": chatID})
}

// ListRuns returns all execution runs for a given agent, ordered by most recent first.
// GET /api/v1/agents/{agentId}/runs
func (h *AgentHandler) ListRuns(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agentId")
	workspaceID := WorkspaceIDFromContext(r.Context())

	rows, err := h.db.QueryContext(r.Context(), `
		SELECT id, agent_id, chat_id, workspace_id, triggered_by,
			trigger_type, status, started_at, finished_at,
			error_message, exit_code, metadata, created_at
		FROM agent_runs
		WHERE agent_id = ? AND workspace_id = ?
		ORDER BY created_at DESC
		LIMIT 100
	`, agentID, workspaceID)
	if err != nil {
		h.logger.Error("list agent runs", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	defer rows.Close()

	var result []runResponse
	for rows.Next() {
		var run runResponse
		var metadataStr sql.NullString
		if err := rows.Scan(&run.ID, &run.AgentID, &run.ChatID, &run.WorkspaceID,
			&run.TriggeredBy, &run.TriggerType, &run.Status,
			&run.StartedAt, &run.FinishedAt, &run.ErrorMessage, &run.ExitCode,
			&metadataStr, &run.CreatedAt); err != nil {
			h.logger.Error("scan run", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
			return
		}
		if metadataStr.Valid {
			run.Metadata = json.RawMessage(metadataStr.String)
		}
		result = append(result, run)
	}
	if err := rows.Err(); err != nil {
		h.logger.Error("rows iteration (runs)", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	if result == nil {
		result = []runResponse{}
	}
	writeJSON(w, http.StatusOK, result)
}
