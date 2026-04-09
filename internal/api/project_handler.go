package api

import (
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/ws"
)

// ProjectHandler implements CRUD endpoints for projects.
type ProjectHandler struct {
	db     *sql.DB
	hub    *ws.Hub
	logger *slog.Logger
}

// NewProjectHandler creates a new ProjectHandler.
func NewProjectHandler(db *sql.DB, hub *ws.Hub, logger *slog.Logger) *ProjectHandler {
	return &ProjectHandler{db: db, hub: hub, logger: logger}
}

// ── Response type ──────────────────────────────────────────────────────────

type projectResponse struct {
	ID          string  `json:"id"`
	WorkspaceID string  `json:"workspace_id"`
	Name        string  `json:"name"`
	Slug        string  `json:"slug"`
	Description *string `json:"description"`
	Icon        *string `json:"icon"`
	Color       string  `json:"color"`
	Status      string  `json:"status"`
	Priority    string  `json:"priority"`
	Health      string  `json:"health"`
	LeadType    *string `json:"lead_type"`
	LeadID      *string `json:"lead_id"`
	LeadName    *string `json:"lead_name,omitempty"`
	StartDate   *string `json:"start_date"`
	TargetDate  *string `json:"target_date"`
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`
	// Computed
	IssueCount int `json:"issue_count"`
	DoneCount  int `json:"done_count"`
	Progress   int `json:"progress"`
}

// ── 1. List — GET /api/v1/projects ─────────────────────────────────────────

func (h *ProjectHandler) List(w http.ResponseWriter, r *http.Request) {
	wsID := WorkspaceIDFromContext(r.Context())

	query := `
		SELECT p.id, p.workspace_id, p.name, p.slug,
		       p.description, p.icon, p.color, p.status, p.priority, p.health,
		       p.lead_type, p.lead_id,
		       CASE
		         WHEN p.lead_type = 'user' THEN (SELECT full_name FROM users WHERE id = p.lead_id)
		         WHEN p.lead_type = 'agent' THEN (SELECT name FROM agents WHERE id = p.lead_id)
		       END,
		       p.start_date, p.target_date, p.created_at, p.updated_at,
		       (SELECT COUNT(*) FROM missions WHERE project_id = p.id AND mission_type = 'issue') AS issue_count,
		       (SELECT COUNT(*) FROM missions WHERE project_id = p.id AND mission_type = 'issue' AND status IN ('DONE','COMPLETED')) AS done_count
		FROM projects p
		WHERE p.workspace_id = ?`
	args := []interface{}{wsID}

	// Status filter
	if statusParam := r.URL.Query().Get("status"); statusParam != "" {
		statuses := strings.Split(statusParam, ",")
		placeholders := make([]string, len(statuses))
		for i, s := range statuses {
			placeholders[i] = "?"
			args = append(args, strings.TrimSpace(s))
		}
		query += " AND p.status IN (" + strings.Join(placeholders, ",") + ")"
	}

	// Sort
	sortCol := "p.name"
	switch r.URL.Query().Get("sort") {
	case "created_at":
		sortCol = "p.created_at"
	case "updated_at":
		sortCol = "p.updated_at"
	}
	query += " ORDER BY " + sortCol + " ASC"

	rows, err := h.db.QueryContext(r.Context(), query, args...)
	if err != nil {
		h.logger.Error("list projects", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	defer rows.Close()

	var result []projectResponse
	for rows.Next() {
		var p projectResponse
		if err := rows.Scan(
			&p.ID, &p.WorkspaceID, &p.Name, &p.Slug,
			&p.Description, &p.Icon, &p.Color, &p.Status, &p.Priority, &p.Health,
			&p.LeadType, &p.LeadID, &p.LeadName,
			&p.StartDate, &p.TargetDate, &p.CreatedAt, &p.UpdatedAt,
			&p.IssueCount, &p.DoneCount,
		); err != nil {
			h.logger.Error("scan project", "error", err)
			writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
			return
		}
		if p.IssueCount > 0 {
			p.Progress = p.DoneCount * 100 / p.IssueCount
		}
		result = append(result, p)
	}
	if err := rows.Err(); err != nil {
		h.logger.Error("rows iteration (projects)", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	if result == nil {
		result = []projectResponse{}
	}
	writeJSON(w, http.StatusOK, result)
}

// ── 2. Create — POST /api/v1/projects ──────────────────────────────────────

func (h *ProjectHandler) Create(w http.ResponseWriter, r *http.Request) {
	role := RoleFromContext(r.Context())
	if !canRole(role, "create") {
		writeProblem(w, r, http.StatusForbidden, "Forbidden")
		return
	}

	wsID := WorkspaceIDFromContext(r.Context())

	var req struct {
		Name        string  `json:"name"`
		Description *string `json:"description"`
		Icon        *string `json:"icon"`
		Color       string  `json:"color"`
		Status      string  `json:"status"`
		Priority    string  `json:"priority"`
		LeadType    *string `json:"lead_type"`
		LeadID      *string `json:"lead_id"`
		StartDate   *string `json:"start_date"`
		TargetDate  *string `json:"target_date"`
	}
	if err := readJSON(r, &req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "Invalid JSON body")
		return
	}
	if req.Name == "" {
		writeProblem(w, r, http.StatusBadRequest, "name is required")
		return
	}
	if req.Color == "" {
		req.Color = "blue"
	}
	if req.Status == "" {
		req.Status = "backlog"
	}
	if req.Priority == "" {
		req.Priority = "none"
	}

	id := generateCUID()
	slug := slugify(req.Name)
	now := time.Now().UTC().Format(time.RFC3339)

	_, err := h.db.ExecContext(r.Context(), `
		INSERT INTO projects (id, workspace_id, name, slug, description, icon, color,
		    status, priority, health, lead_type, lead_id, start_date, target_date,
		    created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'on_track', ?, ?, ?, ?, ?, ?)`,
		id, wsID, req.Name, slug, req.Description, req.Icon, req.Color,
		req.Status, req.Priority, req.LeadType, req.LeadID,
		req.StartDate, req.TargetDate, now, now)
	if err != nil {
		h.logger.Error("insert project", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	resp := projectResponse{
		ID:          id,
		WorkspaceID: wsID,
		Name:        req.Name,
		Slug:        slug,
		Description: req.Description,
		Icon:        req.Icon,
		Color:       req.Color,
		Status:      req.Status,
		Priority:    req.Priority,
		Health:      "on_track",
		LeadType:    req.LeadType,
		LeadID:      req.LeadID,
		StartDate:   req.StartDate,
		TargetDate:  req.TargetDate,
		CreatedAt:   now,
		UpdatedAt:   now,
		IssueCount:  0,
		DoneCount:   0,
		Progress:    0,
	}

	if h.hub != nil {
		h.hub.Broadcast("workspace:"+wsID, ws.ServerMessage{
			Type:    "project.created",
			Channel: "workspace:" + wsID,
			Payload: map[string]string{"id": id},
		})
	}

	writeJSON(w, http.StatusCreated, resp)
}

// ── 3. Get — GET /api/v1/projects/{projectId} ─────────────────────────────

func (h *ProjectHandler) Get(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectId")
	wsID := WorkspaceIDFromContext(r.Context())

	var p projectResponse
	err := h.db.QueryRowContext(r.Context(), `
		SELECT p.id, p.workspace_id, p.name, p.slug,
		       p.description, p.icon, p.color, p.status, p.priority, p.health,
		       p.lead_type, p.lead_id,
		       CASE
		         WHEN p.lead_type = 'user' THEN (SELECT full_name FROM users WHERE id = p.lead_id)
		         WHEN p.lead_type = 'agent' THEN (SELECT name FROM agents WHERE id = p.lead_id)
		       END,
		       p.start_date, p.target_date, p.created_at, p.updated_at,
		       (SELECT COUNT(*) FROM missions WHERE project_id = p.id AND mission_type = 'issue') AS issue_count,
		       (SELECT COUNT(*) FROM missions WHERE project_id = p.id AND mission_type = 'issue' AND status IN ('DONE','COMPLETED')) AS done_count
		FROM projects p
		WHERE p.id = ? AND p.workspace_id = ?`,
		projectID, wsID).Scan(
		&p.ID, &p.WorkspaceID, &p.Name, &p.Slug,
		&p.Description, &p.Icon, &p.Color, &p.Status, &p.Priority, &p.Health,
		&p.LeadType, &p.LeadID, &p.LeadName,
		&p.StartDate, &p.TargetDate, &p.CreatedAt, &p.UpdatedAt,
		&p.IssueCount, &p.DoneCount,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeProblem(w, r, http.StatusNotFound, "Project not found")
			return
		}
		h.logger.Error("get project", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	if p.IssueCount > 0 {
		p.Progress = p.DoneCount * 100 / p.IssueCount
	}

	writeJSON(w, http.StatusOK, p)
}

// ── 4. Update — PATCH /api/v1/projects/{projectId} ────────────────────────

func (h *ProjectHandler) Update(w http.ResponseWriter, r *http.Request) {
	role := RoleFromContext(r.Context())
	if !canRole(role, "create") {
		writeProblem(w, r, http.StatusForbidden, "Forbidden")
		return
	}

	projectID := r.PathValue("projectId")
	wsID := WorkspaceIDFromContext(r.Context())

	// Verify project exists
	var existingID string
	err := h.db.QueryRowContext(r.Context(),
		`SELECT id FROM projects WHERE id = ? AND workspace_id = ?`,
		projectID, wsID).Scan(&existingID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeProblem(w, r, http.StatusNotFound, "Project not found")
			return
		}
		h.logger.Error("get project for update", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	var req struct {
		Name        *string `json:"name"`
		Description *string `json:"description"`
		Icon        *string `json:"icon"`
		Color       *string `json:"color"`
		Status      *string `json:"status"`
		Priority    *string `json:"priority"`
		Health      *string `json:"health"`
		LeadType    *string `json:"lead_type"`
		LeadID      *string `json:"lead_id"`
		StartDate   *string `json:"start_date"`
		TargetDate  *string `json:"target_date"`
	}
	if err := readJSON(r, &req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "Invalid JSON body")
		return
	}

	ub := newUpdate()

	if req.Name != nil {
		ub.Set("name", *req.Name)
		ub.Set("slug", slugify(*req.Name))
	}
	if req.Description != nil {
		ub.Set("description", *req.Description)
	}
	if req.Icon != nil {
		ub.Set("icon", *req.Icon)
	}
	if req.Color != nil {
		ub.Set("color", *req.Color)
	}
	if req.Status != nil {
		ub.Set("status", *req.Status)
	}
	if req.Priority != nil {
		ub.Set("priority", *req.Priority)
	}
	if req.Health != nil {
		ub.Set("health", *req.Health)
	}
	if req.LeadType != nil {
		ub.Set("lead_type", *req.LeadType)
	}
	if req.LeadID != nil {
		ub.Set("lead_id", *req.LeadID)
	}
	if req.StartDate != nil {
		ub.Set("start_date", *req.StartDate)
	}
	if req.TargetDate != nil {
		ub.Set("target_date", *req.TargetDate)
	}

	if ub.Empty() {
		writeProblem(w, r, http.StatusBadRequest, "No fields to update")
		return
	}

	query, args := ub.Build("projects", "id = ? AND workspace_id = ?", projectID, wsID)
	if _, err := h.db.ExecContext(r.Context(), query, args...); err != nil {
		h.logger.Error("update project", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	if h.hub != nil {
		h.hub.Broadcast("workspace:"+wsID, ws.ServerMessage{
			Type:    "project.updated",
			Channel: "workspace:" + wsID,
			Payload: map[string]string{"id": projectID},
		})
	}

	// Return updated project
	var p projectResponse
	err = h.db.QueryRowContext(r.Context(), `
		SELECT p.id, p.workspace_id, p.name, p.slug,
		       p.description, p.icon, p.color, p.status, p.priority, p.health,
		       p.lead_type, p.lead_id,
		       CASE
		         WHEN p.lead_type = 'user' THEN (SELECT full_name FROM users WHERE id = p.lead_id)
		         WHEN p.lead_type = 'agent' THEN (SELECT name FROM agents WHERE id = p.lead_id)
		       END,
		       p.start_date, p.target_date, p.created_at, p.updated_at,
		       (SELECT COUNT(*) FROM missions WHERE project_id = p.id AND mission_type = 'issue') AS issue_count,
		       (SELECT COUNT(*) FROM missions WHERE project_id = p.id AND mission_type = 'issue' AND status IN ('DONE','COMPLETED')) AS done_count
		FROM projects p
		WHERE p.id = ?`, projectID).Scan(
		&p.ID, &p.WorkspaceID, &p.Name, &p.Slug,
		&p.Description, &p.Icon, &p.Color, &p.Status, &p.Priority, &p.Health,
		&p.LeadType, &p.LeadID, &p.LeadName,
		&p.StartDate, &p.TargetDate, &p.CreatedAt, &p.UpdatedAt,
		&p.IssueCount, &p.DoneCount,
	)
	if err != nil {
		h.logger.Error("read updated project", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	if p.IssueCount > 0 {
		p.Progress = p.DoneCount * 100 / p.IssueCount
	}

	writeJSON(w, http.StatusOK, p)
}

// ── 5. Delete — DELETE /api/v1/projects/{projectId} ────────────────────────

func (h *ProjectHandler) Delete(w http.ResponseWriter, r *http.Request) {
	role := RoleFromContext(r.Context())
	if !canRole(role, "manage") {
		writeProblem(w, r, http.StatusForbidden, "Forbidden")
		return
	}

	projectID := r.PathValue("projectId")
	wsID := WorkspaceIDFromContext(r.Context())

	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		h.logger.Error("begin tx", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	defer tx.Rollback() //nolint:errcheck

	// Unlink missions from this project
	_, err = tx.ExecContext(r.Context(),
		`UPDATE missions SET project_id = NULL WHERE project_id = ?`, projectID)
	if err != nil {
		h.logger.Error("unlink missions from project", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	// Delete the project
	res, err := tx.ExecContext(r.Context(),
		`DELETE FROM projects WHERE id = ? AND workspace_id = ?`, projectID, wsID)
	if err != nil {
		h.logger.Error("delete project", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	affected, err := res.RowsAffected()
	if err != nil {
		h.logger.Error("delete project rows affected", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	if affected == 0 {
		writeProblem(w, r, http.StatusNotFound, "Project not found")
		return
	}

	if err := tx.Commit(); err != nil {
		h.logger.Error("commit delete project", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	if h.hub != nil {
		h.hub.Broadcast("workspace:"+wsID, ws.ServerMessage{
			Type:    "project.deleted",
			Channel: "workspace:" + wsID,
			Payload: map[string]string{"id": projectID},
		})
	}

	w.WriteHeader(http.StatusNoContent)
}
