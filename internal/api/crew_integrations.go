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
)

// --- Response types ---

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
	AuthStatus            string  `json:"auth_status"` // "connected", "missing", "expired", "none"
}

type crewIntegrationOverview struct {
	crewMCPServerResponse
	CrewName string `json:"crew_name"`
	CrewSlug string `json:"crew_slug"`
}

// --- Request types ---

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

// ==========================================
// All crew integrations (cross-crew view for Integrations page)
// ==========================================

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
	if err := rows.Err(); err != nil {
		h.logger.Error("iterate all crew integrations", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	// Populate auth_status via a batch query. Use MAX on a priority CASE so
	// EXPIRED (worst) wins over ACTIVE when multiple credentials are bound.
	authStatusMap := make(map[string]string)
	authRows, err := h.db.QueryContext(r.Context(), `
		SELECT ab.mcp_server_id,
			CASE MAX(CASE c.status WHEN 'EXPIRED' THEN 2 WHEN 'ERROR' THEN 2 WHEN 'REVOKED' THEN 2 ELSE 1 END)
				WHEN 2 THEN 'EXPIRED' ELSE 'ACTIVE' END
		FROM agent_mcp_bindings ab
		JOIN credentials c ON c.id = ab.credential_id AND c.deleted_at IS NULL
		WHERE ab.mcp_server_id IN (
			SELECT cs.id FROM crew_mcp_servers cs
			JOIN crews cr ON cr.id = cs.crew_id AND cr.deleted_at IS NULL
			WHERE cr.workspace_id = ?
		) AND ab.credential_id IS NOT NULL AND ab.credential_id != ''
		GROUP BY ab.mcp_server_id`, workspaceID)
	if err != nil {
		h.logger.Error("query auth status batch", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	for authRows.Next() {
		var sid string
		var status sql.NullString
		if authRows.Scan(&sid, &status) == nil && status.Valid {
			authStatusMap[sid] = status.String
		}
	}
	if err := authRows.Err(); err != nil {
		h.logger.Error("iterate auth status batch", "error", err)
		authRows.Close()
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	authRows.Close()
	for i := range results {
		s := &results[i]
		if s.Transport != "streamable-http" || s.Endpoint == nil || *s.Endpoint == "" {
			s.AuthStatus = "none"
			continue
		}
		status, found := authStatusMap[s.ID]
		if !found || status == "" {
			s.AuthStatus = "missing"
		} else if status == "EXPIRED" {
			s.AuthStatus = "expired"
		} else {
			s.AuthStatus = "connected"
		}
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
	if err := rows.Err(); err != nil {
		h.logger.Error("iterate crew integrations", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	// Populate auth_status
	if err := h.populateAuthStatus(r.Context(), results); err != nil {
		h.logger.Error("populate auth status", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	if results == nil {
		results = []crewMCPServerResponse{}
	}
	writeJSON(w, http.StatusOK, results)
}

// populateAuthStatus fills in AuthStatus for crew MCP server responses
// by batch-querying credential statuses from agent bindings.
func (h *IntegrationHandler) populateAuthStatus(ctx context.Context, results []crewMCPServerResponse) error {
	if len(results) == 0 {
		return nil
	}
	// Collect server IDs
	ids := make([]string, len(results))
	for i, s := range results {
		ids[i] = s.ID
	}
	placeholders := strings.Repeat("?,", len(ids))
	placeholders = placeholders[:len(placeholders)-1]

	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}

	authStatusMap := make(map[string]string)
	authRows, err := h.db.QueryContext(ctx, `
		SELECT ab.mcp_server_id,
			CASE MAX(CASE c.status WHEN 'EXPIRED' THEN 2 WHEN 'ERROR' THEN 2 WHEN 'REVOKED' THEN 2 ELSE 1 END)
				WHEN 2 THEN 'EXPIRED' ELSE 'ACTIVE' END
		FROM agent_mcp_bindings ab
		JOIN credentials c ON c.id = ab.credential_id AND c.deleted_at IS NULL
		WHERE ab.mcp_server_id IN (`+placeholders+`)
			AND ab.credential_id IS NOT NULL AND ab.credential_id != ''
		GROUP BY ab.mcp_server_id`, args...)
	if err != nil {
		return fmt.Errorf("query auth status: %w", err)
	}
	for authRows.Next() {
		var sid string
		var status sql.NullString
		if authRows.Scan(&sid, &status) == nil && status.Valid {
			authStatusMap[sid] = status.String
		}
	}
	if err := authRows.Err(); err != nil {
		authRows.Close()
		return fmt.Errorf("iterate auth status: %w", err)
	}
	authRows.Close()
	for i := range results {
		s := &results[i]
		if s.Transport != "streamable-http" || s.Endpoint == nil || *s.Endpoint == "" {
			s.AuthStatus = "none"
			continue
		}
		status, found := authStatusMap[s.ID]
		if !found || status == "" {
			s.AuthStatus = "missing"
		} else if status == "EXPIRED" {
			s.AuthStatus = "expired"
		} else {
			s.AuthStatus = "connected"
		}
	}
	return nil
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
	if req.Transport == "streamable-http" && (req.Endpoint == nil || *req.Endpoint == "") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "endpoint is required for streamable-http transport"})
		return
	}
	if req.Transport == "stdio" && (req.Command == nil || *req.Command == "") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "command is required for stdio transport"})
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
	if !canRole(role, "manage") {
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

	// Verify crew + server exist and are not soft-deleted
	var exists string
	if err := h.db.QueryRowContext(r.Context(), `
		SELECT cs.id FROM crew_mcp_servers cs
		JOIN crews c ON c.id = cs.crew_id
		WHERE cs.id = ? AND cs.crew_id = ? AND c.workspace_id = ?
			AND cs.deleted_at IS NULL AND c.deleted_at IS NULL`,
		id, crewID, workspaceID).Scan(&exists); err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Crew integration not found"})
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
		_ = h.db.QueryRowContext(r.Context(),
			"SELECT endpoint, command FROM crew_mcp_servers WHERE id = ?", id).
			Scan(&existingEndpoint, &existingCommand)

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

	query, args := u.Build("crew_mcp_servers", "id = ?", id)
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
	if err := h.db.QueryRowContext(r.Context(), `
		SELECT id, crew_id, workspace_mcp_server_id, name, display_name, transport,
			endpoint, command, args_json, env_json, config_json, icon, enabled, created_at, updated_at,
			(SELECT COUNT(*) FROM agent_mcp_bindings WHERE mcp_server_id = crew_mcp_servers.id AND mcp_server_scope = 'crew')
		FROM crew_mcp_servers WHERE id = ?`, id).Scan(
		&s.ID, &s.CrewID, &s.WorkspaceMCPServerID, &s.Name, &s.DisplayName, &s.Transport,
		&s.Endpoint, &s.Command, &s.ArgsJSON, &s.EnvJSON, &s.ConfigJSON,
		&s.Icon, &enabled, &s.CreatedAt, &s.UpdatedAt, &s.AgentBindCount); err != nil {
		h.logger.Error("fetch updated crew integration", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	s.Enabled = enabled == 1
	writeJSON(w, http.StatusOK, s)
}

func (h *IntegrationHandler) DeleteCrewIntegration(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())
	if !canRole(role, "manage") {
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

	// Collect credential IDs from bindings — cascade-delete OAuth credentials
	// that were created specifically for this integration (auto-connect flow).
	var credIDs []string
	rows, err := tx.QueryContext(r.Context(),
		`SELECT DISTINCT ab.credential_id FROM agent_mcp_bindings ab
		 JOIN credentials c ON c.id = ab.credential_id
		 WHERE ab.mcp_server_id = ? AND ab.mcp_server_scope = 'crew'
		   AND c.type = 'OAUTH2' AND c.name LIKE '%oauth%'`,
		id)
	if err == nil {
		for rows.Next() {
			var cid string
			if rows.Scan(&cid) == nil && cid != "" {
				credIDs = append(credIDs, cid)
			}
		}
		if err := rows.Err(); err != nil {
			h.logger.Error("iterate credential IDs for deletion", "error", err)
		}
		rows.Close()
	}

	// Delete agent bindings for this crew server
	if _, err := tx.ExecContext(r.Context(),
		"DELETE FROM agent_mcp_bindings WHERE mcp_server_id = ? AND mcp_server_scope = 'crew'", id); err != nil {
		tx.Rollback()
		h.logger.Error("delete agent bindings for crew server", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	// Cascade-delete OAuth credentials only if no other bindings reference them
	for _, cid := range credIDs {
		var remaining int
		if err := tx.QueryRowContext(r.Context(),
			"SELECT COUNT(*) FROM agent_mcp_bindings WHERE credential_id = ?", cid).Scan(&remaining); err != nil {
			h.logger.Warn("check credential bindings", "credential_id", cid, "error", err)
			continue
		}
		if remaining > 0 {
			continue // still referenced elsewhere
		}
		if _, err := tx.ExecContext(r.Context(),
			"DELETE FROM credentials WHERE id = ? AND workspace_id = ?", cid, workspaceID); err != nil {
			h.logger.Warn("cascade delete OAuth credential", "credential_id", cid, "error", err)
		}
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

// ---------------------------------------------------------------------------
// JSON blob → integration table migration
// ---------------------------------------------------------------------------

// parsedMCPServer holds the parsed fields for a single MCP server entry
// extracted from an mcp_config_json blob.
type parsedMCPServer struct {
	name        string
	displayName string
	transport   string
	endpoint    *string
	command     *string
	argsJSON    *string
	envJSON     *string
}

// parseMCPConfigBlob parses an mcp_config_json blob into a slice of
// parsedMCPServer values. Returns nil (no error) when the blob is empty
// or contains no servers.
func parseMCPConfigBlob(mcpJSON string) ([]parsedMCPServer, error) {
	if mcpJSON == "" {
		return nil, nil
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
		return nil, fmt.Errorf("parse mcp_config_json: %w", err)
	}
	if len(config.MCPServers) == 0 {
		return nil, nil
	}

	servers := make([]parsedMCPServer, 0, len(config.MCPServers))
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

		servers = append(servers, parsedMCPServer{
			name:        name,
			displayName: displayName,
			transport:   transport,
			endpoint:    endpoint,
			command:     command,
			argsJSON:    argsJSON,
			envJSON:     envJSON,
		})
	}
	return servers, nil
}

// insertCrewMCPServersFromBlob inserts parsed MCP servers into crew_mcp_servers
// using INSERT OR IGNORE for idempotency (duplicates by crew_id+name are skipped).
func insertCrewMCPServersFromBlob(ctx context.Context, tx *sql.Tx, crewID string, servers []parsedMCPServer) error {
	now := time.Now().UTC().Format(time.RFC3339)
	for _, srv := range servers {
		id := generateCUID()
		if _, err := tx.ExecContext(ctx, `
			INSERT OR IGNORE INTO crew_mcp_servers
				(id, crew_id, name, display_name, transport, endpoint, command, args_json, env_json, enabled, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, ?)`,
			id, crewID, srv.name, srv.displayName, srv.transport, srv.endpoint, srv.command, srv.argsJSON, srv.envJSON, now, now); err != nil {
			return fmt.Errorf("insert crew server %q: %w", srv.name, err)
		}
	}
	return nil
}

// verifyAllServersExist checks that all server names from the parsed blob
// exist in crew_mcp_servers for the given crew. Returns true when the count
// matches.
func verifyAllServersExist(ctx context.Context, tx *sql.Tx, crewID string, servers []parsedMCPServer) (bool, error) {
	args := make([]any, 0, len(servers)+1)
	args = append(args, crewID)
	placeholders := ""
	for _, srv := range servers {
		if placeholders != "" {
			placeholders += ","
		}
		placeholders += "?"
		args = append(args, srv.name)
	}
	var matching int
	if err := tx.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM crew_mcp_servers WHERE crew_id = ? AND name IN ("+placeholders+")",
		args...).Scan(&matching); err != nil {
		return false, err
	}
	return matching == len(servers), nil
}

// MigrateJSONBlobToCrewServers converts a crew's mcp_config_json blob into
// individual crew_mcp_servers rows.  It is idempotent (INSERT OR IGNORE) and
// clears the blob after successful migration.
func MigrateJSONBlobToCrewServers(ctx context.Context, db *sql.DB, logger *slog.Logger, crewID, workspaceID, mcpJSON string) error {
	servers, err := parseMCPConfigBlob(mcpJSON)
	if err != nil || len(servers) == 0 {
		return err
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	if err := insertCrewMCPServersFromBlob(ctx, tx, crewID, servers); err != nil {
		return err
	}

	// Clear the JSON blob only if all configured server names exist in the table.
	allExist, err := verifyAllServersExist(ctx, tx, crewID, servers)
	if err != nil {
		return fmt.Errorf("count matching crew servers: %w", err)
	}
	if allExist {
		if _, err := tx.ExecContext(ctx, `UPDATE crews SET mcp_config_json = NULL WHERE id = ?`, crewID); err != nil {
			return fmt.Errorf("clear mcp_config_json: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration: %w", err)
	}

	logger.Info("migrated crew MCP config from JSON blob to tables", "crew_id", crewID, "servers", len(servers))
	return nil
}

// MigrateJSONBlobToAgentServers converts an agent's mcp_config_json blob into
// crew_mcp_servers rows (owned by the agent's crew) plus agent_mcp_bindings
// that link the agent to each server.  It is idempotent and clears the blob
// after successful migration.
func MigrateJSONBlobToAgentServers(ctx context.Context, db *sql.DB, logger *slog.Logger, agentID, crewID, workspaceID, mcpJSON string) error {
	servers, err := parseMCPConfigBlob(mcpJSON)
	if err != nil || len(servers) == 0 {
		return err
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	if err := insertCrewMCPServersFromBlob(ctx, tx, crewID, servers); err != nil {
		return err
	}

	// Resolve actual server IDs and create agent bindings.
	now := time.Now().UTC().Format(time.RFC3339)
	for _, srv := range servers {
		var resolvedServerID string
		if err := tx.QueryRowContext(ctx,
			`SELECT id FROM crew_mcp_servers WHERE crew_id = ? AND name = ?`,
			crewID, srv.name).Scan(&resolvedServerID); err != nil {
			return fmt.Errorf("resolve crew server id %q: %w", srv.name, err)
		}

		bindingID := generateCUID()
		if _, err := tx.ExecContext(ctx, `
			INSERT OR IGNORE INTO agent_mcp_bindings
				(id, agent_id, mcp_server_id, mcp_server_scope, enabled, created_at)
			VALUES (?, ?, ?, 'crew', 1, ?)`,
			bindingID, agentID, resolvedServerID, now); err != nil {
			return fmt.Errorf("insert agent binding for server %q: %w", srv.name, err)
		}
	}

	// Clear the JSON blob only if all configured server names exist in crew_mcp_servers.
	allExist, err := verifyAllServersExist(ctx, tx, crewID, servers)
	if err != nil {
		return fmt.Errorf("count matching crew servers for agent: %w", err)
	}
	if allExist {
		if _, err := tx.ExecContext(ctx, `UPDATE agents SET mcp_config_json = NULL WHERE id = ?`, agentID); err != nil {
			return fmt.Errorf("clear agent mcp_config_json: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit agent migration: %w", err)
	}

	logger.Info("migrated agent MCP config from JSON blob to tables", "agent_id", agentID, "crew_id", crewID, "servers", len(servers))
	return nil
}
