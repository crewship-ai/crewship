package api

// Agent read paths + Delete + Load — list, get, soft-delete, and the
// snapshot loader used by the agent canvas. Extracted from agents.go.

import (
	"database/sql"
	"fmt"
	"net/http"
	"time"
)

func (h *AgentHandler) List(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	if workspaceID == "" {
		replyError(w, http.StatusBadRequest, "workspace_id is required")
		return
	}

	crewID := r.URL.Query().Get("crew_id")
	limit, offset := parseListPagination(r, 100, 500)

	// Main query: no more per-row scalar COUNT subqueries. Those are batched
	// below in three GROUP BY queries keyed by agent_id so the cost is O(1)
	// extra round-trips instead of O(N) per-row scans.
	const listQuery = `
		SELECT a.id, a.crew_id, a.workspace_id, a.name, a.slug, a.description, a.role_title,
			a.agent_role, a.lead_mode, a.status, a.cli_adapter, a.llm_provider, a.llm_model,
			a.system_prompt_legacy, a.avatar_seed, a.avatar_style, a.timeout_seconds,
			a.tool_profile, a.memory_enabled, a.cli_tools,
			a.schedule_cron, a.schedule_prompt, a.schedule_enabled, a.schedule_last_run, a.schedule_next_run,
			a.mcp_config_json,
			a.created_at, a.updated_at,
			c.name, c.slug, c.color, c.avatar_style
		FROM agents a
		LEFT JOIN crews c ON c.id = a.crew_id
		WHERE a.workspace_id = ? AND a.deleted_at IS NULL
	`

	var rows *sql.Rows
	var err error

	// a.id DESC is the pagination tiebreaker: created_at is stored with
	// second precision, so ties on busy workspaces are realistic. Without a
	// unique secondary sort key, LIMIT/OFFSET windows can drop or duplicate
	// rows between pages when the tied rows straddle a page boundary.
	if crewID != "" {
		rows, err = h.db.QueryContext(r.Context(),
			listQuery+" AND a.crew_id = ? ORDER BY a.created_at DESC, a.id DESC LIMIT ? OFFSET ?",
			workspaceID, crewID, limit, offset)
	} else {
		rows, err = h.db.QueryContext(r.Context(),
			listQuery+" ORDER BY a.created_at DESC, a.id DESC LIMIT ? OFFSET ?",
			workspaceID, limit, offset)
	}

	if err != nil {
		h.logger.Error("list agents", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	defer rows.Close()

	result := make([]agentResponse, 0, capacityHint(limit))
	for rows.Next() {
		var a agentResponse
		var memEnabled, schedEnabled int
		var crewName, crewSlug, crewColor, crewAvatarStyle *string
		if err := rows.Scan(&a.ID, &a.CrewID, &a.WorkspaceID, &a.Name, &a.Slug,
			&a.Description, &a.RoleTitle, &a.AgentRole, &a.LeadMode, &a.Status, &a.CLIAdapter,
			&a.LLMProvider, &a.LLMModel, &a.SystemPrompt, &a.AvatarSeed, &a.AvatarStyle,
			&a.TimeoutSeconds, &a.ToolProfile, &memEnabled, &a.CLITools,
			&a.ScheduleCron, &a.SchedulePrompt, &schedEnabled, &a.ScheduleLastRun, &a.ScheduleNextRun,
			&a.MCPConfigJSON,
			&a.CreatedAt, &a.UpdatedAt,
			&crewName, &crewSlug, &crewColor, &crewAvatarStyle); err != nil {
			h.logger.Error("scan agent", "error", err)
			replyError(w, http.StatusInternalServerError, "Internal server error")
			return
		}
		a.MemoryEnabled = memEnabled == 1
		a.ScheduleEnabled = schedEnabled == 1
		if crewName != nil {
			a.Crew = &agentCrewInfo{Name: *crewName, Slug: *crewSlug, Color: crewColor, AvatarStyle: crewAvatarStyle}
		}
		result = append(result, a)
	}
	if err := rows.Err(); err != nil {
		h.logger.Error("rows iteration (agents)", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	// Batch-load the three count buckets in one round-trip each.
	if len(result) > 0 {
		ids := make([]string, len(result))
		for i, a := range result {
			ids[i] = a.ID
		}
		byID := make(map[string]*agentResponse, len(result))
		for i := range result {
			byID[result[i].ID] = &result[i]
		}

		// loadCounts returns an error so the handler can fail the whole
		// request when a batch query fails. The old "log and continue"
		// shape masked query/schema regressions: a broken GROUP BY would
		// still return HTTP 200 with zeroed _count fields, and the UI
		// would quietly show "0 skills" for every agent until someone
		// eventually noticed. Failing loud is the same behavior the
		// original single-query List handler had.
		loadCounts := func(bucket, query string, assign func(*agentResponse, int)) error {
			counts, err := batchCountByAgentID(r.Context(), h.db, query, ids)
			if err != nil {
				return fmt.Errorf("%s batch count: %w", bucket, err)
			}
			for id, n := range counts {
				if a, ok := byID[id]; ok {
					assign(a, n)
				}
			}
			return nil
		}

		for _, step := range []struct {
			bucket string
			query  string
			assign func(*agentResponse, int)
		}{
			{"skills",
				`SELECT agent_id, COUNT(*) FROM agent_skills WHERE agent_id IN (%s) GROUP BY agent_id`,
				func(a *agentResponse, n int) { a.Count.Skills = n }},
			{"credentials",
				`SELECT agent_id, COUNT(*) FROM agent_credentials WHERE agent_id IN (%s) GROUP BY agent_id`,
				func(a *agentResponse, n int) { a.Count.Credentials = n }},
			{"chats",
				`SELECT agent_id, COUNT(*) FROM chats WHERE agent_id IN (%s) GROUP BY agent_id`,
				func(a *agentResponse, n int) { a.Count.Chats = n }},
		} {
			if err := loadCounts(step.bucket, step.query, step.assign); err != nil {
				h.logger.Error("batch count", "bucket", step.bucket, "error", err)
				replyError(w, http.StatusInternalServerError, "Internal server error")
				return
			}
		}
	}

	writeJSON(w, http.StatusOK, result)
}

// batchCountByAgentID lives in agents_loaders.go — agent-specific
// batch helper kept out of the handler file.

// parseListPagination pulls standard ?limit=&offset= params, clamping to sane
// bounds. defaultLimit is used when unspecified; maxLimit caps what clients
// can request. Shared helper for list endpoints.

func (h *AgentHandler) Get(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agentId")
	if agentID == "" {
		replyError(w, http.StatusBadRequest, "agentId is required")
		return
	}

	workspaceID := WorkspaceIDFromContext(r.Context())

	var a agentResponse
	var memEnabled, schedEnabled int
	var crewName, crewSlug, crewColor, crewAvatarStyle *string
	err := h.db.QueryRowContext(r.Context(), `
		SELECT a.id, a.crew_id, a.workspace_id, a.name, a.slug, a.description, a.role_title,
			a.agent_role, a.lead_mode, a.status, a.cli_adapter, a.llm_provider, a.llm_model,
			a.system_prompt_legacy, a.avatar_seed, a.avatar_style, a.timeout_seconds,
			a.tool_profile, a.memory_enabled, a.cli_tools,
			a.schedule_cron, a.schedule_prompt, a.schedule_enabled, a.schedule_last_run, a.schedule_next_run,
			a.mcp_config_json,
			a.created_at, a.updated_at,
			c.name, c.slug, c.color, c.avatar_style,
			(SELECT COUNT(*) FROM agent_skills WHERE agent_id = a.id),
			(SELECT COUNT(*) FROM agent_credentials WHERE agent_id = a.id),
			(SELECT COUNT(*) FROM chats WHERE agent_id = a.id)
		FROM agents a
		LEFT JOIN crews c ON c.id = a.crew_id
		WHERE a.id = ? AND a.workspace_id = ? AND a.deleted_at IS NULL
	`, agentID, workspaceID).Scan(&a.ID, &a.CrewID, &a.WorkspaceID, &a.Name, &a.Slug,
		&a.Description, &a.RoleTitle, &a.AgentRole, &a.LeadMode, &a.Status, &a.CLIAdapter,
		&a.LLMProvider, &a.LLMModel, &a.SystemPrompt, &a.AvatarSeed, &a.AvatarStyle,
		&a.TimeoutSeconds, &a.ToolProfile, &memEnabled, &a.CLITools,
		&a.ScheduleCron, &a.SchedulePrompt, &schedEnabled, &a.ScheduleLastRun, &a.ScheduleNextRun,
		&a.MCPConfigJSON,
		&a.CreatedAt, &a.UpdatedAt,
		&crewName, &crewSlug, &crewColor, &crewAvatarStyle,
		&a.Count.Skills, &a.Count.Credentials, &a.Count.Chats)
	if err != nil {
		if err == sql.ErrNoRows {
			replyError(w, http.StatusNotFound, "Agent not found")
			return
		}
		h.logger.Error("get agent", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	a.MemoryEnabled = memEnabled == 1
	a.ScheduleEnabled = schedEnabled == 1
	if crewName != nil {
		a.Crew = &agentCrewInfo{Name: *crewName, Slug: *crewSlug, Color: crewColor, AvatarStyle: crewAvatarStyle}
	}

	writeJSON(w, http.StatusOK, a)
}

// Update modifies agent properties such as name, role, model, system prompt, and schedule.
// PATCH /api/v1/agents/{agentId}

func (h *AgentHandler) Delete(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agentId")
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())

	if !canRole(role, "manage") {
		replyError(w, http.StatusForbidden, "Forbidden")
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)
	res, err := h.db.ExecContext(r.Context(),
		"UPDATE agents SET deleted_at = ? WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL",
		now, agentID, workspaceID)
	if err != nil {
		h.logger.Error("delete agent", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		replyError(w, http.StatusNotFound, "Agent not found")
		return
	}

	user := UserFromContext(r.Context())
	userID := ""
	if user != nil {
		userID = user.ID
	}
	WriteAuditLog(r.Context(), h.db, h.journal, "delete", "AGENT", agentID, userID, workspaceID, nil)

	writeJSON(w, http.StatusOK, map[string]bool{"success": true})

	h.broadcastAgentEvent("agent.deleted", workspaceID, map[string]string{"id": agentID})
}

// Load handles GET /api/v1/agent-load — per-agent workload metrics.

func (h *AgentHandler) Load(w http.ResponseWriter, r *http.Request) {
	wsID := WorkspaceIDFromContext(r.Context())

	type agentLoadEntry struct {
		AgentID         string `json:"agent_id"`
		AgentName       string `json:"agent_name"`
		AgentSlug       string `json:"agent_slug"`
		AgentStatus     string `json:"agent_status"`
		ActiveTasks     int    `json:"active_tasks"`
		PendingTasks    int    `json:"pending_tasks"`
		CompletedToday  int    `json:"completed_today"`
		TokensUsedToday int    `json:"tokens_used_today"`
		TokenBudget     int    `json:"token_budget"`
	}

	cutoff := time.Now().Add(-24 * time.Hour).UTC().Format(time.RFC3339)

	// Only join tasks that are currently active/pending OR were completed/failed in the 24h window.
	// This avoids scanning the full mission_tasks history for every agent.
	rows, err := h.db.QueryContext(r.Context(), `
		SELECT
			a.id, a.name, a.slug, a.status,
			COALESCE(SUM(CASE WHEN mt.status = 'IN_PROGRESS' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN mt.status IN ('PENDING', 'BLOCKED') THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN mt.status = 'COMPLETED' AND mt.completed_at >= ? THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(COALESCE(mt.tokens_used, mt.token_count, 0)), 0),
			COALESCE(SUM(CASE WHEN mt.status IN ('IN_PROGRESS', 'PENDING', 'BLOCKED') THEN COALESCE(mt.token_budget, 0) ELSE 0 END), 0)
		FROM agents a
		LEFT JOIN mission_tasks mt ON mt.assigned_agent_id = a.id
			AND (mt.status IN ('IN_PROGRESS', 'PENDING', 'BLOCKED') OR mt.completed_at >= ?)
		WHERE a.workspace_id = ? AND a.deleted_at IS NULL
		GROUP BY a.id, a.name, a.slug, a.status
		ORDER BY 5 DESC, 6 DESC`,
		cutoff, cutoff, wsID)
	if err != nil {
		h.logger.Error("agent load query", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	defer rows.Close()

	var result []agentLoadEntry
	for rows.Next() {
		var e agentLoadEntry
		if err := rows.Scan(&e.AgentID, &e.AgentName, &e.AgentSlug, &e.AgentStatus,
			&e.ActiveTasks, &e.PendingTasks, &e.CompletedToday,
			&e.TokensUsedToday, &e.TokenBudget); err != nil {
			h.logger.Error("scan agent load", "error", err)
			replyError(w, http.StatusInternalServerError, "Internal server error")
			return
		}
		result = append(result, e)
	}
	if err := rows.Err(); err != nil {
		h.logger.Error("rows iteration (agent load)", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	if result == nil {
		result = []agentLoadEntry{}
	}
	writeJSON(w, http.StatusOK, result)
}
