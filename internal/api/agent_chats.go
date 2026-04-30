package api

import (
	"database/sql"
	"encoding/json"
	"net/http"

	"github.com/crewship-ai/crewship/internal/journal"
)

// ListChats returns all chat sessions for a given agent.
// GET /api/v1/agents/{agentId}/chats
func (h *AgentHandler) ListChats(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agentId")
	workspaceID := WorkspaceIDFromContext(r.Context())

	rows, err := h.db.QueryContext(r.Context(), `
		SELECT id, agent_id, workspace_id, title, mode, status,
			message_count, started_at, ended_at, created_at, origin
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
		Origin       *string `json:"origin"`
	}

	var result []chatResponse
	for rows.Next() {
		var c chatResponse
		if err := rows.Scan(&c.ID, &c.AgentID, &c.WorkspaceID, &c.Title,
			&c.Mode, &c.Status, &c.MessageCount,
			&c.StartedAt, &c.EndedAt, &c.CreatedAt, &c.Origin); err != nil {
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
		// Origin distinguishes how the session was started: "UI" (chat
		// page in the browser), "CLI" (`crewship run`), "WEBHOOK",
		// "CRON", "AGENT" (agent-to-agent assignment). The
		// SessionsSidebar renders a colored chip per origin. Unknown
		// or empty values are stored as NULL → no chip shown.
		Origin string `json:"origin"`
	}
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid request"})
		return
	}

	chatID := body.SessionID
	if chatID == "" {
		chatID = generateCUID()
	}

	// Whitelist allowed origin values; anything else becomes NULL so a
	// rogue caller can't shove arbitrary text into a UI-rendered chip.
	var origin sql.NullString
	switch body.Origin {
	case "UI", "CLI", "WEBHOOK", "CRON", "AGENT":
		origin = sql.NullString{String: body.Origin, Valid: true}
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
		`INSERT OR IGNORE INTO chats (id, agent_id, workspace_id, created_by, status, origin)
		 SELECT ?, ?, ?, ?, 'ACTIVE', ?
		 WHERE EXISTS (SELECT 1 FROM agents WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL)`,
		chatID, agentID, workspaceID, userID, origin, agentID, workspaceID)
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
//
// Reads from journal_entries (unified-journal Phase E). Up to 100 most
// recent runs scoped to the workspace + agent_id.
func (h *AgentHandler) ListRuns(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agentId")
	workspaceID := WorkspaceIDFromContext(r.Context())

	aggregated, _, err := journal.ListRuns(r.Context(), h.db, journal.RunsQuery{
		WorkspaceID: workspaceID,
		AgentID:     agentID,
		Limit:       100,
	})
	if err != nil {
		h.logger.Error("list agent runs", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	// Per-agent endpoint doesn't enrich with crew/agent names — caller
	// already knows the agent context. Convert directly.
	result := make([]runResponse, 0, len(aggregated))
	for _, ar := range aggregated {
		resp := runResponse{
			ID:           ar.ID,
			AgentID:      ar.AgentID,
			WorkspaceID:  ar.WorkspaceID,
			TriggerType:  ar.TriggerType,
			Status:       string(ar.Status),
			ErrorMessage: stringPtrOrNil(ar.ErrorMessage),
			ExitCode:     ar.ExitCode,
			CreatedAt:    formatRFC3339(ar.CreatedAt),
		}
		if ar.ChatID != "" {
			c := ar.ChatID
			resp.ChatID = &c
		}
		if ar.TriggeredBy != "" {
			t := ar.TriggeredBy
			resp.TriggeredBy = &t
		}
		if !ar.StartedAt.IsZero() {
			s := formatRFC3339(ar.StartedAt)
			resp.StartedAt = &s
		}
		if ar.FinishedAt != nil && !ar.FinishedAt.IsZero() {
			f := formatRFC3339(*ar.FinishedAt)
			resp.FinishedAt = &f
		}
		if ar.Metadata != nil {
			if b, jerr := json.Marshal(ar.Metadata); jerr == nil {
				resp.Metadata = b
			}
		}
		result = append(result, resp)
	}
	writeJSON(w, http.StatusOK, result)
}
