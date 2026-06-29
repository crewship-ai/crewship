package api

import (
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/ws"
	"github.com/robfig/cron/v3"
)

// RecurringIssueHandler implements CRUD endpoints for recurring issue schedules.
type RecurringIssueHandler struct {
	db     *sql.DB
	hub    *ws.Hub
	logger *slog.Logger
}

// NewRecurringIssueHandler creates a new RecurringIssueHandler.
func NewRecurringIssueHandler(db *sql.DB, hub *ws.Hub, logger *slog.Logger) *RecurringIssueHandler {
	return &RecurringIssueHandler{db: db, hub: hub, logger: logger}
}

// ── Response type ──────────────────────────────────────────────────────────

type recurringIssueResponse struct {
	ID             string  `json:"id"`
	CrewID         string  `json:"crew_id"`
	CrewName       string  `json:"crew_name,omitempty"`
	Title          string  `json:"title"`
	Description    *string `json:"description"`
	Priority       string  `json:"priority"`
	ProjectID      *string `json:"project_id"`
	MilestoneID    *string `json:"milestone_id"`
	AssigneeType   *string `json:"assignee_type"`
	AssigneeID     *string `json:"assignee_id"`
	LabelsJSON     *string `json:"labels_json"`
	CronExpression string  `json:"cron_expression"`
	Enabled        bool    `json:"enabled"`
	NextRun        *string `json:"next_run"`
	LastRun        *string `json:"last_run"`
	RunCount       int     `json:"run_count"`
	CreatedAt      string  `json:"created_at"`
}

// cronParser is a standard 5-field cron parser (Minute | Hour | Dom | Month | Dow).
var cronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

// ── 1. List — GET /api/v1/recurring-issues ─────────────────────────────────

// List returns all recurring issue schedules in the workspace.
// GET /api/v1/recurring-issues
func (h *RecurringIssueHandler) List(w http.ResponseWriter, r *http.Request) {
	wsID := WorkspaceIDFromContext(r.Context())

	query := `
		SELECT ri.id, ri.crew_id, COALESCE(c.name, ''), ri.title, ri.description,
		       COALESCE(ri.priority, 'none'), ri.project_id, ri.milestone_id,
		       ri.assignee_type, ri.assignee_id, ri.labels_json,
		       ri.cron_expression, ri.enabled, ri.next_run, ri.last_run,
		       ri.run_count, ri.created_at
		FROM recurring_issues ri
		LEFT JOIN crews c ON ri.crew_id = c.id
		WHERE ri.workspace_id = ?`
	args := []interface{}{wsID}

	if crewID := r.URL.Query().Get("crew_id"); crewID != "" {
		query += " AND ri.crew_id = ?"
		args = append(args, crewID)
	}

	query += " ORDER BY ri.created_at DESC"

	rows, err := h.db.QueryContext(r.Context(), query, args...)
	if err != nil {
		internalError(w, r, h.logger, "list recurring issues", err)
		return
	}
	defer rows.Close()

	var result []recurringIssueResponse
	for rows.Next() {
		var ri recurringIssueResponse
		if err := rows.Scan(
			&ri.ID, &ri.CrewID, &ri.CrewName, &ri.Title, &ri.Description,
			&ri.Priority, &ri.ProjectID, &ri.MilestoneID,
			&ri.AssigneeType, &ri.AssigneeID, &ri.LabelsJSON,
			&ri.CronExpression, &ri.Enabled, &ri.NextRun, &ri.LastRun,
			&ri.RunCount, &ri.CreatedAt,
		); err != nil {
			internalError(w, r, h.logger, "scan recurring issue", err)
			return
		}
		result = append(result, ri)
	}
	if err := rows.Err(); err != nil {
		internalError(w, r, h.logger, "rows iteration (recurring issues)", err)
		return
	}

	if result == nil {
		result = []recurringIssueResponse{}
	}
	writeJSON(w, http.StatusOK, result)
}

// ── 2. Create — POST /api/v1/recurring-issues ──────────────────────────────

// Create adds a new recurring issue schedule with a cron expression.
// POST /api/v1/recurring-issues
func (h *RecurringIssueHandler) Create(w http.ResponseWriter, r *http.Request) {
	if !requireRole(w, r, "create") {
		return
	}

	wsID := WorkspaceIDFromContext(r.Context())

	var req struct {
		CrewID         string  `json:"crew_id"`
		Title          string  `json:"title"`
		Description    *string `json:"description"`
		Priority       string  `json:"priority"`
		ProjectID      *string `json:"project_id"`
		MilestoneID    *string `json:"milestone_id"`
		AssigneeType   *string `json:"assignee_type"`
		AssigneeID     *string `json:"assignee_id"`
		LabelsJSON     *string `json:"labels_json"`
		CronExpression string  `json:"cron_expression"`
	}
	if err := readJSON(r, &req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "Invalid JSON body")
		return
	}
	if req.CrewID == "" {
		writeProblem(w, r, http.StatusBadRequest, "crew_id is required")
		return
	}
	if req.Title == "" {
		writeProblem(w, r, http.StatusBadRequest, "title is required")
		return
	}
	if req.CronExpression == "" {
		writeProblem(w, r, http.StatusBadRequest, "cron_expression is required")
		return
	}
	if req.Priority == "" {
		req.Priority = "none"
	}

	// Validate cron expression
	schedule, err := cronParser.Parse(req.CronExpression)
	if err != nil {
		writeProblem(w, r, http.StatusBadRequest, "Invalid cron expression: "+err.Error())
		return
	}
	nextRun := schedule.Next(time.Now()).UTC().Format(time.RFC3339)

	// Verify crew belongs to workspace
	var crewExists int
	err = h.db.QueryRowContext(r.Context(),
		`SELECT 1 FROM crews WHERE id = ? AND workspace_id = ?`,
		req.CrewID, wsID).Scan(&crewExists)
	if err != nil {
		writeProblem(w, r, http.StatusBadRequest, "Crew not found in workspace")
		return
	}

	id := generateCUID()
	now := time.Now().UTC().Format(time.RFC3339)

	_, err = h.db.ExecContext(r.Context(), `
		INSERT INTO recurring_issues (id, workspace_id, crew_id, title, description, priority,
		    project_id, milestone_id, assignee_type, assignee_id, labels_json,
		    cron_expression, enabled, next_run, run_count, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, 0, ?)`,
		id, wsID, req.CrewID, req.Title, req.Description, req.Priority,
		req.ProjectID, req.MilestoneID, req.AssigneeType, req.AssigneeID, req.LabelsJSON,
		req.CronExpression, nextRun, now)
	if err != nil {
		internalError(w, r, h.logger, "insert recurring issue", err)
		return
	}

	resp := recurringIssueResponse{
		ID:             id,
		CrewID:         req.CrewID,
		Title:          req.Title,
		Description:    req.Description,
		Priority:       req.Priority,
		ProjectID:      req.ProjectID,
		MilestoneID:    req.MilestoneID,
		AssigneeType:   req.AssigneeType,
		AssigneeID:     req.AssigneeID,
		LabelsJSON:     req.LabelsJSON,
		CronExpression: req.CronExpression,
		Enabled:        true,
		NextRun:        &nextRun,
		RunCount:       0,
		CreatedAt:      now,
	}

	broadcastWorkspaceEvent(h.hub, wsID, "recurring_issue.created", map[string]string{"id": id})

	writeJSON(w, http.StatusCreated, resp)
}

// ── 3. Update — PATCH /api/v1/recurring-issues/{recurringId} ────────────────────────

// Update modifies a recurring issue schedule's properties.
// PATCH /api/v1/recurring-issues/{recurringId}
func (h *RecurringIssueHandler) Update(w http.ResponseWriter, r *http.Request) {
	if !requireRole(w, r, "create") {
		return
	}

	riID := r.PathValue("recurringId")
	wsID := WorkspaceIDFromContext(r.Context())

	// Verify record exists
	var existingID string
	err := h.db.QueryRowContext(r.Context(),
		`SELECT id FROM recurring_issues WHERE id = ? AND workspace_id = ?`,
		riID, wsID).Scan(&existingID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeProblem(w, r, http.StatusNotFound, "Recurring issue not found")
			return
		}
		internalError(w, r, h.logger, "get recurring issue for update", err)
		return
	}

	var req struct {
		CrewID         *string `json:"crew_id"`
		Title          *string `json:"title"`
		Description    *string `json:"description"`
		Priority       *string `json:"priority"`
		ProjectID      *string `json:"project_id"`
		MilestoneID    *string `json:"milestone_id"`
		AssigneeType   *string `json:"assignee_type"`
		AssigneeID     *string `json:"assignee_id"`
		LabelsJSON     *string `json:"labels_json"`
		CronExpression *string `json:"cron_expression"`
		Enabled        *bool   `json:"enabled"`
	}
	if err := readJSON(r, &req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "Invalid JSON body")
		return
	}

	ub := newUpdate()

	if req.CrewID != nil {
		ub.Set("crew_id", *req.CrewID)
	}
	if req.Title != nil {
		ub.Set("title", *req.Title)
	}
	if req.Description != nil {
		ub.Set("description", *req.Description)
	}
	if req.Priority != nil {
		ub.Set("priority", *req.Priority)
	}
	if req.ProjectID != nil {
		if *req.ProjectID == "" {
			ub.SetNull("project_id")
		} else {
			ub.Set("project_id", *req.ProjectID)
		}
	}
	if req.MilestoneID != nil {
		if *req.MilestoneID == "" {
			ub.SetNull("milestone_id")
		} else {
			ub.Set("milestone_id", *req.MilestoneID)
		}
	}
	if req.AssigneeType != nil {
		ub.Set("assignee_type", *req.AssigneeType)
	}
	if req.AssigneeID != nil {
		ub.Set("assignee_id", *req.AssigneeID)
	}
	if req.LabelsJSON != nil {
		ub.Set("labels_json", *req.LabelsJSON)
	}
	if req.Enabled != nil {
		ub.Set("enabled", *req.Enabled)
	}

	// If cron changed, recompute next_run
	if req.CronExpression != nil {
		schedule, err := cronParser.Parse(*req.CronExpression)
		if err != nil {
			writeProblem(w, r, http.StatusBadRequest, "Invalid cron expression: "+err.Error())
			return
		}
		nextRun := schedule.Next(time.Now()).UTC().Format(time.RFC3339)
		ub.Set("cron_expression", *req.CronExpression)
		ub.Set("next_run", nextRun)
	}

	if ub.Empty() {
		writeProblem(w, r, http.StatusBadRequest, "No fields to update")
		return
	}

	query, args := ub.Build("recurring_issues", "id = ? AND workspace_id = ?", riID, wsID)
	if _, err := h.db.ExecContext(r.Context(), query, args...); err != nil {
		internalError(w, r, h.logger, "update recurring issue", err)
		return
	}

	broadcastWorkspaceEvent(h.hub, wsID, "recurring_issue.updated", map[string]string{"id": riID})

	// Return updated record
	var ri recurringIssueResponse
	err = h.db.QueryRowContext(r.Context(), `
		SELECT ri.id, ri.crew_id, COALESCE(c.name, ''), ri.title, ri.description,
		       COALESCE(ri.priority, 'none'), ri.project_id, ri.milestone_id,
		       ri.assignee_type, ri.assignee_id, ri.labels_json,
		       ri.cron_expression, ri.enabled, ri.next_run, ri.last_run,
		       ri.run_count, ri.created_at
		FROM recurring_issues ri
		LEFT JOIN crews c ON ri.crew_id = c.id
		WHERE ri.id = ?`, riID).Scan(
		&ri.ID, &ri.CrewID, &ri.CrewName, &ri.Title, &ri.Description,
		&ri.Priority, &ri.ProjectID, &ri.MilestoneID,
		&ri.AssigneeType, &ri.AssigneeID, &ri.LabelsJSON,
		&ri.CronExpression, &ri.Enabled, &ri.NextRun, &ri.LastRun,
		&ri.RunCount, &ri.CreatedAt,
	)
	if err != nil {
		internalError(w, r, h.logger, "read updated recurring issue", err)
		return
	}

	writeJSON(w, http.StatusOK, ri)
}

// ── 4. Delete — DELETE /api/v1/recurring-issues/{recurringId} ───────────────────────

// Delete removes a recurring issue schedule.
// DELETE /api/v1/recurring-issues/{recurringId}
func (h *RecurringIssueHandler) Delete(w http.ResponseWriter, r *http.Request) {
	if !requireRole(w, r, "manage") {
		return
	}

	riID := r.PathValue("recurringId")
	wsID := WorkspaceIDFromContext(r.Context())

	found, err := deleteByID(r.Context(), h.db, "recurring_issues", riID, wsID)
	if err != nil {
		internalError(w, r, h.logger, "delete recurring issue", err)
		return
	}
	if !found {
		writeProblem(w, r, http.StatusNotFound, "Recurring issue not found")
		return
	}

	broadcastWorkspaceEvent(h.hub, wsID, "recurring_issue.deleted", map[string]string{"id": riID})

	w.WriteHeader(http.StatusNoContent)
}
