package api

import (
	"context"
	"database/sql"
	"errors"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/crewship-ai/crewship/internal/encryption"
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
}

func NewAssignmentHandler(db *sql.DB, orch *orchestrator.Orchestrator, hub *ws.Hub, internalToken string, logger *slog.Logger) *AssignmentHandler {
	return &AssignmentHandler{
		db:            db,
		orch:          orch,
		hub:           hub,
		logger:        logger,
		internalToken: internalToken,
	}
}

// SetMissionCallback registers the MissionEngine to receive assignment completion events.
func (h *AssignmentHandler) SetMissionCallback(cb MissionCallback) {
	h.missionCallback = cb
}

type createAssignmentBody struct {
	TargetSlug   string                    `json:"target_slug"`
	Task         string                    `json:"task"`
	CrewID       string                    `json:"crew_id"`
	WorkspaceID  string                    `json:"workspace_id"`
	ChatID       string                    `json:"chat_id"`
	CrewMembers  []orchestrator.CrewMember `json:"-"` // populated internally for mission dispatches
	LeadPlanning bool                      `json:"-"` // when true, run as LEAD with sidecar
}

// Create handles POST /api/v1/internal/assignments.
// Called by the sidecar when a lead agent invokes `curl localhost:9119/assign`.
func (h *AssignmentHandler) Create(w http.ResponseWriter, r *http.Request) {
	var body createAssignmentBody
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid JSON"})
		return
	}
	if body.TargetSlug == "" || body.Task == "" || body.CrewID == "" || body.WorkspaceID == "" || body.ChatID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "target_slug, task, crew_id, workspace_id, chat_id required",
		})
		return
	}

	// Look up the assigning agent from the parent chat
	var assignedByID string
	err := h.db.QueryRowContext(r.Context(), `SELECT agent_id FROM chats WHERE id = ?`, body.ChatID).Scan(&assignedByID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "chat not found"})
			return
		}
		h.logger.Error("lookup chat for assignment", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	// Cross-crew connection check: if the assigning agent's crew differs
	// from the target crew, verify an active crew connection exists.
	var assignerCrewID string
	if err := h.db.QueryRowContext(r.Context(),
		`SELECT a.crew_id FROM agents a JOIN chats ch ON ch.agent_id = a.id WHERE ch.id = ?`,
		body.ChatID).Scan(&assignerCrewID); err != nil && !errors.Is(err, sql.ErrNoRows) {
		h.logger.Error("lookup assigner crew for connection check", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	if assignerCrewID != "" && assignerCrewID != body.CrewID {
		connected, connErr := AreCrewsConnected(r.Context(), h.db, assignerCrewID, body.CrewID)
		if connErr != nil {
			h.logger.Error("check crew connection for assignment", "error", connErr)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
			return
		}
		if !connected {
			writeJSON(w, http.StatusForbidden, map[string]string{
				"error": "crews are not connected — create a crew connection first",
			})
			return
		}
	}

	// Look up the target agent by slug + crew_id
	var target targetAgentInfo
	err = h.db.QueryRowContext(r.Context(), `
		SELECT a.id, a.slug, a.name, COALESCE(a.role_title,''), COALESCE(a.system_prompt,''),
		       a.cli_adapter, COALESCE(a.llm_model,''), a.tool_profile, a.timeout_seconds, a.memory_enabled, c.slug
		FROM agents a
		JOIN crews c ON c.id = a.crew_id
		WHERE a.slug = ? AND a.crew_id = ? AND a.deleted_at IS NULL
	`, body.TargetSlug, body.CrewID).Scan(
		&target.ID, &target.Slug, &target.Name, &target.RoleTitle,
		&target.SystemPrompt, &target.CLIAdapter, &target.LLMModel,
		&target.ToolProfile, &target.TimeoutSeconds, &target.MemoryEnabled, &target.CrewSlug,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "target agent not found"})
			return
		}
		h.logger.Error("lookup target agent", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	// Load target agent credentials
	creds, err := h.loadAgentCredentials(r.Context(), target.ID)
	if err != nil {
		h.logger.Error("load agent credentials", "agent_id", target.ID, "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	// Create assignment record in PENDING state
	assignmentID := generateCUID()
	now := time.Now().UTC().Format(time.RFC3339)
	_, err = h.db.ExecContext(r.Context(), `
		INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status, created_at)
		VALUES (?, ?, ?, ?, ?, ?, 'PENDING', ?)
	`, assignmentID, body.WorkspaceID, body.ChatID, assignedByID, target.ID, body.Task, now)
	if err != nil {
		h.logger.Error("create assignment", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	// Broadcast assignment_created event to the session channel
	if h.hub != nil {
		h.hub.Broadcast("session:"+body.ChatID, ws.ServerMessage{
			Type:    "assignment_created",
			Channel: "session:" + body.ChatID,
			Payload: map[string]string{
				"id":     assignmentID,
				"target": body.TargetSlug,
				"task":   body.Task,
			},
		})
	}

	h.logger.Info("assignment created",
		"assignment_id", assignmentID,
		"target", body.TargetSlug,
		"crew_id", body.CrewID,
	)

	// Run the sub-agent asynchronously
	go h.runAssignment(context.Background(), assignmentID, body, target, creds)

	writeJSON(w, http.StatusCreated, map[string]string{
		"assignment_id": assignmentID,
		"status":        "PENDING",
	})
}

// targetAgentInfo holds the agent fields needed to run an assignment.
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
		WHERE ac.agent_id = ?
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
func (h *AssignmentHandler) runAssignment(
	ctx context.Context,
	assignmentID string,
	body createAssignmentBody,
	target targetAgentInfo,
	creds []orchestrator.Credential,
) {
	now := time.Now().UTC().Format(time.RFC3339)

	// Create a run record in agent_runs so the dashboard shows sub-agent activity
	runID := generateCUID()
	metadataBytes, err := json.Marshal(map[string]string{
		"assignment_id":    assignmentID,
		"assigned_by_chat": body.ChatID,
	})
	if err != nil {
		h.logger.Error("marshal assignment metadata", "error", err, "assignment_id", assignmentID)
		metadataBytes = []byte("{}")
	}
	if _, err := h.db.ExecContext(ctx, `
		INSERT INTO agent_runs (id, agent_id, chat_id, workspace_id, trigger_type, status, metadata, started_at, created_at)
		VALUES (?, ?, ?, ?, 'ASSIGNMENT', 'RUNNING', ?, ?, ?)`,
		runID, target.ID, body.ChatID, body.WorkspaceID,
		string(metadataBytes),
		now, now,
	); err != nil {
		h.logger.Error("create run record for assignment", "error", err, "assignment_id", assignmentID)
		runID = "" // prevent finishAssignment from updating a non-existent record
	}

	// Mark assignment as RUNNING
	if _, err := h.db.ExecContext(ctx,
		`UPDATE assignments SET status='RUNNING', started_at=? WHERE id=?`, now, assignmentID); err != nil {
		h.logger.Error("update assignment to running", "error", err, "assignment_id", assignmentID)
	}
	if h.hub != nil {
		h.hub.Broadcast("session:"+body.ChatID, ws.ServerMessage{
			Type:    "assignment_running",
			Channel: "session:" + body.ChatID,
			Payload: map[string]string{
				"id":     assignmentID,
				"target": body.TargetSlug,
			},
		})
		// Broadcast to workspace for global visibility
		wsChannel := "workspace:" + body.WorkspaceID
		h.hub.Broadcast(wsChannel, ws.ServerMessage{
			Type:    "assignment.updated",
			Channel: wsChannel,
			Payload: map[string]string{
				"id":     assignmentID,
				"status": "RUNNING",
				"target": body.TargetSlug,
			},
		})
	}

	if h.orch == nil {
		h.finishAssignment(ctx, assignmentID, runID, body.ChatID, body.TargetSlug, body.WorkspaceID, "", "orchestrator not available")
		return
	}

	// Ensure crew container is running
	containerID, err := h.orch.GetOrCreateContainer(ctx, target.CrewSlug, body.CrewID)
	if err != nil {
		h.logger.Error("get container for assignment", "error", err, "assignment_id", assignmentID)
		h.finishAssignment(ctx, assignmentID, runID, body.ChatID, body.TargetSlug, body.WorkspaceID, "",
			fmt.Sprintf("container error: %v", err))
		return
	}

	// Collect agent output
	var outputParts []string
	handler := func(event orchestrator.AgentEvent) {
		if event.Type == "text" && event.Content != "" {
			outputParts = append(outputParts, event.Content)
		}
	}

	agentRole := "AGENT"
	skipSidecar := true
	if body.LeadPlanning {
		agentRole = "LEAD" // Lead planning: full LEAD privileges with sidecar
		skipSidecar = false
	}

	req := orchestrator.AgentRunRequest{
		AgentID:         target.ID,
		AgentSlug:       target.Slug,
		AgentRole:       agentRole,
		CrewID:          body.CrewID,
		CrewSlug:        target.CrewSlug,
		WorkspaceID:     body.WorkspaceID,
		ChatID:          body.ChatID,
		ContainerID:     containerID,
		CLIAdapter:      target.CLIAdapter,
		LLMModel:        target.LLMModel,
		SystemPrompt:    target.SystemPrompt,
		UserMessage:     body.Task,
		ToolProfile:     target.ToolProfile,
		Credentials:     creds,
		TimeoutSecs:     target.TimeoutSeconds,
		MemoryEnabled:   target.MemoryEnabled,
		CrewMembers:     body.CrewMembers,
		SkipSidecar:     skipSidecar,
		SkipConvHistory: true,
	}

	if err := h.orch.RunAgentForAssignment(ctx, req, handler); err != nil {
		h.logger.Error("assignment execution failed", "error", err, "assignment_id", assignmentID)
		h.finishAssignment(ctx, assignmentID, runID, body.ChatID, body.TargetSlug, body.WorkspaceID, "",
			fmt.Sprintf("execution error: %v", err))
		return
	}

	// Build result from collected output (cap at 10k chars)
	result := ""
	for _, part := range outputParts {
		result += part
	}
	if len(result) > 10000 {
		result = result[:10000] + "...(truncated)"
	}

	h.finishAssignment(ctx, assignmentID, runID, body.ChatID, body.TargetSlug, body.WorkspaceID, result, "")
}

// finishAssignment updates the assignment and run records, then broadcasts the final event.
func (h *AssignmentHandler) finishAssignment(
	ctx context.Context,
	assignmentID, runID, chatID, targetSlug, workspaceID, result, errMsg string,
) {
	now := time.Now().UTC().Format(time.RFC3339)
	status := "COMPLETED"
	if errMsg != "" {
		status = "FAILED"
	}

	var resultVal, errVal interface{}
	if result != "" {
		resultVal = result
	}
	if errMsg != "" {
		errVal = errMsg
	}

	if _, err := h.db.ExecContext(ctx,
		`UPDATE assignments SET status=?, result_summary=?, error_message=?, finished_at=? WHERE id=?`,
		status, resultVal, errVal, now, assignmentID); err != nil {
		h.logger.Error("update assignment status", "error", err, "assignment_id", assignmentID)
	}

	// Update the agent_runs record so the dashboard reflects the final state
	if runID != "" {
		runQuery := `UPDATE agent_runs SET status = ?, finished_at = ?`
		runArgs := []interface{}{status, now}
		if errMsg != "" {
			runQuery += `, error_message = ?`
			runArgs = append(runArgs, errMsg)
		}
		if status == "COMPLETED" {
			runQuery += `, exit_code = 0`
		}
		runQuery += ` WHERE id = ?`
		runArgs = append(runArgs, runID)
		if _, err := h.db.ExecContext(ctx, runQuery, runArgs...); err != nil {
			h.logger.Error("update run record for assignment", "error", err, "run_id", runID)
		}
	}

	// Notify MissionEngine first — must run regardless of websocket availability
	if h.missionCallback != nil {
		if err := h.missionCallback.OnAssignmentCompleted(ctx, assignmentID, status, result, errMsg); err != nil {
			h.logger.Error("mission callback failed", "error", err, "assignment_id", assignmentID)
		}
	}

	if h.hub == nil {
		return
	}

	if errMsg != "" {
		h.hub.Broadcast("session:"+chatID, ws.ServerMessage{
			Type:    "assignment_failed",
			Channel: "session:" + chatID,
			Payload: map[string]string{
				"id":     assignmentID,
				"target": targetSlug,
				"error":  errMsg,
			},
		})
	} else {
		h.hub.Broadcast("session:"+chatID, ws.ServerMessage{
			Type:    "assignment_completed",
			Channel: "session:" + chatID,
			Payload: map[string]string{
				"id":     assignmentID,
				"target": targetSlug,
				"result": result,
			},
		})
	}

	// Broadcast to workspace channel for real-time dashboard updates
	if workspaceID != "" {
		wsChannel := "workspace:" + workspaceID
		h.hub.Broadcast(wsChannel, ws.ServerMessage{
			Type:    "assignment.updated",
			Channel: wsChannel,
			Payload: map[string]string{
				"id":     assignmentID,
				"status": status,
				"target": targetSlug,
			},
		})
	}

	h.logger.Info("assignment finished", "assignment_id", assignmentID, "status", status)
}

// List handles GET /api/v1/crews/{crewId}/assignments.
// Returns all assignments for agents in this crew, sorted by created_at DESC.
func (h *AssignmentHandler) List(w http.ResponseWriter, r *http.Request) {
	crewID := r.PathValue("crewId")
	workspaceID := WorkspaceIDFromContext(r.Context())

	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}

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
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	defer rows.Close()

	items := make([]assignmentListItem, 0)
	for rows.Next() {
		var item assignmentListItem
		if err := rows.Scan(
			&item.ID, &item.Task, &item.Status, &item.ResultSummary, &item.ErrorMessage,
			&item.StartedAt, &item.FinishedAt, &item.CreatedAt,
			&item.AssignedByName, &item.AssignedBySlug,
			&item.AssignedToName, &item.AssignedToSlug,
		); err != nil {
			h.logger.Error("scan assignment", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
			return
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		h.logger.Error("rows iteration", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
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
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "assignment not found"})
			return
		}
		h.logger.Error("get assignment", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
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
		SELECT a.id, a.slug, a.name, COALESCE(a.role_title,''), COALESCE(a.system_prompt,''),
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
