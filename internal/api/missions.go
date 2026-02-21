package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/crewship-ai/crewship/internal/ws"
)

type MissionHandler struct {
	db     *sql.DB
	hub    *ws.Hub
	logger *slog.Logger
}

func NewMissionHandler(db *sql.DB, hub *ws.Hub, logger *slog.Logger) *MissionHandler {
	return &MissionHandler{db: db, hub: hub, logger: logger}
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

	_, err = h.db.ExecContext(r.Context(), `
		INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, description, status, workflow_template, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, 'PLANNING', ?, ?, ?)`,
		id, wsID, crewID, req.LeadAgentID, traceID, req.Title, req.Description, req.WorkflowTemplate, now, now)
	if err != nil {
		h.logger.Error("create mission", "error", err)
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
		stats, _ := h.getTaskStats(r, m.ID)
		m.TaskStats = stats
		result = append(result, m)
	}
	if err := rows.Err(); err != nil {
		h.logger.Error("rows iteration (missions all)", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	if result == nil {
		result = []missionResponse{}
	}
	writeJSON(w, http.StatusOK, result)
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
		h.logger.Error("get mission tasks", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	defer taskRows.Close()

	m.Tasks = []missionTaskResponse{}
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
			h.logger.Error("scan mission task", "error", err)
			writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
			return
		}
		m.Tasks = append(m.Tasks, t)
	}

	stats, _ := h.getTaskStats(r, missionID)
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

	// Get current status
	var currentStatus string
	err := h.db.QueryRowContext(r.Context(),
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

		_, err = h.db.ExecContext(r.Context(),
			`UPDATE missions SET status = ?, completed_at = ?, updated_at = ? WHERE id = ?`,
			newStatus, completedAt, now, missionID)
		if err != nil {
			h.logger.Error("update mission status", "error", err)
			writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
			return
		}
	}

	if req.Title != nil {
		if _, err := h.db.ExecContext(r.Context(), `UPDATE missions SET title = ?, updated_at = ? WHERE id = ?`, *req.Title, now, missionID); err != nil {
			h.logger.Error("update mission title", "error", err)
			writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
			return
		}
	}
	if req.Description != nil {
		if _, err := h.db.ExecContext(r.Context(), `UPDATE missions SET description = ?, updated_at = ? WHERE id = ?`, *req.Description, now, missionID); err != nil {
			h.logger.Error("update mission description", "error", err)
			writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
			return
		}
	}
	if req.Plan != nil {
		if _, err := h.db.ExecContext(r.Context(), `UPDATE missions SET plan = ?, updated_at = ? WHERE id = ?`, *req.Plan, now, missionID); err != nil {
			h.logger.Error("update mission plan", "error", err)
			writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
			return
		}
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
		// Check if all dependencies are completed
		allCompleted := true
		for _, depID := range req.DependsOn {
			var depStatus string
			err := h.db.QueryRowContext(r.Context(),
				`SELECT status FROM mission_tasks WHERE id = ? AND mission_id = ?`,
				depID, missionID).Scan(&depStatus)
			if err != nil || depStatus != "COMPLETED" {
				allCompleted = false
				break
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

	// Get current task — scoped by crew + workspace via mission join
	var currentStatus string
	err := h.db.QueryRowContext(r.Context(),
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
		_, err = h.db.ExecContext(r.Context(),
			`UPDATE mission_tasks SET `+updates+` WHERE id = ? AND mission_id = ?`, args...)
		if err != nil {
			h.logger.Error("update task status", "error", err)
			writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
			return
		}

		// Unblock dependent tasks when a task completes
		if newStatus == "COMPLETED" {
			h.unblockDependentTasks(r, missionID, taskID)
		}
	}

	if req.ResultSummary != nil {
		if _, err := h.db.ExecContext(r.Context(), `UPDATE mission_tasks SET result_summary = ?, updated_at = ? WHERE id = ?`, *req.ResultSummary, now, taskID); err != nil {
			h.logger.Error("update task result_summary", "error", err)
			writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
			return
		}
	}
	if req.ErrorMessage != nil {
		if _, err := h.db.ExecContext(r.Context(), `UPDATE mission_tasks SET error_message = ?, updated_at = ? WHERE id = ?`, *req.ErrorMessage, now, taskID); err != nil {
			h.logger.Error("update task error_message", "error", err)
			writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
			return
		}
	}
	if req.OutputPath != nil {
		if _, err := h.db.ExecContext(r.Context(), `UPDATE mission_tasks SET output_path = ?, updated_at = ? WHERE id = ?`, *req.OutputPath, now, taskID); err != nil {
			h.logger.Error("update task output_path", "error", err)
			writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
			return
		}
	}
	if req.AssignedAgentID != nil {
		if _, err := h.db.ExecContext(r.Context(), `UPDATE mission_tasks SET assigned_agent_id = ?, updated_at = ? WHERE id = ?`, *req.AssignedAgentID, now, taskID); err != nil {
			h.logger.Error("update task assigned_agent_id", "error", err)
			writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
			return
		}
	}
	if req.TokenCount != nil {
		if _, err := h.db.ExecContext(r.Context(), `UPDATE mission_tasks SET token_count = ?, updated_at = ? WHERE id = ?`, *req.TokenCount, now, taskID); err != nil {
			h.logger.Error("update task token_count", "error", err)
			writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
			return
		}
	}
	if req.EstimatedCost != nil {
		if _, err := h.db.ExecContext(r.Context(), `UPDATE mission_tasks SET estimated_cost = ?, updated_at = ? WHERE id = ?`, *req.EstimatedCost, now, taskID); err != nil {
			h.logger.Error("update task estimated_cost", "error", err)
			writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
			return
		}
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
	}

	writeJSON(w, http.StatusOK, t)
}

// unblockDependentTasks finds BLOCKED tasks whose all dependencies are now completed
// and transitions them to PENDING.
func (h *MissionHandler) unblockDependentTasks(r *http.Request, missionID, completedTaskID string) {
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
			}
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
