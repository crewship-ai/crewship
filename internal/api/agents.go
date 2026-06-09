package api

import (
	"context"
	"database/sql"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/license"
	"github.com/crewship-ai/crewship/internal/policy"
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
	journal         journal.Emitter
	// policyResolver gates the PR-D F5 ephemeral hire/rehire flow.
	// Nil-safe: the Hire handler falls back to the documented safe
	// default (guided → DecisionInboxApprove) when the router hasn't
	// wired one yet, so tests built with the legacy 2-arg
	// NewAgentHandler keep compiling.
	policyResolver *policy.Resolver
	// modelValidator validates an agent's llm_model against the provider's
	// model set on update. Nil-safe: when unset (e.g. legacy test routers),
	// llm_model passes through unchecked — the historical behaviour.
	modelValidator ModelValidator
}

// ModelValidator resolves the valid model-ID set for a provider in a
// workspace, used by the agent update path to reject a bogus llm_model.
// ok=false means the set is unknowable (no live lister and no curated
// fallback) — the caller must then NOT reject, since it can't prove the model
// invalid. *ModelsHandler implements this.
type ModelValidator interface {
	providerModelIDs(ctx context.Context, wsID, provider string) (map[string]bool, bool)
}

// NewAgentHandler creates an AgentHandler with the given database and logger.

func NewAgentHandler(db *sql.DB, logger *slog.Logger) *AgentHandler {
	return &AgentHandler{db: db, logger: logger, journal: noopEmitter{}}
}

// SetHub attaches a WebSocket hub for broadcasting agent events to connected clients.

func (h *AgentHandler) SetHub(hub *ws.Hub) { h.hub = hub }

// SetJournal wires a journal emitter so per-agent skill assignment
// changes land in the workspace audit feed alongside the registry-level
// import/delete events the SkillHandler emits.
func (h *AgentHandler) SetJournal(j journal.Emitter) {
	if j == nil {
		h.journal = noopEmitter{}
		return
	}
	h.journal = j
}

func (h *AgentHandler) broadcastAgentEvent(eventType, workspaceID string, payload map[string]string) {
	broadcastWorkspaceEvent(h.hub, workspaceID, eventType, payload)
}

// SetLicense attaches the license for enforcing agent-per-crew limits.

func (h *AgentHandler) SetLicense(lic *license.License) { h.license = lic }

// SetScheduler attaches a ScheduleUpdater for live-updating agent cron schedules.
func (h *AgentHandler) SetScheduler(su ScheduleUpdater) { h.scheduleUpdater = su }

// SetPolicyResolver wires the shared per-crew autonomy resolver
// (PR-B F2) so the Hire / Rehire handlers can gate ephemeral spawn
// per the crew's autonomy_level. Nil is allowed — handlers fall
// back to the documented safe default (guided ⇒ inbox_approve)
// when the resolver isn't wired, which matches the v100 column
// default the DB ships with.
func (h *AgentHandler) SetPolicyResolver(r *policy.Resolver) { h.policyResolver = r }

// SetModelValidator wires the model-set validator used by Update to reject a
// bogus llm_model. Nil-safe: when unset, llm_model passes through unchecked.
func (h *AgentHandler) SetModelValidator(v ModelValidator) { h.modelValidator = v }

// CrewsStatus returns lightweight agent counts by status for the toolbar.
//
// The "queued" field counts ASSIGNMENTS (not agents) currently in the
// QUEUED state — the per-crew admission queue introduced in PR #396
// (Phase 1B) can park dispatches when a crew's slot budget is
// saturated. Without surfacing this distinctly, queued dispatches
// were mis-counted as agents-in-error in the toolbar (because no
// underlying agent is in ERROR — the dispatcher simply hasn't
// claimed a slot yet).
//
// Agents and assignments are two different tables, and one
// assignment can target an agent that's otherwise IDLE, so the two
// counts are independent and reported as separate fields. The widget
// renders "X running, Y queued, Z idle" when Y > 0, hiding the
// queued segment entirely when nobody is queued so we don't spam
// "0 queued" on idle workspaces.
//
// On a server that pre-dates the QUEUED migration, the assignments
// table has no rows with status='QUEUED' so the count returns 0 —
// the field is present in the JSON shape but semantically inert.
// Old clients that don't read the field are unaffected.

func (h *AgentHandler) CrewsStatus(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	rows, err := h.db.QueryContext(r.Context(),
		`SELECT status, COUNT(*) FROM agents WHERE workspace_id = ? AND deleted_at IS NULL GROUP BY status`,
		workspaceID)
	if err != nil {
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	defer rows.Close()

	result := struct {
		Total   int `json:"total"`
		Running int `json:"running"`
		Error   int `json:"error"`
		Idle    int `json:"idle"`
		Queued  int `json:"queued"`
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

	// Queued assignments are a workspace-scoped, in-flight count.
	// Failure here is non-fatal: degrade to queued=0 rather than
	// 500-ing the whole toolbar — the rest of the payload is still
	// useful and the next poll cycle will re-attempt. Errors are
	// logged so a persistent issue surfaces in the engine log.
	var queued int
	if err := h.db.QueryRowContext(r.Context(),
		`SELECT COUNT(*) FROM assignments WHERE workspace_id = ? AND status = 'QUEUED'`,
		workspaceID,
	).Scan(&queued); err != nil {
		if h.logger != nil {
			h.logger.Warn("crews-status: count queued assignments failed",
				"workspace_id", workspaceID, "error", err)
		}
	} else {
		result.Queued = queued
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

// validCLIAdapters mirrors lib/validations.ts cli_adapter enum + the
// orchestrator's adapter registry (internal/orchestrator/cli_adapter.go init).
// Pre-fix: API endpoints accepted any string, allowing typos / schema-migration
// drift to silently land in DB and only fail at getAdapter() runtime dispatch.
var validCLIAdapters = map[string]bool{
	"CLAUDE_CODE":   true,
	"OPENCODE":      true,
	"CODEX_CLI":     true,
	"GEMINI_CLI":    true,
	"CURSOR_CLI":    true,
	"FACTORY_DROID": true,
}

// validLLMProviders mirrors lib/validations.ts llm_provider enum. CURSOR
// and FACTORY are first-class for credential routing (see validations.ts
// comment) — their CLI adapters auth via CURSOR_API_KEY / FACTORY_API_KEY
// rather than the underlying model provider's key.
var validLLMProviders = map[string]bool{
	"ANTHROPIC": true,
	"OPENAI":    true,
	"GOOGLE":    true,
	"CURSOR":    true,
	"FACTORY":   true,
	"OLLAMA":    true,
}

// validToolProfiles mirrors lib/validations.ts tool_profile enum.
// MESSAGING was retired in pre-beta hygiene (#261) — only the three
// profiles below are accepted; the orchestrator's gating in exec.go was
// updated to drop the MESSAGING branch at the same time.
var validToolProfiles = map[string]bool{
	"MINIMAL": true,
	"CODING":  true,
	"FULL":    true,
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
	ID          string  `json:"id"`
	CrewID      *string `json:"crew_id"`
	WorkspaceID string  `json:"workspace_id"`
	Name        string  `json:"name"`
	Slug        string  `json:"slug"`
	Description *string `json:"description"`
	RoleTitle   *string `json:"role_title"`
	AgentRole   string  `json:"agent_role"`
	LeadMode    *string `json:"lead_mode"`
	Status      string  `json:"status"`
	CLIAdapter  string  `json:"cli_adapter"`
	LLMProvider *string `json:"llm_provider"`
	LLMModel    *string `json:"llm_model"`
	// Deprecated: PR-Z Z.3 marked agents.system_prompt for removal.
	// PR-E replaces it with the PERSONA.md memory tier (per-agent with
	// crew-level default). New write paths should target PERSONA via the
	// F1 memory.write tool. Reads remain valid until PR-E migration.
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
	// Patch M3 — surfaces the agent's creator to the UI. The
	// per-agent edit gate (canEditAgent) lets the user identified
	// here edit/delete the agent without workspace ADMIN role; the
	// AgentCard in the frontend renders an "Owner" badge so a team
	// scanning the list can answer "who maintains this one" without
	// diving into agent detail. Empty string when the agent predates
	// the v100 migration (legacy rows have NULL created_by_user_id);
	// the JSON `omitempty` keeps such responses untouched.
	CreatedByUserID string `json:"created_by_user_id,omitempty"`
	// PR-D F5 ephemeral lifecycle fields. Permanent agents serialize
	// ephemeral=false with the rest as null; the UI ghost path
	// keys off Ephemeral=true + ExpiredAt!=nil.
	Ephemeral    bool    `json:"ephemeral"`
	ExpiresAt    *string `json:"expires_at"`
	ExpiredAt    *string `json:"expired_at"`
	ParentLeadID *string `json:"parent_lead_id"`
	HireReason   *string `json:"hire_reason"`
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
