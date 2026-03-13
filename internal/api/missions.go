package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/ws"
)

type MissionHandler struct {
	db            *sql.DB
	hub           *ws.Hub
	missionEngine *orchestrator.MissionEngine
	logger        *slog.Logger
}

func NewMissionHandler(db *sql.DB, hub *ws.Hub, me *orchestrator.MissionEngine, logger *slog.Logger) *MissionHandler {
	return &MissionHandler{db: db, hub: hub, missionEngine: me, logger: logger}
}

type missionResponse struct {
	ID                string   `json:"id"`
	WorkspaceID       string   `json:"workspace_id"`
	CrewID            string   `json:"crew_id"`
	LeadAgentID       string   `json:"lead_agent_id"`
	LeadAgentName     string   `json:"lead_agent_name,omitempty"`
	LeadAgentSlug     string   `json:"lead_agent_slug,omitempty"`
	TraceID           string   `json:"trace_id"`
	Title             string   `json:"title"`
	Description       *string  `json:"description"`
	Status            string   `json:"status"`
	Plan              *string  `json:"plan"`
	WorkflowTemplate  *string  `json:"workflow_template"`
	TotalTokenCount   *int     `json:"total_token_count"`
	TotalEstimatedCost *float64 `json:"total_estimated_cost"`
	CreatedAt         string   `json:"created_at"`
	UpdatedAt         string   `json:"updated_at"`
	CompletedAt       *string  `json:"completed_at"`
	TaskStats         *taskStats `json:"task_stats,omitempty"`
	Tasks             []missionTaskResponse `json:"tasks,omitempty"`
}

type missionTaskResponse struct {
	ID              string   `json:"id"`
	MissionID       string   `json:"mission_id"`
	AssignedAgentID *string  `json:"assigned_agent_id"`
	AgentName       *string  `json:"agent_name,omitempty"`
	AgentSlug       *string  `json:"agent_slug,omitempty"`
	Title           string   `json:"title"`
	Description     *string  `json:"description"`
	Status          string   `json:"status"`
	TaskOrder       int      `json:"task_order"`
	DependsOn       string   `json:"depends_on"`
	Iteration       *int     `json:"iteration"`
	MaxIterations   *int     `json:"max_iterations"`
	ResultSummary   *string  `json:"result_summary"`
	OutputPath      *string  `json:"output_path"`
	ErrorMessage    *string  `json:"error_message"`
	AssignmentID    *string  `json:"assignment_id"`
	TokenCount      *int     `json:"token_count"`
	EstimatedCost   *float64 `json:"estimated_cost"`
	StartedAt       *string  `json:"started_at"`
	CompletedAt     *string  `json:"completed_at"`
	DurationMs      *int     `json:"duration_ms"`
	CreatedAt       string   `json:"created_at"`
	UpdatedAt       string   `json:"updated_at"`
}

type taskStats struct {
	Total     int `json:"total"`
	Pending   int `json:"pending"`
	Blocked   int `json:"blocked"`
	InProgress int `json:"in_progress"`
	Completed int `json:"completed"`
	Failed    int `json:"failed"`
	Skipped   int `json:"skipped"`
}

var validMissionTransitions = map[string][]string{
	"PLANNING":    {"IN_PROGRESS", "CANCELLED"},
	"IN_PROGRESS": {"REVIEW", "FAILED", "CANCELLED"},
	"REVIEW":      {"COMPLETED", "IN_PROGRESS", "FAILED", "CANCELLED"},
}

var validTaskTransitions = map[string][]string{
	"PENDING":     {"IN_PROGRESS", "SKIPPED"},
	"BLOCKED":     {"PENDING", "SKIPPED"},
	"IN_PROGRESS": {"COMPLETED", "FAILED", "SKIPPED"},
}

func writeProblem(w http.ResponseWriter, r *http.Request, status int, detail string) {
	writeJSON(w, status, map[string]interface{}{
		"type":     "about:blank",
		"title":    http.StatusText(status),
		"status":   status,
		"detail":   detail,
		"instance": r.URL.Path,
	})
}

// Create handles POST /api/v1/crews/{crewId}/missions
func (h *MissionHandler) Create(w http.ResponseWriter, r *http.Request) {
	role := RoleFromContext(r.Context())
	if !canRole(role, "create") {
		writeProblem(w, r, http.StatusForbidden, "Forbidden")
		return
	}

	crewID := r.PathValue("crewId")
	wsID := WorkspaceIDFromContext(r.Context())

	var req struct {
		Title            string  `json:"title"`
		Description      *string `json:"description"`
		LeadAgentID      string  `json:"lead_agent_id"`
		WorkflowTemplate *string `json:"workflow_template"`
	}
	if err := readJSON(r, &req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "Invalid JSON body")
		return
	}
	if req.Title == "" {
		writeProblem(w, r, http.StatusBadRequest, "title is required")
		return
	}
	if req.LeadAgentID == "" {
		writeProblem(w, r, http.StatusBadRequest, "lead_agent_id is required")
		return
	}

	// Validate lead agent exists in crew with LEAD role
	var agentRole string
	err := h.db.QueryRowContext(r.Context(),
		`SELECT agent_role FROM agents WHERE id = ? AND crew_id = ? AND deleted_at IS NULL`,
		req.LeadAgentID, crewID).Scan(&agentRole)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeProblem(w, r, http.StatusBadRequest, "lead agent not found in crew")
			return
		}
		h.logger.Error("lookup lead agent", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	if agentRole != "LEAD" {
		writeProblem(w, r, http.StatusBadRequest, "agent must have LEAD role")
		return
	}

	id := generateCUID()
	traceID := "mission-" + generateCUID()
	now := time.Now().UTC().Format(time.RFC3339)

	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		h.logger.Error("begin tx", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	defer tx.Rollback() //nolint:errcheck

	_, err = tx.ExecContext(r.Context(), `
		INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, description, status, workflow_template, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, 'PLANNING', ?, ?, ?)`,
		id, wsID, crewID, req.LeadAgentID, traceID, req.Title, req.Description, req.WorkflowTemplate, now, now)
	if err != nil {
		h.logger.Error("create mission", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	// Create a synthetic chat so assignments can reference it (FK on chat_id)
	_, err = tx.ExecContext(r.Context(), `
		INSERT INTO chats (id, agent_id, workspace_id, title, mode, status, started_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'MISSION', 'ACTIVE', ?, ?, ?)`,
		id, req.LeadAgentID, wsID, "Mission: "+req.Title, now, now, now)
	if err != nil {
		h.logger.Error("create synthetic chat for mission", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	if err := tx.Commit(); err != nil {
		h.logger.Error("commit mission", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	resp := missionResponse{
		ID:               id,
		WorkspaceID:      wsID,
		CrewID:           crewID,
		LeadAgentID:      req.LeadAgentID,
		TraceID:          traceID,
		Title:            req.Title,
		Description:      req.Description,
		Status:           "PLANNING",
		WorkflowTemplate: req.WorkflowTemplate,
		CreatedAt:        now,
		UpdatedAt:        now,
	}

	if h.hub != nil {
		h.hub.Broadcast("crew:"+crewID, ws.ServerMessage{
			Type:    "mission.created",
			Channel: "crew:" + crewID,
			Payload: map[string]string{"id": id, "title": req.Title},
		})
		wsChannel := "workspace:" + wsID
		h.hub.Broadcast(wsChannel, ws.ServerMessage{
			Type:    "mission.updated",
			Channel: wsChannel,
			Payload: map[string]string{"id": id, "crew_id": crewID, "status": "PLANNING"},
		})
	}

	writeJSON(w, http.StatusCreated, resp)
}

// List handles GET /api/v1/crews/{crewId}/missions
func (h *MissionHandler) List(w http.ResponseWriter, r *http.Request) {
	crewID := r.PathValue("crewId")
	wsID := WorkspaceIDFromContext(r.Context())
	status := r.URL.Query().Get("status")

	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}

	query := `
		SELECT m.id, m.workspace_id, m.crew_id, m.lead_agent_id, m.trace_id, m.title,
		       m.description, m.status, m.plan, m.workflow_template,
		       m.total_token_count, m.total_estimated_cost,
		       m.created_at, m.updated_at, m.completed_at,
		       a.name, a.slug
		FROM missions m
		JOIN agents a ON a.id = m.lead_agent_id
		WHERE m.crew_id = ? AND m.workspace_id = ?`
	args := []interface{}{crewID, wsID}

	if status != "" {
		query += " AND m.status = ?"
		args = append(args, status)
	}
	query += " ORDER BY m.created_at DESC LIMIT ? OFFSET ?"
	args = append(args, limit, offset)

	rows, err := h.db.QueryContext(r.Context(), query, args...)
	if err != nil {
		h.logger.Error("list missions", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	defer rows.Close()

	var result []missionResponse
	var missionIDs []string
	for rows.Next() {
		var m missionResponse
		if err := rows.Scan(
			&m.ID, &m.WorkspaceID, &m.CrewID, &m.LeadAgentID, &m.TraceID, &m.Title,
			&m.Description, &m.Status, &m.Plan, &m.WorkflowTemplate,
			&m.TotalTokenCount, &m.TotalEstimatedCost,
			&m.CreatedAt, &m.UpdatedAt, &m.CompletedAt,
			&m.LeadAgentName, &m.LeadAgentSlug,
		); err != nil {
			h.logger.Error("scan mission", "error", err)
			writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
			return
		}
		result = append(result, m)
		missionIDs = append(missionIDs, m.ID)
	}
	if err := rows.Err(); err != nil {
		h.logger.Error("rows iteration (missions)", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	// Load task stats for each mission
	for i, m := range result {
		stats, err := h.getTaskStats(r, m.ID)
		if err != nil {
			h.logger.Error("get task stats", "mission_id", m.ID, "error", err)
			continue
		}
		result[i].TaskStats = stats
	}

	if result == nil {
		result = []missionResponse{}
	}
	writeJSON(w, http.StatusOK, result)
}

// ListAll handles GET /api/v1/missions
func (h *MissionHandler) ListAll(w http.ResponseWriter, r *http.Request) {
	wsID := WorkspaceIDFromContext(r.Context())
	status := r.URL.Query().Get("status")
	includeTasks := r.URL.Query().Get("include_tasks") == "true"

	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}

	query := `
		SELECT m.id, m.workspace_id, m.crew_id, m.lead_agent_id, m.trace_id, m.title,
		       m.description, m.status, m.plan, m.workflow_template,
		       m.total_token_count, m.total_estimated_cost,
		       m.created_at, m.updated_at, m.completed_at,
		       a.name, a.slug
		FROM missions m
		JOIN agents a ON a.id = m.lead_agent_id
		WHERE m.workspace_id = ?`
	args := []interface{}{wsID}

	if status != "" {
		query += " AND m.status = ?"
		args = append(args, status)
	}
	query += " ORDER BY m.created_at DESC LIMIT ? OFFSET ?"
	args = append(args, limit, offset)

	rows, err := h.db.QueryContext(r.Context(), query, args...)
	if err != nil {
		h.logger.Error("list all missions", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	defer rows.Close()

	var result []missionResponse
	for rows.Next() {
		var m missionResponse
		if err := rows.Scan(
			&m.ID, &m.WorkspaceID, &m.CrewID, &m.LeadAgentID, &m.TraceID, &m.Title,
			&m.Description, &m.Status, &m.Plan, &m.WorkflowTemplate,
			&m.TotalTokenCount, &m.TotalEstimatedCost,
			&m.CreatedAt, &m.UpdatedAt, &m.CompletedAt,
			&m.LeadAgentName, &m.LeadAgentSlug,
		); err != nil {
			h.logger.Error("scan mission", "error", err)
			writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
			return
		}
		result = append(result, m)
	}
	if err := rows.Err(); err != nil {
		h.logger.Error("rows iteration (missions all)", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	// Load task stats and optionally tasks for each mission
	for i, m := range result {
		stats, statsErr := h.getTaskStats(r, m.ID)
		if statsErr != nil {
			h.logger.Error("get task stats", "mission_id", m.ID, "error", statsErr)
		}
		result[i].TaskStats = stats

		if includeTasks {
			tasks, tasksErr := h.loadTasksForMission(r, m.ID)
			if tasksErr != nil {
				h.logger.Error("load tasks for mission", "mission_id", m.ID, "error", tasksErr)
				result[i].Tasks = []missionTaskResponse{}
			} else {
				result[i].Tasks = tasks
			}
		}
	}

	if result == nil {
		result = []missionResponse{}
	}
	writeJSON(w, http.StatusOK, result)
}

// loadTasksForMission loads all tasks for a mission with agent info.
func (h *MissionHandler) loadTasksForMission(r *http.Request, missionID string) ([]missionTaskResponse, error) {
	taskRows, err := h.db.QueryContext(r.Context(), `
		SELECT mt.id, mt.mission_id, mt.assigned_agent_id, mt.title, mt.description,
		       mt.status, mt.task_order, mt.depends_on, mt.iteration, mt.max_iterations,
		       mt.result_summary, mt.output_path, mt.error_message, mt.assignment_id,
		       mt.token_count, mt.estimated_cost, mt.started_at, mt.completed_at,
		       mt.duration_ms, mt.created_at, mt.updated_at,
		       ag.name, ag.slug
		FROM mission_tasks mt
		LEFT JOIN agents ag ON ag.id = mt.assigned_agent_id
		WHERE mt.mission_id = ?
		ORDER BY mt.task_order ASC`, missionID)
	if err != nil {
		return nil, err
	}
	defer taskRows.Close()

	tasks := []missionTaskResponse{}
	for taskRows.Next() {
		var t missionTaskResponse
		if err := taskRows.Scan(
			&t.ID, &t.MissionID, &t.AssignedAgentID, &t.Title, &t.Description,
			&t.Status, &t.TaskOrder, &t.DependsOn, &t.Iteration, &t.MaxIterations,
			&t.ResultSummary, &t.OutputPath, &t.ErrorMessage, &t.AssignmentID,
			&t.TokenCount, &t.EstimatedCost, &t.StartedAt, &t.CompletedAt,
			&t.DurationMs, &t.CreatedAt, &t.UpdatedAt,
			&t.AgentName, &t.AgentSlug,
		); err != nil {
			return nil, err
		}
		tasks = append(tasks, t)
	}
	return tasks, taskRows.Err()
}

// Get handles GET /api/v1/crews/{crewId}/missions/{missionId}
func (h *MissionHandler) Get(w http.ResponseWriter, r *http.Request) {
	crewID := r.PathValue("crewId")
	missionID := r.PathValue("missionId")
	wsID := WorkspaceIDFromContext(r.Context())

	var m missionResponse
	err := h.db.QueryRowContext(r.Context(), `
		SELECT m.id, m.workspace_id, m.crew_id, m.lead_agent_id, m.trace_id, m.title,
		       m.description, m.status, m.plan, m.workflow_template,
		       m.total_token_count, m.total_estimated_cost,
		       m.created_at, m.updated_at, m.completed_at,
		       a.name, a.slug
		FROM missions m
		JOIN agents a ON a.id = m.lead_agent_id
		WHERE m.id = ? AND m.crew_id = ? AND m.workspace_id = ?`,
		missionID, crewID, wsID).Scan(
		&m.ID, &m.WorkspaceID, &m.CrewID, &m.LeadAgentID, &m.TraceID, &m.Title,
		&m.Description, &m.Status, &m.Plan, &m.WorkflowTemplate,
		&m.TotalTokenCount, &m.TotalEstimatedCost,
		&m.CreatedAt, &m.UpdatedAt, &m.CompletedAt,
		&m.LeadAgentName, &m.LeadAgentSlug,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeProblem(w, r, http.StatusNotFound, "Mission not found")
			return
		}
		h.logger.Error("get mission", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	// Load tasks
	tasks, tasksErr := h.loadTasksForMission(r, missionID)
	if tasksErr != nil {
		h.logger.Error("get mission tasks", "error", tasksErr)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	m.Tasks = tasks

	stats, statsErr := h.getTaskStats(r, missionID)
	if statsErr != nil {
		h.logger.Error("get task stats", "mission_id", missionID, "error", statsErr)
	}
	m.TaskStats = stats

	writeJSON(w, http.StatusOK, m)
}

// Update handles PATCH /api/v1/crews/{crewId}/missions/{missionId}
func (h *MissionHandler) Update(w http.ResponseWriter, r *http.Request) {
	role := RoleFromContext(r.Context())
	if !canRole(role, "create") {
		writeProblem(w, r, http.StatusForbidden, "Forbidden")
		return
	}

	crewID := r.PathValue("crewId")
	missionID := r.PathValue("missionId")
	wsID := WorkspaceIDFromContext(r.Context())

	var req struct {
		Status      *string  `json:"status"`
		Title       *string  `json:"title"`
		Description *string  `json:"description"`
		Plan        *string  `json:"plan"`
	}
	if err := readJSON(r, &req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "Invalid JSON body")
		return
	}

	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		h.logger.Error("begin transaction", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	defer tx.Rollback() //nolint:errcheck

	// Get current status
	var currentStatus string
	err = tx.QueryRowContext(r.Context(),
		`SELECT status FROM missions WHERE id = ? AND crew_id = ? AND workspace_id = ?`,
		missionID, crewID, wsID).Scan(&currentStatus)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeProblem(w, r, http.StatusNotFound, "Mission not found")
			return
		}
		h.logger.Error("get mission for update", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)

	// Validate status transition
	if req.Status != nil {
		newStatus := *req.Status
		allowed := validMissionTransitions[currentStatus]
		valid := false
		for _, s := range allowed {
			if s == newStatus {
				valid = true
				break
			}
		}
		if !valid {
			writeProblem(w, r, http.StatusBadRequest, "Invalid status transition from "+currentStatus+" to "+newStatus)
			return
		}

		completedAt := sql.NullString{}
		if newStatus == "COMPLETED" || newStatus == "FAILED" || newStatus == "CANCELLED" {
			completedAt = sql.NullString{String: now, Valid: true}
		}

		if _, err = tx.ExecContext(r.Context(),
			`UPDATE missions SET status = ?, completed_at = ?, updated_at = ? WHERE id = ?`,
			newStatus, completedAt, now, missionID); err != nil {
			h.logger.Error("update mission status", "error", err)
			writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
			return
		}
	}

	if req.Title != nil {
		if _, err = tx.ExecContext(r.Context(), `UPDATE missions SET title = ?, updated_at = ? WHERE id = ?`, *req.Title, now, missionID); err != nil {
			h.logger.Error("update mission title", "error", err)
			writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
			return
		}
	}
	if req.Description != nil {
		if _, err = tx.ExecContext(r.Context(), `UPDATE missions SET description = ?, updated_at = ? WHERE id = ?`, *req.Description, now, missionID); err != nil {
			h.logger.Error("update mission description", "error", err)
			writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
			return
		}
	}
	if req.Plan != nil {
		if _, err = tx.ExecContext(r.Context(), `UPDATE missions SET plan = ?, updated_at = ? WHERE id = ?`, *req.Plan, now, missionID); err != nil {
			h.logger.Error("update mission plan", "error", err)
			writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
			return
		}
	}

	if err = tx.Commit(); err != nil {
		h.logger.Error("commit mission update", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	// Return updated mission
	var m missionResponse
	if err := h.db.QueryRowContext(r.Context(), `
		SELECT m.id, m.workspace_id, m.crew_id, m.lead_agent_id, m.trace_id, m.title,
		       m.description, m.status, m.plan, m.workflow_template,
		       m.total_token_count, m.total_estimated_cost,
		       m.created_at, m.updated_at, m.completed_at,
		       a.name, a.slug
		FROM missions m
		JOIN agents a ON a.id = m.lead_agent_id
		WHERE m.id = ?`, missionID).Scan(
		&m.ID, &m.WorkspaceID, &m.CrewID, &m.LeadAgentID, &m.TraceID, &m.Title,
		&m.Description, &m.Status, &m.Plan, &m.WorkflowTemplate,
		&m.TotalTokenCount, &m.TotalEstimatedCost,
		&m.CreatedAt, &m.UpdatedAt, &m.CompletedAt,
		&m.LeadAgentName, &m.LeadAgentSlug,
	); err != nil {
		h.logger.Error("read updated mission", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	if h.hub != nil && req.Status != nil {
		h.hub.Broadcast("mission:"+missionID, ws.ServerMessage{
			Type:    "mission.status",
			Channel: "mission:" + missionID,
			Payload: map[string]string{"id": missionID, "status": *req.Status},
		})
		// Broadcast to workspace for dashboard visibility
		wsChannel := "workspace:" + m.WorkspaceID
		h.hub.Broadcast(wsChannel, ws.ServerMessage{
			Type:    "mission.updated",
			Channel: wsChannel,
			Payload: map[string]string{"id": missionID, "crew_id": crewID, "status": *req.Status},
		})
	}

	writeJSON(w, http.StatusOK, m)
}

// Delete handles DELETE /api/v1/crews/{crewId}/missions/{missionId}
func (h *MissionHandler) Delete(w http.ResponseWriter, r *http.Request) {
	role := RoleFromContext(r.Context())
	if !canRole(role, "create") {
		writeProblem(w, r, http.StatusForbidden, "Forbidden")
		return
	}

	crewID := r.PathValue("crewId")
	missionID := r.PathValue("missionId")
	wsID := WorkspaceIDFromContext(r.Context())

	var status string
	err := h.db.QueryRowContext(r.Context(),
		`SELECT status FROM missions WHERE id = ? AND crew_id = ? AND workspace_id = ?`,
		missionID, crewID, wsID).Scan(&status)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeProblem(w, r, http.StatusNotFound, "Mission not found")
			return
		}
		h.logger.Error("get mission for delete", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	if status != "PLANNING" && status != "CANCELLED" {
		writeProblem(w, r, http.StatusBadRequest, "Only PLANNING or CANCELLED missions can be deleted")
		return
	}

	_, err = h.db.ExecContext(r.Context(), `DELETE FROM missions WHERE id = ?`, missionID)
	if err != nil {
		h.logger.Error("delete mission", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// Start handles POST /api/v1/crews/{crewId}/missions/{missionId}/start
// Transitions a PLANNING mission to IN_PROGRESS and kicks off the MissionEngine.
func (h *MissionHandler) Start(w http.ResponseWriter, r *http.Request) {
	role := RoleFromContext(r.Context())
	if !canRole(role, "create") {
		writeProblem(w, r, http.StatusForbidden, "Forbidden")
		return
	}

	crewID := r.PathValue("crewId")
	missionID := r.PathValue("missionId")
	wsID := WorkspaceIDFromContext(r.Context())

	var currentStatus string
	err := h.db.QueryRowContext(r.Context(),
		`SELECT status FROM missions WHERE id = ? AND crew_id = ? AND workspace_id = ?`,
		missionID, crewID, wsID).Scan(&currentStatus)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeProblem(w, r, http.StatusNotFound, "Mission not found")
			return
		}
		h.logger.Error("get mission for start", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	if currentStatus != "PLANNING" {
		writeProblem(w, r, http.StatusBadRequest,
			fmt.Sprintf("cannot start mission in %s state, must be PLANNING", currentStatus))
		return
	}

	// Validate DAG before starting (circular deps, nonexistent dep IDs)
	if h.missionEngine != nil {
		if dagErr := h.missionEngine.ValidateDAG(r.Context(), missionID); dagErr != nil {
			writeProblem(w, r, http.StatusBadRequest, "Invalid task DAG: "+dagErr.Error())
			return
		}
	}

	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := h.db.ExecContext(r.Context(),
		`UPDATE missions SET status = 'IN_PROGRESS', updated_at = ? WHERE id = ?`,
		now, missionID); err != nil {
		h.logger.Error("update mission status", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Failed to start mission")
		return
	}

	if h.missionEngine != nil {
		if err := h.missionEngine.StartMission(context.Background(), missionID); err != nil {
			h.logger.Error("mission engine start failed, rolling back to PLANNING", "error", err, "mission_id", missionID)
			if _, rbErr := h.db.ExecContext(r.Context(),
				`UPDATE missions SET status = 'PLANNING', updated_at = ? WHERE id = ?`,
				now, missionID); rbErr != nil {
				h.logger.Error("rollback mission status", "error", rbErr, "mission_id", missionID)
			}
			writeProblem(w, r, http.StatusInternalServerError, "Failed to start mission engine")
			return
		}
	}

	if h.hub != nil {
		h.hub.Broadcast("mission:"+missionID, ws.ServerMessage{
			Type:    "mission.updated",
			Channel: "mission:" + missionID,
			Payload: map[string]interface{}{"id": missionID, "status": "IN_PROGRESS"},
		})
		wsChannel := "workspace:" + wsID
		h.hub.Broadcast(wsChannel, ws.ServerMessage{
			Type:    "mission.updated",
			Channel: wsChannel,
			Payload: map[string]interface{}{"id": missionID, "crew_id": crewID, "status": "IN_PROGRESS"},
		})
	}

	writeJSON(w, http.StatusOK, map[string]string{"id": missionID, "status": "IN_PROGRESS"})
}

// CreateTask handles POST /api/v1/crews/{crewId}/missions/{missionId}/tasks
func (h *MissionHandler) CreateTask(w http.ResponseWriter, r *http.Request) {
	role := RoleFromContext(r.Context())
	if !canRole(role, "create") {
		writeProblem(w, r, http.StatusForbidden, "Forbidden")
		return
	}

	missionID := r.PathValue("missionId")
	crewID := r.PathValue("crewId")
	wsID := WorkspaceIDFromContext(r.Context())

	// Verify mission exists and is in valid state
	var missionStatus string
	err := h.db.QueryRowContext(r.Context(),
		`SELECT status FROM missions WHERE id = ? AND crew_id = ? AND workspace_id = ?`,
		missionID, crewID, wsID).Scan(&missionStatus)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeProblem(w, r, http.StatusNotFound, "Mission not found")
			return
		}
		h.logger.Error("get mission for task creation", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	if missionStatus != "PLANNING" && missionStatus != "IN_PROGRESS" {
		writeProblem(w, r, http.StatusBadRequest, "Tasks can only be added to PLANNING or IN_PROGRESS missions")
		return
	}

	var req struct {
		Title           string  `json:"title"`
		Description     *string `json:"description"`
		AssignedAgentID *string `json:"assigned_agent_id"`
		TaskOrder       int     `json:"task_order"`
		DependsOn       []string `json:"depends_on"`
		MaxIterations   *int    `json:"max_iterations"`
	}
	if err := readJSON(r, &req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "Invalid JSON body")
		return
	}
	if req.Title == "" {
		writeProblem(w, r, http.StatusBadRequest, "title is required")
		return
	}

	dependsOnJSON := "[]"
	if len(req.DependsOn) > 0 {
		b, _ := json.Marshal(req.DependsOn)
		dependsOnJSON = string(b)
	}

	// Determine initial status based on dependencies
	status := "PENDING"
	if len(req.DependsOn) > 0 {
		// Validate all dependency IDs exist and check if all are completed
		allCompleted := true
		for _, depID := range req.DependsOn {
			var depStatus string
			depErr := h.db.QueryRowContext(r.Context(),
				`SELECT status FROM mission_tasks WHERE id = ? AND mission_id = ?`,
				depID, missionID).Scan(&depStatus)
			if depErr != nil {
				if errors.Is(depErr, sql.ErrNoRows) {
					writeProblem(w, r, http.StatusBadRequest, "dependency task not found: "+depID)
					return
				}
				h.logger.Error("lookup dependency task", "error", depErr)
				writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
				return
			}
			if depStatus != "COMPLETED" {
				allCompleted = false
			}
		}
		if !allCompleted {
			status = "BLOCKED"
		}
	}

	id := generateCUID()
	now := time.Now().UTC().Format(time.RFC3339)

	_, err = h.db.ExecContext(r.Context(), `
		INSERT INTO mission_tasks (id, mission_id, assigned_agent_id, title, description, status, task_order, depends_on, max_iterations, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, missionID, req.AssignedAgentID, req.Title, req.Description, status, req.TaskOrder, dependsOnJSON, req.MaxIterations, now, now)
	if err != nil {
		h.logger.Error("create mission task", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	resp := missionTaskResponse{
		ID:              id,
		MissionID:       missionID,
		AssignedAgentID: req.AssignedAgentID,
		Title:           req.Title,
		Description:     req.Description,
		Status:          status,
		TaskOrder:       req.TaskOrder,
		DependsOn:       dependsOnJSON,
		MaxIterations:   req.MaxIterations,
		CreatedAt:       now,
		UpdatedAt:       now,
	}

	if h.hub != nil {
		h.hub.Broadcast("mission:"+missionID, ws.ServerMessage{
			Type:    "task.created",
			Channel: "mission:" + missionID,
			Payload: map[string]string{"id": id, "title": req.Title, "status": status},
		})
		wsChannel := "workspace:" + wsID
		h.hub.Broadcast(wsChannel, ws.ServerMessage{
			Type:    "task.updated",
			Channel: wsChannel,
			Payload: map[string]string{"id": id, "mission_id": missionID, "status": status},
		})
	}

	writeJSON(w, http.StatusCreated, resp)
}

// UpdateTask handles PATCH /api/v1/crews/{crewId}/missions/{missionId}/tasks/{taskId}
func (h *MissionHandler) UpdateTask(w http.ResponseWriter, r *http.Request) {
	role := RoleFromContext(r.Context())
	if !canRole(role, "create") {
		writeProblem(w, r, http.StatusForbidden, "Forbidden")
		return
	}

	crewID := r.PathValue("crewId")
	missionID := r.PathValue("missionId")
	taskID := r.PathValue("taskId")
	wsID := WorkspaceIDFromContext(r.Context())

	var req struct {
		Status          *string  `json:"status"`
		Title           *string  `json:"title"`
		Description     *string  `json:"description"`
		DependsOn       *string  `json:"depends_on"`
		AssignedAgentID *string  `json:"assigned_agent_id"`
		ResultSummary   *string  `json:"result_summary"`
		ErrorMessage    *string  `json:"error_message"`
		OutputPath      *string  `json:"output_path"`
		TokenCount      *int     `json:"token_count"`
		EstimatedCost   *float64 `json:"estimated_cost"`
	}
	if err := readJSON(r, &req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "Invalid JSON body")
		return
	}

	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		h.logger.Error("begin transaction", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	defer tx.Rollback() //nolint:errcheck

	// Get current task — scoped by crew + workspace via mission join
	var currentStatus string
	err = tx.QueryRowContext(r.Context(),
		`SELECT mt.status FROM mission_tasks mt
		 JOIN missions m ON m.id = mt.mission_id
		 WHERE mt.id = ? AND mt.mission_id = ? AND m.crew_id = ? AND m.workspace_id = ?`,
		taskID, missionID, crewID, wsID).Scan(&currentStatus)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeProblem(w, r, http.StatusNotFound, "Task not found")
			return
		}
		h.logger.Error("get task for update", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)
	unblockNeeded := false

	if req.Status != nil {
		newStatus := *req.Status
		allowed := validTaskTransitions[currentStatus]
		valid := false
		for _, s := range allowed {
			if s == newStatus {
				valid = true
				break
			}
		}
		if !valid {
			writeProblem(w, r, http.StatusBadRequest, "Invalid status transition from "+currentStatus+" to "+newStatus)
			return
		}

		updates := `status = ?, updated_at = ?`
		args := []interface{}{newStatus, now}

		if newStatus == "IN_PROGRESS" && currentStatus != "IN_PROGRESS" {
			updates += `, started_at = ?`
			args = append(args, now)
		}
		if newStatus == "COMPLETED" || newStatus == "FAILED" || newStatus == "SKIPPED" {
			updates += `, completed_at = ?`
			args = append(args, now)
		}

		args = append(args, taskID, missionID)
		if _, err = tx.ExecContext(r.Context(),
			`UPDATE mission_tasks SET `+updates+` WHERE id = ? AND mission_id = ?`, args...); err != nil {
			h.logger.Error("update task status", "error", err)
			writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
			return
		}

		unblockNeeded = newStatus == "COMPLETED"
	}

	// Editable fields — only when task hasn't started yet
	if req.Title != nil || req.Description != nil || req.DependsOn != nil {
		if currentStatus != "PENDING" && currentStatus != "BLOCKED" {
			writeProblem(w, r, http.StatusBadRequest, "Can only edit title/description/depends_on for PENDING or BLOCKED tasks")
			tx.Rollback() //nolint:errcheck
			return
		}
	}
	if req.Title != nil {
		if _, err = tx.ExecContext(r.Context(), `UPDATE mission_tasks SET title = ?, updated_at = ? WHERE id = ?`, *req.Title, now, taskID); err != nil {
			h.logger.Error("update task title", "error", err)
			writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
			return
		}
	}
	if req.Description != nil {
		if _, err = tx.ExecContext(r.Context(), `UPDATE mission_tasks SET description = ?, updated_at = ? WHERE id = ?`, *req.Description, now, taskID); err != nil {
			h.logger.Error("update task description", "error", err)
			writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
			return
		}
	}
	if req.DependsOn != nil {
		var depIDs []string
		if err := json.Unmarshal([]byte(*req.DependsOn), &depIDs); err != nil {
			writeProblem(w, r, http.StatusBadRequest, "depends_on must be a JSON array of task IDs")
			return
		}
		for _, dep := range depIDs {
			if dep == taskID {
				writeProblem(w, r, http.StatusBadRequest, "Task cannot depend on itself")
				return
			}
			var depExists bool
			if qErr := tx.QueryRowContext(r.Context(),
				`SELECT 1 FROM mission_tasks WHERE id = ? AND mission_id = ?`, dep, missionID).Scan(&depExists); qErr != nil {
				writeProblem(w, r, http.StatusBadRequest, fmt.Sprintf("Dependency task %s not found in this mission", dep))
				return
			}
		}
		// Update status based on deps: BLOCKED if any dep is not COMPLETED
		newStatus := "PENDING"
		for _, dep := range depIDs {
			var depStatus string
			tx.QueryRowContext(r.Context(), `SELECT status FROM mission_tasks WHERE id = ?`, dep).Scan(&depStatus)
			if depStatus != "COMPLETED" {
				newStatus = "BLOCKED"
				break
			}
		}
		if len(depIDs) == 0 {
			newStatus = "PENDING"
		}
		if _, err = tx.ExecContext(r.Context(),
			`UPDATE mission_tasks SET depends_on = ?, status = ?, updated_at = ? WHERE id = ?`,
			*req.DependsOn, newStatus, now, taskID); err != nil {
			h.logger.Error("update task depends_on", "error", err)
			writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
			return
		}
	}

	if req.ResultSummary != nil {
		if _, err = tx.ExecContext(r.Context(), `UPDATE mission_tasks SET result_summary = ?, updated_at = ? WHERE id = ?`, *req.ResultSummary, now, taskID); err != nil {
			h.logger.Error("update task result_summary", "error", err)
			writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
			return
		}
	}
	if req.ErrorMessage != nil {
		if _, err = tx.ExecContext(r.Context(), `UPDATE mission_tasks SET error_message = ?, updated_at = ? WHERE id = ?`, *req.ErrorMessage, now, taskID); err != nil {
			h.logger.Error("update task error_message", "error", err)
			writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
			return
		}
	}
	if req.OutputPath != nil {
		if _, err = tx.ExecContext(r.Context(), `UPDATE mission_tasks SET output_path = ?, updated_at = ? WHERE id = ?`, *req.OutputPath, now, taskID); err != nil {
			h.logger.Error("update task output_path", "error", err)
			writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
			return
		}
	}
	if req.AssignedAgentID != nil {
		if _, err = tx.ExecContext(r.Context(), `UPDATE mission_tasks SET assigned_agent_id = ?, updated_at = ? WHERE id = ?`, *req.AssignedAgentID, now, taskID); err != nil {
			h.logger.Error("update task assigned_agent_id", "error", err)
			writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
			return
		}
	}
	if req.TokenCount != nil {
		if _, err = tx.ExecContext(r.Context(), `UPDATE mission_tasks SET token_count = ?, updated_at = ? WHERE id = ?`, *req.TokenCount, now, taskID); err != nil {
			h.logger.Error("update task token_count", "error", err)
			writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
			return
		}
	}
	if req.EstimatedCost != nil {
		if _, err = tx.ExecContext(r.Context(), `UPDATE mission_tasks SET estimated_cost = ?, updated_at = ? WHERE id = ?`, *req.EstimatedCost, now, taskID); err != nil {
			h.logger.Error("update task estimated_cost", "error", err)
			writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
			return
		}
	}

	if err = tx.Commit(); err != nil {
		h.logger.Error("commit task update", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	// After commit: unblock dependents and broadcast status change
	if unblockNeeded {
		h.unblockDependentTasks(r, missionID, taskID)
	}

	// Return updated task
	var t missionTaskResponse
	if err := h.db.QueryRowContext(r.Context(), `
		SELECT mt.id, mt.mission_id, mt.assigned_agent_id, mt.title, mt.description,
		       mt.status, mt.task_order, mt.depends_on, mt.iteration, mt.max_iterations,
		       mt.result_summary, mt.output_path, mt.error_message, mt.assignment_id,
		       mt.token_count, mt.estimated_cost, mt.started_at, mt.completed_at,
		       mt.duration_ms, mt.created_at, mt.updated_at
		FROM mission_tasks mt
		WHERE mt.id = ?`, taskID).Scan(
		&t.ID, &t.MissionID, &t.AssignedAgentID, &t.Title, &t.Description,
		&t.Status, &t.TaskOrder, &t.DependsOn, &t.Iteration, &t.MaxIterations,
		&t.ResultSummary, &t.OutputPath, &t.ErrorMessage, &t.AssignmentID,
		&t.TokenCount, &t.EstimatedCost, &t.StartedAt, &t.CompletedAt,
		&t.DurationMs, &t.CreatedAt, &t.UpdatedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeProblem(w, r, http.StatusNotFound, "Task not found")
			return
		}
		h.logger.Error("read updated task", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	if h.hub != nil && req.Status != nil {
		h.hub.Broadcast("mission:"+missionID, ws.ServerMessage{
			Type:    "task.status",
			Channel: "mission:" + missionID,
			Payload: map[string]string{"id": taskID, "status": *req.Status},
		})
		// Broadcast to workspace for dashboard visibility
		wsChannel := "workspace:" + wsID
		h.hub.Broadcast(wsChannel, ws.ServerMessage{
			Type:    "task.updated",
			Channel: wsChannel,
			Payload: map[string]string{"id": taskID, "mission_id": missionID, "status": *req.Status},
		})
	}

	writeJSON(w, http.StatusOK, t)
}

// unblockDependentTasks finds BLOCKED tasks whose all dependencies are now completed
// and transitions them to PENDING.
func (h *MissionHandler) unblockDependentTasks(r *http.Request, missionID, completedTaskID string) {
	wsID := WorkspaceIDFromContext(r.Context())
	rows, err := h.db.QueryContext(r.Context(),
		`SELECT id, depends_on FROM mission_tasks WHERE mission_id = ? AND status = 'BLOCKED'`,
		missionID)
	if err != nil {
		h.logger.Error("query blocked tasks", "error", err)
		return
	}
	defer rows.Close()

	now := time.Now().UTC().Format(time.RFC3339)
	for rows.Next() {
		var id, depsJSON string
		if err := rows.Scan(&id, &depsJSON); err != nil {
			continue
		}

		var deps []string
		if err := json.Unmarshal([]byte(depsJSON), &deps); err != nil {
			continue
		}

		// Check if this task depends on the completed task
		dependsOnCompleted := false
		for _, dep := range deps {
			if dep == completedTaskID {
				dependsOnCompleted = true
				break
			}
		}
		if !dependsOnCompleted {
			continue
		}

		// Check if ALL dependencies are now completed
		allDone := true
		for _, dep := range deps {
			var depStatus string
			err := h.db.QueryRowContext(r.Context(),
				`SELECT status FROM mission_tasks WHERE id = ?`, dep).Scan(&depStatus)
			if err != nil || depStatus != "COMPLETED" {
				allDone = false
				break
			}
		}

		if allDone {
			if _, err := h.db.ExecContext(r.Context(),
				`UPDATE mission_tasks SET status = 'PENDING', updated_at = ? WHERE id = ?`,
				now, id); err != nil {
				h.logger.Error("unblock task failed", "task_id", id, "error", err)
			}
			if h.hub != nil {
				h.hub.Broadcast("mission:"+missionID, ws.ServerMessage{
					Type:    "task.status",
					Channel: "mission:" + missionID,
					Payload: map[string]string{"id": id, "status": "PENDING"},
				})
				wsChannel := "workspace:" + wsID
				h.hub.Broadcast(wsChannel, ws.ServerMessage{
					Type:    "task.updated",
					Channel: wsChannel,
					Payload: map[string]string{"id": id, "mission_id": missionID, "status": "PENDING"},
				})
			}
		}
	}
}

// unblockCompletedDeps iterates all BLOCKED tasks in a mission and transitions
// those whose dependencies are all COMPLETED to PENDING. Used after restart
// to fix tasks that were blindly set to BLOCKED despite having met deps.
func (h *MissionHandler) unblockCompletedDeps(r *http.Request, missionID string) {
	rows, err := h.db.QueryContext(r.Context(),
		`SELECT id, depends_on FROM mission_tasks WHERE mission_id = ? AND status = 'BLOCKED'`,
		missionID)
	if err != nil {
		h.logger.Error("unblockCompletedDeps: query", "error", err)
		return
	}

	type blockedTask struct {
		id   string
		deps []string
	}
	var candidates []blockedTask
	for rows.Next() {
		var id, depsJSON string
		if err := rows.Scan(&id, &depsJSON); err != nil {
			continue
		}
		var deps []string
		if err := json.Unmarshal([]byte(depsJSON), &deps); err != nil || len(deps) == 0 {
			continue
		}
		candidates = append(candidates, blockedTask{id: id, deps: deps})
	}
	rows.Close()

	now := time.Now().UTC().Format(time.RFC3339)
	for _, bt := range candidates {
		allDone := true
		for _, dep := range bt.deps {
			var depStatus string
			err := h.db.QueryRowContext(r.Context(),
				`SELECT status FROM mission_tasks WHERE id = ?`, dep).Scan(&depStatus)
			if err != nil || depStatus != "COMPLETED" {
				allDone = false
				break
			}
		}
		if allDone {
			h.db.ExecContext(r.Context(),
				`UPDATE mission_tasks SET status = 'PENDING', updated_at = ? WHERE id = ?`,
				now, bt.id)
		}
	}
}

func (h *MissionHandler) getTaskStats(r *http.Request, missionID string) (*taskStats, error) {
	rows, err := h.db.QueryContext(r.Context(),
		`SELECT status, COUNT(*) FROM mission_tasks WHERE mission_id = ? GROUP BY status`,
		missionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	stats := &taskStats{}
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return nil, err
		}
		stats.Total += count
		switch status {
		case "PENDING":
			stats.Pending = count
		case "BLOCKED":
			stats.Blocked = count
		case "IN_PROGRESS":
			stats.InProgress = count
		case "COMPLETED":
			stats.Completed = count
		case "FAILED":
			stats.Failed = count
		case "SKIPPED":
			stats.Skipped = count
		}
	}
	return stats, rows.Err()
}

// Restart resets a FAILED/CANCELLED/REVIEW/COMPLETED mission: resets non-completed tasks,
// increments their iteration, and re-starts. Completed tasks stay completed (no re-run).
func (h *MissionHandler) Restart(w http.ResponseWriter, r *http.Request) {
	role := RoleFromContext(r.Context())
	if !canRole(role, "create") {
		writeProblem(w, r, http.StatusForbidden, "Forbidden")
		return
	}

	crewID := r.PathValue("crewId")
	missionID := r.PathValue("missionId")
	wsID := WorkspaceIDFromContext(r.Context())

	var currentStatus string
	err := h.db.QueryRowContext(r.Context(),
		`SELECT status FROM missions WHERE id = ? AND crew_id = ? AND workspace_id = ?`,
		missionID, crewID, wsID).Scan(&currentStatus)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeProblem(w, r, http.StatusNotFound, "Mission not found")
			return
		}
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	if currentStatus == "IN_PROGRESS" || currentStatus == "PLANNING" {
		writeProblem(w, r, http.StatusBadRequest,
			fmt.Sprintf("cannot restart mission in %s state", currentStatus))
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)

	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	defer tx.Rollback() //nolint:errcheck

	// Reset mission to PLANNING
	if _, err = tx.ExecContext(r.Context(),
		`UPDATE missions SET status = 'PLANNING', updated_at = ?, completed_at = NULL WHERE id = ?`,
		now, missionID); err != nil {
		writeProblem(w, r, http.StatusInternalServerError, "Failed to reset mission")
		return
	}

	// Reset FAILED/PENDING/BLOCKED/SKIPPED tasks; increment iteration; clear errors
	if _, err = tx.ExecContext(r.Context(),
		`UPDATE mission_tasks SET
			status = CASE WHEN depends_on = '[]' OR depends_on IS NULL THEN 'PENDING' ELSE 'BLOCKED' END,
			iteration = COALESCE(iteration, 0) + 1,
			error_message = NULL,
			result_summary = CASE WHEN status = 'COMPLETED' THEN result_summary ELSE NULL END,
			started_at = NULL,
			completed_at = NULL,
			duration_ms = NULL,
			assignment_id = NULL,
			updated_at = ?
		WHERE mission_id = ? AND status != 'COMPLETED'`,
		now, missionID); err != nil {
		writeProblem(w, r, http.StatusInternalServerError, "Failed to reset tasks")
		return
	}

	if err = tx.Commit(); err != nil {
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	// Post-reset: unblock tasks whose dependencies are all COMPLETED.
	// The SQL above blindly sets tasks with deps to BLOCKED, but some deps
	// may already be COMPLETED (they were not reset). Unblock those now.
	h.unblockCompletedDeps(r, missionID)

	if h.hub != nil {
		wsChannel := "workspace:" + wsID
		h.hub.Broadcast(wsChannel, ws.ServerMessage{
			Type:    "mission.updated",
			Channel: wsChannel,
			Payload: map[string]interface{}{"id": missionID, "crew_id": crewID, "status": "PLANNING"},
		})
	}

	writeJSON(w, http.StatusOK, map[string]string{"id": missionID, "status": "PLANNING"})
}

// Resume restarts a FAILED mission from the point of failure. Only the FAILED
// task(s) and their downstream dependents are reset; COMPLETED tasks stay.
// The DAG engine is started automatically — no separate Start call needed.
func (h *MissionHandler) Resume(w http.ResponseWriter, r *http.Request) {
	role := RoleFromContext(r.Context())
	if !canRole(role, "create") {
		writeProblem(w, r, http.StatusForbidden, "Forbidden")
		return
	}

	crewID := r.PathValue("crewId")
	missionID := r.PathValue("missionId")
	wsID := WorkspaceIDFromContext(r.Context())

	var currentStatus string
	err := h.db.QueryRowContext(r.Context(),
		`SELECT status FROM missions WHERE id = ? AND crew_id = ? AND workspace_id = ?`,
		missionID, crewID, wsID).Scan(&currentStatus)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeProblem(w, r, http.StatusNotFound, "Mission not found")
			return
		}
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	if currentStatus != "FAILED" {
		writeProblem(w, r, http.StatusBadRequest,
			fmt.Sprintf("can only resume FAILED missions, current status: %s", currentStatus))
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)

	// Collect all tasks to build dependency graph
	type taskRow struct {
		ID        string
		Status    string
		DependsOn string
	}
	rows, err := h.db.QueryContext(r.Context(),
		`SELECT id, status, depends_on FROM mission_tasks WHERE mission_id = ?`, missionID)
	if err != nil {
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	var tasks []taskRow
	for rows.Next() {
		var t taskRow
		if err := rows.Scan(&t.ID, &t.Status, &t.DependsOn); err != nil {
			rows.Close()
			writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
			return
		}
		tasks = append(tasks, t)
	}
	rows.Close()

	// Build reverse dependency map: taskID -> list of tasks that depend on it
	reverseDeps := make(map[string][]string)
	for _, t := range tasks {
		var deps []string
		if t.DependsOn != "" && t.DependsOn != "[]" {
			_ = json.Unmarshal([]byte(t.DependsOn), &deps)
		}
		for _, dep := range deps {
			reverseDeps[dep] = append(reverseDeps[dep], t.ID)
		}
	}

	// Find FAILED tasks and cascade downstream via BFS
	toReset := make(map[string]bool)
	queue := []string{}
	for _, t := range tasks {
		if t.Status == "FAILED" {
			toReset[t.ID] = true
			queue = append(queue, t.ID)
		}
	}
	for len(queue) > 0 {
		curr := queue[0]
		queue = queue[1:]
		for _, child := range reverseDeps[curr] {
			if !toReset[child] {
				toReset[child] = true
				queue = append(queue, child)
			}
		}
	}

	if len(toReset) == 0 {
		writeProblem(w, r, http.StatusBadRequest, "No failed tasks to resume from")
		return
	}

	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	defer tx.Rollback() //nolint:errcheck

	// Reset the identified tasks
	resetIDs := make([]string, 0, len(toReset))
	for id := range toReset {
		resetIDs = append(resetIDs, id)
	}

	// Build task status map for checking deps
	statusMap := make(map[string]string)
	for _, t := range tasks {
		if toReset[t.ID] {
			statusMap[t.ID] = "RESET"
		} else {
			statusMap[t.ID] = t.Status
		}
	}

	// Parse deps map
	depsMap := make(map[string][]string)
	for _, t := range tasks {
		var deps []string
		if t.DependsOn != "" && t.DependsOn != "[]" {
			_ = json.Unmarshal([]byte(t.DependsOn), &deps)
		}
		depsMap[t.ID] = deps
	}

	resetStatusMap := make(map[string]string, len(resetIDs))
	for _, id := range resetIDs {
		// Determine correct initial status: PENDING if all deps are COMPLETED, BLOCKED otherwise
		newStatus := "PENDING"
		for _, dep := range depsMap[id] {
			if statusMap[dep] != "COMPLETED" {
				newStatus = "BLOCKED"
				break
			}
		}
		resetStatusMap[id] = newStatus
		if _, err = tx.ExecContext(r.Context(),
			`UPDATE mission_tasks SET
				status = ?,
				iteration = COALESCE(iteration, 0) + 1,
				error_message = NULL,
				result_summary = NULL,
				started_at = NULL,
				completed_at = NULL,
				duration_ms = NULL,
				assignment_id = NULL,
				updated_at = ?
			WHERE id = ?`,
			newStatus, now, id); err != nil {
			writeProblem(w, r, http.StatusInternalServerError, "Failed to reset task")
			return
		}
	}

	// Set mission to IN_PROGRESS directly (skip PLANNING)
	if _, err = tx.ExecContext(r.Context(),
		`UPDATE missions SET status = 'IN_PROGRESS', updated_at = ?, completed_at = NULL WHERE id = ?`,
		now, missionID); err != nil {
		writeProblem(w, r, http.StatusInternalServerError, "Failed to update mission")
		return
	}

	if err = tx.Commit(); err != nil {
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	// Start DAG engine immediately
	if h.missionEngine != nil {
		if err := h.missionEngine.StartMission(context.Background(), missionID); err != nil {
			h.logger.Error("resume: mission engine start failed, rolling back", "error", err, "mission_id", missionID)
			if _, rbErr := h.db.ExecContext(r.Context(),
				`UPDATE missions SET status = 'FAILED', updated_at = ? WHERE id = ?`,
				now, missionID); rbErr != nil {
				h.logger.Error("resume: rollback mission status", "error", rbErr)
			}
			writeProblem(w, r, http.StatusInternalServerError, "Failed to start mission engine")
			return
		}
	}

	if h.hub != nil {
		wsChannel := "workspace:" + wsID
		h.hub.Broadcast(wsChannel, ws.ServerMessage{
			Type:    "mission.updated",
			Channel: wsChannel,
			Payload: map[string]interface{}{"id": missionID, "crew_id": crewID, "status": "IN_PROGRESS"},
		})
		for _, id := range resetIDs {
			h.hub.Broadcast(wsChannel, ws.ServerMessage{
				Type:    "task.updated",
				Channel: wsChannel,
				Payload: map[string]string{"id": id, "mission_id": missionID, "status": resetStatusMap[id]},
			})
		}
	}

	h.logger.Info("mission resumed from failure",
		"mission_id", missionID,
		"reset_tasks", len(toReset),
	)

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"id":          missionID,
		"status":      "IN_PROGRESS",
		"reset_tasks": len(toReset),
	})
}

// Clone creates a deep copy of a mission with all its tasks, assigning new IDs.
func (h *MissionHandler) Clone(w http.ResponseWriter, r *http.Request) {
	role := RoleFromContext(r.Context())
	if !canRole(role, "create") {
		writeProblem(w, r, http.StatusForbidden, "Forbidden")
		return
	}

	crewID := r.PathValue("crewId")
	missionID := r.PathValue("missionId")
	wsID := WorkspaceIDFromContext(r.Context())

	// Read original mission
	var m struct {
		Title       string
		Description sql.NullString
		LeadAgentID string
		Template    sql.NullString
	}
	err := h.db.QueryRowContext(r.Context(),
		`SELECT title, description, lead_agent_id, workflow_template
		 FROM missions WHERE id = ? AND crew_id = ? AND workspace_id = ?`,
		missionID, crewID, wsID).Scan(&m.Title, &m.Description, &m.LeadAgentID, &m.Template)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeProblem(w, r, http.StatusNotFound, "Mission not found")
			return
		}
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	// Read original tasks
	type taskRow struct {
		ID          string
		AgentID     sql.NullString
		Title       string
		Description sql.NullString
		TaskOrder   int
		DependsOn   string
		MaxIter     sql.NullInt64
	}
	rows, err := h.db.QueryContext(r.Context(),
		`SELECT id, assigned_agent_id, title, description, task_order, depends_on, max_iterations
		 FROM mission_tasks WHERE mission_id = ? ORDER BY task_order`, missionID)
	if err != nil {
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	var origTasks []taskRow
	for rows.Next() {
		var t taskRow
		if err := rows.Scan(&t.ID, &t.AgentID, &t.Title, &t.Description, &t.TaskOrder, &t.DependsOn, &t.MaxIter); err != nil {
			rows.Close()
			writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
			return
		}
		origTasks = append(origTasks, t)
	}
	rows.Close()

	// Create new IDs and remap dependencies
	newMissionID := generateCUID()
	idMap := make(map[string]string) // oldID -> newID
	for _, t := range origTasks {
		idMap[t.ID] = generateCUID()
	}

	now := time.Now().UTC().Format(time.RFC3339)

	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	defer tx.Rollback() //nolint:errcheck

	// Create synthetic chat (same pattern as Create)
	if _, err = tx.ExecContext(r.Context(),
		`INSERT INTO chats (id, agent_id, workspace_id, title, mode, status, started_at, created_at, updated_at)
		 VALUES (?, ?, ?, ?, 'MISSION', 'ACTIVE', ?, ?, ?)`,
		newMissionID, m.LeadAgentID, wsID, "Mission: "+m.Title+" (clone)", now, now, now); err != nil {
		h.logger.Error("create synthetic chat for clone", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Failed to create mission")
		return
	}

	// Insert new mission
	traceID := "mission-" + generateCUID()
	if _, err = tx.ExecContext(r.Context(),
		`INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, description,
		 status, workflow_template, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, 'PLANNING', ?, ?, ?)`,
		newMissionID, wsID, crewID, m.LeadAgentID, traceID,
		m.Title+" (copy)", m.Description, m.Template, now, now); err != nil {
		h.logger.Error("clone mission insert", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Failed to clone mission")
		return
	}

	// Insert cloned tasks with remapped dependencies
	for _, t := range origTasks {
		newTaskID := idMap[t.ID]
		newDeps := remapDependencies(t.DependsOn, idMap)
		status := "PENDING"
		if newDeps != "[]" {
			status = "BLOCKED"
		}
		if _, err = tx.ExecContext(r.Context(),
			`INSERT INTO mission_tasks (id, mission_id, assigned_agent_id, title, description,
			 status, task_order, depends_on, max_iterations, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			newTaskID, newMissionID, t.AgentID, t.Title, t.Description,
			status, t.TaskOrder, newDeps, t.MaxIter, now, now); err != nil {
			h.logger.Error("clone task insert", "error", err)
			writeProblem(w, r, http.StatusInternalServerError, "Failed to clone task")
			return
		}
	}

	if err = tx.Commit(); err != nil {
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	if h.hub != nil {
		wsChannel := "workspace:" + wsID
		h.hub.Broadcast(wsChannel, ws.ServerMessage{
			Type:    "mission.created",
			Channel: wsChannel,
			Payload: map[string]interface{}{"id": newMissionID, "crew_id": crewID, "status": "PLANNING"},
		})
	}

	writeJSON(w, http.StatusCreated, map[string]string{"id": newMissionID, "status": "PLANNING"})
}

func remapDependencies(depsJSON string, idMap map[string]string) string {
	var deps []string
	if err := json.Unmarshal([]byte(depsJSON), &deps); err != nil || len(deps) == 0 {
		return "[]"
	}
	newDeps := make([]string, 0, len(deps))
	for _, d := range deps {
		if newID, ok := idMap[d]; ok {
			newDeps = append(newDeps, newID)
		}
	}
	out, _ := json.Marshal(newDeps)
	return string(out)
}
