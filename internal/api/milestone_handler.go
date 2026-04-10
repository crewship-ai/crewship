package api

import (
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/ws"
)

// MilestoneHandler implements CRUD endpoints for project milestones.
type MilestoneHandler struct {
	db     *sql.DB
	hub    *ws.Hub
	logger *slog.Logger
}

// NewMilestoneHandler creates a new MilestoneHandler.
func NewMilestoneHandler(db *sql.DB, hub *ws.Hub, logger *slog.Logger) *MilestoneHandler {
	return &MilestoneHandler{db: db, hub: hub, logger: logger}
}

// ── Response type ──────────────────────────────────────────────────────────

type milestoneResponse struct {
	ID          string  `json:"id"`
	ProjectID   string  `json:"project_id"`
	Name        string  `json:"name"`
	Description *string `json:"description"`
	TargetDate  *string `json:"target_date"`
	Status      string  `json:"status"`
	Position    int     `json:"position"`
	IssueCount  int     `json:"issue_count"`
	DoneCount   int     `json:"done_count"`
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`
}

// ── 1. List — GET /api/v1/projects/{projectId}/milestones ─────────────────

// List returns all milestones for a project.
// GET /api/v1/projects/{projectId}/milestones
func (h *MilestoneHandler) List(w http.ResponseWriter, r *http.Request) {
	wsID := WorkspaceIDFromContext(r.Context())
	projectID := r.PathValue("projectId")

	// Verify project belongs to workspace
	if err := projectExists(r.Context(), h.db, projectID, wsID); err != nil {
		writeProblem(w, r, http.StatusNotFound, "Project not found")
		return
	}

	rows, err := h.db.QueryContext(r.Context(), `
		SELECT m.id, m.project_id, m.name, m.description, m.target_date,
		       m.status, m.position, m.created_at, m.updated_at,
		       COALESCE(ic.issue_count, 0),
		       COALESCE(ic.done_count, 0)
		FROM milestones m
		LEFT JOIN (
		    SELECT milestone_id,
		           COUNT(*) AS issue_count,
		           SUM(CASE WHEN status IN ('DONE','COMPLETED') THEN 1 ELSE 0 END) AS done_count
		    FROM missions WHERE mission_type = 'issue' GROUP BY milestone_id
		) ic ON ic.milestone_id = m.id
		WHERE m.project_id = ?
		ORDER BY m.position ASC, m.created_at ASC`, projectID)
	if err != nil {
		h.logger.Error("list milestones", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	defer rows.Close()

	var result []milestoneResponse
	for rows.Next() {
		var ms milestoneResponse
		if err := rows.Scan(
			&ms.ID, &ms.ProjectID, &ms.Name, &ms.Description, &ms.TargetDate,
			&ms.Status, &ms.Position, &ms.CreatedAt, &ms.UpdatedAt,
			&ms.IssueCount, &ms.DoneCount,
		); err != nil {
			h.logger.Error("scan milestone", "error", err)
			writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
			return
		}
		result = append(result, ms)
	}
	if err := rows.Err(); err != nil {
		h.logger.Error("rows iteration (milestones)", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	if result == nil {
		result = []milestoneResponse{}
	}
	writeJSON(w, http.StatusOK, result)
}

// ── 2. Create — POST /api/v1/projects/{projectId}/milestones ──────────────

// Create adds a new milestone to a project.
// POST /api/v1/projects/{projectId}/milestones
func (h *MilestoneHandler) Create(w http.ResponseWriter, r *http.Request) {
	role := RoleFromContext(r.Context())
	if !canRole(role, "create") {
		writeProblem(w, r, http.StatusForbidden, "Forbidden")
		return
	}

	wsID := WorkspaceIDFromContext(r.Context())
	projectID := r.PathValue("projectId")

	// Verify project belongs to workspace
	if err := projectExists(r.Context(), h.db, projectID, wsID); err != nil {
		writeProblem(w, r, http.StatusNotFound, "Project not found")
		return
	}

	var req struct {
		Name        string  `json:"name"`
		Description *string `json:"description"`
		TargetDate  *string `json:"target_date"`
		Status      string  `json:"status"`
	}
	if err := readJSON(r, &req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "Invalid JSON body")
		return
	}
	if req.Name == "" {
		writeProblem(w, r, http.StatusBadRequest, "name is required")
		return
	}
	if req.Status == "" {
		req.Status = "active"
	}

	// Determine next position
	var maxPos int
	if err := h.db.QueryRowContext(r.Context(),
		`SELECT COALESCE(MAX(position), 0) FROM milestones WHERE project_id = ?`,
		projectID).Scan(&maxPos); err != nil {
		h.logger.Error("get max milestone position", "project_id", projectID, "error", err)
	}

	id := generateCUID()
	now := time.Now().UTC().Format(time.RFC3339)
	position := maxPos + 1

	_, err := h.db.ExecContext(r.Context(), `
		INSERT INTO milestones (id, project_id, name, description, target_date,
		    status, position, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, projectID, req.Name, req.Description, req.TargetDate,
		req.Status, position, now, now)
	if err != nil {
		h.logger.Error("insert milestone", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	resp := milestoneResponse{
		ID:          id,
		ProjectID:   projectID,
		Name:        req.Name,
		Description: req.Description,
		TargetDate:  req.TargetDate,
		Status:      req.Status,
		Position:    position,
		IssueCount:  0,
		DoneCount:   0,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	if h.hub != nil {
		h.hub.Broadcast("workspace:"+wsID, ws.ServerMessage{
			Type:    "milestone.created",
			Channel: "workspace:" + wsID,
			Payload: map[string]string{"id": id, "project_id": projectID},
		})
	}

	writeJSON(w, http.StatusCreated, resp)
}

// ── 3. Update — PATCH /api/v1/milestones/{milestoneId} ────────────────────

// Update modifies a milestone's name, description, target date, or status.
// PATCH /api/v1/projects/{projectId}/milestones/{milestoneId}
func (h *MilestoneHandler) Update(w http.ResponseWriter, r *http.Request) {
	role := RoleFromContext(r.Context())
	if !canRole(role, "create") {
		writeProblem(w, r, http.StatusForbidden, "Forbidden")
		return
	}

	milestoneID := r.PathValue("milestoneId")
	wsID := WorkspaceIDFromContext(r.Context())

	// Verify milestone exists and belongs to a project in this workspace
	var projectID string
	err := h.db.QueryRowContext(r.Context(), `
		SELECT m.project_id FROM milestones m
		JOIN projects p ON p.id = m.project_id
		WHERE m.id = ? AND p.workspace_id = ?`,
		milestoneID, wsID).Scan(&projectID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeProblem(w, r, http.StatusNotFound, "Milestone not found")
			return
		}
		h.logger.Error("get milestone for update", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	var req struct {
		Name        *string `json:"name"`
		Description *string `json:"description"`
		TargetDate  *string `json:"target_date"`
		Status      *string `json:"status"`
		Position    *int    `json:"position"`
	}
	if err := readJSON(r, &req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "Invalid JSON body")
		return
	}

	ub := newUpdate()

	if req.Name != nil {
		ub.Set("name", *req.Name)
	}
	if req.Description != nil {
		ub.Set("description", *req.Description)
	}
	if req.TargetDate != nil {
		ub.Set("target_date", *req.TargetDate)
	}
	if req.Status != nil {
		ub.Set("status", *req.Status)
	}
	if req.Position != nil {
		ub.Set("position", *req.Position)
	}

	if ub.Empty() {
		writeProblem(w, r, http.StatusBadRequest, "No fields to update")
		return
	}

	query, args := ub.Build("milestones", "id = ?", milestoneID)
	if _, err := h.db.ExecContext(r.Context(), query, args...); err != nil {
		h.logger.Error("update milestone", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	if h.hub != nil {
		h.hub.Broadcast("workspace:"+wsID, ws.ServerMessage{
			Type:    "milestone.updated",
			Channel: "workspace:" + wsID,
			Payload: map[string]string{"id": milestoneID, "project_id": projectID},
		})
	}

	// Return updated milestone
	var ms milestoneResponse
	err = h.db.QueryRowContext(r.Context(), `
		SELECT m.id, m.project_id, m.name, m.description, m.target_date,
		       m.status, m.position, m.created_at, m.updated_at,
		       (SELECT COUNT(*) FROM missions WHERE milestone_id = m.id AND mission_type = 'issue') AS issue_count,
		       (SELECT COUNT(*) FROM missions WHERE milestone_id = m.id AND mission_type = 'issue' AND status IN ('DONE','COMPLETED')) AS done_count
		FROM milestones m
		WHERE m.id = ?`, milestoneID).Scan(
		&ms.ID, &ms.ProjectID, &ms.Name, &ms.Description, &ms.TargetDate,
		&ms.Status, &ms.Position, &ms.CreatedAt, &ms.UpdatedAt,
		&ms.IssueCount, &ms.DoneCount,
	)
	if err != nil {
		h.logger.Error("read updated milestone", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	writeJSON(w, http.StatusOK, ms)
}

// ── 4. Delete — DELETE /api/v1/milestones/{milestoneId} ───────────────────

// Delete removes a milestone from a project.
// DELETE /api/v1/projects/{projectId}/milestones/{milestoneId}
func (h *MilestoneHandler) Delete(w http.ResponseWriter, r *http.Request) {
	role := RoleFromContext(r.Context())
	if !canRole(role, "manage") {
		writeProblem(w, r, http.StatusForbidden, "Forbidden")
		return
	}

	milestoneID := r.PathValue("milestoneId")
	wsID := WorkspaceIDFromContext(r.Context())

	// Verify milestone exists and belongs to a project in this workspace
	var projectID string
	err := h.db.QueryRowContext(r.Context(), `
		SELECT m.project_id FROM milestones m
		JOIN projects p ON p.id = m.project_id
		WHERE m.id = ? AND p.workspace_id = ?`,
		milestoneID, wsID).Scan(&projectID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeProblem(w, r, http.StatusNotFound, "Milestone not found")
			return
		}
		h.logger.Error("get milestone for delete", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		h.logger.Error("begin tx", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	defer tx.Rollback() //nolint:errcheck

	// Unlink missions from this milestone
	_, err = tx.ExecContext(r.Context(),
		`UPDATE missions SET milestone_id = NULL WHERE milestone_id = ?`, milestoneID)
	if err != nil {
		h.logger.Error("unlink missions from milestone", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	// Delete the milestone
	res, err := tx.ExecContext(r.Context(),
		`DELETE FROM milestones WHERE id = ?`, milestoneID)
	if err != nil {
		h.logger.Error("delete milestone", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	affected, err := res.RowsAffected()
	if err != nil {
		h.logger.Error("delete milestone rows affected", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	if affected == 0 {
		writeProblem(w, r, http.StatusNotFound, "Milestone not found")
		return
	}

	if err := tx.Commit(); err != nil {
		h.logger.Error("commit delete milestone", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	if h.hub != nil {
		h.hub.Broadcast("workspace:"+wsID, ws.ServerMessage{
			Type:    "milestone.deleted",
			Channel: "workspace:" + wsID,
			Payload: map[string]string{"id": milestoneID, "project_id": projectID},
		})
	}

	w.WriteHeader(http.StatusNoContent)
}
