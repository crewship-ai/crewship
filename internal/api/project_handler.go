package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/ws"
)

// projectLeadInWorkspaceOrReject validates the polymorphic lead_type/lead_id
// pair against the caller's workspace, writing the error response and returning
// false when it does not hold.
//
// Polymorphic references cannot carry a foreign key — lead_id points into
// `users` or into `agents` depending on lead_type — so the database cannot
// catch this and the application is the only guard. That is the same root cause
// the audit recorded for issues' assignee_type/assignee_id, where the missing
// guard turned up in seven separate write paths.
//
// A lead_id with no resolvable lead_type is rejected rather than waved through:
// an unvalidatable reference is the state this whole class of bug lives in.
func projectLeadInWorkspaceOrReject(w http.ResponseWriter, r *http.Request, db *sql.DB, logger *slog.Logger,
	leadType, leadID *string, wsID string) bool {
	// The enum is checked first and independently of lead_id, so a request that
	// moves only lead_type cannot store a value neither read-path branch
	// resolves. Empty means "clearing", which is allowed.
	var table string
	if leadType != nil && *leadType != "" {
		switch *leadType {
		case "user":
			table = "users"
		case "agent":
			table = "agents"
		default:
			writeProblem(w, r, http.StatusBadRequest, "lead_type must be 'user' or 'agent'")
			return false
		}
	}
	if leadID == nil || *leadID == "" {
		return true // no reference to resolve
	}
	if table == "" {
		writeProblem(w, r, http.StatusBadRequest, "lead_type is required when lead_id is set")
		return false
	}
	return fkInWorkspaceOrReject(w, r, db, logger, table, "lead_id", *leadID, wsID)
}

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

// List returns all projects in the workspace with issue counts and milestone stats.
// GET /api/v1/projects
func (h *ProjectHandler) List(w http.ResponseWriter, r *http.Request) {
	wsID := WorkspaceIDFromContext(r.Context())

	query := `
		SELECT p.id, p.workspace_id, p.name, p.slug,
		       p.description, p.icon, p.color, p.status, p.priority, p.health,
		       p.lead_type, p.lead_id,
		       COALESCE(u.full_name, ag.name),
		       p.start_date, p.target_date, p.created_at, p.updated_at,
		       COALESCE(ic.issue_count, 0),
		       COALESCE(ic.done_count, 0)
		FROM projects p
		LEFT JOIN users u ON p.lead_type = 'user' AND u.id = p.lead_id
		LEFT JOIN agents ag ON p.lead_type = 'agent' AND ag.id = p.lead_id
		LEFT JOIN (
		    SELECT project_id,
		           COUNT(*) AS issue_count,
		           SUM(CASE WHEN status IN ('DONE','COMPLETED','REVIEW') THEN 1 ELSE 0 END) AS done_count
		    FROM missions WHERE mission_type = 'issue' GROUP BY project_id
		) ic ON ic.project_id = p.id
		WHERE p.workspace_id = ?`
	args := []interface{}{wsID}

	// Status filter
	if statusParam := r.URL.Query().Get("status"); statusParam != "" {
		statuses := strings.Split(statusParam, ",")
		for _, s := range statuses {
			args = append(args, strings.TrimSpace(s))
		}
		query += " AND p.status IN (" + sqlPlaceholders(len(statuses)) + ")"
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
		internalError(w, r, h.logger, "list projects", err)
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
			internalError(w, r, h.logger, "scan project", err)
			return
		}
		if p.IssueCount > 0 {
			p.Progress = p.DoneCount * 100 / p.IssueCount
		}
		result = append(result, p)
	}
	if err := rows.Err(); err != nil {
		internalError(w, r, h.logger, "rows iteration (projects)", err)
		return
	}

	if result == nil {
		result = []projectResponse{}
	}
	writeJSON(w, http.StatusOK, result)
}

// Create provisions a new project in the workspace with the given name, slug, and metadata.
// POST /api/v1/projects
func (h *ProjectHandler) Create(w http.ResponseWriter, r *http.Request) {
	if !requireRole(w, r, "create") {
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

	// lead_type/lead_id is a polymorphic reference — the same shape as
	// assignee_type/assignee_id on issues, which #1471 had to fix seven times.
	// It was unvalidated here, so a project could name a lead from another
	// tenant and the project read path would resolve that person's name.
	if !projectLeadInWorkspaceOrReject(w, r, h.db, h.logger, req.LeadType, req.LeadID, wsID) {
		return
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
		// A duplicate (workspace_id, slug) is the caller's conflict, not our
		// internal error, and the demo seed depends on the difference: it
		// treats 409 as "already present" and keeps going, so a 500 here made
		// re-seeding an already-seeded install die on the first project.
		if strings.Contains(err.Error(), "UNIQUE constraint failed") {
			writeProblem(w, r, http.StatusConflict,
				fmt.Sprintf("A project with the slug %q already exists in this workspace", slug))
			return
		}
		internalError(w, r, h.logger, "insert project", err)
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

	broadcastWorkspaceEvent(h.hub, wsID, "project.created", map[string]string{"id": id})

	writeJSON(w, http.StatusCreated, resp)
}

// getProjectByID loads a single project (with computed issue/done/progress
// counts) scoped to the workspace. It is the shared read used by both Get and
// the trailing re-read in Update, so the multi-column SELECT + Scan lives in
// exactly one place. Returns sql.ErrNoRows when the project does not exist in
// the workspace; callers map that to 404 and any other error to 500.
func getProjectByID(ctx context.Context, db *sql.DB, projectID, wsID string) (projectResponse, error) {
	var p projectResponse
	err := db.QueryRowContext(ctx, `
		SELECT p.id, p.workspace_id, p.name, p.slug,
		       p.description, p.icon, p.color, p.status, p.priority, p.health,
		       p.lead_type, p.lead_id,
		       CASE
		         WHEN p.lead_type = 'user' THEN (SELECT full_name FROM users WHERE id = p.lead_id)
		         WHEN p.lead_type = 'agent' THEN (SELECT name FROM agents WHERE id = p.lead_id)
		       END,
		       p.start_date, p.target_date, p.created_at, p.updated_at,
		       (SELECT COUNT(*) FROM missions WHERE project_id = p.id AND mission_type = 'issue') AS issue_count,
		       (SELECT COUNT(*) FROM missions WHERE project_id = p.id AND mission_type = 'issue' AND status IN ('DONE','COMPLETED','REVIEW')) AS done_count
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
		return projectResponse{}, err
	}
	if p.IssueCount > 0 {
		p.Progress = p.DoneCount * 100 / p.IssueCount
	}
	return p, nil
}

// Get returns a single project by ID with full details.
// GET /api/v1/projects/{projectId}
func (h *ProjectHandler) Get(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectId")
	wsID := WorkspaceIDFromContext(r.Context())

	p, err := getProjectByID(r.Context(), h.db, projectID, wsID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeProblem(w, r, http.StatusNotFound, "Project not found")
			return
		}
		internalError(w, r, h.logger, "get project", err)
		return
	}

	writeJSON(w, http.StatusOK, p)
}

// Update modifies project properties such as name, description, status, and priority.
// PATCH /api/v1/projects/{projectId}
func (h *ProjectHandler) Update(w http.ResponseWriter, r *http.Request) {
	if !requireRole(w, r, "create") {
		return
	}

	projectID := r.PathValue("projectId")
	wsID := WorkspaceIDFromContext(r.Context())

	// Verify project exists
	found, err := projectExists(r.Context(), h.db, projectID, wsID)
	if err != nil {
		internalError(w, r, h.logger, "get project for update", err)
		return
	}
	if !found {
		writeProblem(w, r, http.StatusNotFound, "Project not found")
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
	// lead_type and lead_id are one polymorphic reference, so the pair is
	// validated whenever EITHER half moves — validating only on lead_id would
	// let `{"lead_type":"agent"}` alone re-point a stored user id at the agents
	// table (or set an arbitrary string that neither read-path branch resolves),
	// leaving the pair desynced without ever failing a check.
	//
	// Whichever half the request omits is read back from the stored row, so the
	// check always runs against the pair the row will actually hold. Validating
	// a moved id against the wrong table would be worse than not validating: it
	// would 400 legitimate edits.
	if req.LeadType != nil || req.LeadID != nil {
		leadType, leadID := req.LeadType, req.LeadID
		if leadType == nil || leadID == nil {
			var storedType, storedID sql.NullString
			if err := h.db.QueryRowContext(r.Context(),
				`SELECT lead_type, lead_id FROM projects WHERE id = ? AND workspace_id = ?`,
				projectID, wsID).Scan(&storedType, &storedID); err != nil && !errors.Is(err, sql.ErrNoRows) {
				internalError(w, r, h.logger, "read project lead pair", err)
				return
			}
			if leadType == nil && storedType.Valid {
				leadType = &storedType.String
			}
			if leadID == nil && storedID.Valid {
				leadID = &storedID.String
			}
		}
		if !projectLeadInWorkspaceOrReject(w, r, h.db, h.logger, leadType, leadID, wsID) {
			return
		}
		if req.LeadType != nil {
			ub.Set("lead_type", *req.LeadType)
		}
		if req.LeadID != nil {
			ub.Set("lead_id", *req.LeadID)
		}
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
		internalError(w, r, h.logger, "update project", err)
		return
	}

	broadcastWorkspaceEvent(h.hub, wsID, "project.updated", map[string]string{"id": projectID})

	// Return updated project
	p, err := getProjectByID(r.Context(), h.db, projectID, wsID)
	if err != nil {
		internalError(w, r, h.logger, "read updated project", err)
		return
	}

	writeJSON(w, http.StatusOK, p)
}

// Delete removes a project and unlinks all its associated issues.
// DELETE /api/v1/projects/{projectId}
func (h *ProjectHandler) Delete(w http.ResponseWriter, r *http.Request) {
	if !requireRole(w, r, "manage") {
		return
	}

	projectID := r.PathValue("projectId")
	wsID := WorkspaceIDFromContext(r.Context())

	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		internalError(w, r, h.logger, "begin tx", err)
		return
	}
	defer tx.Rollback() //nolint:errcheck

	// Unlink missions from this project
	_, err = tx.ExecContext(r.Context(),
		`UPDATE missions SET project_id = NULL WHERE project_id = ?`, projectID)
	if err != nil {
		internalError(w, r, h.logger, "unlink missions from project", err)
		return
	}

	// Delete the project
	res, err := tx.ExecContext(r.Context(),
		`DELETE FROM projects WHERE id = ? AND workspace_id = ?`, projectID, wsID)
	if err != nil {
		internalError(w, r, h.logger, "delete project", err)
		return
	}
	affected, err := res.RowsAffected()
	if err != nil {
		internalError(w, r, h.logger, "delete project rows affected", err)
		return
	}
	if affected == 0 {
		writeProblem(w, r, http.StatusNotFound, "Project not found")
		return
	}

	if err := tx.Commit(); err != nil {
		internalError(w, r, h.logger, "commit delete project", err)
		return
	}

	broadcastWorkspaceEvent(h.hub, wsID, "project.deleted", map[string]string{"id": projectID})

	w.WriteHeader(http.StatusNoContent)
}

// Stats returns project breakdown data for the detail panel.
func (h *ProjectHandler) Stats(w http.ResponseWriter, r *http.Request) {
	projectID := r.PathValue("projectId")
	wsID := WorkspaceIDFromContext(r.Context())

	// Verify project exists
	found, err := projectExists(r.Context(), h.db, projectID, wsID)
	if err != nil {
		internalError(w, r, h.logger, "project exists check", err)
		return
	}
	if !found {
		writeProblem(w, r, http.StatusNotFound, "Project not found")
		return
	}

	type assigneeStat struct {
		AgentID   string `json:"agent_id"`
		AgentName string `json:"agent_name"`
		Total     int    `json:"total"`
		Completed int    `json:"completed"`
	}
	type labelStat struct {
		LabelName string `json:"label_name"`
		Color     string `json:"color"`
		Count     int    `json:"count"`
	}
	type statsResponse struct {
		TotalIssues     int            `json:"total_issues"`
		CompletedIssues int            `json:"completed_issues"`
		ByStatus        map[string]int `json:"by_status"`
		ByAssignee      []assigneeStat `json:"by_assignee"`
		ByLabel         []labelStat    `json:"by_label"`
		Crews           []string       `json:"crews"`
	}

	var resp statsResponse
	resp.ByStatus = map[string]int{}
	resp.ByAssignee = []assigneeStat{}
	resp.ByLabel = []labelStat{}
	resp.Crews = []string{}

	// Total + completed in one query.
	// COALESCE needed because SUM returns NULL for projects with no issues.
	if err := h.db.QueryRowContext(r.Context(), `
		SELECT COUNT(*),
		       COALESCE(SUM(CASE WHEN status IN ('DONE','COMPLETED','REVIEW') THEN 1 ELSE 0 END), 0)
		FROM missions WHERE project_id = ? AND mission_type = 'issue'`,
		projectID).Scan(&resp.TotalIssues, &resp.CompletedIssues); err != nil {
		h.logger.Error("load project stats counts", "project_id", projectID, "error", err)
	}

	// By status
	statusRows, err := h.db.QueryContext(r.Context(),
		`SELECT status, COUNT(*) FROM missions WHERE project_id = ? AND mission_type = 'issue' GROUP BY status`, projectID)
	if err == nil {
		defer statusRows.Close()
		for statusRows.Next() {
			var s string
			var c int
			if statusRows.Scan(&s, &c) == nil {
				resp.ByStatus[s] = c
			}
		}
	}

	// By assignee
	assigneeRows, err := h.db.QueryContext(r.Context(), `
		SELECT m.assignee_id, COALESCE(a.name, 'Unassigned'),
		       COUNT(*),
		       SUM(CASE WHEN m.status IN ('DONE','COMPLETED','REVIEW') THEN 1 ELSE 0 END)
		FROM missions m
		LEFT JOIN agents a ON m.assignee_id = a.id
		WHERE m.project_id = ? AND m.mission_type = 'issue'
		GROUP BY m.assignee_id`, projectID)
	if err == nil {
		defer assigneeRows.Close()
		for assigneeRows.Next() {
			var as assigneeStat
			var aid sql.NullString
			if assigneeRows.Scan(&aid, &as.AgentName, &as.Total, &as.Completed) == nil {
				as.AgentID = aid.String
				resp.ByAssignee = append(resp.ByAssignee, as)
			}
		}
	}

	// By label
	labelRows, err := h.db.QueryContext(r.Context(), `
		SELECT l.name, l.color, COUNT(DISTINCT m.id)
		FROM missions m
		JOIN mission_labels ml ON ml.mission_id = m.id
		JOIN labels l ON l.id = ml.label_id
		WHERE m.project_id = ? AND m.mission_type = 'issue'
		GROUP BY l.name, l.color
		ORDER BY COUNT(DISTINCT m.id) DESC`, projectID)
	if err == nil {
		defer labelRows.Close()
		for labelRows.Next() {
			var ls labelStat
			if labelRows.Scan(&ls.LabelName, &ls.Color, &ls.Count) == nil {
				resp.ByLabel = append(resp.ByLabel, ls)
			}
		}
	}

	// Crews
	crewRows, err := h.db.QueryContext(r.Context(), `
		SELECT DISTINCT c.slug FROM missions m
		JOIN crews c ON m.crew_id = c.id
		WHERE m.project_id = ? AND m.mission_type = 'issue'`, projectID)
	if err == nil {
		defer crewRows.Close()
		for crewRows.Next() {
			var slug string
			if crewRows.Scan(&slug) == nil {
				resp.Crews = append(resp.Crews, slug)
			}
		}
	}

	writeJSON(w, http.StatusOK, resp)
}
