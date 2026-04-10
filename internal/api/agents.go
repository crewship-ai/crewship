package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/license"
	"github.com/crewship-ai/crewship/internal/ws"
)

// ScheduleUpdater is implemented by the scheduler to receive live schedule changes.
type ScheduleUpdater interface {
	UpdateSchedule(ctx context.Context, agentID, cronExpr, prompt string, enabled bool) error
}

// AgentHandler provides CRUD endpoints for managing AI agents within a workspace.
type AgentHandler struct {
	db              *sql.DB
	hub             *ws.Hub
	logger          *slog.Logger
	license         *license.License
	scheduleUpdater ScheduleUpdater
}

// NewAgentHandler creates an AgentHandler with the given database and logger.
func NewAgentHandler(db *sql.DB, logger *slog.Logger) *AgentHandler {
	return &AgentHandler{db: db, logger: logger}
}

// SetHub attaches a WebSocket hub for broadcasting agent events to connected clients.
func (h *AgentHandler) SetHub(hub *ws.Hub) { h.hub = hub }

func (h *AgentHandler) broadcastAgentEvent(eventType, workspaceID string, payload map[string]string) {
	broadcastWorkspaceEvent(h.hub, workspaceID, eventType, payload)
}

// SetLicense attaches the license for enforcing agent-per-crew limits.
func (h *AgentHandler) SetLicense(lic *license.License) { h.license = lic }

// SetScheduler attaches a ScheduleUpdater for live-updating agent cron schedules.
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
	MCPConfigJSON   *string        `json:"mcp_config_json,omitempty"`
	CreatedAt       string         `json:"created_at"`
	UpdatedAt       string         `json:"updated_at"`
	Crew            *agentCrewInfo `json:"crew"`
	Count           agentCounts    `json:"_count"`
}

// List returns all non-deleted agents in the workspace with their crew and count metadata.
// GET /api/v1/agents
func (h *AgentHandler) List(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	if workspaceID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "workspace_id is required"})
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
			a.system_prompt, a.avatar_seed, a.avatar_style, a.timeout_seconds,
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
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	defer rows.Close()

	result := make([]agentResponse, 0, limit)
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
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
				return
			}
		}
	}

	writeJSON(w, http.StatusOK, result)
}

// batchCountByAgentID runs a "SELECT agent_id, COUNT(*) ... WHERE agent_id IN (?) GROUP BY agent_id"
// query with a placeholder list matching len(ids) and returns the id->count map.
// The caller passes the template with a single "%s" where the placeholder list goes.
func batchCountByAgentID(ctx context.Context, db *sql.DB, tmpl string, ids []string) (map[string]int, error) {
	if len(ids) == 0 {
		return map[string]int{}, nil
	}
	placeholders := strings.Repeat("?,", len(ids))
	placeholders = placeholders[:len(placeholders)-1]
	query := fmt.Sprintf(tmpl, placeholders)

	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string]int, len(ids))
	for rows.Next() {
		var id string
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}

// parseListPagination pulls standard ?limit=&offset= params, clamping to sane
// bounds. defaultLimit is used when unspecified; maxLimit caps what clients
// can request. Shared helper for list endpoints.
func parseListPagination(r *http.Request, defaultLimit, maxLimit int) (int, int) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if limit <= 0 {
		limit = defaultLimit
	}
	if limit > maxLimit {
		limit = maxLimit
	}
	if offset < 0 {
		offset = 0
	}
	return limit, offset
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

// Update modifies agent properties such as name, role, model, system prompt, and schedule.
// PATCH /api/v1/agents/{agentId}
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
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
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
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
			return
		}
		result = append(result, e)
	}
	if err := rows.Err(); err != nil {
		h.logger.Error("rows iteration (agent load)", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	if result == nil {
		result = []agentLoadEntry{}
	}
	writeJSON(w, http.StatusOK, result)
}
