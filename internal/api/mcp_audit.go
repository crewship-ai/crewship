package api

import (
	"database/sql"
	"log/slog"
	"net/http"
)

// MCPAuditHandler provides the public API for querying MCP tool call audit records.
type MCPAuditHandler struct {
	db     *sql.DB
	logger *slog.Logger
}

func NewMCPAuditHandler(db *sql.DB, logger *slog.Logger) *MCPAuditHandler {
	return &MCPAuditHandler{db: db, logger: logger}
}

type mcpToolCallEntry struct {
	ID             string  `json:"id"`
	WorkspaceID    string  `json:"workspace_id"`
	CrewID         *string `json:"crew_id"`
	AgentID        string  `json:"agent_id"`
	MCPServerID    string  `json:"mcp_server_id"`
	MCPServerScope string  `json:"mcp_server_scope"`
	ToolName       string  `json:"tool_name"`
	InputHash      *string `json:"input_hash"`
	Status         string  `json:"status"`
	DurationMS     *int64  `json:"duration_ms"`
	ErrorMessage   *string `json:"error_message"`
	CreatedAt      string  `json:"created_at"`
}

// List returns MCP tool call audit records filtered by workspace and optional agent/date.
func (h *MCPAuditHandler) List(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())

	query := `SELECT id, workspace_id, crew_id, agent_id, mcp_server_id, mcp_server_scope,
		tool_name, input_hash, status, duration_ms, error_message, created_at
		FROM mcp_tool_calls WHERE workspace_id = ?`
	args := []interface{}{workspaceID}

	if agentID := r.URL.Query().Get("agent_id"); agentID != "" {
		query += " AND agent_id = ?"
		args = append(args, agentID)
	}
	if serverID := r.URL.Query().Get("server_id"); serverID != "" {
		query += " AND mcp_server_id = ?"
		args = append(args, serverID)
	}
	if status := r.URL.Query().Get("status"); status != "" {
		query += " AND status = ?"
		args = append(args, status)
	}
	if since := r.URL.Query().Get("since"); since != "" {
		query += " AND datetime(created_at) >= datetime(?)"
		args = append(args, since)
	}
	if until := r.URL.Query().Get("until"); until != "" {
		query += " AND datetime(created_at) <= datetime(?)"
		args = append(args, until)
	}

	query += " ORDER BY created_at DESC LIMIT 200"

	rows, err := h.db.QueryContext(r.Context(), query, args...)
	if err != nil {
		h.logger.Error("list mcp tool calls", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	defer rows.Close()

	var results []mcpToolCallEntry
	for rows.Next() {
		var e mcpToolCallEntry
		if err := rows.Scan(&e.ID, &e.WorkspaceID, &e.CrewID, &e.AgentID,
			&e.MCPServerID, &e.MCPServerScope, &e.ToolName, &e.InputHash,
			&e.Status, &e.DurationMS, &e.ErrorMessage, &e.CreatedAt); err != nil {
			h.logger.Error("scan mcp tool call", "error", err)
			continue
		}
		results = append(results, e)
	}
	if results == nil {
		results = []mcpToolCallEntry{}
	}
	writeJSON(w, http.StatusOK, results)
}
