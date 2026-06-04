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
	user := UserFromContext(r.Context())
	callerUserID := ""
	if user != nil {
		callerUserID = user.ID
	}

	// Patch M3: per-agent owner gate. OWNER/ADMIN edit anything;
	// MANAGER edits agents they created OR agents inside crews
	// where they hold ADMIN/OWNER role override (per-crew elevation
	// from Patch M1). MEMBER/VIEWER refused.
	ok, err := canEditAgent(r.Context(), h.db, callerUserID, role, agentID)
	if err != nil {
		h.logger.Error("agent edit gate query failed", "agent_id", agentID, "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	if !ok {
		replyForbidden(w, h.logger, callerUserID, role,
			"agent.update", "agent:"+agentID)
		return
	}

	found, err := agentExists(r.Context(), h.db, agentID, workspaceID)
	if err != nil {
		h.logger.Error("check agent exists", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	if !found {
		replyError(w, http.StatusNotFound, "Agent not found")
		return
	}

	var body map[string]interface{}
	if err := readJSON(r, &body); err != nil {
		replyError(w, http.StatusBadRequest, "Invalid JSON body")
		return
	}

	allowed := map[string]string{
		"name": "name", "slug": "slug", "description": "description",
		"role_title": "role_title", "agent_role": "agent_role",
		"lead_mode":   "lead_mode",
		"cli_adapter": "cli_adapter", "llm_provider": "llm_provider",
		"llm_model": "llm_model", "system_prompt": "system_prompt_legacy",
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
				replyError(w, http.StatusBadRequest, "slug must be 2-50 characters")
				return
			}
			if !validSlugFormat(slugStr) {
				replyError(w, http.StatusBadRequest, "slug must contain only lowercase letters, numbers, underscores, and hyphens")
				return
			}
		}
	}

	// Validate agent_role if being updated
	if roleVal, ok := body["agent_role"]; ok {
		roleStr, _ := roleVal.(string)
		if !validAgentRoles[roleStr] {
			replyError(w, http.StatusBadRequest, "agent_role must be AGENT or LEAD")
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
				replyError(w, http.StatusInternalServerError, "Internal server error")
				return
			}

			if !crewIDNull.Valid || crewIDNull.String == "" {
				replyError(w, http.StatusBadRequest, "LEAD role requires crew_id")
				return
			}

			// Demote existing lead in the same crew
			if _, err := h.db.ExecContext(r.Context(),
				"UPDATE agents SET agent_role = 'AGENT' WHERE crew_id = ? AND agent_role = 'LEAD' AND deleted_at IS NULL AND id != ?",
				crewIDNull.String, agentID); err != nil {
				h.logger.Error("demote existing lead", "error", err)
				replyError(w, http.StatusInternalServerError, "Internal server error")
				return
			}
		}
	}

	// Validate lead_mode if being updated. The presence of the key
	// alone is treated as a write intent — empty / non-string values
	// are rejected so a PATCH cannot persist a blank value via the
	// fall-through default.
	if modeVal, ok := body["lead_mode"]; ok {
		modeStr, isStr := modeVal.(string)
		if !isStr || !validLeadModes[modeStr] {
			replyError(w, http.StatusBadRequest, "lead_mode must be 'active' or 'passive'")
			return
		}
	}

	// Validate cli_adapter / llm_provider / tool_profile when present in the
	// update body. These are required enum columns — the create path rejects
	// empty/null, and an update that sends "" or null would silently clear
	// the column to a value the runtime dispatch can't handle (getAdapter
	// would fall back to a minimal `claude --print` for an empty
	// cli_adapter, etc.). Reject:
	//   - non-string types (numbers, objects, arrays, bool)
	//   - explicit nil  (key present, value json null)
	//   - empty string  (would blank a required column)
	//   - any string outside the validated enum set
	// The `ok` from the map lookup means "key present in body" — if the
	// caller wants to leave the column untouched they simply omit the key.
	if v, ok := body["cli_adapter"]; ok {
		s, isStr := v.(string)
		if !isStr || s == "" {
			replyError(w, http.StatusBadRequest, "cli_adapter must be CLAUDE_CODE, OPENCODE, CODEX_CLI, GEMINI_CLI, CURSOR_CLI, or FACTORY_DROID")
			return
		}
		if !validCLIAdapters[s] {
			replyError(w, http.StatusBadRequest, "cli_adapter must be CLAUDE_CODE, OPENCODE, CODEX_CLI, GEMINI_CLI, CURSOR_CLI, or FACTORY_DROID")
			return
		}
	}
	if v, ok := body["llm_provider"]; ok {
		s, isStr := v.(string)
		if !isStr || s == "" {
			replyError(w, http.StatusBadRequest, "llm_provider must be ANTHROPIC, OPENAI, GOOGLE, CURSOR, FACTORY, or OLLAMA")
			return
		}
		if !validLLMProviders[s] {
			replyError(w, http.StatusBadRequest, "llm_provider must be ANTHROPIC, OPENAI, GOOGLE, CURSOR, FACTORY, or OLLAMA")
			return
		}
	}
	if v, ok := body["tool_profile"]; ok {
		s, isStr := v.(string)
		if !isStr || s == "" {
			replyError(w, http.StatusBadRequest, "tool_profile must be MINIMAL, CODING, or FULL")
			return
		}
		if !validToolProfiles[s] {
			replyError(w, http.StatusBadRequest, "tool_profile must be MINIMAL, CODING, or FULL")
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
				replyError(w, http.StatusBadRequest, "mcp_config_json is not valid JSON: "+err.Error())
				return
			}
			if mcpCheck.MCPServers == nil {
				replyError(w, http.StatusBadRequest, "mcp_config_json must contain a \"mcpServers\" object")
				return
			}
		}
	}

	// Validate crew_id if being updated. crew_id is a relational field —
	// without this check a PATCH could reassign an agent into a crew that
	// belongs to ANOTHER workspace (cross-tenant IDOR), since the update
	// builder applies it verbatim. Mirror the credentials_mutate.go
	// pattern: a present non-empty crew_id must exist in the CALLER's
	// workspace. An explicit empty string (detach from crew) is allowed;
	// a non-string value is rejected.
	if crewVal, ok := body["crew_id"]; ok && crewVal != nil {
		crewStr, isStr := crewVal.(string)
		if !isStr {
			replyError(w, http.StatusBadRequest, "Invalid crew_id")
			return
		}
		if crewStr != "" {
			crewFound, err := crewExists(r.Context(), h.db, crewStr, workspaceID)
			if err != nil {
				h.logger.Error("check crew exists for agent update", "crew_id", crewStr, "error", err)
				replyError(w, http.StatusInternalServerError, "Internal server error")
				return
			}
			if !crewFound {
				replyError(w, http.StatusBadRequest, "Invalid crew_id")
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
		replyError(w, http.StatusBadRequest, "No fields to update")
		return
	}

	query, args := ub.Build("agents", "id = ? AND workspace_id = ?", agentID, workspaceID)
	if _, err := h.db.ExecContext(r.Context(), query, args...); err != nil {
		h.logger.Error("update agent", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	userID := callerUserID
	changes := make(map[string]interface{})
	for jsonKey := range allowed {
		if val, ok := body[jsonKey]; ok {
			changes[jsonKey] = val
		}
	}
	WriteAuditLog(r.Context(), h.db, h.journal, "update", "AGENT", agentID, userID, workspaceID, changes)

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
