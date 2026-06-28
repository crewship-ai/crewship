package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// maxTaskIterations bounds the configured retry cap for an assignment task.
// The runtime loop (orchestrator/loop.go ShouldRetry) honours whatever value
// is stored, so an absurd value here would let a single task retry far beyond
// any sane budget. Mirrors the pipeline outcomes cap (dsl_validate_gates.go),
// just with more headroom for multi-step assignment tasks.
const maxTaskIterations = 50

// CreateTask handles POST /api/v1/crews/{crewId}/missions/{missionId}/tasks
func (h *MissionHandler) CreateTask(w http.ResponseWriter, r *http.Request) {
	if !requireRole(w, r, "create") {
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
		internalError(w, r, h.logger, "get mission for task creation", err)
		return
	}
	if missionStatus != "PLANNING" && missionStatus != "IN_PROGRESS" {
		writeProblem(w, r, http.StatusBadRequest, "Tasks can only be added to PLANNING or IN_PROGRESS missions")
		return
	}

	var req struct {
		Title           string   `json:"title"`
		Description     *string  `json:"description"`
		AssignedAgentID *string  `json:"assigned_agent_id"`
		TaskOrder       int      `json:"task_order"`
		DependsOn       []string `json:"depends_on"`
		MaxIterations   *int     `json:"max_iterations"`
	}
	if err := readJSON(r, &req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "Invalid JSON body")
		return
	}
	if req.Title == "" {
		writeProblem(w, r, http.StatusBadRequest, "title is required")
		return
	}
	if req.MaxIterations != nil && (*req.MaxIterations < 0 || *req.MaxIterations > maxTaskIterations) {
		writeProblem(w, r, http.StatusBadRequest,
			fmt.Sprintf("max_iterations must be between 0 and %d", maxTaskIterations))
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
		// Validate all dependency IDs exist in one batch query
		args := make([]interface{}, 0, len(req.DependsOn)+1)
		args = append(args, missionID)
		for _, depID := range req.DependsOn {
			args = append(args, depID)
		}
		depRows, depErr := h.db.QueryContext(r.Context(),
			`SELECT id, status FROM mission_tasks WHERE mission_id = ? AND id IN (`+sqlPlaceholders(len(req.DependsOn))+`)`, args...)
		if depErr != nil {
			internalError(w, r, h.logger, "lookup dependency tasks", depErr)
			return
		}
		depStatuses := make(map[string]string, len(req.DependsOn))
		for depRows.Next() {
			var id, st string
			if err := depRows.Scan(&id, &st); err != nil {
				depRows.Close()
				internalError(w, r, h.logger, "scan dependency task", err)
				return
			}
			depStatuses[id] = st
		}
		depRows.Close()

		allCompleted := true
		for _, depID := range req.DependsOn {
			st, ok := depStatuses[depID]
			if !ok {
				writeProblem(w, r, http.StatusBadRequest, "dependency task not found: "+depID)
				return
			}
			if st != "COMPLETED" {
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
		internalError(w, r, h.logger, "create mission task", err)
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

	broadcastChannelEvent(h.hub, "mission", missionID, "task.created",
		map[string]string{"id": id, "title": req.Title, "status": status})
	broadcastWorkspaceEvent(h.hub, wsID, "task.updated",
		map[string]string{"id": id, "mission_id": missionID, "status": status})

	writeJSON(w, http.StatusCreated, resp)
}

// updateTaskRequest holds the parsed JSON body for UpdateTask.
type updateTaskRequest struct {
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
	MaxIterations   *int     `json:"max_iterations"`
}

// validateTaskStatusTransition checks whether the transition from currentStatus
// to newStatus is allowed and returns an error message if not.
func validateTaskStatusTransition(currentStatus, newStatus string) string {
	allowed := validTaskTransitions[currentStatus]
	for _, s := range allowed {
		if s == newStatus {
			return ""
		}
	}
	return "Invalid status transition from " + currentStatus + " to " + newStatus
}

// applyTaskStatus updates the task status and related timestamp columns within the transaction.
func (h *MissionHandler) applyTaskStatus(ctx context.Context, tx *sql.Tx, taskID, missionID, currentStatus, newStatus, now string) error {
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
	_, err := tx.ExecContext(ctx, `UPDATE mission_tasks SET `+updates+` WHERE id = ? AND mission_id = ?`, args...)
	return err
}

// applyTaskEditableFields handles title, description, depends_on updates.
// These are only allowed for PENDING or BLOCKED tasks.
// Returns a non-nil error if the response was already written (validation failure).
func (h *MissionHandler) applyTaskEditableFields(ctx context.Context, tx *sql.Tx, req *updateTaskRequest, taskID, missionID, currentStatus, now string, w http.ResponseWriter, r *http.Request) error {
	// Editable fields — only when task hasn't started yet
	if req.Title != nil || req.Description != nil || req.DependsOn != nil {
		if currentStatus != "PENDING" && currentStatus != "BLOCKED" {
			writeProblem(w, r, http.StatusBadRequest, "Can only edit title/description/depends_on for PENDING or BLOCKED tasks")
			tx.Rollback() //nolint:errcheck
			return fmt.Errorf("validation failed")
		}
	}
	if req.Title != nil {
		if _, err := tx.ExecContext(ctx, `UPDATE mission_tasks SET title = ?, updated_at = ? WHERE id = ?`, *req.Title, now, taskID); err != nil {
			h.logger.Error("update task title", "error", err)
			writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
			return fmt.Errorf("update title: %w", err)
		}
	}
	if req.Description != nil {
		if _, err := tx.ExecContext(ctx, `UPDATE mission_tasks SET description = ?, updated_at = ? WHERE id = ?`, *req.Description, now, taskID); err != nil {
			h.logger.Error("update task description", "error", err)
			writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
			return fmt.Errorf("update description: %w", err)
		}
	}
	if req.DependsOn != nil {
		var depIDs []string
		if err := json.Unmarshal([]byte(*req.DependsOn), &depIDs); err != nil {
			writeProblem(w, r, http.StatusBadRequest, "depends_on must be a JSON array of task IDs")
			return fmt.Errorf("validation failed")
		}
		// Validate: no self-dependency and all deps exist in this mission (batch query)
		for _, dep := range depIDs {
			if dep == taskID {
				writeProblem(w, r, http.StatusBadRequest, "Task cannot depend on itself")
				return fmt.Errorf("validation failed")
			}
		}
		newStatus := "PENDING"
		if len(depIDs) > 0 {
			qa := make([]interface{}, 0, len(depIDs)+1)
			qa = append(qa, missionID)
			for _, dep := range depIDs {
				qa = append(qa, dep)
			}
			dRows, dErr := tx.QueryContext(ctx,
				`SELECT id, status FROM mission_tasks WHERE mission_id = ? AND id IN (`+sqlPlaceholders(len(depIDs))+`)`, qa...)
			if dErr != nil {
				h.logger.Error("batch query dep tasks", "error", dErr)
				writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
				return fmt.Errorf("batch query deps: %w", dErr)
			}
			depMap := make(map[string]string, len(depIDs))
			for dRows.Next() {
				var id, st string
				if err := dRows.Scan(&id, &st); err != nil {
					dRows.Close()
					return fmt.Errorf("scan dep: %w", err)
				}
				depMap[id] = st
			}
			dRows.Close()

			for _, dep := range depIDs {
				st, ok := depMap[dep]
				if !ok {
					writeProblem(w, r, http.StatusBadRequest, fmt.Sprintf("Dependency task %s not found in this mission", dep))
					return fmt.Errorf("validation failed")
				}
				if st != "COMPLETED" {
					newStatus = "BLOCKED"
				}
			}
		}
		if len(depIDs) == 0 {
			newStatus = "PENDING"
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE mission_tasks SET depends_on = ?, status = ?, updated_at = ? WHERE id = ?`,
			*req.DependsOn, newStatus, now, taskID); err != nil {
			h.logger.Error("update task depends_on", "error", err)
			writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
			return fmt.Errorf("update depends_on: %w", err)
		}
	}
	return nil
}

// applyTaskMetadataFields handles result_summary, error_message, output_path,
// assigned_agent_id, token_count, and estimated_cost updates within the transaction.
func (h *MissionHandler) applyTaskMetadataFields(ctx context.Context, tx *sql.Tx, req *updateTaskRequest, taskID string) error {
	ub := newUpdate()
	if req.ResultSummary != nil {
		ub.Set("result_summary", *req.ResultSummary)
	}
	if req.ErrorMessage != nil {
		ub.Set("error_message", *req.ErrorMessage)
	}
	if req.OutputPath != nil {
		ub.Set("output_path", *req.OutputPath)
	}
	if req.AssignedAgentID != nil {
		ub.Set("assigned_agent_id", *req.AssignedAgentID)
	}
	if req.TokenCount != nil {
		ub.Set("token_count", *req.TokenCount)
	}
	if req.EstimatedCost != nil {
		ub.Set("estimated_cost", *req.EstimatedCost)
	}
	if req.MaxIterations != nil {
		ub.Set("max_iterations", *req.MaxIterations)
	}

	if ub.Empty() {
		return nil
	}

	query, args := ub.Build("mission_tasks", "id = ?", taskID)
	_, err := tx.ExecContext(ctx, query, args...)
	return err
}

// UpdateTask handles PATCH /api/v1/crews/{crewId}/missions/{missionId}/tasks/{taskId}
func (h *MissionHandler) UpdateTask(w http.ResponseWriter, r *http.Request) {
	if !requireRole(w, r, "create") {
		return
	}

	crewID := r.PathValue("crewId")
	missionID := r.PathValue("missionId")
	taskID := r.PathValue("taskId")
	wsID := WorkspaceIDFromContext(r.Context())

	var req updateTaskRequest
	if err := readJSON(r, &req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "Invalid JSON body")
		return
	}
	if req.MaxIterations != nil && (*req.MaxIterations < 0 || *req.MaxIterations > maxTaskIterations) {
		writeProblem(w, r, http.StatusBadRequest,
			fmt.Sprintf("max_iterations must be between 0 and %d", maxTaskIterations))
		return
	}

	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		internalError(w, r, h.logger, "begin transaction", err)
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
		internalError(w, r, h.logger, "get task for update", err)
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)
	unblockNeeded := false

	// Reject conflicting updates: status and depends_on cannot be set simultaneously
	// because depends_on recalculates status based on deps, which would silently
	// override the explicit status transition.
	if req.Status != nil && req.DependsOn != nil {
		writeProblem(w, r, http.StatusBadRequest, "Cannot update status and depends_on in the same request")
		return
	}

	// Apply status transition
	if req.Status != nil {
		if msg := validateTaskStatusTransition(currentStatus, *req.Status); msg != "" {
			writeProblem(w, r, http.StatusBadRequest, msg)
			return
		}

		if err := h.applyTaskStatus(r.Context(), tx, taskID, missionID, currentStatus, *req.Status, now); err != nil {
			internalError(w, r, h.logger, "update task status", err)
			return
		}

		unblockNeeded = *req.Status == "COMPLETED"
	}

	// Apply editable fields (title/description/depends_on only for PENDING/BLOCKED tasks)
	if err := h.applyTaskEditableFields(r.Context(), tx, &req, taskID, missionID, currentStatus, now, w, r); err != nil {
		// Error response already written by applyTaskEditableFields
		return
	}

	// Apply result/metadata fields
	if err := h.applyTaskMetadataFields(r.Context(), tx, &req, taskID); err != nil {
		internalError(w, r, h.logger, "update task metadata", err)
		return
	}

	if err = tx.Commit(); err != nil {
		internalError(w, r, h.logger, "commit task update", err)
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
		internalError(w, r, h.logger, "read updated task", err)
		return
	}

	if req.Status != nil {
		broadcastChannelEvent(h.hub, "mission", missionID, "task.status",
			map[string]string{"id": taskID, "status": *req.Status})
		broadcastWorkspaceEvent(h.hub, wsID, "task.updated",
			map[string]string{"id": taskID, "mission_id": missionID, "status": *req.Status})
	}

	writeJSON(w, http.StatusOK, t)
}
