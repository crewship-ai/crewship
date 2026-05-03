package api

// Agent update handler — applies field-level patches with role-based
// guards, schedule rebinding, and crew/role transition validation.
// Extracted from agents.go.

import (
	"database/sql"
	"encoding/json"
	"net/http"
)

func (h *AgentHandler) Update(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agentId")
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())

	if !canRole(role, "create") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Forbidden"})
		return
	}

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

	var body map[string]interface{}
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid JSON body"})
		return
	}

	allowed := map[string]string{
		"name": "name", "slug": "slug", "description": "description",
		"role_title": "role_title", "agent_role": "agent_role",
		"lead_mode":   "lead_mode",
		"cli_adapter": "cli_adapter", "llm_provider": "llm_provider",
		"llm_model": "llm_model", "system_prompt": "system_prompt",
		"avatar_seed": "avatar_seed", "avatar_style": "avatar_style",
		"timeout_seconds": "timeout_seconds", "tool_profile": "tool_profile",
		"memory_enabled": "memory_enabled", "cli_tools": "cli_tools", "crew_id": "crew_id",
		"schedule_cron": "schedule_cron", "schedule_prompt": "schedule_prompt",
		"schedule_enabled": "schedule_enabled",
		"mcp_config_json":  "mcp_config_json",
	}

	// Validate slug format if being updated
	if slugVal, ok := body["slug"]; ok {
		if slugStr, ok := slugVal.(string); ok {
			if slugStr == "" || len(slugStr) < 2 || len(slugStr) > 50 {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "slug must be 2-50 characters"})
				return
			}
			if !validSlugFormat(slugStr) {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "slug must contain only lowercase letters, numbers, underscores, and hyphens"})
				return
			}
		}
	}

	// Validate agent_role if being updated
	if roleVal, ok := body["agent_role"]; ok {
		roleStr, _ := roleVal.(string)
		if !validAgentRoles[roleStr] {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "agent_role must be AGENT, LEAD, or COORDINATOR"})
			return
		}

		// If promoting to LEAD, auto-demote existing lead in the same crew (transactional)
		if roleStr == "LEAD" {
			// Find the agent's crew_id
			var crewIDNull sql.NullString
			if err := h.db.QueryRowContext(r.Context(),
				"SELECT crew_id FROM agents WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL",
				agentID, workspaceID).Scan(&crewIDNull); err != nil {
				h.logger.Error("query agent crew_id for promotion", "error", err)
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
				return
			}

			if !crewIDNull.Valid || crewIDNull.String == "" {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "LEAD role requires crew_id"})
				return
			}

			// Demote existing lead in the same crew
			if _, err := h.db.ExecContext(r.Context(),
				"UPDATE agents SET agent_role = 'AGENT' WHERE crew_id = ? AND agent_role = 'LEAD' AND deleted_at IS NULL AND id != ?",
				crewIDNull.String, agentID); err != nil {
				h.logger.Error("demote existing lead", "error", err)
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
				return
			}
		}
	}

	// Validate lead_mode if being updated
	if modeVal, ok := body["lead_mode"]; ok {
		modeStr, _ := modeVal.(string)
		if modeStr != "" && !validLeadModes[modeStr] {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "lead_mode must be 'active' or 'passive'"})
			return
		}
	}

	// Validate cli_adapter if being updated. Pre-fix any string passed
	// validation, allowing typos to land in DB and only fail at runtime
	// dispatch (getAdapter falls back to a minimal claude command for
	// unknown adapters — silent regression).
	if v, ok := body["cli_adapter"]; ok {
		s, _ := v.(string)
		if s != "" && !validCLIAdapters[s] {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "cli_adapter must be CLAUDE_CODE, OPENCODE, CODEX_CLI, GEMINI_CLI, CURSOR_CLI, or FACTORY_DROID"})
			return
		}
	}
	if v, ok := body["llm_provider"]; ok {
		s, _ := v.(string)
		if s != "" && !validLLMProviders[s] {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "llm_provider must be ANTHROPIC, OPENAI, GOOGLE, CURSOR, FACTORY, or OLLAMA"})
			return
		}
	}
	if v, ok := body["tool_profile"]; ok {
		s, _ := v.(string)
		if s != "" && !validToolProfiles[s] {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "tool_profile must be MINIMAL, CODING, MESSAGING, FULL, or CONSULTATIVE"})
			return
		}
	}

	// Validate mcp_config_json if being updated
	if mcpVal, ok := body["mcp_config_json"]; ok {
		if mcpStr, ok := mcpVal.(string); ok && mcpStr != "" {
			var mcpCheck struct {
				MCPServers map[string]json.RawMessage `json:"mcpServers"`
			}
			if err := json.Unmarshal([]byte(mcpStr), &mcpCheck); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "mcp_config_json is not valid JSON: " + err.Error()})
				return
			}
			if mcpCheck.MCPServers == nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "mcp_config_json must contain a \"mcpServers\" object"})
				return
			}
		}
	}

	ub := newUpdate()
	for jsonKey, col := range allowed {
		if val, ok := body[jsonKey]; ok {
			if col == "memory_enabled" || col == "schedule_enabled" {
				if b, ok := val.(bool); ok {
					if b {
						val = 1
					} else {
						val = 0
					}
				}
			}
			ub.Set(col, val)
		}
	}

	if ub.Empty() {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "No fields to update"})
		return
	}

	query, args := ub.Build("agents", "id = ? AND workspace_id = ?", agentID, workspaceID)
	if _, err := h.db.ExecContext(r.Context(), query, args...); err != nil {
		h.logger.Error("update agent", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	user := UserFromContext(r.Context())
	userID := ""
	if user != nil {
		userID = user.ID
	}
	changes := make(map[string]interface{})
	for jsonKey := range allowed {
		if val, ok := body[jsonKey]; ok {
			changes[jsonKey] = val
		}
	}
	WriteAuditLog(r.Context(), h.db, "update", "AGENT", agentID, userID, workspaceID, changes)

	// Notify scheduler of schedule changes
	if h.scheduleUpdater != nil {
		if _, hasCron := body["schedule_cron"]; hasCron {
			cronStr, _ := body["schedule_cron"].(string)
			promptStr, _ := body["schedule_prompt"].(string)
			enabledVal, hasEnabled := body["schedule_enabled"]
			enabled := false
			if hasEnabled {
				switch v := enabledVal.(type) {
				case bool:
					enabled = v
				case float64:
					enabled = v == 1
				}
			} else {
				// schedule_cron changed but schedule_enabled wasn't in body — read from DB
				var e int
				if err := h.db.QueryRowContext(r.Context(), "SELECT schedule_enabled FROM agents WHERE id = ?", agentID).Scan(&e); err != nil {
					h.logger.Warn("read schedule_enabled", "agent_id", agentID, "error", err)
				}
				enabled = e == 1
			}
			if err := h.scheduleUpdater.UpdateSchedule(r.Context(), agentID, cronStr, promptStr, enabled); err != nil {
				h.logger.Warn("schedule update callback failed", "agent_id", agentID, "error", err)
			}
		} else if _, hasEnabled := body["schedule_enabled"]; hasEnabled {
			var cronStr, promptStr sql.NullString
			if err := h.db.QueryRowContext(r.Context(), "SELECT schedule_cron, schedule_prompt FROM agents WHERE id = ?", agentID).Scan(&cronStr, &promptStr); err != nil {
				h.logger.Warn("read schedule fields", "agent_id", agentID, "error", err)
			}
			enabledVal := body["schedule_enabled"]
			enabled := false
			switch v := enabledVal.(type) {
			case bool:
				enabled = v
			case float64:
				enabled = v == 1
			}
			cron := ""
			if cronStr.Valid {
				cron = cronStr.String
			}
			prompt := ""
			if promptStr.Valid {
				prompt = promptStr.String
			}
			if err := h.scheduleUpdater.UpdateSchedule(r.Context(), agentID, cron, prompt, enabled); err != nil {
				h.logger.Warn("schedule update callback failed", "agent_id", agentID, "error", err)
			}
		}
	}

	h.Get(w, r)

	h.broadcastAgentEvent("agent.updated", workspaceID, map[string]string{"id": agentID})
}

// Delete soft-deletes an agent by setting deleted_at.
// DELETE /api/v1/agents/{agentId}
