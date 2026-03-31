package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/ws"
)

// IntegrationHandler manages MCP server integrations at workspace, crew, and agent levels.
type IntegrationHandler struct {
	db     *sql.DB
	logger *slog.Logger
	hub    *ws.Hub
}

func NewIntegrationHandler(db *sql.DB, logger *slog.Logger) *IntegrationHandler {
	return &IntegrationHandler{db: db, logger: logger}
}

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

type crewMCPServerResponse struct {
	ID                    string  `json:"id"`
	CrewID                string  `json:"crew_id"`
	WorkspaceMCPServerID  *string `json:"workspace_mcp_server_id"`
	Name                  string  `json:"name"`
	DisplayName           string  `json:"display_name"`
	Transport             string  `json:"transport"`
	Endpoint              *string `json:"endpoint"`
	Command               *string `json:"command"`
	ArgsJSON              *string `json:"args_json"`
	EnvJSON               *string `json:"env_json"`
	ConfigJSON            *string `json:"config_json"`
	Icon                  *string `json:"icon"`
	Enabled               bool    `json:"enabled"`
	CreatedAt             string  `json:"created_at"`
	UpdatedAt             string  `json:"updated_at"`
	AgentBindCount        int     `json:"agent_binding_count"`
}

type agentMCPBindingResponse struct {
	ID               string  `json:"id"`
	AgentID          string  `json:"agent_id"`
	MCPServerID      string  `json:"mcp_server_id"`
	MCPServerScope   string  `json:"mcp_server_scope"`
	CredentialID     *string `json:"credential_id"`
	CredType         *string `json:"cred_type"`
	CredHeader       *string `json:"cred_header"`
	Enabled          bool    `json:"enabled"`
	ConfigOverride   *string `json:"config_override_json"`
	CreatedAt        string  `json:"created_at"`
	ServerName       string  `json:"server_name"`
	ServerDisplay    string  `json:"server_display_name"`
	CredentialName   *string `json:"credential_name"`
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

type createCrewIntegrationRequest struct {
	WorkspaceMCPServerID *string `json:"workspace_mcp_server_id"`
	Name                 string  `json:"name"`
	DisplayName          string  `json:"display_name"`
	Transport            string  `json:"transport"`
	Endpoint             *string `json:"endpoint"`
	Command              *string `json:"command"`
	ArgsJSON             *string `json:"args_json"`
	EnvJSON              *string `json:"env_json"`
	ConfigJSON           *string `json:"config_json"`
	Icon                 *string `json:"icon"`
}

type createAgentBindingRequest struct {
	MCPServerID    string  `json:"mcp_server_id"`
	MCPServerScope string  `json:"mcp_server_scope"`
	CredentialID   *string `json:"credential_id"`
	CredType       *string `json:"cred_type"`      // "bearer", "api_key", "basic"
	CredHeader     *string `json:"cred_header"`     // custom header for api_key type
	EnvVarName     *string `json:"env_var_name"`    // env var name for stdio credential injection
	Enabled        *bool   `json:"enabled"`
	ConfigOverride *string `json:"config_override_json"`
}

type updateAgentBindingRequest struct {
	CredentialID   *string `json:"credential_id"`
	CredType       *string `json:"cred_type"`
	CredHeader     *string `json:"cred_header"`
	EnvVarName     *string `json:"env_var_name"`
	Enabled        *bool   `json:"enabled"`
	ConfigOverride *string `json:"config_override_json"`
}

// ==========================================
// Workspace MCP Servers
// ==========================================

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
	if results == nil {
		results = []workspaceMCPServerResponse{}
	}
	writeJSON(w, http.StatusOK, results)
}

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

	now := time.Now().UTC().Format(time.RFC3339)

	// Build dynamic UPDATE
	sets := []string{"updated_at = ?"}
	args := []interface{}{now}
	if req.DisplayName != nil {
		sets = append(sets, "display_name = ?")
		args = append(args, *req.DisplayName)
	}
	if req.Transport != nil {
		if *req.Transport != "streamable-http" && *req.Transport != "stdio" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "transport must be 'streamable-http' or 'stdio'"})
			return
		}
		sets = append(sets, "transport = ?")
		args = append(args, *req.Transport)
	}
	if req.Endpoint != nil {
		sets = append(sets, "endpoint = ?")
		args = append(args, *req.Endpoint)
	}
	if req.Command != nil {
		sets = append(sets, "command = ?")
		args = append(args, *req.Command)
	}
	if req.ArgsJSON != nil {
		sets = append(sets, "args_json = ?")
		args = append(args, *req.ArgsJSON)
	}
	if req.EnvJSON != nil {
		sets = append(sets, "env_json = ?")
		args = append(args, *req.EnvJSON)
	}
	if req.ConfigJSON != nil {
		sets = append(sets, "config_json = ?")
		args = append(args, *req.ConfigJSON)
	}
	if req.Icon != nil {
		sets = append(sets, "icon = ?")
		args = append(args, *req.Icon)
	}
	if req.Enabled != nil {
		enabled := 0
		if *req.Enabled {
			enabled = 1
		}
		sets = append(sets, "enabled = ?")
		args = append(args, enabled)
	}
	args = append(args, id, workspaceID)

	query := "UPDATE workspace_mcp_servers SET " + strings.Join(sets, ", ") + " WHERE id = ? AND workspace_id = ?"
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

func (h *IntegrationHandler) DeleteWorkspaceIntegration(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())
	if !canRole(role, "manage") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Forbidden"})
		return
	}

	id := r.PathValue("integrationId")

	// Cascade: delete agent bindings → crew servers → workspace server
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	// Delete agent bindings for this workspace server
	if _, err := tx.ExecContext(r.Context(),
		"DELETE FROM agent_mcp_bindings WHERE mcp_server_id = ? AND mcp_server_scope = 'workspace'", id); err != nil {
		tx.Rollback()
		h.logger.Error("delete agent bindings for workspace server", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	// Delete agent bindings for crew servers that override this workspace server
	if _, err := tx.ExecContext(r.Context(), `
		DELETE FROM agent_mcp_bindings WHERE mcp_server_scope = 'crew' AND mcp_server_id IN
		(SELECT id FROM crew_mcp_servers WHERE workspace_mcp_server_id = ?)`, id); err != nil {
		tx.Rollback()
		h.logger.Error("delete crew agent bindings", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	// Delete crew server overrides
	if _, err := tx.ExecContext(r.Context(),
		"DELETE FROM crew_mcp_servers WHERE workspace_mcp_server_id = ?", id); err != nil {
		tx.Rollback()
		h.logger.Error("delete crew server overrides", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	// Delete the workspace server itself
	result, err := tx.ExecContext(r.Context(),
		"DELETE FROM workspace_mcp_servers WHERE id = ? AND workspace_id = ?", id, workspaceID)
	if err != nil {
		tx.Rollback()
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		tx.Rollback()
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Integration not found"})
		return
	}
	if err := tx.Commit(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	h.broadcastEvent("integration.deleted", workspaceID, map[string]string{
		"id": id, "scope": "workspace",
	})

	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// ==========================================
// All crew integrations (cross-crew view for Integrations page)
// ==========================================

type crewIntegrationOverview struct {
	crewMCPServerResponse
	CrewName string `json:"crew_name"`
	CrewSlug string `json:"crew_slug"`
}

func (h *IntegrationHandler) ListAllCrewIntegrations(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())

	// Auto-migrate any crews that still have JSON blob config.
	blobRows, err := h.db.QueryContext(r.Context(), `
		SELECT id, mcp_config_json FROM crews
		WHERE workspace_id = ? AND mcp_config_json IS NOT NULL AND mcp_config_json != '' AND deleted_at IS NULL`,
		workspaceID)
	if err == nil {
		defer blobRows.Close()
		for blobRows.Next() {
			var cid, blob string
			if blobRows.Scan(&cid, &blob) == nil {
				if err := MigrateJSONBlobToCrewServers(r.Context(), h.db, h.logger, cid, workspaceID, blob); err != nil {
					h.logger.Warn("auto-migrate crew MCP config", "crew_id", cid, "error", err)
				}
			}
		}
	}

	rows, err := h.db.QueryContext(r.Context(), `
		SELECT cs.id, cs.crew_id, cs.workspace_mcp_server_id, cs.name, cs.display_name,
			cs.transport, cs.endpoint, cs.command, cs.args_json, cs.env_json, cs.config_json,
			cs.icon, cs.enabled, cs.created_at, cs.updated_at,
			c.name AS crew_name, c.slug AS crew_slug,
			(SELECT COUNT(*) FROM agent_mcp_bindings WHERE mcp_server_id = cs.id AND mcp_server_scope = 'crew') AS bind_count
		FROM crew_mcp_servers cs
		JOIN crews c ON c.id = cs.crew_id AND c.deleted_at IS NULL
		WHERE c.workspace_id = ? AND cs.deleted_at IS NULL
		ORDER BY c.name, cs.name`, workspaceID)
	if err != nil {
		h.logger.Error("list all crew integrations", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	defer rows.Close()

	var results []crewIntegrationOverview
	for rows.Next() {
		var s crewIntegrationOverview
		var enabled int
		if err := rows.Scan(&s.ID, &s.CrewID, &s.WorkspaceMCPServerID, &s.Name, &s.DisplayName,
			&s.Transport, &s.Endpoint, &s.Command, &s.ArgsJSON, &s.EnvJSON, &s.ConfigJSON,
			&s.Icon, &enabled, &s.CreatedAt, &s.UpdatedAt,
			&s.CrewName, &s.CrewSlug, &s.AgentBindCount); err != nil {
			h.logger.Error("scan crew integration overview", "error", err)
			continue
		}
		s.Enabled = enabled == 1
		results = append(results, s)
	}
	if results == nil {
		results = []crewIntegrationOverview{}
	}
	writeJSON(w, http.StatusOK, results)
}

// ==========================================
// Crew MCP Servers
// ==========================================

func (h *IntegrationHandler) ListCrewIntegrations(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	crewID := r.PathValue("crewId")

	// Verify crew belongs to workspace
	var crewExists string
	if err := h.db.QueryRowContext(r.Context(),
		"SELECT id FROM crews WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL",
		crewID, workspaceID).Scan(&crewExists); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Crew not found"})
		return
	}

	// Auto-migrate JSON blob if present.
	var mcpBlob sql.NullString
	_ = h.db.QueryRowContext(r.Context(),
		"SELECT mcp_config_json FROM crews WHERE id = ?", crewID).Scan(&mcpBlob)
	if mcpBlob.Valid && mcpBlob.String != "" {
		if err := MigrateJSONBlobToCrewServers(r.Context(), h.db, h.logger, crewID, workspaceID, mcpBlob.String); err != nil {
			h.logger.Warn("auto-migrate crew MCP config", "crew_id", crewID, "error", err)
		}
	}

	rows, err := h.db.QueryContext(r.Context(), `
		SELECT cs.id, cs.crew_id, cs.workspace_mcp_server_id, cs.name, cs.display_name,
			cs.transport, cs.endpoint, cs.command, cs.args_json, cs.env_json, cs.config_json,
			cs.icon, cs.enabled, cs.created_at, cs.updated_at,
			(SELECT COUNT(*) FROM agent_mcp_bindings WHERE mcp_server_id = cs.id AND mcp_server_scope = 'crew') AS bind_count
		FROM crew_mcp_servers cs
		WHERE cs.crew_id = ? AND cs.deleted_at IS NULL
		ORDER BY cs.created_at DESC`, crewID)
	if err != nil {
		h.logger.Error("list crew integrations", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	defer rows.Close()

	var results []crewMCPServerResponse
	for rows.Next() {
		var s crewMCPServerResponse
		var enabled int
		if err := rows.Scan(&s.ID, &s.CrewID, &s.WorkspaceMCPServerID, &s.Name, &s.DisplayName,
			&s.Transport, &s.Endpoint, &s.Command, &s.ArgsJSON, &s.EnvJSON, &s.ConfigJSON,
			&s.Icon, &enabled, &s.CreatedAt, &s.UpdatedAt, &s.AgentBindCount); err != nil {
			h.logger.Error("scan crew integration", "error", err)
			continue
		}
		s.Enabled = enabled == 1
		results = append(results, s)
	}
	if results == nil {
		results = []crewMCPServerResponse{}
	}
	writeJSON(w, http.StatusOK, results)
}

func (h *IntegrationHandler) CreateCrewIntegration(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())
	if !canRole(role, "create") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Forbidden"})
		return
	}

	crewID := r.PathValue("crewId")
	// Verify crew
	var crewExists string
	if err := h.db.QueryRowContext(r.Context(),
		"SELECT id FROM crews WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL",
		crewID, workspaceID).Scan(&crewExists); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Crew not found"})
		return
	}

	var req createCrewIntegrationRequest
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

	// If linking to workspace server, verify it exists and belongs to same workspace
	if req.WorkspaceMCPServerID != nil && *req.WorkspaceMCPServerID != "" {
		var wsServerWS string
		if err := h.db.QueryRowContext(r.Context(),
			"SELECT workspace_id FROM workspace_mcp_servers WHERE id = ?",
			*req.WorkspaceMCPServerID).Scan(&wsServerWS); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Referenced workspace integration not found"})
			return
		}
		if wsServerWS != workspaceID {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Workspace integration belongs to a different workspace"})
			return
		}
	}

	now := time.Now().UTC().Format(time.RFC3339)
	id := generateCUID()

	_, err := h.db.ExecContext(r.Context(), `
		INSERT INTO crew_mcp_servers (id, crew_id, workspace_mcp_server_id, name, display_name, transport,
			endpoint, command, args_json, env_json, config_json, icon, enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?)`,
		id, crewID, req.WorkspaceMCPServerID, req.Name, req.DisplayName, req.Transport,
		req.Endpoint, req.Command, req.ArgsJSON, req.EnvJSON, req.ConfigJSON, req.Icon, now, now)
	if err != nil {
		h.logger.Error("create crew integration", "error", err)
		writeJSON(w, http.StatusConflict, map[string]string{"error": "Integration with this name already exists in this crew"})
		return
	}

	h.broadcastEvent("integration.created", workspaceID, map[string]string{
		"id": id, "name": req.Name, "scope": "crew", "crew_id": crewID,
	})

	writeJSON(w, http.StatusCreated, crewMCPServerResponse{
		ID: id, CrewID: crewID, WorkspaceMCPServerID: req.WorkspaceMCPServerID,
		Name: req.Name, DisplayName: req.DisplayName, Transport: req.Transport,
		Endpoint: req.Endpoint, Command: req.Command,
		ArgsJSON: req.ArgsJSON, EnvJSON: req.EnvJSON, ConfigJSON: req.ConfigJSON,
		Icon: req.Icon, Enabled: true, CreatedAt: now, UpdatedAt: now,
	})
}

func (h *IntegrationHandler) UpdateCrewIntegration(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())
	if !canRole(role, "create") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Forbidden"})
		return
	}

	crewID := r.PathValue("crewId")
	id := r.PathValue("integrationId")

	var req updateIntegrationRequest
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid JSON body"})
		return
	}

	// Verify crew + server exist
	var exists string
	if err := h.db.QueryRowContext(r.Context(), `
		SELECT cs.id FROM crew_mcp_servers cs
		JOIN crews c ON c.id = cs.crew_id
		WHERE cs.id = ? AND cs.crew_id = ? AND c.workspace_id = ?`,
		id, crewID, workspaceID).Scan(&exists); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Crew integration not found"})
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)
	sets := []string{"updated_at = ?"}
	args := []interface{}{now}
	if req.DisplayName != nil {
		sets = append(sets, "display_name = ?")
		args = append(args, *req.DisplayName)
	}
	if req.Transport != nil {
		if *req.Transport != "streamable-http" && *req.Transport != "stdio" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "transport must be 'streamable-http' or 'stdio'"})
			return
		}
		sets = append(sets, "transport = ?")
		args = append(args, *req.Transport)
	}
	if req.Endpoint != nil {
		sets = append(sets, "endpoint = ?")
		args = append(args, *req.Endpoint)
	}
	if req.Command != nil {
		sets = append(sets, "command = ?")
		args = append(args, *req.Command)
	}
	if req.ArgsJSON != nil {
		sets = append(sets, "args_json = ?")
		args = append(args, *req.ArgsJSON)
	}
	if req.EnvJSON != nil {
		sets = append(sets, "env_json = ?")
		args = append(args, *req.EnvJSON)
	}
	if req.ConfigJSON != nil {
		sets = append(sets, "config_json = ?")
		args = append(args, *req.ConfigJSON)
	}
	if req.Icon != nil {
		sets = append(sets, "icon = ?")
		args = append(args, *req.Icon)
	}
	if req.Enabled != nil {
		enabled := 0
		if *req.Enabled {
			enabled = 1
		}
		sets = append(sets, "enabled = ?")
		args = append(args, enabled)
	}
	args = append(args, id)

	query := "UPDATE crew_mcp_servers SET " + strings.Join(sets, ", ") + " WHERE id = ?"
	if _, err := h.db.ExecContext(r.Context(), query, args...); err != nil {
		h.logger.Error("update crew integration", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	h.broadcastEvent("integration.updated", workspaceID, map[string]string{
		"id": id, "scope": "crew", "crew_id": crewID,
	})

	// Return updated
	var s crewMCPServerResponse
	var enabled int
	h.db.QueryRowContext(r.Context(), `
		SELECT id, crew_id, workspace_mcp_server_id, name, display_name, transport,
			endpoint, command, args_json, env_json, config_json, icon, enabled, created_at, updated_at,
			(SELECT COUNT(*) FROM agent_mcp_bindings WHERE mcp_server_id = crew_mcp_servers.id AND mcp_server_scope = 'crew')
		FROM crew_mcp_servers WHERE id = ?`, id).Scan(
		&s.ID, &s.CrewID, &s.WorkspaceMCPServerID, &s.Name, &s.DisplayName, &s.Transport,
		&s.Endpoint, &s.Command, &s.ArgsJSON, &s.EnvJSON, &s.ConfigJSON,
		&s.Icon, &enabled, &s.CreatedAt, &s.UpdatedAt, &s.AgentBindCount)
	s.Enabled = enabled == 1
	writeJSON(w, http.StatusOK, s)
}

func (h *IntegrationHandler) DeleteCrewIntegration(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())
	if !canRole(role, "create") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Forbidden"})
		return
	}

	crewID := r.PathValue("crewId")
	id := r.PathValue("integrationId")

	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	// Delete agent bindings for this crew server
	if _, err := tx.ExecContext(r.Context(),
		"DELETE FROM agent_mcp_bindings WHERE mcp_server_id = ? AND mcp_server_scope = 'crew'", id); err != nil {
		tx.Rollback()
		h.logger.Error("delete agent bindings for crew server", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	result, err := tx.ExecContext(r.Context(), `
		DELETE FROM crew_mcp_servers WHERE id = ? AND crew_id = ? AND crew_id IN
		(SELECT id FROM crews WHERE workspace_id = ?)`, id, crewID, workspaceID)
	if err != nil {
		tx.Rollback()
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		tx.Rollback()
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Crew integration not found"})
		return
	}
	if err := tx.Commit(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	h.broadcastEvent("integration.deleted", workspaceID, map[string]string{
		"id": id, "scope": "crew", "crew_id": crewID,
	})

	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// ==========================================
// Agent MCP Bindings
// ==========================================

func (h *IntegrationHandler) ListAgentBindings(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	agentID := r.PathValue("agentId")

	// Verify agent
	var agentExists string
	if err := h.db.QueryRowContext(r.Context(),
		"SELECT id FROM agents WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL",
		agentID, workspaceID).Scan(&agentExists); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Agent not found"})
		return
	}

	rows, err := h.db.QueryContext(r.Context(), `
		SELECT b.id, b.agent_id, b.mcp_server_id, b.mcp_server_scope,
			b.credential_id, b.cred_type, b.cred_header, b.enabled, b.config_override_json, b.created_at,
			CASE
				WHEN b.mcp_server_scope = 'workspace' THEN COALESCE(ws.name, '')
				WHEN b.mcp_server_scope = 'crew' THEN COALESCE(cs.name, '')
			END AS server_name,
			CASE
				WHEN b.mcp_server_scope = 'workspace' THEN COALESCE(ws.display_name, '')
				WHEN b.mcp_server_scope = 'crew' THEN COALESCE(cs.display_name, '')
			END AS server_display,
			c.name AS credential_name
		FROM agent_mcp_bindings b
		LEFT JOIN workspace_mcp_servers ws ON b.mcp_server_id = ws.id AND b.mcp_server_scope = 'workspace'
		LEFT JOIN crew_mcp_servers cs ON b.mcp_server_id = cs.id AND b.mcp_server_scope = 'crew'
		LEFT JOIN credentials c ON b.credential_id = c.id
		WHERE b.agent_id = ?
		ORDER BY b.created_at DESC`, agentID)
	if err != nil {
		h.logger.Error("list agent bindings", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	defer rows.Close()

	var results []agentMCPBindingResponse
	for rows.Next() {
		var b agentMCPBindingResponse
		var enabled int
		if err := rows.Scan(&b.ID, &b.AgentID, &b.MCPServerID, &b.MCPServerScope,
			&b.CredentialID, &b.CredType, &b.CredHeader, &enabled, &b.ConfigOverride, &b.CreatedAt,
			&b.ServerName, &b.ServerDisplay, &b.CredentialName); err != nil {
			h.logger.Error("scan agent binding", "error", err)
			continue
		}
		b.Enabled = enabled == 1
		results = append(results, b)
	}
	if results == nil {
		results = []agentMCPBindingResponse{}
	}
	writeJSON(w, http.StatusOK, results)
}

func (h *IntegrationHandler) CreateAgentBinding(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())
	if !canRole(role, "create") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Forbidden"})
		return
	}

	agentID := r.PathValue("agentId")
	var agentExists string
	if err := h.db.QueryRowContext(r.Context(),
		"SELECT id FROM agents WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL",
		agentID, workspaceID).Scan(&agentExists); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Agent not found"})
		return
	}

	var req createAgentBindingRequest
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid JSON body"})
		return
	}
	if req.MCPServerID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "mcp_server_id is required"})
		return
	}
	if req.MCPServerScope != "workspace" && req.MCPServerScope != "crew" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "mcp_server_scope must be 'workspace' or 'crew'"})
		return
	}

	// Verify MCP server exists
	switch req.MCPServerScope {
	case "workspace":
		var wsID string
		if err := h.db.QueryRowContext(r.Context(),
			"SELECT workspace_id FROM workspace_mcp_servers WHERE id = ?",
			req.MCPServerID).Scan(&wsID); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Workspace integration not found"})
			return
		}
		if wsID != workspaceID {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Integration belongs to a different workspace"})
			return
		}
	case "crew":
		var crewWS string
		if err := h.db.QueryRowContext(r.Context(), `
			SELECT c.workspace_id FROM crew_mcp_servers cs
			JOIN crews c ON c.id = cs.crew_id
			WHERE cs.id = ?`, req.MCPServerID).Scan(&crewWS); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Crew integration not found"})
			return
		}
		if crewWS != workspaceID {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Integration belongs to a different workspace"})
			return
		}
	}

	// Verify credential if provided
	if req.CredentialID != nil && *req.CredentialID != "" {
		var credWS string
		if err := h.db.QueryRowContext(r.Context(),
			"SELECT workspace_id FROM credentials WHERE id = ? AND deleted_at IS NULL",
			*req.CredentialID).Scan(&credWS); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Credential not found"})
			return
		}
		if credWS != workspaceID {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Credential belongs to a different workspace"})
			return
		}
	}

	id := generateCUID()
	now := time.Now().UTC().Format(time.RFC3339)
	enabled := 1
	if req.Enabled != nil && !*req.Enabled {
		enabled = 0
	}

	credType := "bearer"
	if req.CredType != nil && *req.CredType != "" {
		credType = *req.CredType
	}

	_, err := h.db.ExecContext(r.Context(), `
		INSERT INTO agent_mcp_bindings (id, agent_id, mcp_server_id, mcp_server_scope,
			credential_id, cred_type, cred_header, env_var_name, enabled, config_override_json, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, agentID, req.MCPServerID, req.MCPServerScope,
		req.CredentialID, credType, req.CredHeader, req.EnvVarName, enabled, req.ConfigOverride, now)
	if err != nil {
		h.logger.Error("create agent binding", "error", err)
		writeJSON(w, http.StatusConflict, map[string]string{"error": "Agent already has a binding for this integration"})
		return
	}

	writeJSON(w, http.StatusCreated, agentMCPBindingResponse{
		ID: id, AgentID: agentID, MCPServerID: req.MCPServerID,
		MCPServerScope: req.MCPServerScope, CredentialID: req.CredentialID,
		CredType: &credType, CredHeader: req.CredHeader,
		Enabled: enabled == 1, ConfigOverride: req.ConfigOverride, CreatedAt: now,
	})
}

func (h *IntegrationHandler) UpdateAgentBinding(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())
	if !canRole(role, "create") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Forbidden"})
		return
	}

	agentID := r.PathValue("agentId")
	id := r.PathValue("integrationId")

	var req updateAgentBindingRequest
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid JSON body"})
		return
	}

	// Verify binding exists and agent belongs to workspace
	var exists string
	if err := h.db.QueryRowContext(r.Context(), `
		SELECT b.id FROM agent_mcp_bindings b
		JOIN agents a ON a.id = b.agent_id
		WHERE b.id = ? AND b.agent_id = ? AND a.workspace_id = ?`,
		id, agentID, workspaceID).Scan(&exists); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Agent binding not found"})
		return
	}

	sets := []string{}
	args := []interface{}{}
	if req.CredentialID != nil {
		if *req.CredentialID != "" {
			// Verify credential
			var credWS string
			if err := h.db.QueryRowContext(r.Context(),
				"SELECT workspace_id FROM credentials WHERE id = ? AND deleted_at IS NULL",
				*req.CredentialID).Scan(&credWS); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Credential not found"})
				return
			}
			if credWS != workspaceID {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Credential belongs to a different workspace"})
				return
			}
		}
		sets = append(sets, "credential_id = ?")
		args = append(args, req.CredentialID)
	}
	if req.Enabled != nil {
		enabled := 0
		if *req.Enabled {
			enabled = 1
		}
		sets = append(sets, "enabled = ?")
		args = append(args, enabled)
	}
	if req.CredType != nil {
		sets = append(sets, "cred_type = ?")
		args = append(args, *req.CredType)
	}
	if req.CredHeader != nil {
		sets = append(sets, "cred_header = ?")
		args = append(args, *req.CredHeader)
	}
	if req.ConfigOverride != nil {
		sets = append(sets, "config_override_json = ?")
		args = append(args, *req.ConfigOverride)
	}

	if len(sets) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "No fields to update"})
		return
	}

	args = append(args, id)
	query := "UPDATE agent_mcp_bindings SET " + strings.Join(sets, ", ") + " WHERE id = ?"
	if _, err := h.db.ExecContext(r.Context(), query, args...); err != nil {
		h.logger.Error("update agent binding", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "updated"})
}

func (h *IntegrationHandler) DeleteAgentBinding(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())
	if !canRole(role, "create") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Forbidden"})
		return
	}

	agentID := r.PathValue("agentId")
	id := r.PathValue("integrationId")

	result, err := h.db.ExecContext(r.Context(), `
		DELETE FROM agent_mcp_bindings WHERE id = ? AND agent_id = ? AND agent_id IN
		(SELECT id FROM agents WHERE workspace_id = ? AND deleted_at IS NULL)`, id, agentID, workspaceID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Agent binding not found"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// ==========================================
// Cascade Resolution
// ==========================================

// ResolvedIntegration is the effective MCP server config for a specific agent.
type ResolvedIntegration struct {
	ServerID     string  `json:"server_id"`
	Scope        string  `json:"scope"` // "workspace" or "crew"
	Name         string  `json:"name"`
	DisplayName  string  `json:"display_name"`
	Transport    string  `json:"transport"`
	Endpoint     *string `json:"endpoint"`
	Command      *string `json:"command"`
	ArgsJSON     *string `json:"args_json"`
	EnvJSON      *string `json:"env_json"`
	ConfigJSON   *string `json:"config_json"`
	Icon         *string `json:"icon"`
	Enabled      bool    `json:"enabled"`
	CredentialID *string `json:"credential_id"`
	CredName     *string `json:"credential_name"`
}

func (h *IntegrationHandler) ResolveAgentIntegrations(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	agentID := r.PathValue("agentId")

	// Get agent's crew_id from the agents table
	var crewID sql.NullString
	if err := h.db.QueryRowContext(r.Context(),
		"SELECT crew_id FROM agents WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL",
		agentID, workspaceID).Scan(&crewID); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Agent not found"})
		return
	}

	// Step 1: Workspace MCP servers
	wsServers := make(map[string]*ResolvedIntegration)
	if wsRows, err := h.db.QueryContext(r.Context(), `
		SELECT id, name, display_name, transport, endpoint, command,
			args_json, env_json, config_json, icon, enabled
		FROM workspace_mcp_servers
		WHERE workspace_id = ? AND enabled = 1 AND deleted_at IS NULL`, workspaceID); err == nil {
		for wsRows.Next() {
			var s ResolvedIntegration
			var enabled int
			if err := wsRows.Scan(&s.ServerID, &s.Name, &s.DisplayName, &s.Transport,
				&s.Endpoint, &s.Command, &s.ArgsJSON, &s.EnvJSON, &s.ConfigJSON,
				&s.Icon, &enabled); err != nil {
				continue
			}
			s.Scope = "workspace"
			s.Enabled = enabled == 1
			wsServers[s.Name] = &s
		}
		if err := wsRows.Err(); err != nil {
			h.logger.Error("iterate workspace MCP servers", "error", err)
		}
		wsRows.Close()
	}

	// Step 2: Crew MCP servers (override workspace by name)
	merged := make(map[string]*ResolvedIntegration)
	for k, v := range wsServers {
		merged[k] = v
	}

	if crewID.Valid {
		if crewRows, err := h.db.QueryContext(r.Context(), `
			SELECT id, workspace_mcp_server_id, name, display_name, transport,
				endpoint, command, args_json, env_json, config_json, icon, enabled
			FROM crew_mcp_servers
			WHERE crew_id = ? AND enabled = 1 AND deleted_at IS NULL`, crewID.String); err == nil {
			for crewRows.Next() {
				var s ResolvedIntegration
				var wsServerID sql.NullString
				var enabled int
				if err := crewRows.Scan(&s.ServerID, &wsServerID, &s.Name, &s.DisplayName, &s.Transport,
					&s.Endpoint, &s.Command, &s.ArgsJSON, &s.EnvJSON, &s.ConfigJSON,
					&s.Icon, &enabled); err != nil {
					continue
				}
				s.Scope = "crew"
				s.Enabled = enabled == 1
				merged[s.Name] = &s
			}
			if err := crewRows.Err(); err != nil {
				h.logger.Error("iterate crew MCP servers", "error", err)
			}
			crewRows.Close()
		}
	}

	// Step 3: Apply agent bindings (opt-out and credential assignment)
	type bindingInfo struct {
		credentialID *string
		credName     *string
		enabled      bool
		configJSON   *string
	}
	bindings := make(map[string]*bindingInfo)
	if bindingRows, err := h.db.QueryContext(r.Context(), `
		SELECT b.mcp_server_id, b.mcp_server_scope, b.credential_id, b.enabled, b.config_override_json,
			c.name AS cred_name
		FROM agent_mcp_bindings b
		LEFT JOIN credentials c ON b.credential_id = c.id
		WHERE b.agent_id = ?`, agentID); err == nil {
		for bindingRows.Next() {
			var serverID, scope string
			var credID, credName, configJSON *string
			var enabled int
			if err := bindingRows.Scan(&serverID, &scope, &credID, &enabled, &configJSON, &credName); err != nil {
				continue
			}
			bindings[serverID] = &bindingInfo{
				credentialID: credID, credName: credName,
				enabled: enabled == 1, configJSON: configJSON,
			}
		}
		if err := bindingRows.Err(); err != nil {
			h.logger.Error("iterate agent MCP bindings", "error", err)
		}
		bindingRows.Close()
	}
	{

		// Apply bindings to merged servers
		for _, s := range merged {
			if b, ok := bindings[s.ServerID]; ok {
				if !b.enabled {
					s.Enabled = false
				}
				s.CredentialID = b.credentialID
				s.CredName = b.credName
				if b.configJSON != nil {
					s.ConfigJSON = b.configJSON
				}
			}
		}
	}

	// Check which servers have ANY bindings (for opt-in filtering).
	serversWithBindings := make(map[string]bool)
	if bcRows, err := h.db.QueryContext(r.Context(), `
		SELECT mcp_server_id FROM agent_mcp_bindings
		GROUP BY mcp_server_id HAVING COUNT(*) > 0`); err == nil {
		for bcRows.Next() {
			var sid string
			if bcRows.Scan(&sid) == nil {
				serversWithBindings[sid] = true
			}
		}
		bcRows.Close()
	}

	// Build result (only enabled, respecting opt-in bindings)
	var result []ResolvedIntegration
	for _, s := range merged {
		if !s.Enabled {
			continue
		}
		_, hasBind := bindings[s.ServerID]
		if !hasBind && serversWithBindings[s.ServerID] {
			// Server has bindings for other agents but not this one → skip
			continue
		}
		result = append(result, *s)
	}
	if result == nil {
		result = []ResolvedIntegration{}
	}
	writeJSON(w, http.StatusOK, result)
}

// ---------------------------------------------------------------------------
// JSON blob → integration table migration
// ---------------------------------------------------------------------------

// MigrateJSONBlobToCrewServers converts a crew's mcp_config_json blob into
// individual crew_mcp_servers rows.  It is idempotent (INSERT OR IGNORE) and
// clears the blob after successful migration.
func MigrateJSONBlobToCrewServers(ctx context.Context, db *sql.DB, logger *slog.Logger, crewID, workspaceID, mcpJSON string) error {
	if mcpJSON == "" {
		return nil
	}

	var config struct {
		MCPServers map[string]struct {
			Command   string            `json:"command"`
			Args      []string          `json:"args"`
			Env       map[string]string `json:"env"`
			URL       string            `json:"url"`
			Type      string            `json:"type"`
			Transport string            `json:"transport"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(mcpJSON), &config); err != nil {
		return fmt.Errorf("parse mcp_config_json: %w", err)
	}
	if len(config.MCPServers) == 0 {
		return nil
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	now := time.Now().UTC().Format(time.RFC3339)

	for name, srv := range config.MCPServers {
		transport := "stdio"
		if srv.Transport == "streamable-http" || srv.Type == "http" || (srv.Command == "" && srv.URL != "") {
			transport = "streamable-http"
		}

		var argsJSON *string
		if len(srv.Args) > 0 {
			b, _ := json.Marshal(srv.Args)
			s := string(b)
			argsJSON = &s
		}

		var envJSON *string
		if len(srv.Env) > 0 {
			b, _ := json.Marshal(srv.Env)
			s := string(b)
			envJSON = &s
		}

		var endpoint *string
		if srv.URL != "" {
			endpoint = &srv.URL
		}

		var command *string
		if srv.Command != "" {
			command = &srv.Command
		}

		displayName := strings.ReplaceAll(name, "-", " ")
		displayName = strings.Title(displayName) //nolint:staticcheck

		id := generateCUID()

		_, err := tx.ExecContext(ctx, `
			INSERT OR IGNORE INTO crew_mcp_servers
				(id, crew_id, name, display_name, transport, endpoint, command, args_json, env_json, enabled, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?)`,
			id, crewID, name, displayName, transport, endpoint, command, argsJSON, envJSON, now, now)
		if err != nil {
			return fmt.Errorf("insert crew server %q: %w", name, err)
		}
	}

	// Clear the JSON blob now that data lives in the table.
	if _, err := tx.ExecContext(ctx, `UPDATE crews SET mcp_config_json = NULL WHERE id = ?`, crewID); err != nil {
		return fmt.Errorf("clear mcp_config_json: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration: %w", err)
	}

	logger.Info("migrated crew MCP config from JSON blob to tables", "crew_id", crewID, "servers", len(config.MCPServers))
	return nil
}

