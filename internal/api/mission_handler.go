package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/crewship-ai/crewship/internal/ws"
)

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

	// Atomic delete with status guard — prevents TOCTOU race where a concurrent
	// Start flips the row to IN_PROGRESS between a separate check and delete.
	res, err := h.db.ExecContext(r.Context(),
		`DELETE FROM missions WHERE id = ? AND crew_id = ? AND workspace_id = ? AND status IN ('PLANNING', 'CANCELLED')`,
		missionID, crewID, wsID)
	if err != nil {
		h.logger.Error("delete mission", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	affected, err := res.RowsAffected()
	if err != nil {
		h.logger.Error("delete mission rows affected", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	if affected == 0 {
		// Distinguish "not found" from "exists but wrong status"
		var currentStatus string
		qErr := h.db.QueryRowContext(r.Context(),
			`SELECT status FROM missions WHERE id = ? AND crew_id = ? AND workspace_id = ?`,
			missionID, crewID, wsID).Scan(&currentStatus)
		if qErr != nil {
			if errors.Is(qErr, sql.ErrNoRows) {
				writeProblem(w, r, http.StatusNotFound, "Mission not found")
				return
			}
			h.logger.Error("delete mission follow-up query", "error", qErr)
			writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
			return
		}
		writeProblem(w, r, http.StatusBadRequest, "Only PLANNING or CANCELLED missions can be deleted")
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
	// Atomic compare-and-swap: only update if still PLANNING (prevents concurrent start race)
	res, err := h.db.ExecContext(r.Context(),
		`UPDATE missions SET status = 'IN_PROGRESS', updated_at = ? WHERE id = ? AND status = 'PLANNING'`,
		now, missionID)
	if err != nil {
		h.logger.Error("update mission status", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Failed to start mission")
		return
	}
	rows, err := res.RowsAffected()
	if err != nil {
		h.logger.Error("check rows affected", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	if rows == 0 {
		writeProblem(w, r, http.StatusConflict, "Mission was already started by another request")
		return
	}

	if h.missionEngine != nil {
		if err := h.missionEngine.StartMission(context.Background(), missionID); err != nil {
			h.logger.Error("mission engine start failed, rolling back to PLANNING", "error", err, "mission_id", missionID)
			if _, rbErr := h.db.ExecContext(context.Background(),
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
