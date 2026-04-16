package api

// File: internal_status.go — internal API handlers used by the sidecar on
// behalf of agents for workspace-level operations.
//
// NOTE: Most handlers in this file were designed for the COORDINATOR role
// (deprecated 2026-04-16). They remain callable by any agent via sidecar for
// backward compatibility. See docs/guides/coordinator.mdx and
// internal/orchestrator/lead.go (BuildCoordinatorContext).

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// ListCrews handles GET /api/v1/internal/crews?workspace_id=...
// Used by the sidecar on behalf of COORDINATOR agents.
//
// Deprecated: primary caller (COORDINATOR role) is deprecated. Retained for
// backward compat; see file header.
func (h *InternalHandler) ListCrews(w http.ResponseWriter, r *http.Request) {
	wsID := r.URL.Query().Get("workspace_id")
	if wsID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "workspace_id required"})
		return
	}

	type crewEntry struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Slug string `json:"slug"`
	}

	rows, err := h.db.QueryContext(r.Context(),
		`SELECT id, name, slug FROM crews WHERE workspace_id = ? AND deleted_at IS NULL ORDER BY name`, wsID)
	if err != nil {
		h.logger.Error("list crews internal", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	defer rows.Close()

	result := []crewEntry{}
	for rows.Next() {
		var c crewEntry
		if err := rows.Scan(&c.ID, &c.Name, &c.Slug); err != nil {
			continue
		}
		result = append(result, c)
	}
	writeJSON(w, http.StatusOK, result)
}

// CreateCrew handles POST /api/v1/internal/crews?workspace_id=...
// Allows COORDINATOR agents (via sidecar) to create a new crew in the workspace.
//
// Deprecated: primary caller (COORDINATOR role) is deprecated. Retained for
// backward compat; see file header.
func (h *InternalHandler) CreateCrew(w http.ResponseWriter, r *http.Request) {
	wsID := r.URL.Query().Get("workspace_id")
	if wsID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "workspace_id required"})
		return
	}

	var body struct {
		Name        string `json:"name"`
		Slug        string `json:"slug"`
		Description string `json:"description"`
		Icon        string `json:"icon"`
		Color       string `json:"color"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	if body.Name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name is required"})
		return
	}
	if body.Slug == "" {
		body.Slug = slugify(body.Name)
	} else {
		body.Slug = slugify(body.Slug)
	}
	if body.Slug == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "slug is required (could not derive from name)"})
		return
	}

	var existing int
	if err := h.db.QueryRowContext(r.Context(),
		`SELECT COUNT(*) FROM crews WHERE slug = ? AND workspace_id = ? AND deleted_at IS NULL`,
		body.Slug, wsID).Scan(&existing); err != nil {
		h.logger.Error("check crew slug uniqueness", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal server error"})
		return
	}
	if existing > 0 {
		writeJSON(w, http.StatusConflict, map[string]string{"error": fmt.Sprintf("crew with slug '%s' already exists", body.Slug)})
		return
	}

	crewID := generateCUID()
	now := time.Now().UTC().Format(time.RFC3339)
	var icon, color *string
	if body.Icon != "" {
		icon = &body.Icon
	}
	if body.Color != "" {
		color = &body.Color
	}

	_, err := h.db.ExecContext(r.Context(), `
		INSERT INTO crews (id, workspace_id, name, slug, description, icon, color, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		crewID, wsID, body.Name, body.Slug, body.Description, icon, color, now, now)
	if err != nil {
		h.logger.Error("internal create crew", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to create crew"})
		return
	}

	h.logger.Info("crew created via coordinator", "crew_id", crewID, "name", body.Name, "workspace", wsID)
	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"id":           crewID,
		"name":         body.Name,
		"slug":         body.Slug,
		"workspace_id": wsID,
	})
}

// CreateAgent handles POST /api/v1/internal/agents?workspace_id=...
// Allows COORDINATOR agents (via sidecar) to create a new agent within a crew.
//
// Deprecated: primary caller (COORDINATOR role) is deprecated. Retained for
// backward compat; see file header.
func (h *InternalHandler) CreateAgent(w http.ResponseWriter, r *http.Request) {
	wsID := r.URL.Query().Get("workspace_id")
	if wsID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "workspace_id required"})
		return
	}

	var body struct {
		CrewID       string `json:"crew_id"`
		Name         string `json:"name"`
		Slug         string `json:"slug"`
		RoleTitle    string `json:"role_title"`
		AgentRole    string `json:"agent_role"`
		Description  string `json:"description"`
		SystemPrompt string `json:"system_prompt"`
		CLIAdapter   string `json:"cli_adapter"`
		LLMProvider  string `json:"llm_provider"`
		LLMModel     string `json:"llm_model"`
		ToolProfile  string `json:"tool_profile"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	if body.Name == "" || body.CrewID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name and crew_id are required"})
		return
	}
	if body.Slug == "" {
		body.Slug = slugify(body.Name)
	} else {
		body.Slug = slugify(body.Slug)
	}
	if body.Slug == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "slug is required (could not derive from name)"})
		return
	}
	if body.AgentRole == "" {
		body.AgentRole = "AGENT"
	}
	if body.CLIAdapter == "" {
		body.CLIAdapter = "CLAUDE_CODE"
	}
	if body.ToolProfile == "" {
		body.ToolProfile = "CODING"
	}

	// Suffix slug with crew slug to prevent workspace-wide UNIQUE conflicts
	var crewSlug string
	if err := h.db.QueryRowContext(r.Context(), `SELECT slug FROM crews WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL`, body.CrewID, wsID).Scan(&crewSlug); err != nil {
		h.logger.Warn("lookup crew slug", "crew_id", body.CrewID, "error", err)
	}
	if crewSlug != "" {
		body.Slug = body.Slug + "-" + crewSlug
	}

	var existing int
	if err := h.db.QueryRowContext(r.Context(),
		`SELECT COUNT(*) FROM agents WHERE slug = ? AND workspace_id = ? AND deleted_at IS NULL`,
		body.Slug, wsID).Scan(&existing); err != nil {
		h.logger.Error("check agent slug uniqueness", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal server error"})
		return
	}
	if existing > 0 {
		writeJSON(w, http.StatusConflict, map[string]string{"error": fmt.Sprintf("agent with slug '%s' already exists", body.Slug)})
		return
	}

	agentID := generateCUID()
	webhookSecret := generateWebhookSecret()
	now := time.Now().UTC().Format(time.RFC3339)

	_, err := h.db.ExecContext(r.Context(), `
		INSERT INTO agents (id, workspace_id, crew_id, name, slug, description, role_title, agent_role,
			cli_adapter, llm_provider, llm_model, tool_profile, system_prompt,
			timeout_seconds, memory_enabled, webhook_secret, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		agentID, wsID, body.CrewID, body.Name, body.Slug, body.Description,
		body.RoleTitle, body.AgentRole,
		body.CLIAdapter, nilIfEmpty(body.LLMProvider), nilIfEmpty(body.LLMModel), body.ToolProfile, body.SystemPrompt,
		1800, true, webhookSecret, now, now)
	if err != nil {
		h.logger.Error("internal create agent", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to create agent"})
		return
	}

	// Auto-assign workspace AI credentials so the new agent can run immediately.
	autoAssignCredentials(r.Context(), h.db, wsID, agentID, now)

	h.logger.Info("agent created via coordinator", "agent_id", agentID, "name", body.Name, "crew_id", body.CrewID)
	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"id":           agentID,
		"name":         body.Name,
		"slug":         body.Slug,
		"crew_id":      body.CrewID,
		"workspace_id": wsID,
	})
}

// ListCrewConnections handles GET /api/v1/internal/crew-connections?workspace_id=...&crew_id=...
// Used by the sidecar on behalf of COORDINATOR agents.
// When crew_id is provided, only connections involving that crew are returned.
//
// Deprecated: primary caller (COORDINATOR role) is deprecated. Retained for
// backward compat; see file header.
func (h *InternalHandler) ListCrewConnections(w http.ResponseWriter, r *http.Request) {
	wsID := r.URL.Query().Get("workspace_id")
	if wsID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "workspace_id required"})
		return
	}

	query := `
		SELECT cc.id, cc.from_crew_id, cc.to_crew_id, cc.direction, cc.status,
		       fc.name, fc.slug, tc.name, tc.slug
		FROM crew_connections cc
		JOIN crews fc ON fc.id = cc.from_crew_id
		JOIN crews tc ON tc.id = cc.to_crew_id
		WHERE cc.workspace_id = ? AND cc.status = 'active'`
	args := []interface{}{wsID}

	if crewID := r.URL.Query().Get("crew_id"); crewID != "" {
		query += " AND (cc.from_crew_id = ? OR cc.to_crew_id = ?)"
		args = append(args, crewID, crewID)
	}

	query += " ORDER BY cc.created_at DESC"

	rows, err := h.db.QueryContext(r.Context(), query, args...)
	if err != nil {
		h.logger.Error("list crew connections internal", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	defer rows.Close()

	type connEntry struct {
		ID           string `json:"id"`
		FromCrewID   string `json:"from_crew_id"`
		FromCrewName string `json:"from_crew_name"`
		FromCrewSlug string `json:"from_crew_slug"`
		ToCrewID     string `json:"to_crew_id"`
		ToCrewName   string `json:"to_crew_name"`
		ToCrewSlug   string `json:"to_crew_slug"`
		Direction    string `json:"direction"`
		Status       string `json:"status"`
	}

	result := []connEntry{}
	for rows.Next() {
		var c connEntry
		if err := rows.Scan(&c.ID, &c.FromCrewID, &c.ToCrewID, &c.Direction, &c.Status,
			&c.FromCrewName, &c.FromCrewSlug, &c.ToCrewName, &c.ToCrewSlug); err != nil {
			continue
		}
		result = append(result, c)
	}
	writeJSON(w, http.StatusOK, result)
}

// RecordMCPToolCall records an MCP tool call audit entry from the sidecar gateway.
func (h *InternalHandler) RecordMCPToolCall(w http.ResponseWriter, r *http.Request) {
	var body struct {
		WorkspaceID    string `json:"workspace_id"`
		AgentID        string `json:"agent_id"`
		CrewID         string `json:"crew_id"`
		MCPServerID    string `json:"mcp_server_id"`
		MCPServerScope string `json:"mcp_server_scope"`
		ToolName       string `json:"tool_name"`
		Status         string `json:"status"`
		DurationMS     int64  `json:"duration_ms"`
		ErrorMessage   string `json:"error_message"`
	}
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid JSON"})
		return
	}
	if body.WorkspaceID == "" || body.AgentID == "" || body.MCPServerID == "" || body.ToolName == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "workspace_id, agent_id, mcp_server_id, and tool_name are required"})
		return
	}
	if body.MCPServerScope == "" {
		body.MCPServerScope = "workspace"
	}

	id := generateCUID()
	_, err := h.db.ExecContext(r.Context(), `
		INSERT INTO mcp_tool_calls (id, workspace_id, crew_id, agent_id, mcp_server_id,
			mcp_server_scope, tool_name, status, duration_ms, error_message, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, datetime('now'))`,
		id, body.WorkspaceID, body.CrewID, body.AgentID, body.MCPServerID, body.MCPServerScope,
		body.ToolName, body.Status, body.DurationMS, body.ErrorMessage)
	if err != nil {
		h.logger.Error("record mcp tool call", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Failed to record"})
		return
	}

	writeJSON(w, http.StatusCreated, map[string]string{"id": id})
}
