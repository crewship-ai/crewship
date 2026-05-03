package api

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http"
	"strconv"

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

// CrewsStatus returns lightweight agent counts by status for the toolbar.

func (h *AgentHandler) CrewsStatus(w http.ResponseWriter, r *http.Request) {
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

// validAgentRoles lists all accepted agent_role values.
//
// COORDINATOR was a workspace-level cross-crew role. It was deprecated
// 2026-04-16 and removed from the accepted set in v0.1. The orchestrator
// branches that handled it remain in the codebase but are unreachable
// from the public API. v0.2 will replace cross-crew orchestration with a
// crew-to-crew handoff primitive.
var validAgentRoles = map[string]bool{
	"AGENT": true,
	"LEAD":  true,
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
