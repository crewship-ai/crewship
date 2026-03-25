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

	"github.com/crewship-ai/crewship/internal/license"
	"github.com/crewship-ai/crewship/internal/ws"
)

// ScheduleUpdater is implemented by the scheduler to receive live schedule changes.
type ScheduleUpdater interface {
	UpdateSchedule(ctx context.Context, agentID, cronExpr, prompt string, enabled bool) error
}

type AgentHandler struct {
	db               *sql.DB
	hub              *ws.Hub
	logger           *slog.Logger
	license          *license.License
	scheduleUpdater  ScheduleUpdater
}

func NewAgentHandler(db *sql.DB, logger *slog.Logger) *AgentHandler {
	return &AgentHandler{db: db, logger: logger}
}

func (h *AgentHandler) SetHub(hub *ws.Hub) { h.hub = hub }

func (h *AgentHandler) broadcastAgentEvent(eventType, workspaceID string, payload map[string]string) {
	if h.hub == nil {
		return
	}
	h.hub.Broadcast("workspace:"+workspaceID, ws.ServerMessage{
		Type:    eventType,
		Channel: "workspace:" + workspaceID,
		Payload: payload,
	})
}

func (h *AgentHandler) SetLicense(lic *license.License) { h.license = lic }
func (h *AgentHandler) SetScheduler(su ScheduleUpdater) { h.scheduleUpdater = su }

// FleetStatus returns lightweight agent counts by status for the toolbar.
func (h *AgentHandler) FleetStatus(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	rows, err := h.db.QueryContext(r.Context(),
		`SELECT status, COUNT(*) FROM agents WHERE workspace_id = ? AND deleted_at IS NULL GROUP BY status`,
		workspaceID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	defer rows.Close()

	result := struct {
		Total   int `json:"total"`
		Running int `json:"running"`
		Error   int `json:"error"`
		Idle    int `json:"idle"`
	}{}
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			continue
		}
		result.Total += count
		switch status {
		case "RUNNING":
			result.Running += count
		case "ERROR":
			result.Error += count
		default:
			result.Idle += count
		}
	}
	writeJSON(w, http.StatusOK, result)
}

var validAgentRoles = map[string]bool{
	"AGENT":       true,
	"LEAD":        true,
	"COORDINATOR": true,
}

var validLeadModes = map[string]bool{
	"active":  true,
	"passive": true,
}

type agentCrewInfo struct {
	Name        string  `json:"name"`
	Slug        string  `json:"slug"`
	Color       *string `json:"color"`
	AvatarStyle *string `json:"avatar_style"`
}

type agentCounts struct {
	Skills      int `json:"skills"`
	Credentials int `json:"credentials"`
	Chats       int `json:"chats"`
}

type agentResponse struct {
	ID              string         `json:"id"`
	CrewID          *string        `json:"crew_id"`
	WorkspaceID     string         `json:"workspace_id"`
	Name            string         `json:"name"`
	Slug            string         `json:"slug"`
	Description     *string        `json:"description"`
	RoleTitle       *string        `json:"role_title"`
	AgentRole       string         `json:"agent_role"`
	LeadMode        *string        `json:"lead_mode"`
	Status          string         `json:"status"`
	CLIAdapter      string         `json:"cli_adapter"`
	LLMProvider     *string        `json:"llm_provider"`
	LLMModel        *string        `json:"llm_model"`
	SystemPrompt    *string        `json:"system_prompt"`
	AvatarSeed      *string        `json:"avatar_seed"`
	AvatarStyle     *string        `json:"avatar_style"`
	TimeoutSeconds  int            `json:"timeout_seconds"`
	ToolProfile     string         `json:"tool_profile"`
	MemoryEnabled   bool           `json:"memory_enabled"`
	CLITools        *string        `json:"cli_tools"`
	ScheduleCron    *string        `json:"schedule_cron"`
	SchedulePrompt  *string        `json:"schedule_prompt"`
	ScheduleEnabled bool           `json:"schedule_enabled"`
	ScheduleLastRun *string        `json:"schedule_last_run"`
	ScheduleNextRun *string        `json:"schedule_next_run"`
	CreatedAt       string         `json:"created_at"`
	UpdatedAt       string         `json:"updated_at"`
	Crew            *agentCrewInfo `json:"crew"`
	Count           agentCounts    `json:"_count"`
}

func (h *AgentHandler) List(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	if workspaceID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "workspace_id is required"})
		return
	}

	crewID := r.URL.Query().Get("crew_id")

	const listQuery = `
		SELECT a.id, a.crew_id, a.workspace_id, a.name, a.slug, a.description, a.role_title,
			a.agent_role, a.lead_mode, a.status, a.cli_adapter, a.llm_provider, a.llm_model,
			a.system_prompt, a.avatar_seed, a.avatar_style, a.timeout_seconds,
			a.tool_profile, a.memory_enabled, a.cli_tools,
			a.schedule_cron, a.schedule_prompt, a.schedule_enabled, a.schedule_last_run, a.schedule_next_run,
			a.created_at, a.updated_at,
			c.name, c.slug, c.color, c.avatar_style,
			(SELECT COUNT(*) FROM agent_skills WHERE agent_id = a.id),
			(SELECT COUNT(*) FROM agent_credentials WHERE agent_id = a.id),
			(SELECT COUNT(*) FROM chats WHERE agent_id = a.id)
		FROM agents a
		LEFT JOIN crews c ON c.id = a.crew_id
		WHERE a.workspace_id = ? AND a.deleted_at IS NULL
	`

	var rows *sql.Rows
	var err error

	if crewID != "" {
		rows, err = h.db.QueryContext(r.Context(), listQuery+" AND a.crew_id = ? ORDER BY a.created_at DESC", workspaceID, crewID)
	} else {
		rows, err = h.db.QueryContext(r.Context(), listQuery+" ORDER BY a.created_at DESC", workspaceID)
	}

	if err != nil {
		h.logger.Error("list agents", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	defer rows.Close()

	var result []agentResponse
	for rows.Next() {
		var a agentResponse
		var memEnabled, schedEnabled int
		var crewName, crewSlug, crewColor, crewAvatarStyle *string
		if err := rows.Scan(&a.ID, &a.CrewID, &a.WorkspaceID, &a.Name, &a.Slug,
			&a.Description, &a.RoleTitle, &a.AgentRole, &a.LeadMode, &a.Status, &a.CLIAdapter,
			&a.LLMProvider, &a.LLMModel, &a.SystemPrompt, &a.AvatarSeed, &a.AvatarStyle,
			&a.TimeoutSeconds, &a.ToolProfile, &memEnabled, &a.CLITools,
			&a.ScheduleCron, &a.SchedulePrompt, &schedEnabled, &a.ScheduleLastRun, &a.ScheduleNextRun,
			&a.CreatedAt, &a.UpdatedAt,
			&crewName, &crewSlug, &crewColor, &crewAvatarStyle,
			&a.Count.Skills, &a.Count.Credentials, &a.Count.Chats); err != nil {
			h.logger.Error("scan agent", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
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
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	if result == nil {
		result = []agentResponse{}
	}

	writeJSON(w, http.StatusOK, result)
}

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

func (h *AgentHandler) Get(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agentId")
	if agentID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "agentId is required"})
		return
	}

	workspaceID := WorkspaceIDFromContext(r.Context())

	var a agentResponse
	var memEnabled, schedEnabled int
	var crewName, crewSlug, crewColor, crewAvatarStyle *string
	err := h.db.QueryRowContext(r.Context(), `
		SELECT a.id, a.crew_id, a.workspace_id, a.name, a.slug, a.description, a.role_title,
			a.agent_role, a.lead_mode, a.status, a.cli_adapter, a.llm_provider, a.llm_model,
			a.system_prompt, a.avatar_seed, a.avatar_style, a.timeout_seconds,
			a.tool_profile, a.memory_enabled, a.cli_tools,
			a.schedule_cron, a.schedule_prompt, a.schedule_enabled, a.schedule_last_run, a.schedule_next_run,
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
		&a.CreatedAt, &a.UpdatedAt,
		&crewName, &crewSlug, &crewColor, &crewAvatarStyle,
		&a.Count.Skills, &a.Count.Credentials, &a.Count.Chats)
	if err != nil {
		if err == sql.ErrNoRows {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "Agent not found"})
			return
		}
		h.logger.Error("get agent", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	a.MemoryEnabled = memEnabled == 1
	a.ScheduleEnabled = schedEnabled == 1
	if crewName != nil {
		a.Crew = &agentCrewInfo{Name: *crewName, Slug: *crewSlug, Color: crewColor, AvatarStyle: crewAvatarStyle}
	}

	writeJSON(w, http.StatusOK, a)
}

func (h *AgentHandler) Update(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agentId")
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())

	if !canRole(role, "create") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Forbidden"})
		return
	}

	var existing string
	if err := h.db.QueryRowContext(r.Context(),
		"SELECT id FROM agents WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL",
		agentID, workspaceID).Scan(&existing); err != nil {
		if err == sql.ErrNoRows {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "Agent not found"})
			return
		}
		h.logger.Error("check agent exists", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
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
		"lead_mode": "lead_mode",
		"cli_adapter": "cli_adapter", "llm_provider": "llm_provider",
		"llm_model": "llm_model", "system_prompt": "system_prompt",
		"avatar_seed": "avatar_seed", "avatar_style": "avatar_style",
		"timeout_seconds": "timeout_seconds", "tool_profile": "tool_profile",
		"memory_enabled": "memory_enabled", "cli_tools": "cli_tools", "crew_id": "crew_id",
		"schedule_cron": "schedule_cron", "schedule_prompt": "schedule_prompt",
		"schedule_enabled": "schedule_enabled",
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

	var setClauses []string
	var args []interface{}
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
			setClauses = append(setClauses, col+" = ?")
			args = append(args, val)
		}
	}

	if len(setClauses) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "No fields to update"})
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)
	setClauses = append(setClauses, "updated_at = ?")
	args = append(args, now, agentID, workspaceID)

	query := fmt.Sprintf("UPDATE agents SET %s WHERE id = ? AND workspace_id = ?", strings.Join(setClauses, ", "))
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

func (h *AgentHandler) Delete(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agentId")
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())

	if !canRole(role, "manage") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Forbidden"})
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)
	res, err := h.db.ExecContext(r.Context(),
		"UPDATE agents SET deleted_at = ? WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL",
		now, agentID, workspaceID)
	if err != nil {
		h.logger.Error("delete agent", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Agent not found"})
		return
	}

	user := UserFromContext(r.Context())
	userID := ""
	if user != nil {
		userID = user.ID
	}
	WriteAuditLog(r.Context(), h.db, "delete", "AGENT", agentID, userID, workspaceID, nil)

	writeJSON(w, http.StatusOK, map[string]bool{"success": true})

	h.broadcastAgentEvent("agent.deleted", workspaceID, map[string]string{"id": agentID})
}

type agentSkillSkillData struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Slug        string  `json:"slug"`
	DisplayName *string `json:"display_name"`
	Description *string `json:"description"`
	Category    *string `json:"category"`
	Source      string  `json:"source"`
	Icon        *string `json:"icon"`
	Version     *string `json:"version"`
}

type agentSkillResponse struct {
	ID      string              `json:"id"`
	AgentID string              `json:"agent_id"`
	SkillID string              `json:"skill_id"`
	Enabled bool                `json:"enabled"`
	Config  *string             `json:"config"`
	Skill   agentSkillSkillData `json:"skill"`
}

func (h *AgentHandler) ListSkills(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agentId")
	workspaceID := WorkspaceIDFromContext(r.Context())

	var exists string
	if err := h.db.QueryRowContext(r.Context(),
		"SELECT id FROM agents WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL",
		agentID, workspaceID).Scan(&exists); err != nil {
		if err == sql.ErrNoRows {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "Agent not found"})
			return
		}
		h.logger.Error("check agent exists", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	rows, err := h.db.QueryContext(r.Context(), `
		SELECT as2.id, as2.agent_id, as2.skill_id, as2.enabled, as2.config,
			s.id, s.name, s.slug, s.display_name, s.description,
			s.category, s.source, s.icon, s.version
		FROM agent_skills as2
		JOIN skills s ON s.id = as2.skill_id
		WHERE as2.agent_id = ?
	`, agentID)
	if err != nil {
		h.logger.Error("list agent skills", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	defer rows.Close()

	var result []agentSkillResponse
	for rows.Next() {
		var s agentSkillResponse
		var enabled int
		if err := rows.Scan(&s.ID, &s.AgentID, &s.SkillID, &enabled, &s.Config,
			&s.Skill.ID, &s.Skill.Name, &s.Skill.Slug, &s.Skill.DisplayName,
			&s.Skill.Description, &s.Skill.Category, &s.Skill.Source,
			&s.Skill.Icon, &s.Skill.Version); err != nil {
			h.logger.Error("scan agent skill", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
			return
		}
		s.Enabled = enabled == 1
		result = append(result, s)
	}
	if err := rows.Err(); err != nil {
		h.logger.Error("rows iteration (agent skills)", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	if result == nil {
		result = []agentSkillResponse{}
	}
	writeJSON(w, http.StatusOK, result)
}

type addAgentSkillRequest struct {
	SkillID string  `json:"skill_id"`
	Config  *string `json:"config"`
}

func (h *AgentHandler) AddSkill(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agentId")
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())

	if !canRole(role, "create") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Forbidden"})
		return
	}

	var exists string
	if err := h.db.QueryRowContext(r.Context(),
		"SELECT id FROM agents WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL",
		agentID, workspaceID).Scan(&exists); err != nil {
		if err == sql.ErrNoRows {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "Agent not found"})
			return
		}
		h.logger.Error("check agent exists", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	var req addAgentSkillRequest
	if err := readJSON(r, &req); err != nil || req.SkillID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "skill_id is required"})
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)
	id := generateCUID()

	_, err := h.db.ExecContext(r.Context(),
		"INSERT INTO agent_skills (id, agent_id, skill_id, config, enabled, created_at) VALUES (?, ?, ?, ?, 1, ?)",
		id, agentID, req.SkillID, req.Config, now)
	if err != nil {
		h.logger.Error("add agent skill", "error", err)
		writeJSON(w, http.StatusConflict, map[string]string{"error": "Skill already assigned to agent"})
		return
	}

	writeJSON(w, http.StatusCreated, map[string]string{"id": id})
}

func (h *AgentHandler) RemoveSkill(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agentId")
	skillID := r.PathValue("skillId")
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())

	if !canRole(role, "create") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Forbidden"})
		return
	}

	var exists string
	if err := h.db.QueryRowContext(r.Context(),
		"SELECT id FROM agents WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL",
		agentID, workspaceID).Scan(&exists); err != nil {
		if err == sql.ErrNoRows {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "Agent not found"})
			return
		}
		h.logger.Error("check agent exists", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	res, err := h.db.ExecContext(r.Context(),
		"DELETE FROM agent_skills WHERE agent_id = ? AND skill_id = ?",
		agentID, skillID)
	if err != nil {
		h.logger.Error("remove agent skill", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Skill not assigned to agent"})
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

type agentCredentialResponse struct {
	ID           string  `json:"id"`
	AgentID      string  `json:"agent_id"`
	CredentialID string  `json:"credential_id"`
	CredName     string  `json:"credential_name"`
	CredType     string  `json:"credential_type"`
	CredProvider string  `json:"credential_provider"`
	CredStatus   string  `json:"credential_status"`
	EnvVarName   string  `json:"env_var_name"`
	Priority     int     `json:"priority"`
	CreatedAt    string  `json:"created_at"`
}

func (h *AgentHandler) ListCredentials(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agentId")
	workspaceID := WorkspaceIDFromContext(r.Context())

	var exists string
	if err := h.db.QueryRowContext(r.Context(),
		"SELECT id FROM agents WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL",
		agentID, workspaceID).Scan(&exists); err != nil {
		if err == sql.ErrNoRows {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "Agent not found"})
			return
		}
		h.logger.Error("check agent exists", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	rows, err := h.db.QueryContext(r.Context(), `
		SELECT ac.id, ac.agent_id, ac.credential_id, c.name, c.type, c.provider, c.status,
			ac.env_var_name, ac.priority, ac.created_at
		FROM agent_credentials ac
		JOIN credentials c ON c.id = ac.credential_id
		WHERE ac.agent_id = ?
		ORDER BY ac.env_var_name, ac.priority DESC
	`, agentID)
	if err != nil {
		h.logger.Error("list agent credentials", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	defer rows.Close()

	var result []agentCredentialResponse
	for rows.Next() {
		var c agentCredentialResponse
		if err := rows.Scan(&c.ID, &c.AgentID, &c.CredentialID, &c.CredName,
			&c.CredType, &c.CredProvider, &c.CredStatus,
			&c.EnvVarName, &c.Priority, &c.CreatedAt); err != nil {
			h.logger.Error("scan agent credential", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
			return
		}
		result = append(result, c)
	}
	if err := rows.Err(); err != nil {
		h.logger.Error("rows iteration (agent credentials)", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	if result == nil {
		result = []agentCredentialResponse{}
	}
	writeJSON(w, http.StatusOK, result)
}

type addAgentCredentialRequest struct {
	CredentialID string `json:"credential_id"`
	EnvVarName   string `json:"env_var_name"`
	Priority     int    `json:"priority"`
}

func (h *AgentHandler) AddCredential(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agentId")
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())

	if !canRole(role, "manage") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Forbidden"})
		return
	}

	var exists string
	if err := h.db.QueryRowContext(r.Context(),
		"SELECT id FROM agents WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL",
		agentID, workspaceID).Scan(&exists); err != nil {
		if err == sql.ErrNoRows {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "Agent not found"})
			return
		}
		h.logger.Error("check agent exists", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	var req addAgentCredentialRequest
	if err := readJSON(r, &req); err != nil || req.CredentialID == "" || req.EnvVarName == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "credential_id and env_var_name are required"})
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)
	id := generateCUID()

	_, err := h.db.ExecContext(r.Context(),
		`INSERT INTO agent_credentials (id, agent_id, credential_id, env_var_name, priority, created_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		id, agentID, req.CredentialID, req.EnvVarName, req.Priority, now)
	if err != nil {
		h.logger.Error("add agent credential", "error", err)
		writeJSON(w, http.StatusConflict, map[string]string{"error": "Credential already assigned to agent"})
		return
	}

	writeJSON(w, http.StatusCreated, map[string]string{"id": id})
}

func (h *AgentHandler) RemoveCredential(w http.ResponseWriter, r *http.Request) {
	assignmentID := r.PathValue("assignmentId")
	agentID := r.PathValue("agentId")
	role := RoleFromContext(r.Context())

	if !canRole(role, "manage") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Forbidden"})
		return
	}

	res, err := h.db.ExecContext(r.Context(),
		"DELETE FROM agent_credentials WHERE id = ? AND agent_id = ?",
		assignmentID, agentID)
	if err != nil {
		h.logger.Error("remove agent credential", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Assignment not found"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}

func (h *AgentHandler) ListChats(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agentId")
	workspaceID := WorkspaceIDFromContext(r.Context())

	rows, err := h.db.QueryContext(r.Context(), `
		SELECT id, agent_id, workspace_id, title, mode, status,
			message_count, started_at, ended_at, created_at
		FROM chats
		WHERE agent_id = ? AND workspace_id = ?
		ORDER BY created_at DESC
		LIMIT 100
	`, agentID, workspaceID)
	if err != nil {
		h.logger.Error("list agent chats", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	defer rows.Close()

	type chatResponse struct {
		ID           string  `json:"id"`
		AgentID      string  `json:"agent_id"`
		WorkspaceID  string  `json:"workspace_id"`
		Title        *string `json:"title"`
		Mode         string  `json:"mode"`
		Status       string  `json:"status"`
		MessageCount int     `json:"message_count"`
		StartedAt    string  `json:"started_at"`
		EndedAt      *string `json:"ended_at"`
		CreatedAt    string  `json:"created_at"`
	}

	var result []chatResponse
	for rows.Next() {
		var c chatResponse
		if err := rows.Scan(&c.ID, &c.AgentID, &c.WorkspaceID, &c.Title,
			&c.Mode, &c.Status, &c.MessageCount,
			&c.StartedAt, &c.EndedAt, &c.CreatedAt); err != nil {
			h.logger.Error("scan chat", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
			return
		}
		result = append(result, c)
	}
	if err := rows.Err(); err != nil {
		h.logger.Error("rows iteration (chats)", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	if result == nil {
		result = []chatResponse{}
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *AgentHandler) CreateChat(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agentId")
	workspaceID := WorkspaceIDFromContext(r.Context())
	userID := UserFromContext(r.Context()).ID

	var body struct {
		SessionID string `json:"session_id"`
	}
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid request"})
		return
	}

	chatID := body.SessionID
	if chatID == "" {
		chatID = generateCUID()
	}

	// Check agent exists
	var exists string
	if err := h.db.QueryRowContext(r.Context(),
		"SELECT id FROM agents WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL",
		agentID, workspaceID).Scan(&exists); err != nil {
		if err == sql.ErrNoRows {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "Agent not found"})
			return
		}
		h.logger.Error("check agent exists", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	// Upsert: if chat already exists, return it
	var existingID string
	if err := h.db.QueryRowContext(r.Context(),
		"SELECT id FROM chats WHERE id = ? AND agent_id = ?",
		chatID, agentID).Scan(&existingID); err == nil {
		writeJSON(w, http.StatusOK, map[string]string{"id": existingID})
		return
	}

	_, err := h.db.ExecContext(r.Context(),
		`INSERT INTO chats (id, agent_id, workspace_id, created_by, status)
		 VALUES (?, ?, ?, ?, 'ACTIVE')`,
		chatID, agentID, workspaceID, userID)
	if err != nil {
		h.logger.Error("create chat", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	writeJSON(w, http.StatusCreated, map[string]string{"id": chatID})
}

func (h *AgentHandler) ListRuns(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agentId")
	workspaceID := WorkspaceIDFromContext(r.Context())

	rows, err := h.db.QueryContext(r.Context(), `
		SELECT id, agent_id, chat_id, workspace_id, triggered_by,
			trigger_type, status, started_at, finished_at,
			error_message, exit_code, metadata, created_at
		FROM agent_runs
		WHERE agent_id = ? AND workspace_id = ?
		ORDER BY created_at DESC
		LIMIT 100
	`, agentID, workspaceID)
	if err != nil {
		h.logger.Error("list agent runs", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	defer rows.Close()

	var result []runResponse
	for rows.Next() {
		var run runResponse
		var metadataStr sql.NullString
		if err := rows.Scan(&run.ID, &run.AgentID, &run.ChatID, &run.WorkspaceID,
			&run.TriggeredBy, &run.TriggerType, &run.Status,
			&run.StartedAt, &run.FinishedAt, &run.ErrorMessage, &run.ExitCode,
			&metadataStr, &run.CreatedAt); err != nil {
			h.logger.Error("scan run", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
			return
		}
		if metadataStr.Valid {
			run.Metadata = json.RawMessage(metadataStr.String)
		}
		result = append(result, run)
	}
	if err := rows.Err(); err != nil {
		h.logger.Error("rows iteration (runs)", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	if result == nil {
		result = []runResponse{}
	}
	writeJSON(w, http.StatusOK, result)
}
