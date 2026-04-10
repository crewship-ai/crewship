package api

import (
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/ws"
)

// IntegrationHandler manages MCP server integrations at workspace, crew, and agent levels.
type IntegrationHandler struct {
	db     *sql.DB
	logger *slog.Logger
	hub    *ws.Hub
}

// NewIntegrationHandler creates an IntegrationHandler with the given database and logger.
func NewIntegrationHandler(db *sql.DB, logger *slog.Logger) *IntegrationHandler {
	return &IntegrationHandler{db: db, logger: logger}
}

// SetHub attaches a WebSocket hub for broadcasting integration change events.
func (h *IntegrationHandler) SetHub(hub *ws.Hub) { h.hub = hub }

func (h *IntegrationHandler) broadcastEvent(eventType, workspaceID string, payload map[string]string) {
	if h.hub == nil {
		return
	}
	channel := "workspace:" + workspaceID
	h.hub.Broadcast(channel, ws.ServerMessage{
		Type:    eventType,
		Channel: channel,
		Payload: payload,
	})
}

// --- Response types ---

type workspaceMCPServerResponse struct {
	ID               string  `json:"id"`
	WorkspaceID      string  `json:"workspace_id"`
	Name             string  `json:"name"`
	DisplayName      string  `json:"display_name"`
	Transport        string  `json:"transport"`
	Endpoint         *string `json:"endpoint"`
	Command          *string `json:"command"`
	ArgsJSON         *string `json:"args_json"`
	EnvJSON          *string `json:"env_json"`
	ConfigJSON       *string `json:"config_json"`
	Icon             *string `json:"icon"`
	Enabled          bool    `json:"enabled"`
	CreatedAt        string  `json:"created_at"`
	UpdatedAt        string  `json:"updated_at"`
	AgentBindCount   int     `json:"agent_binding_count"`
	CrewServerCount  int     `json:"crew_server_count"`
}

// --- Request types ---

type createWorkspaceIntegrationRequest struct {
	Name        string  `json:"name"`
	DisplayName string  `json:"display_name"`
	Transport   string  `json:"transport"`
	Endpoint    *string `json:"endpoint"`
	Command     *string `json:"command"`
	ArgsJSON    *string `json:"args_json"`
	EnvJSON     *string `json:"env_json"`
	ConfigJSON  *string `json:"config_json"`
	Icon        *string `json:"icon"`
}

type updateIntegrationRequest struct {
	DisplayName *string `json:"display_name"`
	Transport   *string `json:"transport"`
	Endpoint    *string `json:"endpoint"`
	Command     *string `json:"command"`
	ArgsJSON    *string `json:"args_json"`
	EnvJSON     *string `json:"env_json"`
	ConfigJSON  *string `json:"config_json"`
	Icon        *string `json:"icon"`
	Enabled     *bool   `json:"enabled"`
}

// ==========================================
// Workspace MCP Servers
// ==========================================

// ListWorkspaceIntegrations returns all workspace-level MCP server integrations.
// GET /api/v1/integrations/workspace
func (h *IntegrationHandler) ListWorkspaceIntegrations(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())

	rows, err := h.db.QueryContext(r.Context(), `
		SELECT ws.id, ws.workspace_id, ws.name, ws.display_name, ws.transport,
			ws.endpoint, ws.command, ws.args_json, ws.env_json, ws.config_json,
			ws.icon, ws.enabled, ws.created_at, ws.updated_at,
			(SELECT COUNT(*) FROM agent_mcp_bindings WHERE mcp_server_id = ws.id AND mcp_server_scope = 'workspace') AS bind_count,
			(SELECT COUNT(*) FROM crew_mcp_servers WHERE workspace_mcp_server_id = ws.id) AS crew_count
		FROM workspace_mcp_servers ws
		WHERE ws.workspace_id = ? AND ws.deleted_at IS NULL
		ORDER BY ws.created_at DESC`, workspaceID)
	if err != nil {
		h.logger.Error("list workspace integrations", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	defer rows.Close()

	var results []workspaceMCPServerResponse
	for rows.Next() {
		var s workspaceMCPServerResponse
		var enabled int
		if err := rows.Scan(&s.ID, &s.WorkspaceID, &s.Name, &s.DisplayName, &s.Transport,
			&s.Endpoint, &s.Command, &s.ArgsJSON, &s.EnvJSON, &s.ConfigJSON,
			&s.Icon, &enabled, &s.CreatedAt, &s.UpdatedAt,
			&s.AgentBindCount, &s.CrewServerCount); err != nil {
			h.logger.Error("scan workspace integration", "error", err)
			continue
		}
		s.Enabled = enabled == 1
		results = append(results, s)
	}
	if err := rows.Err(); err != nil {
		h.logger.Error("iterate workspace integrations", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	if results == nil {
		results = []workspaceMCPServerResponse{}
	}
	writeJSON(w, http.StatusOK, results)
}

// CreateWorkspaceIntegration adds a new workspace-level MCP server integration.
// POST /api/v1/integrations/workspace
func (h *IntegrationHandler) CreateWorkspaceIntegration(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())
	if !canRole(role, "manage") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Forbidden"})
		return
	}

	var req createWorkspaceIntegrationRequest
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid JSON body"})
		return
	}
	if req.Name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name is required"})
		return
	}
	if req.DisplayName == "" {
		req.DisplayName = req.Name
	}
	if req.Transport == "" {
		req.Transport = "streamable-http"
	}
	if req.Transport != "streamable-http" && req.Transport != "stdio" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "transport must be 'streamable-http' or 'stdio'"})
		return
	}
	if req.Transport == "streamable-http" && (req.Endpoint == nil || *req.Endpoint == "") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "endpoint is required for streamable-http transport"})
		return
	}
	if req.Transport == "stdio" && (req.Command == nil || *req.Command == "") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "command is required for stdio transport"})
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)
	id := generateCUID()

	_, err := h.db.ExecContext(r.Context(), `
		INSERT INTO workspace_mcp_servers (id, workspace_id, name, display_name, transport,
			endpoint, command, args_json, env_json, config_json, icon, enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?)`,
		id, workspaceID, req.Name, req.DisplayName, req.Transport,
		req.Endpoint, req.Command, req.ArgsJSON, req.EnvJSON, req.ConfigJSON, req.Icon, now, now)
	if err != nil {
		h.logger.Error("create workspace integration", "error", err)
		writeJSON(w, http.StatusConflict, map[string]string{"error": "Integration with this name already exists"})
		return
	}

	h.broadcastEvent("integration.created", workspaceID, map[string]string{
		"id": id, "name": req.Name, "scope": "workspace",
	})

	writeJSON(w, http.StatusCreated, workspaceMCPServerResponse{
		ID: id, WorkspaceID: workspaceID, Name: req.Name, DisplayName: req.DisplayName,
		Transport: req.Transport, Endpoint: req.Endpoint, Command: req.Command,
		ArgsJSON: req.ArgsJSON, EnvJSON: req.EnvJSON, ConfigJSON: req.ConfigJSON,
		Icon: req.Icon, Enabled: true, CreatedAt: now, UpdatedAt: now,
	})
}

// GetWorkspaceIntegration returns a single workspace-level MCP server integration by ID.
// GET /api/v1/integrations/workspace/{integrationId}
func (h *IntegrationHandler) GetWorkspaceIntegration(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	id := r.PathValue("integrationId")

	var s workspaceMCPServerResponse
	var enabled int
	err := h.db.QueryRowContext(r.Context(), `
		SELECT ws.id, ws.workspace_id, ws.name, ws.display_name, ws.transport,
			ws.endpoint, ws.command, ws.args_json, ws.env_json, ws.config_json,
			ws.icon, ws.enabled, ws.created_at, ws.updated_at,
			(SELECT COUNT(*) FROM agent_mcp_bindings WHERE mcp_server_id = ws.id AND mcp_server_scope = 'workspace') AS bind_count,
			(SELECT COUNT(*) FROM crew_mcp_servers WHERE workspace_mcp_server_id = ws.id) AS crew_count
		FROM workspace_mcp_servers ws
		WHERE ws.id = ? AND ws.workspace_id = ?`, id, workspaceID).Scan(
		&s.ID, &s.WorkspaceID, &s.Name, &s.DisplayName, &s.Transport,
		&s.Endpoint, &s.Command, &s.ArgsJSON, &s.EnvJSON, &s.ConfigJSON,
		&s.Icon, &enabled, &s.CreatedAt, &s.UpdatedAt,
		&s.AgentBindCount, &s.CrewServerCount)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Integration not found"})
		return
	}
	s.Enabled = enabled == 1
	writeJSON(w, http.StatusOK, s)
}

// UpdateWorkspaceIntegration modifies an existing workspace-level MCP server integration.
// PATCH /api/v1/integrations/workspace/{integrationId}
func (h *IntegrationHandler) UpdateWorkspaceIntegration(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())
	if !canRole(role, "manage") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Forbidden"})
		return
	}

	id := r.PathValue("integrationId")
	var req updateIntegrationRequest
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid JSON body"})
		return
	}

	// Verify exists
	var exists string
	if err := h.db.QueryRowContext(r.Context(),
		"SELECT id FROM workspace_mcp_servers WHERE id = ? AND workspace_id = ?",
		id, workspaceID).Scan(&exists); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Integration not found"})
		return
	}

	u := newUpdate()
	if req.DisplayName != nil {
		u.Set("display_name", *req.DisplayName)
	}
	if req.Transport != nil {
		if *req.Transport != "streamable-http" && *req.Transport != "stdio" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "transport must be 'streamable-http' or 'stdio'"})
			return
		}
		u.Set("transport", *req.Transport)
	}
	if req.Endpoint != nil {
		u.Set("endpoint", *req.Endpoint)
	}
	if req.Command != nil {
		u.Set("command", *req.Command)
	}
	if req.ArgsJSON != nil {
		u.Set("args_json", *req.ArgsJSON)
	}
	if req.EnvJSON != nil {
		u.Set("env_json", *req.EnvJSON)
	}
	if req.ConfigJSON != nil {
		u.Set("config_json", *req.ConfigJSON)
	}
	if req.Icon != nil {
		u.Set("icon", *req.Icon)
	}
	if req.Enabled != nil {
		enabled := 0
		if *req.Enabled {
			enabled = 1
		}
		u.Set("enabled", enabled)
	}

	// Validate transport/field combination against merged final state
	if req.Transport != nil {
		var existingEndpoint, existingCommand sql.NullString
		if err := h.db.QueryRowContext(r.Context(),
			"SELECT endpoint, command FROM workspace_mcp_servers WHERE id = ?", id).
			Scan(&existingEndpoint, &existingCommand); err != nil {
			h.logger.Error("load existing workspace integration", "id", id, "error", err)
		}

		finalEndpoint := existingEndpoint.String
		if req.Endpoint != nil {
			finalEndpoint = *req.Endpoint
		}
		finalCommand := existingCommand.String
		if req.Command != nil {
			finalCommand = *req.Command
		}

		if *req.Transport == "streamable-http" && finalEndpoint == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "endpoint is required for streamable-http transport"})
			return
		}
		if *req.Transport == "stdio" && finalCommand == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "command is required for stdio transport"})
			return
		}
	}

	query, args := u.Build("workspace_mcp_servers", "id = ? AND workspace_id = ?", id, workspaceID)
	if _, err := h.db.ExecContext(r.Context(), query, args...); err != nil {
		h.logger.Error("update workspace integration", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	h.broadcastEvent("integration.updated", workspaceID, map[string]string{
		"id": id, "scope": "workspace",
	})

	// Return updated record
	h.GetWorkspaceIntegration(w, r)
}

var errIntegrationNotFound = errors.New("integration not found")

// DeleteWorkspaceIntegration removes a workspace-level MCP server integration and its crew/agent bindings.
// DELETE /api/v1/integrations/workspace/{integrationId}
func (h *IntegrationHandler) DeleteWorkspaceIntegration(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())
	if !canRole(role, "manage") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Forbidden"})
		return
	}

	id := r.PathValue("integrationId")

	// Cascade: delete agent bindings → crew servers → workspace server
	err := database.WithTx(r.Context(), h.db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(r.Context(),
			"DELETE FROM agent_mcp_bindings WHERE mcp_server_id = ? AND mcp_server_scope = 'workspace'", id); err != nil {
			h.logger.Error("delete agent bindings for workspace server", "error", err)
			return err
		}

		if _, err := tx.ExecContext(r.Context(), `
			DELETE FROM agent_mcp_bindings WHERE mcp_server_scope = 'crew' AND mcp_server_id IN
			(SELECT id FROM crew_mcp_servers WHERE workspace_mcp_server_id = ?)`, id); err != nil {
			h.logger.Error("delete crew agent bindings", "error", err)
			return err
		}

		if _, err := tx.ExecContext(r.Context(),
			"DELETE FROM crew_mcp_servers WHERE workspace_mcp_server_id = ?", id); err != nil {
			h.logger.Error("delete crew server overrides", "error", err)
			return err
		}

		result, err := tx.ExecContext(r.Context(),
			"DELETE FROM workspace_mcp_servers WHERE id = ? AND workspace_id = ?", id, workspaceID)
		if err != nil {
			return err
		}
		affected, _ := result.RowsAffected()
		if affected == 0 {
			return errIntegrationNotFound
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, errIntegrationNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "Integration not found"})
		} else {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		}
		return
	}

	h.broadcastEvent("integration.deleted", workspaceID, map[string]string{
		"id": id, "scope": "workspace",
	})

	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}
