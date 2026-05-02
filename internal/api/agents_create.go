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
	Name           string  `json:"name"`
	Slug           string  `json:"slug"`
	CrewID         *string `json:"crew_id"`
	Description    *string `json:"description"`
	RoleTitle      *string `json:"role_title"`
	AgentRole      string  `json:"agent_role"`
	LeadMode       *string `json:"lead_mode"`
	CLIAdapter     string  `json:"cli_adapter"`
	LLMProvider    *string `json:"llm_provider"`
	LLMModel       *string `json:"llm_model"`
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

	if !canRole(role, "create") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Forbidden"})
		return
	}

	var req createAgentRequest
	if err := readJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid JSON body"})
		return
	}

	if req.Name == "" || len(req.Name) < 2 || len(req.Name) > 100 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name must be 2-100 characters"})
		return
	}
	if req.Slug == "" || len(req.Slug) < 2 || len(req.Slug) > 50 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "slug must be 2-50 characters"})
		return
	}
	// V-17: Validate slug format to prevent injection via container names / file paths
	if !validSlugFormat(req.Slug) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "slug must contain only lowercase letters, numbers, and hyphens"})
		return
	}

	if req.AgentRole == "" {
		req.AgentRole = "AGENT"
	}
	if !validAgentRoles[req.AgentRole] {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "agent_role must be AGENT, LEAD, or COORDINATOR"})
		return
	}

	// LEAD requires crew_id
	if req.AgentRole == "LEAD" && (req.CrewID == nil || *req.CrewID == "") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "LEAD role requires crew_id"})
		return
	}
	// COORDINATOR must NOT have crew_id
	if req.AgentRole == "COORDINATOR" && req.CrewID != nil && *req.CrewID != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "COORDINATOR role must not have crew_id"})
		return
	}

	// License: check agent-per-crew limit
	if h.license != nil && req.CrewID != nil && *req.CrewID != "" {
		if err := h.license.CheckAgentLimit(r.Context(), h.db, *req.CrewID); err != nil {
			if license.IsLimitError(err) {
				writeJSON(w, http.StatusPaymentRequired, map[string]string{"error": err.Error()})
				return
			}
			h.logger.Error("check agent limit", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
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
			writeJSON(w, http.StatusConflict, map[string]string{"error": "Crew already has a lead agent"})
			return
		}
		if err != sql.ErrNoRows {
			h.logger.Error("check existing lead", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
			return
		}
	}

	// Validate lead_mode
	if req.LeadMode != nil && *req.LeadMode != "" {
		if !validLeadModes[*req.LeadMode] {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "lead_mode must be 'active' or 'passive'"})
			return
		}
	}

	if req.CLIAdapter == "" {
		req.CLIAdapter = "CLAUDE_CODE"
	}
	if req.ToolProfile == "" {
		req.ToolProfile = "CODING"
	}
	if req.TimeoutSeconds == 0 {
		req.TimeoutSeconds = 1800
	}

	var existingID string
	err := h.db.QueryRowContext(r.Context(),
		"SELECT id FROM agents WHERE workspace_id = ? AND slug = ? AND deleted_at IS NULL", workspaceID, req.Slug).Scan(&existingID)
	if err == nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "Agent slug already taken in this workspace"})
		return
	}
	if err != sql.ErrNoRows {
		h.logger.Error("check agent slug", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
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

	_, err = h.db.ExecContext(r.Context(), `
		INSERT INTO agents (id, crew_id, workspace_id, name, slug, description, role_title,
			agent_role, lead_mode, status, cli_adapter, llm_provider, llm_model, system_prompt,
			avatar_seed, avatar_style, timeout_seconds, tool_profile, memory_enabled,
			created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'IDLE', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		agentID, req.CrewID, workspaceID, req.Name, req.Slug, req.Description, req.RoleTitle,
		req.AgentRole, leadMode, req.CLIAdapter, req.LLMProvider, req.LLMModel, req.SystemPrompt,
		req.AvatarSeed, req.AvatarStyle, req.TimeoutSeconds, req.ToolProfile, memEnabled,
		now, now)
	if err != nil {
		h.logger.Error("insert agent", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	user := UserFromContext(r.Context())
	userID := ""
	if user != nil {
		userID = user.ID
	}
	WriteAuditLog(r.Context(), h.db, "create", "AGENT", agentID, userID, workspaceID, map[string]interface{}{
		"name": req.Name, "slug": req.Slug, "cli_adapter": req.CLIAdapter,
	})

	// Auto-assign workspace AI credentials so the agent can chat
	// immediately. Without this, the agent runs claude CLI but with no
	// API key — the run completes with empty output and the user sees
	// silence. Captain and crew templates already do this; we now match
	// them so wizard-created agents are equally functional.
	autoAssignCredentials(r.Context(), h.db, workspaceID, agentID, now)

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
