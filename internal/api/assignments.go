package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/crewship-ai/crewship/internal/encryption"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/ws"
)

// MissionCallback is notified when assignments linked to mission tasks complete.

type MissionCallback interface {
	OnAssignmentCompleted(ctx context.Context, assignmentID, status, resultSummary, errorMessage string) error
}

// AssignmentHandler handles internal assignment API requests.
// Assignments are created by the sidecar on behalf of lead agents and
// executed as sub-agent runs in the crew container.

type AssignmentHandler struct {
	db              *sql.DB
	orch            *orchestrator.Orchestrator
	hub             *ws.Hub
	logger          *slog.Logger
	internalToken   string
	missionCallback MissionCallback
	journal         journal.Emitter
}

// NewAssignmentHandler creates an AssignmentHandler with the given orchestrator, WebSocket hub, and internal token.

func NewAssignmentHandler(db *sql.DB, orch *orchestrator.Orchestrator, hub *ws.Hub, internalToken string, logger *slog.Logger) *AssignmentHandler {
	return &AssignmentHandler{
		db:            db,
		orch:          orch,
		hub:           hub,
		logger:        logger,
		internalToken: internalToken,
		journal:       noopEmitter{},
	}
}

// SetMissionCallback registers the MissionEngine to receive assignment completion events.

func (h *AssignmentHandler) SetMissionCallback(cb MissionCallback) {
	h.missionCallback = cb
}

// SetJournal wires a journal emitter for run lifecycle events. nil maps
// to the no-op so callers don't have to branch.
func (h *AssignmentHandler) SetJournal(j journal.Emitter) {
	if j == nil {
		h.journal = noopEmitter{}
		return
	}
	h.journal = j
}

type targetAgentInfo struct {
	ID             string
	Slug           string
	Name           string
	RoleTitle      string
	SystemPrompt   string
	CLIAdapter     string
	LLMModel       string
	ToolProfile    string
	TimeoutSeconds int
	MemoryEnabled  bool
	CrewSlug       string
}

// loadAgentCredentials queries and decrypts all credentials for an agent.

func (h *AssignmentHandler) loadAgentCredentials(ctx context.Context, agentID string) ([]orchestrator.Credential, error) {
	rows, err := h.db.QueryContext(ctx, `
		SELECT ac.credential_id, ac.env_var_name, ac.priority, c.encrypted_value, c.type
		FROM agent_credentials ac
		JOIN credentials c ON c.id = ac.credential_id
		WHERE ac.agent_id = ? AND c.deleted_at IS NULL
		ORDER BY ac.priority ASC
	`, agentID)
	if err != nil {
		return nil, fmt.Errorf("query credentials: %w", err)
	}
	defer rows.Close()

	var creds []orchestrator.Credential
	for rows.Next() {
		var c orchestrator.Credential
		var encValue string
		if err := rows.Scan(&c.ID, &c.EnvVarName, &c.Priority, &encValue, &c.Type); err != nil {
			return nil, fmt.Errorf("scan credential: %w", err)
		}
		dec, err := encryption.Decrypt(encValue)
		if err != nil {
			h.logger.Error("decrypt credential", "id", c.ID, "error", err)
			continue
		}
		c.PlainValue = dec
		creds = append(creds, c)
	}
	return creds, rows.Err()
}

// runAssignment executes the sub-agent for an assignment in a goroutine.

func (h *AssignmentHandler) List(w http.ResponseWriter, r *http.Request) {
	crewID := r.PathValue("crewId")
	workspaceID := WorkspaceIDFromContext(r.Context())

	limit, offset := parsePagination(r, 50, 100)

	type assignmentListItem struct {
		ID             string  `json:"id"`
		Task           string  `json:"task"`
		Status         string  `json:"status"`
		AssignedByName string  `json:"assigned_by_name"`
		AssignedBySlug string  `json:"assigned_by_slug"`
		AssignedToName string  `json:"assigned_to_name"`
		AssignedToSlug string  `json:"assigned_to_slug"`
		ResultSummary  *string `json:"result_summary"`
		ErrorMessage   *string `json:"error_message"`
		StartedAt      *string `json:"started_at"`
		FinishedAt     *string `json:"finished_at"`
		CreatedAt      string  `json:"created_at"`
	}

	rows, err := h.db.QueryContext(r.Context(), `
		SELECT a.id, a.task, a.status, a.result_summary, a.error_message,
		       a.started_at, a.finished_at, a.created_at,
		       by_agent.name, by_agent.slug,
		       to_agent.name, to_agent.slug
		FROM assignments a
		JOIN agents by_agent ON by_agent.id = a.assigned_by_id
		JOIN agents to_agent ON to_agent.id = a.assigned_to_id
		WHERE (by_agent.crew_id = ? OR to_agent.crew_id = ?)
		  AND a.workspace_id = ?
		ORDER BY a.created_at DESC
		LIMIT ? OFFSET ?
	`, crewID, crewID, workspaceID, limit, offset)
	if err != nil {
		h.logger.Error("list assignments", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	defer rows.Close()

	items := make([]assignmentListItem, 0, capacityHint(limit))
	for rows.Next() {
		var item assignmentListItem
		if err := rows.Scan(
			&item.ID, &item.Task, &item.Status, &item.ResultSummary, &item.ErrorMessage,
			&item.StartedAt, &item.FinishedAt, &item.CreatedAt,
			&item.AssignedByName, &item.AssignedBySlug,
			&item.AssignedToName, &item.AssignedToSlug,
		); err != nil {
			h.logger.Error("scan assignment", "error", err)
			replyError(w, http.StatusInternalServerError, "Internal server error")
			return
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		h.logger.Error("rows iteration", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	writeJSON(w, http.StatusOK, items)
}

// Get handles GET /api/v1/internal/assignments/{assignmentId}.
// Called by the sidecar when a lead agent polls for assignment results.

func (h *AssignmentHandler) Get(w http.ResponseWriter, r *http.Request) {
	assignmentID := r.PathValue("assignmentId")

	type assignmentResult struct {
		ID            string  `json:"id"`
		WorkspaceID   string  `json:"workspace_id"`
		ChatID        string  `json:"chat_id"`
		AssignedByID  string  `json:"assigned_by_id"`
		AssignedToID  string  `json:"assigned_to_id"`
		Task          string  `json:"task"`
		Status        string  `json:"status"`
		StartedAt     *string `json:"started_at"`
		FinishedAt    *string `json:"finished_at"`
		ResultSummary *string `json:"result_summary"`
		ErrorMessage  *string `json:"error_message"`
		CreatedAt     string  `json:"created_at"`
	}

	var a assignmentResult
	err := h.db.QueryRowContext(r.Context(), `
		SELECT id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status,
		       started_at, finished_at, result_summary, error_message, created_at
		FROM assignments WHERE id = ?
	`, assignmentID).Scan(
		&a.ID, &a.WorkspaceID, &a.ChatID, &a.AssignedByID, &a.AssignedToID,
		&a.Task, &a.Status, &a.StartedAt, &a.FinishedAt,
		&a.ResultSummary, &a.ErrorMessage, &a.CreatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			replyError(w, http.StatusNotFound, "assignment not found")
			return
		}
		h.logger.Error("get assignment", "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	writeJSON(w, http.StatusOK, a)
}

// DispatchAssignment implements orchestrator.TaskDispatcher. It loads the
// target agent's configuration and credentials, then runs the agent in the
// correct crew container -- exactly like the Create handler but driven by the
// MissionEngine instead of a sidecar HTTP call.

func (h *AssignmentHandler) DispatchAssignment(ctx context.Context, req orchestrator.DispatchRequest) error {
	var target targetAgentInfo
	err := h.db.QueryRowContext(ctx, `
		SELECT a.id, a.slug, a.name, COALESCE(a.role_title,''), COALESCE(a.system_prompt_legacy,''),
		       a.cli_adapter, COALESCE(a.llm_model,''), a.tool_profile, a.timeout_seconds, a.memory_enabled, c.slug
		FROM agents a
		JOIN crews c ON c.id = a.crew_id
		WHERE a.id = ? AND a.deleted_at IS NULL
	`, req.AgentID).Scan(
		&target.ID, &target.Slug, &target.Name, &target.RoleTitle,
		&target.SystemPrompt, &target.CLIAdapter, &target.LLMModel,
		&target.ToolProfile, &target.TimeoutSeconds, &target.MemoryEnabled, &target.CrewSlug,
	)
	if err != nil {
		return fmt.Errorf("lookup agent %s: %w", req.AgentID, err)
	}

	creds, err := h.loadAgentCredentials(ctx, target.ID)
	if err != nil {
		return fmt.Errorf("load credentials for agent %s: %w", target.ID, err)
	}

	// Inject trace context into task for observability
	task := req.Task
	if req.TraceID != "" {
		task = fmt.Sprintf("[trace:%s] %s", req.TraceID, req.Task)
	}

	// Load crew members for peer context (so the agent knows its teammates)
	crewMembers := h.loadCrewMembers(ctx, req.CrewID, req.AgentID)

	body := createAssignmentBody{
		TargetSlug:   target.Slug,
		Task:         task,
		CrewID:       req.CrewID,
		WorkspaceID:  req.WorkspaceID,
		ChatID:       req.ChatID,
		MissionID:    req.MissionID,
		CrewMembers:  crewMembers,
		LeadPlanning: req.LeadPlanning,
	}

	h.logger.Info("dispatching mission assignment",
		"assignment_id", req.AssignmentID,
		"mission_id", req.MissionID,
		"trace_id", req.TraceID,
		"agent", target.Slug,
		"crew", target.CrewSlug,
		"brief_len", len(body.Task),
	)

	// Per-crew admission control. Lead-planning assignments skip
	// the queue: a deferred lead deadlocks its whole mission while
	// it waits for slots that won't free until the lead's sub-
	// assignments complete. The lead is allowed to oversubscribe by
	// one. Everyone else competes for crew budget.
	if !req.LeadPlanning {
		budget, budgetErr := computeCrewBudget(ctx, h.db, req.CrewID)
		if budgetErr != nil {
			// Fall back to budget=1 so we under-provision rather
			// than oversubscribe. The completion-path pump catches
			// up on the next terminal status.
			h.logger.Warn("computeCrewBudget failed; falling back to budget=1",
				"crew_id", req.CrewID, "error", budgetErr)
			budget = 1
		}
		claimed, claimErr := claimCrewSlot(ctx, h.db, req.AssignmentID, req.CrewID, budget)
		if claimErr != nil {
			return fmt.Errorf("claim crew slot for %s: %w", req.AssignmentID, claimErr)
		}
		if !claimed {
			if err := markAssignmentQueued(ctx, h.db, req.AssignmentID); err != nil {
				return fmt.Errorf("mark queued %s: %w", req.AssignmentID, err)
			}
			h.emitAssignmentQueued(ctx, req.AssignmentID, req.ChatID, req.WorkspaceID, req.CrewID, target.Slug)
			h.logger.Info("assignment queued (crew at budget)",
				"assignment_id", req.AssignmentID,
				"mission_id", req.MissionID,
				"crew_id", req.CrewID,
				"crew", target.CrewSlug,
				"budget", budget,
			)
			// QUEUED is a tracked in-flight state from the
			// orchestrator's perspective — pumpAndDispatch picks it
			// up when an inflight run completes. Return nil so the
			// mission engine treats this as a successful dispatch.
			return nil
		}
		// Claim succeeded — emit the unqueued event for
		// observability (claim turned the row to RUNNING; UI may
		// want to animate even when the wait was zero). The
		// assignment_running event still follows from
		// runAssignment.
		h.emitAssignmentUnqueued(ctx, req.AssignmentID, req.ChatID, req.WorkspaceID, req.CrewID)
	}

	h.runAssignment(ctx, req.AssignmentID, body, target, creds)
	return nil
}

// loadCrewMembers fetches all agents in a crew (except the given agent) for peer context.

func (h *AssignmentHandler) loadCrewMembers(ctx context.Context, crewID, excludeAgentID string) []orchestrator.CrewMember {
	rows, err := h.db.QueryContext(ctx, `
		SELECT a.id, a.slug, a.name, COALESCE(a.role_title, ''), COALESCE(a.description, '')
		FROM agents a
		WHERE a.crew_id = ? AND a.deleted_at IS NULL AND a.id != ?
		ORDER BY a.name ASC`, crewID, excludeAgentID)
	if err != nil {
		h.logger.Warn("load crew members for dispatch", "error", err)
		return nil
	}
	defer rows.Close()

	var members []orchestrator.CrewMember
	for rows.Next() {
		var m orchestrator.CrewMember
		if err := rows.Scan(&m.ID, &m.Slug, &m.Name, &m.RoleTitle, &m.Description); err != nil {
			continue
		}
		members = append(members, m)
	}
	return members
}
