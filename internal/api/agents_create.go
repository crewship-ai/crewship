package api

// Agent creation handler — large enough on its own to deserve a file.
// Owns createAgentRequest type. Extracted from agents.go.

import (
	"database/sql"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/license"
)

type createAgentRequest struct {
	Name        string  `json:"name"`
	Slug        string  `json:"slug"`
	CrewID      *string `json:"crew_id"`
	Description *string `json:"description"`
	RoleTitle   *string `json:"role_title"`
	AgentRole   string  `json:"agent_role"`
	LeadMode    *string `json:"lead_mode"`
	CLIAdapter  string  `json:"cli_adapter"`
	LLMProvider *string `json:"llm_provider"`
	LLMModel    *string `json:"llm_model"`
	// Deprecated: see agentResponse.SystemPrompt in agents.go — PR-Z
	// Z.3 / PR-E migrate this to the PERSONA.md memory tier. Accepted
	// in create requests for now; new clients should set PERSONA via
	// memory.write once F1 ships.
	SystemPrompt   *string `json:"system_prompt"`
	AvatarSeed     *string `json:"avatar_seed"`
	AvatarStyle    *string `json:"avatar_style"`
	TimeoutSeconds int     `json:"timeout_seconds"`
	ToolProfile    string  `json:"tool_profile"`
	MemoryEnabled  bool    `json:"memory_enabled"`
}

// Create provisions a new agent in the workspace, optionally assigning it to a crew.
// POST /api/v1/agents

func (h *AgentHandler) Create(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())
	user := UserFromContext(r.Context())
	callerUserID := ""
	if user != nil {
		callerUserID = user.ID
	}

	var req createAgentRequest
	if err := readJSON(r, &req); err != nil {
		replyError(w, http.StatusBadRequest, "Invalid JSON body")
		return
	}

	// Patch M5: per-crew role elevation gate. The pre-M5 check was just
	// canRole(role, "create") which read the workspace role from
	// RoleFromContext — a workspace MEMBER promoted to MANAGER inside a
	// specific crew (via crew_members.role from Patch M1) would still
	// see 403 here because the elevation was data-layer only.
	//
	// Now: when the request includes a crew_id, compute the effective
	// role against THAT crew (max of workspace role + per-crew role)
	// and gate on it. When no crew_id is given, fall back to the
	// workspace role only — crewless agents stay workspace-admin
	// concerns. Same semantics as canEditAgent (Patch M3) which
	// already composed M1 + workspace role; M5 brings Create to
	// parity with Update/Delete.
	effective := role
	if req.CrewID != nil && *req.CrewID != "" {
		crewRole, crewErr := CrewRoleFromDB(r.Context(), h.db, callerUserID, *req.CrewID)
		if crewErr != nil {
			h.logger.Error("agent.create: crew role lookup", "error", crewErr,
				"crew_id", *req.CrewID, "user_id", callerUserID)
			replyError(w, http.StatusInternalServerError, "Internal server error")
			return
		}
		// CrewRoleFromDB returns "" when the user isn't a member of
		// the crew's workspace. Don't let that "" leak through and
		// outrank a real workspace role — fall back to workspace.
		if crewRole != "" {
			effective = crewRole
		}
	}
	if !canRole(effective, "create") {
		replyForbidden(w, h.logger, callerUserID, effective,
			"agent.create", "workspace:"+workspaceID)
		return
	}

	if req.Name == "" || len(req.Name) < 2 || len(req.Name) > 100 {
		replyError(w, http.StatusBadRequest, "name must be 2-100 characters")
		return
	}
	if req.Slug == "" || len(req.Slug) < 2 || len(req.Slug) > 50 {
		replyError(w, http.StatusBadRequest, "slug must be 2-50 characters")
		return
	}
	// V-17: Validate slug format to prevent injection via container names / file paths
	if !validSlugFormat(req.Slug) {
		replyError(w, http.StatusBadRequest, "slug must contain only lowercase letters, numbers, and hyphens")
		return
	}

	if req.AgentRole == "" {
		req.AgentRole = "AGENT"
	}
	if !validAgentRoles[req.AgentRole] {
		replyError(w, http.StatusBadRequest, "agent_role must be AGENT or LEAD")
		return
	}

	// LEAD requires crew_id
	if req.AgentRole == "LEAD" && (req.CrewID == nil || *req.CrewID == "") {
		replyError(w, http.StatusBadRequest, "LEAD role requires crew_id")
		return
	}

	// License: check agent-per-crew limit
	if h.license != nil && req.CrewID != nil && *req.CrewID != "" {
		if err := h.license.CheckAgentLimit(r.Context(), h.db, *req.CrewID); err != nil {
			if license.IsLimitError(err) {
				replyError(w, http.StatusPaymentRequired, err.Error())
				return
			}
			h.logger.Error("check agent limit", "error", err)
			replyError(w, http.StatusInternalServerError, "Internal server error")
			return
		}
	}

	// Max 1 LEAD per crew
	if req.AgentRole == "LEAD" && req.CrewID != nil {
		var existingLeadID string
		err := h.db.QueryRowContext(r.Context(),
			"SELECT id FROM agents WHERE crew_id = ? AND agent_role = 'LEAD' AND deleted_at IS NULL",
			*req.CrewID).Scan(&existingLeadID)
		if err == nil {
			replyError(w, http.StatusConflict, "Crew already has a lead agent")
			return
		}
		if err != sql.ErrNoRows {
			h.logger.Error("check existing lead", "error", err)
			replyError(w, http.StatusInternalServerError, "Internal server error")
			return
		}
	}

	// Validate lead_mode
	if req.LeadMode != nil && *req.LeadMode != "" {
		if !validLeadModes[*req.LeadMode] {
			replyError(w, http.StatusBadRequest, "lead_mode must be 'active' or 'passive'")
			return
		}
	}

	if req.CLIAdapter == "" {
		req.CLIAdapter = "CLAUDE_CODE"
	}
	if !validCLIAdapters[req.CLIAdapter] {
		replyError(w, http.StatusBadRequest, "cli_adapter must be CLAUDE_CODE, OPENCODE, CODEX_CLI, GEMINI_CLI, CURSOR_CLI, or FACTORY_DROID")
		return
	}
	if req.LLMProvider != nil && *req.LLMProvider != "" && !validLLMProviders[*req.LLMProvider] {
		replyError(w, http.StatusBadRequest, "llm_provider must be ANTHROPIC, OPENAI, GOOGLE, CURSOR, FACTORY, or OLLAMA")
		return
	}
	if req.ToolProfile == "" {
		req.ToolProfile = "CODING"
	}
	if !validToolProfiles[req.ToolProfile] {
		replyError(w, http.StatusBadRequest, "tool_profile must be MINIMAL, CODING, or FULL")
		return
	}
	if req.TimeoutSeconds == 0 {
		req.TimeoutSeconds = 1800
	}

	var existingID string
	err := h.db.QueryRowContext(r.Context(),
		"SELECT id FROM agents WHERE workspace_id = ? AND slug = ? AND deleted_at IS NULL", workspaceID, req.Slug).Scan(&existingID)
	if err == nil {
		replyError(w, http.StatusConflict, "Agent slug already taken in this workspace")
		return
	}
	if err != sql.ErrNoRows {
		h.logger.Error("check agent slug", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	// Free slug from soft-deleted agents so the UNIQUE constraint doesn't block re-creation.
	if _, err := h.db.ExecContext(r.Context(),
		"UPDATE agents SET slug = slug || '_deleted_' || id WHERE workspace_id = ? AND slug = ? AND deleted_at IS NOT NULL",
		workspaceID, req.Slug); err != nil {
		h.logger.Warn("free deleted agent slug", "slug", req.Slug, "error", err)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	agentID := generateCUID()
	memEnabled := 0
	if req.MemoryEnabled {
		memEnabled = 1
	}

	// Default lead_mode for LEAD
	leadMode := req.LeadMode
	if req.AgentRole == "LEAD" && (leadMode == nil || *leadMode == "") {
		defaultMode := "active"
		leadMode = &defaultMode
	}

	// Patch M3: stamp created_by_user_id so the per-agent edit gate
	// can let a MANAGER edit agents they made without blanket rights
	// over peers' agents. NULL when called from a code path that
	// doesn't carry a user (legacy internal flows); the gate then
	// degrades to workspace-role-only for that agent.
	var createdByUserID sql.NullString
	if callerUserID != "" {
		createdByUserID = sql.NullString{String: callerUserID, Valid: true}
	}

	_, err = h.db.ExecContext(r.Context(), `
		INSERT INTO agents (id, crew_id, workspace_id, name, slug, description, role_title,
			agent_role, lead_mode, status, cli_adapter, llm_provider, llm_model, system_prompt,
			avatar_seed, avatar_style, timeout_seconds, tool_profile, memory_enabled,
			created_by_user_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'IDLE', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		agentID, req.CrewID, workspaceID, req.Name, req.Slug, req.Description, req.RoleTitle,
		req.AgentRole, leadMode, req.CLIAdapter, req.LLMProvider, req.LLMModel, req.SystemPrompt,
		req.AvatarSeed, req.AvatarStyle, req.TimeoutSeconds, req.ToolProfile, memEnabled,
		createdByUserID, now, now)
	if err != nil {
		h.logger.Error("insert agent", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	WriteAuditLog(r.Context(), h.db, h.journal, "create", "AGENT", agentID, callerUserID, workspaceID, map[string]interface{}{
		"name": req.Name, "slug": req.Slug, "cli_adapter": req.CLIAdapter,
	})

	// CLI/UI-created agents require explicit credential assignment per
	// CLAUDE.md policy ("Agents created via CLI/UI assign credentials
	// manually"). The Create Agent dialog surfaces a follow-up prompt to
	// link a workspace credential after the 201; the CLI uses
	// `crewship credential assign`. Auto-assign is reserved for
	// template, Captain, and internal-API flows (see autoAssignCredentials
	// callers in crew_templates.go, captain_tools_mutate.go, internal_status.go).

	writeJSON(w, http.StatusCreated, agentResponse{
		ID:             agentID,
		CrewID:         req.CrewID,
		WorkspaceID:    workspaceID,
		Name:           req.Name,
		Slug:           req.Slug,
		Description:    req.Description,
		RoleTitle:      req.RoleTitle,
		AgentRole:      req.AgentRole,
		LeadMode:       leadMode,
		Status:         "IDLE",
		CLIAdapter:     req.CLIAdapter,
		LLMProvider:    req.LLMProvider,
		LLMModel:       req.LLMModel,
		SystemPrompt:   req.SystemPrompt,
		AvatarSeed:     req.AvatarSeed,
		AvatarStyle:    req.AvatarStyle,
		TimeoutSeconds: req.TimeoutSeconds,
		ToolProfile:    req.ToolProfile,
		MemoryEnabled:  req.MemoryEnabled,
		CreatedAt:      now,
		UpdatedAt:      now,
	})

	h.broadcastAgentEvent("agent.created", workspaceID, map[string]string{
		"id": agentID, "name": req.Name, "slug": req.Slug, "status": "IDLE",
	})
}

// Get returns a single agent by ID with full details including crew info and counts.
// GET /api/v1/agents/{agentId}
