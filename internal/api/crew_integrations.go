package api

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"strings"
)

// --- Response types ---

type crewMCPServerResponse struct {
	ID                   string  `json:"id"`
	CrewID               string  `json:"crew_id"`
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
	Enabled              bool    `json:"enabled"`
	CreatedAt            string  `json:"created_at"`
	UpdatedAt            string  `json:"updated_at"`
	AgentBindCount       int     `json:"agent_binding_count"`
	AuthStatus           string  `json:"auth_status"` // "connected", "missing", "expired", "none"
}

type crewIntegrationOverview struct {
	crewMCPServerResponse
	CrewName string `json:"crew_name"`
	CrewSlug string `json:"crew_slug"`
}

// --- Request types ---

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
		replyError(w, http.StatusInternalServerError, "Internal server error")
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
		replyError(w, http.StatusInternalServerError, "Internal server error")
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
		replyError(w, http.StatusInternalServerError, "Internal server error")
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
		replyError(w, http.StatusInternalServerError, "Internal server error")
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

// ListCrewIntegrations returns all MCP server integrations for a specific crew.
// GET /api/v1/crews/{crewId}/integrations

func (h *IntegrationHandler) ListCrewIntegrations(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	crewID := r.PathValue("crewId")

	// Verify crew belongs to workspace
	found, err := crewExists(r.Context(), h.db, crewID, workspaceID)
	if err != nil {
		h.logger.Error("crew exists check", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	if !found {
		replyError(w, http.StatusNotFound, "Crew not found")
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
		replyError(w, http.StatusInternalServerError, "Internal server error")
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
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	// Populate auth_status
	if err := h.populateAuthStatus(r.Context(), results); err != nil {
		h.logger.Error("populate auth status", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
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

// CreateCrewIntegration adds a new MCP server integration to a crew.
// POST /api/v1/crews/{crewId}/integrations
