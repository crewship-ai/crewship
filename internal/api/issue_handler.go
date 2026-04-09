package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/ws"
)

// IssueHandler implements endpoints for the issue tracker (Linear-like).
// Uses MissionStarter interface defined in captain.go.
type IssueHandler struct {
	db            *sql.DB
	hub           *ws.Hub
	missionEngine MissionStarter
	logger        *slog.Logger
}

// NewIssueHandler creates a new IssueHandler.
func NewIssueHandler(db *sql.DB, hub *ws.Hub, me MissionStarter, logger *slog.Logger) *IssueHandler {
	return &IssueHandler{db: db, hub: hub, missionEngine: me, logger: logger}
}

// issueSelectColumns is the shared SELECT clause for fetching issues/missions.
const issueSelectColumns = `
	SELECT m.id, m.workspace_id, m.crew_id, COALESCE(c.name, ''), COALESCE(c.slug, ''),
	       m.number, m.identifier, m.title, m.description, m.status,
	       COALESCE(m.priority, 'none'), m.assignee_type, m.assignee_id,
	       CASE
	         WHEN m.assignee_type = 'user' THEN (SELECT full_name FROM users WHERE id = m.assignee_id)
	         WHEN m.assignee_type = 'agent' THEN (SELECT name FROM agents WHERE id = m.assignee_id)
	       END,
	       m.due_date, COALESCE(m.sort_order, 0), COALESCE(m.mission_type, 'mission'),
	       m.lead_agent_id, m.created_at, m.updated_at, m.completed_at,
	       m.project_id, m.estimate, m.parent_issue_id, m.milestone_id,
	       (SELECT COUNT(*) FROM missions sub WHERE sub.parent_issue_id = m.id) AS sub_issues_count
	FROM missions m
	LEFT JOIN crews c ON m.crew_id = c.id`

// scanIssue scans a row into an issueResponse using the standard column order.
func scanIssue(s interface{ Scan(...interface{}) error }) (issueResponse, error) {
	var issue issueResponse
	err := s.Scan(
		&issue.ID, &issue.WorkspaceID, &issue.CrewID, &issue.CrewName, &issue.CrewSlug,
		&issue.Number, &issue.Identifier, &issue.Title, &issue.Description, &issue.Status,
		&issue.Priority, &issue.AssigneeType, &issue.AssigneeID, &issue.AssigneeName,
		&issue.DueDate, &issue.SortOrder, &issue.MissionType,
		&issue.LeadAgentID, &issue.CreatedAt, &issue.UpdatedAt, &issue.CompletedAt,
		&issue.ProjectID, &issue.Estimate, &issue.ParentIssueID, &issue.MilestoneID,
		&issue.SubIssuesCount,
	)
	if err == nil {
		issue.Labels = []labelResponse{}
	}
	return issue, err
}

// ── Response types ──────────────────────────────────────────────────────────

type issueResponse struct {
	ID           string          `json:"id"`
	WorkspaceID  string          `json:"workspace_id"`
	CrewID       string          `json:"crew_id"`
	CrewName     string          `json:"crew_name,omitempty"`
	CrewSlug     string          `json:"crew_slug,omitempty"`
	Number       *int            `json:"number"`
	Identifier   *string         `json:"identifier"`
	Title        string          `json:"title"`
	Description  *string         `json:"description"`
	Status       string          `json:"status"`
	Priority     string          `json:"priority"`
	AssigneeType *string         `json:"assignee_type"`
	AssigneeID   *string         `json:"assignee_id"`
	AssigneeName *string         `json:"assignee_name,omitempty"`
	DueDate      *string         `json:"due_date"`
	SortOrder    float64         `json:"sort_order"`
	MissionType  string          `json:"mission_type"`
	LeadAgentID  string          `json:"lead_agent_id"`
	CreatedAt    string          `json:"created_at"`
	UpdatedAt    string          `json:"updated_at"`
	CompletedAt  *string         `json:"completed_at"`
	Labels         []labelResponse `json:"labels"`
	ProjectID      *string         `json:"project_id"`
	ProjectName    *string         `json:"project_name,omitempty"`
	Estimate       *int            `json:"estimate"`
	ParentIssueID  *string         `json:"parent_issue_id"`
	MilestoneID    *string         `json:"milestone_id"`
	SubIssuesCount int             `json:"sub_issues_count"`
	CommentCount   int             `json:"comment_count"`
}

type labelResponse struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	Color      string  `json:"color"`
	LabelGroup *string `json:"label_group"`
}

type relationResponse struct {
	ID           string  `json:"id"`
	SourceID     string  `json:"source_id"`
	TargetID     string  `json:"target_id"`
	RelationType string  `json:"relation_type"`
	// Resolved target info
	TargetIdentifier *string `json:"target_identifier,omitempty"`
	TargetTitle      string  `json:"target_title,omitempty"`
	TargetStatus     string  `json:"target_status,omitempty"`
	CreatedAt        string  `json:"created_at"`
}

type commentResponse struct {
	ID         string `json:"id"`
	MissionID  string `json:"mission_id"`
	AuthorType string `json:"author_type"`
	AuthorID   string `json:"author_id"`
	AuthorName string `json:"author_name,omitempty"`
	Body       string `json:"body"`
	CreatedAt  string `json:"created_at"`
	UpdatedAt  string `json:"updated_at"`
}

// validIssueTransitions defines allowed status transitions for issues.
// Issue statuses are a superset of the existing mission statuses.
var validIssueTransitions = map[string][]string{
	"BACKLOG":     {"TODO", "IN_PROGRESS", "CANCELLED"},
	"TODO":        {"IN_PROGRESS", "BACKLOG", "CANCELLED"},
	"IN_PROGRESS": {"REVIEW", "DONE", "FAILED", "CANCELLED", "TODO"},
	"REVIEW":      {"DONE", "TODO", "IN_PROGRESS", "FAILED", "CANCELLED"},
	"DONE":        {"BACKLOG"},
	"FAILED":      {"BACKLOG", "TODO", "IN_PROGRESS"},
	"CANCELLED":   {"BACKLOG", "TODO"},
	"DUPLICATE":   {},
}

// ── 1. List — GET /api/v1/issues ────────────────────────────────────────────

func (h *IssueHandler) List(w http.ResponseWriter, r *http.Request) {
	wsID := WorkspaceIDFromContext(r.Context())

	// Pagination
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}

	query := issueSelectColumns + `
		WHERE m.workspace_id = ?`
	args := []interface{}{wsID}

	// Default filter: only issues unless explicitly overridden
	missionType := r.URL.Query().Get("mission_type")
	if missionType == "" {
		missionType = "issue"
	}
	query += " AND COALESCE(m.mission_type, 'mission') = ?"
	args = append(args, missionType)

	// Status filter (comma-separated)
	if statusParam := r.URL.Query().Get("status"); statusParam != "" {
		statuses := strings.Split(statusParam, ",")
		placeholders := make([]string, len(statuses))
		for i, s := range statuses {
			placeholders[i] = "?"
			args = append(args, strings.TrimSpace(s))
		}
		query += " AND m.status IN (" + strings.Join(placeholders, ",") + ")"
	}

	// Priority filter (comma-separated)
	if priorityParam := r.URL.Query().Get("priority"); priorityParam != "" {
		priorities := strings.Split(priorityParam, ",")
		placeholders := make([]string, len(priorities))
		for i, p := range priorities {
			placeholders[i] = "?"
			args = append(args, strings.TrimSpace(p))
		}
		query += " AND m.priority IN (" + strings.Join(placeholders, ",") + ")"
	}

	// Project filter
	if projectID := r.URL.Query().Get("project_id"); projectID != "" {
		query += " AND m.project_id = ?"
		args = append(args, projectID)
	}

	// Crew filter
	if crewID := r.URL.Query().Get("crew_id"); crewID != "" {
		query += " AND m.crew_id = ?"
		args = append(args, crewID)
	}

	// Assignee filter
	if assigneeID := r.URL.Query().Get("assignee_id"); assigneeID != "" {
		query += " AND m.assignee_id = ?"
		args = append(args, assigneeID)
	}

	// Label filter
	if labelName := r.URL.Query().Get("label"); labelName != "" {
		query += " AND m.id IN (SELECT ml.mission_id FROM mission_labels ml JOIN labels l ON ml.label_id = l.id WHERE l.name = ?)"
		args = append(args, labelName)
	}

	// Search (LIKE on title)
	if search := r.URL.Query().Get("search"); search != "" {
		query += " AND m.title LIKE ?"
		args = append(args, "%"+search+"%")
	}

	// Sort
	sortCol := "m.created_at"
	switch r.URL.Query().Get("sort") {
	case "updated_at":
		sortCol = "m.updated_at"
	case "priority":
		sortCol = "m.priority"
	case "sort_order":
		sortCol = "COALESCE(m.sort_order, 0)"
	}
	query += " ORDER BY " + sortCol + " DESC LIMIT ? OFFSET ?"
	args = append(args, limit, offset)

	rows, err := h.db.QueryContext(r.Context(), query, args...)
	if err != nil {
		h.logger.Error("list issues", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	defer rows.Close()

	var result []issueResponse
	var issueIDs []string
	for rows.Next() {
		issue, err := scanIssue(rows)
		if err != nil {
			h.logger.Error("scan issue", "error", err)
			writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
			return
		}
		result = append(result, issue)
		issueIDs = append(issueIDs, issue.ID)
	}
	if err := rows.Err(); err != nil {
		h.logger.Error("rows iteration (issues)", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	// Batch-load labels
	if len(issueIDs) > 0 {
		placeholders := make([]string, len(issueIDs))
		labelArgs := make([]interface{}, len(issueIDs))
		for i, id := range issueIDs {
			placeholders[i] = "?"
			labelArgs[i] = id
		}
		labelQuery := fmt.Sprintf(`
			SELECT ml.mission_id, l.id, l.name, l.color, l.label_group
			FROM mission_labels ml
			JOIN labels l ON ml.label_id = l.id
			WHERE ml.mission_id IN (%s)`, strings.Join(placeholders, ","))

		labelRows, err := h.db.QueryContext(r.Context(), labelQuery, labelArgs...)
		if err != nil {
			h.logger.Error("batch load labels", "error", err)
		} else {
			defer labelRows.Close()
			labelMap := make(map[string][]labelResponse)
			for labelRows.Next() {
				var missionID string
				var lbl labelResponse
				if err := labelRows.Scan(&missionID, &lbl.ID, &lbl.Name, &lbl.Color, &lbl.LabelGroup); err != nil {
					h.logger.Error("scan label", "error", err)
					continue
				}
				labelMap[missionID] = append(labelMap[missionID], lbl)
			}
			for i := range result {
				if labels, ok := labelMap[result[i].ID]; ok {
					result[i].Labels = labels
				}
			}
		}

		// Batch-load comment counts
		commentQuery := fmt.Sprintf(`
			SELECT mission_id, COUNT(*)
			FROM mission_comments
			WHERE mission_id IN (%s)
			GROUP BY mission_id`, strings.Join(placeholders, ","))

		commentRows, err := h.db.QueryContext(r.Context(), commentQuery, labelArgs...)
		if err != nil {
			h.logger.Error("batch load comment counts", "error", err)
		} else {
			defer commentRows.Close()
			commentMap := make(map[string]int)
			for commentRows.Next() {
				var missionID string
				var count int
				if err := commentRows.Scan(&missionID, &count); err != nil {
					h.logger.Error("scan comment count", "error", err)
					continue
				}
				commentMap[missionID] = count
			}
			for i := range result {
				if count, ok := commentMap[result[i].ID]; ok {
					result[i].CommentCount = count
				}
			}
		}
	}

	if result == nil {
		result = []issueResponse{}
	}
	writeJSON(w, http.StatusOK, result)
}

// ── 2. Create — POST /api/v1/crews/{crewId}/issues ─────────────────────────

func (h *IssueHandler) Create(w http.ResponseWriter, r *http.Request) {
	role := RoleFromContext(r.Context())
	if !canRole(role, "create") {
		writeProblem(w, r, http.StatusForbidden, "Forbidden")
		return
	}

	crewID := r.PathValue("crewId")
	wsID := WorkspaceIDFromContext(r.Context())

	var req struct {
		Title         string   `json:"title"`
		Description   *string  `json:"description"`
		Priority      string   `json:"priority"`
		AssigneeType  *string  `json:"assignee_type"`
		AssigneeID    *string  `json:"assignee_id"`
		DueDate       *string  `json:"due_date"`
		ProjectID     *string  `json:"project_id"`
		Estimate      *int     `json:"estimate"`
		ParentIssueID *string  `json:"parent_issue_id"`
		MilestoneID   *string  `json:"milestone_id"`
		Labels        []string `json:"labels"`
	}
	if err := readJSON(r, &req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "Invalid JSON body")
		return
	}
	if req.Title == "" {
		writeProblem(w, r, http.StatusBadRequest, "title is required")
		return
	}
	if req.Priority == "" {
		req.Priority = "none"
	}

	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		h.logger.Error("begin tx", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	defer tx.Rollback() //nolint:errcheck

	// Get crew info
	var issuePrefix sql.NullString
	var crewSlug string
	err = tx.QueryRowContext(r.Context(),
		`SELECT issue_prefix, slug FROM crews WHERE id = ? AND workspace_id = ?`,
		crewID, wsID).Scan(&issuePrefix, &crewSlug)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeProblem(w, r, http.StatusNotFound, "Crew not found")
			return
		}
		h.logger.Error("get crew for issue", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	// Derive prefix from slug if not set
	prefix := issuePrefix.String
	if !issuePrefix.Valid || prefix == "" {
		slugUpper := strings.ToUpper(crewSlug)
		if len(slugUpper) >= 3 {
			prefix = slugUpper[:3]
		} else {
			prefix = slugUpper
		}
	}

	// Atomic counter for issue number
	var issueNumber int
	err = tx.QueryRowContext(r.Context(), `
		INSERT INTO issue_counters(crew_id, next_number) VALUES(?, 1)
		ON CONFLICT(crew_id) DO UPDATE SET next_number = issue_counters.next_number + 1
		RETURNING next_number`,
		crewID).Scan(&issueNumber)
	if err != nil {
		h.logger.Error("issue counter upsert", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	identifier := fmt.Sprintf("%s-%d", prefix, issueNumber)

	// Find lead agent for the crew
	var leadAgentID string
	err = tx.QueryRowContext(r.Context(),
		`SELECT id FROM agents WHERE crew_id = ? AND agent_role = 'LEAD' AND deleted_at IS NULL LIMIT 1`,
		crewID).Scan(&leadAgentID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeProblem(w, r, http.StatusBadRequest, "Crew has no lead agent")
			return
		}
		h.logger.Error("find lead agent", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	id := generateCUID()
	traceID := "issue-" + generateCUID()
	now := time.Now().UTC().Format(time.RFC3339)

	_, err = tx.ExecContext(r.Context(), `
		INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id,
		    title, description, status, number, identifier, priority,
		    assignee_type, assignee_id, due_date, project_id, estimate,
		    parent_issue_id, milestone_id, sort_order, mission_type,
		    created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, 'BACKLOG', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, 'issue', ?, ?)`,
		id, wsID, crewID, leadAgentID, traceID,
		req.Title, req.Description, issueNumber, identifier, req.Priority,
		req.AssigneeType, req.AssigneeID, req.DueDate, req.ProjectID,
		req.Estimate, req.ParentIssueID, req.MilestoneID,
		now, now)
	if err != nil {
		h.logger.Error("insert issue", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	// Insert label associations
	for _, labelID := range req.Labels {
		_, err = tx.ExecContext(r.Context(),
			`INSERT OR IGNORE INTO mission_labels (mission_id, label_id) VALUES (?, ?)`,
			id, labelID)
		if err != nil {
			h.logger.Error("insert mission label", "error", err)
			writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
			return
		}
	}

	if err := tx.Commit(); err != nil {
		h.logger.Error("commit issue", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	resp := issueResponse{
		ID:            id,
		WorkspaceID:   wsID,
		CrewID:        crewID,
		Number:        &issueNumber,
		Identifier:    &identifier,
		Title:         req.Title,
		Description:   req.Description,
		Status:        "BACKLOG",
		Priority:      req.Priority,
		AssigneeType:  req.AssigneeType,
		AssigneeID:    req.AssigneeID,
		DueDate:       req.DueDate,
		SortOrder:     0,
		MissionType:   "issue",
		LeadAgentID:   leadAgentID,
		Estimate:      req.Estimate,
		ParentIssueID: req.ParentIssueID,
		MilestoneID:   req.MilestoneID,
		CreatedAt:     now,
		UpdatedAt:     now,
		Labels:        []labelResponse{},
	}

	if h.hub != nil {
		h.hub.Broadcast("workspace:"+wsID, ws.ServerMessage{
			Type:    "issue.created",
			Channel: "workspace:" + wsID,
			Payload: map[string]string{"id": id},
		})
	}

	writeJSON(w, http.StatusCreated, resp)
}

// ── 3. Get — GET /api/v1/crews/{crewId}/issues/{identifier} ────────────────

func (h *IssueHandler) Get(w http.ResponseWriter, r *http.Request) {
	crewID := r.PathValue("crewId")
	ident := r.PathValue("identifier")
	wsID := WorkspaceIDFromContext(r.Context())

	issue, err := scanIssue(h.db.QueryRowContext(r.Context(),
		issueSelectColumns+` WHERE m.identifier = ? AND m.crew_id = ? AND m.workspace_id = ?`,
		ident, crewID, wsID))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeProblem(w, r, http.StatusNotFound, "Issue not found")
			return
		}
		h.logger.Error("get issue", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	labelRows, err := h.db.QueryContext(r.Context(), `
		SELECT l.id, l.name, l.color, l.label_group
		FROM mission_labels ml
		JOIN labels l ON ml.label_id = l.id
		WHERE ml.mission_id = ?`, issue.ID)
	if err != nil {
		h.logger.Error("load issue labels", "error", err)
	} else {
		defer labelRows.Close()
		for labelRows.Next() {
			var lbl labelResponse
			if err := labelRows.Scan(&lbl.ID, &lbl.Name, &lbl.Color, &lbl.LabelGroup); err != nil {
				h.logger.Error("scan issue label", "error", err)
				continue
			}
			issue.Labels = append(issue.Labels, lbl)
		}
	}

	// Load comment count
	_ = h.db.QueryRowContext(r.Context(),
		`SELECT COUNT(*) FROM mission_comments WHERE mission_id = ?`,
		issue.ID).Scan(&issue.CommentCount)

	writeJSON(w, http.StatusOK, issue)
}

// ── 3b. GetByIdentifier — GET /api/v1/issues/{identifier} (workspace-scoped) ─

func (h *IssueHandler) GetByIdentifier(w http.ResponseWriter, r *http.Request) {
	ident := r.PathValue("identifier")
	wsID := WorkspaceIDFromContext(r.Context())

	issue, err := scanIssue(h.db.QueryRowContext(r.Context(),
		issueSelectColumns+` WHERE m.identifier = ? AND m.workspace_id = ?`,
		ident, wsID))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeProblem(w, r, http.StatusNotFound, "Issue not found")
			return
		}
		h.logger.Error("get issue by identifier", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	// Load labels
	issue.Labels = []labelResponse{}
	labelRows, err := h.db.QueryContext(r.Context(), `
		SELECT l.id, l.name, l.color, l.label_group
		FROM mission_labels ml JOIN labels l ON ml.label_id = l.id
		WHERE ml.mission_id = ?`, issue.ID)
	if err == nil {
		defer labelRows.Close()
		for labelRows.Next() {
			var lbl labelResponse
			if err := labelRows.Scan(&lbl.ID, &lbl.Name, &lbl.Color, &lbl.LabelGroup); err == nil {
				issue.Labels = append(issue.Labels, lbl)
			}
		}
	}

	_ = h.db.QueryRowContext(r.Context(),
		`SELECT COUNT(*) FROM mission_comments WHERE mission_id = ?`,
		issue.ID).Scan(&issue.CommentCount)

	writeJSON(w, http.StatusOK, issue)
}

// ── 4. Update — PATCH /api/v1/crews/{crewId}/issues/{identifier} ───────────

func (h *IssueHandler) Update(w http.ResponseWriter, r *http.Request) {
	role := RoleFromContext(r.Context())
	if !canRole(role, "create") {
		writeProblem(w, r, http.StatusForbidden, "Forbidden")
		return
	}

	crewID := r.PathValue("crewId")
	ident := r.PathValue("identifier")
	wsID := WorkspaceIDFromContext(r.Context())

	var req struct {
		Title         *string   `json:"title"`
		Description   *string   `json:"description"`
		Status        *string   `json:"status"`
		Priority      *string   `json:"priority"`
		AssigneeType  *string   `json:"assignee_type"`
		AssigneeID    *string   `json:"assignee_id"`
		DueDate       *string   `json:"due_date"`
		ProjectID     *string   `json:"project_id"`
		Estimate      *int      `json:"estimate"`
		ParentIssueID *string   `json:"parent_issue_id"`
		MilestoneID   *string   `json:"milestone_id"`
		SortOrder     *float64  `json:"sort_order"`
		Labels        *[]string `json:"labels"`
	}
	if err := readJSON(r, &req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "Invalid JSON body")
		return
	}

	// Look up current status
	var missionID, currentStatus string
	err := h.db.QueryRowContext(r.Context(),
		`SELECT id, status FROM missions WHERE identifier = ? AND crew_id = ? AND workspace_id = ?`,
		ident, crewID, wsID).Scan(&missionID, &currentStatus)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeProblem(w, r, http.StatusNotFound, "Issue not found")
			return
		}
		h.logger.Error("get issue for update", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	ub := newUpdate()

	if req.Title != nil {
		ub.Set("title", *req.Title)
	}
	if req.Description != nil {
		ub.Set("description", *req.Description)
	}
	if req.Priority != nil {
		ub.Set("priority", *req.Priority)
	}
	if req.AssigneeType != nil {
		ub.Set("assignee_type", *req.AssigneeType)
	}
	if req.AssigneeID != nil {
		ub.Set("assignee_id", *req.AssigneeID)
	}
	if req.DueDate != nil {
		ub.Set("due_date", *req.DueDate)
	}
	if req.SortOrder != nil {
		ub.Set("sort_order", *req.SortOrder)
	}
	if req.ProjectID != nil {
		if *req.ProjectID == "" {
			ub.SetNull("project_id")
		} else {
			ub.Set("project_id", *req.ProjectID)
		}
	}
	if req.Estimate != nil {
		ub.Set("estimate", *req.Estimate)
	}
	if req.ParentIssueID != nil {
		if *req.ParentIssueID == "" {
			ub.SetNull("parent_issue_id")
		} else {
			ub.Set("parent_issue_id", *req.ParentIssueID)
		}
	}
	if req.MilestoneID != nil {
		if *req.MilestoneID == "" {
			ub.SetNull("milestone_id")
		} else {
			ub.Set("milestone_id", *req.MilestoneID)
		}
	}

	// Validate status transition
	if req.Status != nil {
		newStatus := *req.Status
		allowed := validIssueTransitions[currentStatus]
		valid := false
		for _, s := range allowed {
			if s == newStatus {
				valid = true
				break
			}
		}
		if !valid {
			writeProblem(w, r, http.StatusBadRequest,
				"Invalid status transition from "+currentStatus+" to "+newStatus)
			return
		}
		ub.Set("status", newStatus)

		if newStatus == "DONE" || newStatus == "CANCELLED" {
			now := time.Now().UTC().Format(time.RFC3339)
			ub.Set("completed_at", now)
		}
	}

	if ub.Empty() {
		writeProblem(w, r, http.StatusBadRequest, "No fields to update")
		return
	}

	query, args := ub.Build("missions", "id = ?", missionID)
	if _, err := h.db.ExecContext(r.Context(), query, args...); err != nil {
		h.logger.Error("update issue", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	// Update labels if provided
	if req.Labels != nil {
		// Replace all labels: delete existing, insert new
		if _, err := h.db.ExecContext(r.Context(), `DELETE FROM mission_labels WHERE mission_id = ?`, missionID); err != nil {
			h.logger.Error("delete issue labels", "error", err)
		}
		for _, labelID := range *req.Labels {
			_, _ = h.db.ExecContext(r.Context(),
				`INSERT OR IGNORE INTO mission_labels (mission_id, label_id) VALUES (?, ?)`,
				missionID, labelID)
		}
	}

	// Log activity for significant changes
	user := UserFromContext(r.Context())
	actorType := "user"
	actorID := user.ID
	now := time.Now().UTC().Format(time.RFC3339)

	if req.Status != nil {
		actID := generateCUID()
		_, _ = h.db.ExecContext(r.Context(),
			`INSERT INTO mission_activity (id, mission_id, actor_type, actor_id, action, details, created_at) VALUES (?, ?, ?, ?, 'status_changed', ?, ?)`,
			actID, missionID, actorType, actorID, fmt.Sprintf("%s → %s", currentStatus, *req.Status), now)
	}
	if req.AssigneeID != nil {
		actID := generateCUID()
		_, _ = h.db.ExecContext(r.Context(),
			`INSERT INTO mission_activity (id, mission_id, actor_type, actor_id, action, details, created_at) VALUES (?, ?, ?, ?, 'assignee_changed', ?, ?)`,
			actID, missionID, actorType, actorID, fmt.Sprintf("assignee_id: %s", *req.AssigneeID), now)
	}
	if req.Priority != nil {
		actID := generateCUID()
		_, _ = h.db.ExecContext(r.Context(),
			`INSERT INTO mission_activity (id, mission_id, actor_type, actor_id, action, details, created_at) VALUES (?, ?, ?, ?, 'priority_changed', ?, ?)`,
			actID, missionID, actorType, actorID, *req.Priority, now)
	}

	if h.hub != nil {
		h.hub.Broadcast("workspace:"+wsID, ws.ServerMessage{
			Type:    "issue.updated",
			Channel: "workspace:" + wsID,
			Payload: map[string]string{"id": missionID},
		})
	}

	// Return updated issue
	issue, err := scanIssue(h.db.QueryRowContext(r.Context(),
		issueSelectColumns+` WHERE m.id = ?`, missionID))
	if err != nil {
		h.logger.Error("read updated issue", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	writeJSON(w, http.StatusOK, issue)
}

// ── 5. Delete — DELETE /api/v1/crews/{crewId}/issues/{identifier} ───────────

func (h *IssueHandler) Delete(w http.ResponseWriter, r *http.Request) {
	role := RoleFromContext(r.Context())
	if !canRole(role, "create") {
		writeProblem(w, r, http.StatusForbidden, "Forbidden")
		return
	}

	crewID := r.PathValue("crewId")
	ident := r.PathValue("identifier")
	wsID := WorkspaceIDFromContext(r.Context())

	// Only allow deletion of BACKLOG or CANCELLED issues
	res, err := h.db.ExecContext(r.Context(),
		`DELETE FROM missions WHERE identifier = ? AND crew_id = ? AND workspace_id = ? AND status IN ('BACKLOG', 'CANCELLED')`,
		ident, crewID, wsID)
	if err != nil {
		h.logger.Error("delete issue", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	affected, err := res.RowsAffected()
	if err != nil {
		h.logger.Error("delete issue rows affected", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	if affected == 0 {
		var currentStatus string
		qErr := h.db.QueryRowContext(r.Context(),
			`SELECT status FROM missions WHERE identifier = ? AND crew_id = ? AND workspace_id = ?`,
			ident, crewID, wsID).Scan(&currentStatus)
		if qErr != nil {
			if errors.Is(qErr, sql.ErrNoRows) {
				writeProblem(w, r, http.StatusNotFound, "Issue not found")
				return
			}
			h.logger.Error("delete issue follow-up query", "error", qErr)
			writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
			return
		}
		writeProblem(w, r, http.StatusBadRequest, "Only BACKLOG or CANCELLED issues can be deleted")
		return
	}

	if h.hub != nil {
		h.hub.Broadcast("workspace:"+wsID, ws.ServerMessage{
			Type:    "issue.deleted",
			Channel: "workspace:" + wsID,
			Payload: map[string]string{"identifier": ident},
		})
	}

	w.WriteHeader(http.StatusNoContent)
}

// ── 6. ListLabels — GET /api/v1/labels ──────────────────────────────────────

func (h *IssueHandler) ListLabels(w http.ResponseWriter, r *http.Request) {
	wsID := WorkspaceIDFromContext(r.Context())

	rows, err := h.db.QueryContext(r.Context(),
		`SELECT id, name, color, label_group FROM labels WHERE workspace_id = ? ORDER BY name ASC`,
		wsID)
	if err != nil {
		h.logger.Error("list labels", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	defer rows.Close()

	result := []labelResponse{}
	for rows.Next() {
		var lbl labelResponse
		if err := rows.Scan(&lbl.ID, &lbl.Name, &lbl.Color, &lbl.LabelGroup); err != nil {
			h.logger.Error("scan label", "error", err)
			writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
			return
		}
		result = append(result, lbl)
	}
	if err := rows.Err(); err != nil {
		h.logger.Error("rows iteration (labels)", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	writeJSON(w, http.StatusOK, result)
}

// ── 7. CreateLabel — POST /api/v1/labels ────────────────────────────────────

func (h *IssueHandler) CreateLabel(w http.ResponseWriter, r *http.Request) {
	role := RoleFromContext(r.Context())
	if !canRole(role, "create") {
		writeProblem(w, r, http.StatusForbidden, "Forbidden")
		return
	}

	wsID := WorkspaceIDFromContext(r.Context())

	var req struct {
		Name       string  `json:"name"`
		Color      string  `json:"color"`
		LabelGroup *string `json:"label_group"`
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
		writeProblem(w, r, http.StatusBadRequest, "color is required")
		return
	}

	id := generateCUID()
	now := time.Now().UTC().Format(time.RFC3339)

	_, err := h.db.ExecContext(r.Context(),
		`INSERT INTO labels (id, workspace_id, name, color, label_group, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		id, wsID, req.Name, req.Color, req.LabelGroup, now)
	if err != nil {
		h.logger.Error("create label", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	resp := labelResponse{
		ID:         id,
		Name:       req.Name,
		Color:      req.Color,
		LabelGroup: req.LabelGroup,
	}
	writeJSON(w, http.StatusCreated, resp)
}

// ── 8. UpdateLabel — PATCH /api/v1/labels/{labelId} ─────────────────────────

func (h *IssueHandler) UpdateLabel(w http.ResponseWriter, r *http.Request) {
	role := RoleFromContext(r.Context())
	if !canRole(role, "create") {
		writeProblem(w, r, http.StatusForbidden, "Forbidden")
		return
	}

	labelID := r.PathValue("labelId")
	wsID := WorkspaceIDFromContext(r.Context())

	var req struct {
		Name       *string `json:"name"`
		Color      *string `json:"color"`
		LabelGroup *string `json:"label_group"`
	}
	if err := readJSON(r, &req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "Invalid JSON body")
		return
	}

	ub := newUpdate()
	if req.Name != nil {
		ub.Set("name", *req.Name)
	}
	if req.Color != nil {
		ub.Set("color", *req.Color)
	}
	if req.LabelGroup != nil {
		ub.Set("label_group", *req.LabelGroup)
	}

	if ub.Empty() {
		writeProblem(w, r, http.StatusBadRequest, "No fields to update")
		return
	}

	query, args := ub.Build("labels", "id = ? AND workspace_id = ?", labelID, wsID)
	res, err := h.db.ExecContext(r.Context(), query, args...)
	if err != nil {
		h.logger.Error("update label", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	affected, err := res.RowsAffected()
	if err != nil {
		h.logger.Error("update label rows affected", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	if affected == 0 {
		writeProblem(w, r, http.StatusNotFound, "Label not found")
		return
	}

	var lbl labelResponse
	err = h.db.QueryRowContext(r.Context(),
		`SELECT id, name, color, label_group FROM labels WHERE id = ?`, labelID).
		Scan(&lbl.ID, &lbl.Name, &lbl.Color, &lbl.LabelGroup)
	if err != nil {
		h.logger.Error("read updated label", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	writeJSON(w, http.StatusOK, lbl)
}

// ── 9. DeleteLabel — DELETE /api/v1/labels/{labelId} ────────────────────────

func (h *IssueHandler) DeleteLabel(w http.ResponseWriter, r *http.Request) {
	role := RoleFromContext(r.Context())
	if !canRole(role, "manage") {
		writeProblem(w, r, http.StatusForbidden, "Forbidden")
		return
	}

	labelID := r.PathValue("labelId")
	wsID := WorkspaceIDFromContext(r.Context())

	res, err := h.db.ExecContext(r.Context(),
		`DELETE FROM labels WHERE id = ? AND workspace_id = ?`, labelID, wsID)
	if err != nil {
		h.logger.Error("delete label", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	affected, err := res.RowsAffected()
	if err != nil {
		h.logger.Error("delete label rows affected", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	if affected == 0 {
		writeProblem(w, r, http.StatusNotFound, "Label not found")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// ── 10. ListComments — GET /api/v1/crews/{crewId}/issues/{identifier}/comments

func (h *IssueHandler) ListComments(w http.ResponseWriter, r *http.Request) {
	crewID := r.PathValue("crewId")
	ident := r.PathValue("identifier")
	wsID := WorkspaceIDFromContext(r.Context())

	// Resolve identifier to mission_id
	var missionID string
	err := h.db.QueryRowContext(r.Context(),
		`SELECT id FROM missions WHERE identifier = ? AND crew_id = ? AND workspace_id = ?`,
		ident, crewID, wsID).Scan(&missionID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeProblem(w, r, http.StatusNotFound, "Issue not found")
			return
		}
		h.logger.Error("resolve issue for comments", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	rows, err := h.db.QueryContext(r.Context(), `
		SELECT mc.id, mc.mission_id, mc.author_type, mc.author_id,
		       CASE
		         WHEN mc.author_type = 'user' THEN (SELECT full_name FROM users WHERE id = mc.author_id)
		         WHEN mc.author_type = 'agent' THEN (SELECT name FROM agents WHERE id = mc.author_id)
		         ELSE ''
		       END,
		       mc.body, mc.created_at, mc.updated_at
		FROM mission_comments mc
		WHERE mc.mission_id = ?
		ORDER BY mc.created_at ASC`, missionID)
	if err != nil {
		h.logger.Error("list comments", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	defer rows.Close()

	result := []commentResponse{}
	for rows.Next() {
		var c commentResponse
		if err := rows.Scan(&c.ID, &c.MissionID, &c.AuthorType, &c.AuthorID,
			&c.AuthorName, &c.Body, &c.CreatedAt, &c.UpdatedAt); err != nil {
			h.logger.Error("scan comment", "error", err)
			writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
			return
		}
		result = append(result, c)
	}
	if err := rows.Err(); err != nil {
		h.logger.Error("rows iteration (comments)", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	writeJSON(w, http.StatusOK, result)
}

// ── 11. CreateComment — POST /api/v1/crews/{crewId}/issues/{identifier}/comments

func (h *IssueHandler) CreateComment(w http.ResponseWriter, r *http.Request) {
	role := RoleFromContext(r.Context())
	if !canRole(role, "create") {
		writeProblem(w, r, http.StatusForbidden, "Forbidden")
		return
	}

	crewID := r.PathValue("crewId")
	ident := r.PathValue("identifier")
	wsID := WorkspaceIDFromContext(r.Context())
	user := UserFromContext(r.Context())

	var req struct {
		Body string `json:"body"`
	}
	if err := readJSON(r, &req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "Invalid JSON body")
		return
	}
	if req.Body == "" {
		writeProblem(w, r, http.StatusBadRequest, "body is required")
		return
	}

	// Resolve identifier to mission_id
	var missionID string
	err := h.db.QueryRowContext(r.Context(),
		`SELECT id FROM missions WHERE identifier = ? AND crew_id = ? AND workspace_id = ?`,
		ident, crewID, wsID).Scan(&missionID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeProblem(w, r, http.StatusNotFound, "Issue not found")
			return
		}
		h.logger.Error("resolve issue for comment creation", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	id := generateCUID()
	now := time.Now().UTC().Format(time.RFC3339)

	_, err = h.db.ExecContext(r.Context(), `
		INSERT INTO mission_comments (id, mission_id, author_type, author_id, body, created_at, updated_at)
		VALUES (?, ?, 'user', ?, ?, ?, ?)`,
		id, missionID, user.ID, req.Body, now, now)
	if err != nil {
		h.logger.Error("create comment", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	resp := commentResponse{
		ID:         id,
		MissionID:  missionID,
		AuthorType: "user",
		AuthorID:   user.ID,
		AuthorName: user.Name,
		Body:       req.Body,
		CreatedAt:  now,
		UpdatedAt:  now,
	}

	if h.hub != nil {
		h.hub.Broadcast("workspace:"+wsID, ws.ServerMessage{
			Type:    "issue.updated",
			Channel: "workspace:" + wsID,
			Payload: map[string]string{"id": missionID},
		})
	}

	writeJSON(w, http.StatusCreated, resp)
}

// ── 12. ListRelations — GET /api/v1/crews/{crewId}/issues/{identifier}/relations

func (h *IssueHandler) ListRelations(w http.ResponseWriter, r *http.Request) {
	crewID := r.PathValue("crewId")
	ident := r.PathValue("identifier")
	wsID := WorkspaceIDFromContext(r.Context())

	var missionID string
	err := h.db.QueryRowContext(r.Context(),
		`SELECT id FROM missions WHERE identifier = ? AND crew_id = ? AND workspace_id = ?`,
		ident, crewID, wsID).Scan(&missionID)
	if err != nil {
		writeProblem(w, r, http.StatusNotFound, "Issue not found")
		return
	}

	rows, err := h.db.QueryContext(r.Context(), `
		SELECT mr.id, mr.source_id, mr.target_id, mr.relation_type, mr.created_at,
		       m.identifier, m.title, m.status
		FROM mission_relations mr
		JOIN missions m ON m.id = CASE WHEN mr.source_id = ? THEN mr.target_id ELSE mr.source_id END
		WHERE mr.source_id = ? OR mr.target_id = ?`,
		missionID, missionID, missionID)
	if err != nil {
		h.logger.Error("list relations", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	defer rows.Close()

	var result []relationResponse
	for rows.Next() {
		var rel relationResponse
		if err := rows.Scan(&rel.ID, &rel.SourceID, &rel.TargetID, &rel.RelationType, &rel.CreatedAt,
			&rel.TargetIdentifier, &rel.TargetTitle, &rel.TargetStatus); err != nil {
			h.logger.Error("scan relation", "error", err)
			continue
		}
		if rel.TargetID == missionID {
			switch rel.RelationType {
			case "blocks":
				rel.RelationType = "blocked_by"
			case "blocked_by":
				rel.RelationType = "blocks"
			}
		}
		result = append(result, rel)
	}
	if result == nil {
		result = []relationResponse{}
	}
	writeJSON(w, http.StatusOK, result)
}

// ── 13. CreateRelation — POST /api/v1/crews/{crewId}/issues/{identifier}/relations

func (h *IssueHandler) CreateRelation(w http.ResponseWriter, r *http.Request) {
	role := RoleFromContext(r.Context())
	if !canRole(role, "create") {
		writeProblem(w, r, http.StatusForbidden, "Forbidden")
		return
	}

	crewID := r.PathValue("crewId")
	ident := r.PathValue("identifier")
	wsID := WorkspaceIDFromContext(r.Context())

	var req struct {
		TargetIdentifier string `json:"target_identifier"`
		RelationType     string `json:"relation_type"`
	}
	if err := readJSON(r, &req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "Invalid JSON body")
		return
	}
	validTypes := map[string]bool{"blocks": true, "blocked_by": true, "relates_to": true, "duplicate_of": true}
	if !validTypes[req.RelationType] {
		writeProblem(w, r, http.StatusBadRequest, "relation_type must be: blocks, blocked_by, relates_to, duplicate_of")
		return
	}

	var sourceID string
	err := h.db.QueryRowContext(r.Context(),
		`SELECT id FROM missions WHERE identifier = ? AND crew_id = ? AND workspace_id = ?`,
		ident, crewID, wsID).Scan(&sourceID)
	if err != nil {
		writeProblem(w, r, http.StatusNotFound, "Source issue not found")
		return
	}

	var targetID string
	err = h.db.QueryRowContext(r.Context(),
		`SELECT id FROM missions WHERE identifier = ? AND workspace_id = ?`,
		req.TargetIdentifier, wsID).Scan(&targetID)
	if err != nil {
		writeProblem(w, r, http.StatusNotFound, "Target issue not found: "+req.TargetIdentifier)
		return
	}

	if sourceID == targetID {
		writeProblem(w, r, http.StatusBadRequest, "Cannot relate an issue to itself")
		return
	}

	actualSource, actualTarget, actualType := sourceID, targetID, req.RelationType
	if req.RelationType == "blocked_by" {
		actualSource, actualTarget, actualType = targetID, sourceID, "blocks"
	}

	id := generateCUID()
	_, err = h.db.ExecContext(r.Context(), `
		INSERT INTO mission_relations (id, source_id, target_id, relation_type) VALUES (?, ?, ?, ?)`,
		id, actualSource, actualTarget, actualType)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			writeProblem(w, r, http.StatusConflict, "Relation already exists")
			return
		}
		h.logger.Error("create relation", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	if h.hub != nil {
		h.hub.Broadcast("workspace:"+wsID, ws.ServerMessage{
			Type:    "issue.updated",
			Channel: "workspace:" + wsID,
			Payload: map[string]string{"id": sourceID},
		})
	}

	writeJSON(w, http.StatusCreated, map[string]string{"id": id, "status": "ok"})
}

// ── Review — POST /api/v1/crews/{crewId}/issues/{identifier}/review ────────

func (h *IssueHandler) Review(w http.ResponseWriter, r *http.Request) {
	role := RoleFromContext(r.Context())
	if !canRole(role, "create") {
		writeProblem(w, r, http.StatusForbidden, "Forbidden")
		return
	}

	crewID := r.PathValue("crewId")
	ident := r.PathValue("identifier")
	wsID := WorkspaceIDFromContext(r.Context())
	user := UserFromContext(r.Context())

	var req struct {
		Action     string  `json:"action"`       // "approve" or "request_changes"
		Comment    string  `json:"comment"`
		ReassignTo *string `json:"reassign_to"`  // agent slug for request_changes
	}
	if err := readJSON(r, &req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "Invalid JSON body")
		return
	}
	if req.Action != "approve" && req.Action != "request_changes" {
		writeProblem(w, r, http.StatusBadRequest, "action must be 'approve' or 'request_changes'")
		return
	}

	// Get issue
	var missionID, status string
	err := h.db.QueryRowContext(r.Context(),
		`SELECT id, status FROM missions WHERE identifier = ? AND crew_id = ? AND workspace_id = ?`,
		ident, crewID, wsID).Scan(&missionID, &status)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeProblem(w, r, http.StatusNotFound, "Issue not found")
			return
		}
		h.logger.Error("review: load issue", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	if status != "REVIEW" && status != "IN_PROGRESS" {
		writeProblem(w, r, http.StatusBadRequest, "Issue must be in REVIEW or IN_PROGRESS to review (current: "+status+")")
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)

	if req.Action == "approve" {
		// REVIEW → DONE
		_, err = h.db.ExecContext(r.Context(),
			`UPDATE missions SET status = 'DONE', completed_at = ?, updated_at = ? WHERE id = ?`,
			now, now, missionID)
		if err != nil {
			h.logger.Error("review: approve", "error", err)
			writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
			return
		}

		// Add comment
		commentBody := "Approved"
		if req.Comment != "" {
			commentBody = "Approved: " + req.Comment
		}
		commentID := generateCUID()
		_, _ = h.db.ExecContext(r.Context(),
			`INSERT INTO mission_comments (id, mission_id, author_type, author_id, body, created_at, updated_at) VALUES (?, ?, 'user', ?, ?, ?, ?)`,
			commentID, missionID, user.ID, commentBody, now, now)

		// Activity
		actID := generateCUID()
		_, _ = h.db.ExecContext(r.Context(),
			`INSERT INTO mission_activity (id, mission_id, actor_type, actor_id, action, details, created_at) VALUES (?, ?, 'user', ?, 'review_approved', ?, ?)`,
			actID, missionID, user.ID, commentBody, now)

	} else {
		// request_changes → TODO
		ub := newUpdate()
		ub.Set("status", "TODO")

		if req.ReassignTo != nil && *req.ReassignTo != "" {
			// Resolve agent slug to ID
			var agentID string
			err := h.db.QueryRowContext(r.Context(),
				`SELECT id FROM agents WHERE slug = ? AND deleted_at IS NULL LIMIT 1`,
				*req.ReassignTo).Scan(&agentID)
			if err == nil {
				ub.Set("assignee_type", "agent")
				ub.Set("assignee_id", agentID)
			}
		}

		query, args := ub.Build("missions", "id = ?", missionID)
		_, err = h.db.ExecContext(r.Context(), query, args...)
		if err != nil {
			h.logger.Error("review: request_changes", "error", err)
			writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
			return
		}

		// Add comment
		commentBody := "Changes requested"
		if req.Comment != "" {
			commentBody = "Changes requested: " + req.Comment
		}
		commentID := generateCUID()
		_, _ = h.db.ExecContext(r.Context(),
			`INSERT INTO mission_comments (id, mission_id, author_type, author_id, body, created_at, updated_at) VALUES (?, ?, 'user', ?, ?, ?, ?)`,
			commentID, missionID, user.ID, commentBody, now, now)

		// Activity
		actID := generateCUID()
		_, _ = h.db.ExecContext(r.Context(),
			`INSERT INTO mission_activity (id, mission_id, actor_type, actor_id, action, details, created_at) VALUES (?, ?, 'user', ?, 'review_changes_requested', ?, ?)`,
			actID, missionID, user.ID, commentBody, now)
	}

	if h.hub != nil {
		h.hub.Broadcast("workspace:"+wsID, ws.ServerMessage{
			Type:    "issue.updated",
			Channel: "workspace:" + wsID,
			Payload: map[string]string{"id": missionID, "identifier": ident},
		})
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "action": req.Action})
}

// ── ListActivity — GET /api/v1/crews/{crewId}/issues/{identifier}/activity

func (h *IssueHandler) ListActivity(w http.ResponseWriter, r *http.Request) {
	crewID := r.PathValue("crewId")
	ident := r.PathValue("identifier")
	wsID := WorkspaceIDFromContext(r.Context())

	var missionID string
	err := h.db.QueryRowContext(r.Context(),
		`SELECT id FROM missions WHERE identifier = ? AND crew_id = ? AND workspace_id = ?`,
		ident, crewID, wsID).Scan(&missionID)
	if err != nil {
		writeProblem(w, r, http.StatusNotFound, "Issue not found")
		return
	}

	rows, err := h.db.QueryContext(r.Context(), `
		SELECT a.id, a.mission_id, a.actor_type, a.actor_id, a.action, a.details, a.created_at,
			CASE
				WHEN a.actor_type = 'user' THEN (SELECT full_name FROM users WHERE id = a.actor_id)
				WHEN a.actor_type = 'agent' THEN (SELECT name FROM agents WHERE id = a.actor_id)
				ELSE 'System'
			END AS actor_name
		FROM mission_activity a
		WHERE a.mission_id = ?
		ORDER BY a.created_at DESC
		LIMIT 50`, missionID)
	if err != nil {
		h.logger.Error("list activity", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	defer rows.Close()

	type activityResponse struct {
		ID        string  `json:"id"`
		MissionID string  `json:"mission_id"`
		ActorType string  `json:"actor_type"`
		ActorID   string  `json:"actor_id"`
		ActorName *string `json:"actor_name,omitempty"`
		Action    string  `json:"action"`
		Details   *string `json:"details"`
		CreatedAt string  `json:"created_at"`
	}

	var result []activityResponse
	for rows.Next() {
		var a activityResponse
		if err := rows.Scan(&a.ID, &a.MissionID, &a.ActorType, &a.ActorID, &a.Action, &a.Details, &a.CreatedAt, &a.ActorName); err != nil {
			h.logger.Error("scan activity", "error", err)
			continue
		}
		result = append(result, a)
	}
	if result == nil {
		result = []activityResponse{}
	}
	writeJSON(w, http.StatusOK, result)
}

// ── 14. DeleteRelation — DELETE /api/v1/relations/{relationId}

func (h *IssueHandler) DeleteRelation(w http.ResponseWriter, r *http.Request) {
	role := RoleFromContext(r.Context())
	if !canRole(role, "create") {
		writeProblem(w, r, http.StatusForbidden, "Forbidden")
		return
	}

	relID := r.PathValue("relationId")
	res, err := h.db.ExecContext(r.Context(), `DELETE FROM mission_relations WHERE id = ?`, relID)
	if err != nil {
		h.logger.Error("delete relation", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		writeProblem(w, r, http.StatusNotFound, "Relation not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ── 15. Start — POST /api/v1/crews/{crewId}/issues/{identifier}/start ──────

func (h *IssueHandler) Start(w http.ResponseWriter, r *http.Request) {
	role := RoleFromContext(r.Context())
	if !canRole(role, "create") {
		writeProblem(w, r, http.StatusForbidden, "Forbidden")
		return
	}

	crewID := r.PathValue("crewId")
	ident := r.PathValue("identifier")
	wsID := WorkspaceIDFromContext(r.Context())

	// 1. Load issue
	var missionID, status, title, leadAgentID string
	var description, assigneeID sql.NullString
	err := h.db.QueryRowContext(r.Context(), `
		SELECT id, status, title, description, assignee_id, lead_agent_id
		FROM missions WHERE identifier = ? AND crew_id = ? AND workspace_id = ?`,
		ident, crewID, wsID).Scan(&missionID, &status, &title, &description, &assigneeID, &leadAgentID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeProblem(w, r, http.StatusNotFound, "Issue not found")
			return
		}
		h.logger.Error("start issue: load", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	// 2. Validate status
	if status != "BACKLOG" && status != "TODO" {
		writeProblem(w, r, http.StatusBadRequest, "Issue must be in BACKLOG or TODO to start (current: "+status+")")
		return
	}

	// 3. Validate assignee
	if !assigneeID.Valid || assigneeID.String == "" {
		writeProblem(w, r, http.StatusBadRequest, "Issue must have an assignee before starting")
		return
	}

	// 3b. Create synthetic chat so assignments can reference it (FK on chat_id)
	var chatExists int
	_ = h.db.QueryRowContext(r.Context(), `SELECT 1 FROM chats WHERE id = ?`, missionID).Scan(&chatExists)
	if chatExists == 0 {
		chatNow := time.Now().UTC().Format(time.RFC3339)
		_, err = h.db.ExecContext(r.Context(), `
			INSERT INTO chats (id, agent_id, workspace_id, title, mode, status, started_at, created_at, updated_at)
			VALUES (?, ?, ?, ?, 'MISSION', 'ACTIVE', ?, ?, ?)`,
			missionID, leadAgentID, wsID, "Issue: "+title, chatNow, chatNow, chatNow)
		if err != nil {
			h.logger.Error("start issue: create synthetic chat", "error", err)
			writeProblem(w, r, http.StatusInternalServerError, "Failed to prepare issue execution")
			return
		}
	}

	// 4. Reset existing tasks to PENDING or create new one
	var taskCount int
	_ = h.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM mission_tasks WHERE mission_id = ?`, missionID).Scan(&taskCount)
	if taskCount > 0 {
		// Reset existing tasks for re-run
		resetNow := time.Now().UTC().Format(time.RFC3339)
		_, _ = h.db.ExecContext(r.Context(), `
			UPDATE mission_tasks SET status = 'PENDING', started_at = NULL, completed_at = NULL,
			duration_ms = NULL, result_summary = NULL, error_message = NULL, assignment_id = NULL,
			iteration = COALESCE(iteration, 0) + 1, updated_at = ?
			WHERE mission_id = ?`, resetNow, missionID)
	} else {
		taskID := generateCUID()
		now := time.Now().UTC().Format(time.RFC3339)
		desc := ""
		if description.Valid {
			desc = description.String
		}
		_, err = h.db.ExecContext(r.Context(), `
			INSERT INTO mission_tasks (id, mission_id, assigned_agent_id, title, description, status, task_order, depends_on, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, 'PENDING', 1, '[]', ?, ?)`,
			taskID, missionID, assigneeID.String, title, desc, now, now)
		if err != nil {
			h.logger.Error("start issue: create task", "error", err)
			writeProblem(w, r, http.StatusInternalServerError, "Failed to create task")
			return
		}
	}

	// 5. Update status → IN_PROGRESS (atomic CAS)
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := h.db.ExecContext(r.Context(), `
		UPDATE missions SET status = 'IN_PROGRESS', updated_at = ? WHERE id = ? AND status IN ('BACKLOG', 'TODO')`,
		now, missionID)
	if err != nil {
		h.logger.Error("start issue: update status", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		writeProblem(w, r, http.StatusConflict, "Issue was already started by another request")
		return
	}

	// 6. Start mission engine (async)
	if h.missionEngine != nil {
		go func() {
			if err := h.missionEngine.StartMission(context.Background(), missionID); err != nil {
				h.logger.Error("start issue: engine", "error", err, "issue", ident)
			}
		}()
	}

	// 7. Broadcast
	if h.hub != nil {
		h.hub.Broadcast("workspace:"+wsID, ws.ServerMessage{
			Type:    "issue.started",
			Channel: "workspace:" + wsID,
			Payload: map[string]string{"id": missionID, "identifier": ident, "status": "IN_PROGRESS"},
		})
	}

	h.logger.Info("issue started", "identifier", ident, "agent", assigneeID.String)
	writeJSON(w, http.StatusOK, map[string]string{"status": "IN_PROGRESS", "identifier": ident})
}

// ── 16. Stop — POST /api/v1/crews/{crewId}/issues/{identifier}/stop ────────

func (h *IssueHandler) Stop(w http.ResponseWriter, r *http.Request) {
	role := RoleFromContext(r.Context())
	if !canRole(role, "create") {
		writeProblem(w, r, http.StatusForbidden, "Forbidden")
		return
	}

	crewID := r.PathValue("crewId")
	ident := r.PathValue("identifier")
	wsID := WorkspaceIDFromContext(r.Context())

	var missionID, status string
	err := h.db.QueryRowContext(r.Context(), `
		SELECT id, status FROM missions WHERE identifier = ? AND crew_id = ? AND workspace_id = ?`,
		ident, crewID, wsID).Scan(&missionID, &status)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeProblem(w, r, http.StatusNotFound, "Issue not found")
			return
		}
		h.logger.Error("stop issue: load", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	if status != "IN_PROGRESS" && status != "REVIEW" {
		writeProblem(w, r, http.StatusBadRequest, "Issue must be IN_PROGRESS or REVIEW to stop (current: "+status+")")
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)

	// Cancel running/pending tasks
	_, _ = h.db.ExecContext(r.Context(), `
		UPDATE mission_tasks SET status = 'CANCELLED', updated_at = ? WHERE mission_id = ? AND status IN ('PENDING', 'IN_PROGRESS', 'BLOCKED')`,
		now, missionID)

	// Update issue status → CANCELLED
	_, err = h.db.ExecContext(r.Context(), `
		UPDATE missions SET status = 'CANCELLED', completed_at = ?, updated_at = ? WHERE id = ?`,
		now, now, missionID)
	if err != nil {
		h.logger.Error("stop issue: update", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	if h.hub != nil {
		h.hub.Broadcast("workspace:"+wsID, ws.ServerMessage{
			Type:    "issue.updated",
			Channel: "workspace:" + wsID,
			Payload: map[string]string{"id": missionID, "identifier": ident, "status": "CANCELLED"},
		})
	}

	h.logger.Info("issue stopped", "identifier", ident)
	writeJSON(w, http.StatusOK, map[string]string{"status": "CANCELLED", "identifier": ident})
}

// ── 17. BulkUpdate — POST /api/v1/issues/bulk-update ──────────────────────

func (h *IssueHandler) BulkUpdate(w http.ResponseWriter, r *http.Request) {
	role := RoleFromContext(r.Context())
	if !canRole(role, "create") {
		writeProblem(w, r, http.StatusForbidden, "Forbidden")
		return
	}

	wsID := WorkspaceIDFromContext(r.Context())
	user := UserFromContext(r.Context())

	var req struct {
		IDs     []string `json:"ids"`
		Updates struct {
			Status       *string   `json:"status"`
			Priority     *string   `json:"priority"`
			AssigneeType *string   `json:"assignee_type"`
			AssigneeID   *string   `json:"assignee_id"`
			ProjectID    *string   `json:"project_id"`
			Labels       *[]string `json:"labels"`
		} `json:"updates"`
	}
	if err := readJSON(r, &req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "Invalid JSON body")
		return
	}
	if len(req.IDs) == 0 {
		writeProblem(w, r, http.StatusBadRequest, "ids is required")
		return
	}
	if len(req.IDs) > 100 {
		writeProblem(w, r, http.StatusBadRequest, "Maximum 100 issues per bulk update")
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)
	updated := 0

	for _, issueID := range req.IDs {
		// Verify issue belongs to workspace
		var currentStatus string
		err := h.db.QueryRowContext(r.Context(),
			`SELECT status FROM missions WHERE id = ? AND workspace_id = ?`,
			issueID, wsID).Scan(&currentStatus)
		if err != nil {
			continue // skip missing issues
		}

		ub := newUpdate()

		if req.Updates.Status != nil {
			newStatus := *req.Updates.Status
			allowed := validIssueTransitions[currentStatus]
			valid := false
			for _, s := range allowed {
				if s == newStatus {
					valid = true
					break
				}
			}
			if !valid {
				continue // skip invalid transitions
			}
			ub.Set("status", newStatus)
			if newStatus == "DONE" || newStatus == "CANCELLED" {
				ub.Set("completed_at", now)
			}
		}
		if req.Updates.Priority != nil {
			ub.Set("priority", *req.Updates.Priority)
		}
		if req.Updates.AssigneeType != nil {
			ub.Set("assignee_type", *req.Updates.AssigneeType)
		}
		if req.Updates.AssigneeID != nil {
			ub.Set("assignee_id", *req.Updates.AssigneeID)
		}
		if req.Updates.ProjectID != nil {
			if *req.Updates.ProjectID == "" {
				ub.SetNull("project_id")
			} else {
				ub.Set("project_id", *req.Updates.ProjectID)
			}
		}

		if ub.Empty() {
			continue
		}

		query, args := ub.Build("missions", "id = ? AND workspace_id = ?", issueID, wsID)
		if _, err := h.db.ExecContext(r.Context(), query, args...); err != nil {
			h.logger.Error("bulk update issue", "error", err, "issue_id", issueID)
			continue
		}

		// Update labels if provided
		if req.Updates.Labels != nil {
			_, _ = h.db.ExecContext(r.Context(), `DELETE FROM mission_labels WHERE mission_id = ?`, issueID)
			for _, labelID := range *req.Updates.Labels {
				_, _ = h.db.ExecContext(r.Context(),
					`INSERT OR IGNORE INTO mission_labels (mission_id, label_id) VALUES (?, ?)`,
					issueID, labelID)
			}
		}

		// Activity log
		if req.Updates.Status != nil {
			actID := generateCUID()
			_, _ = h.db.ExecContext(r.Context(),
				`INSERT INTO mission_activity (id, mission_id, actor_type, actor_id, action, details, created_at) VALUES (?, ?, 'user', ?, 'status_changed', ?, ?)`,
				actID, issueID, user.ID, fmt.Sprintf("%s -> %s (bulk)", currentStatus, *req.Updates.Status), now)
		}

		updated++
	}

	if h.hub != nil && updated > 0 {
		h.hub.Broadcast("workspace:"+wsID, ws.ServerMessage{
			Type:    "issues.bulk_updated",
			Channel: "workspace:" + wsID,
			Payload: map[string]string{"count": fmt.Sprintf("%d", updated)},
		})
	}

	writeJSON(w, http.StatusOK, map[string]int{"updated": updated})
}

// ── 18. ListSubIssues — GET /api/v1/crews/{crewId}/issues/{identifier}/subtasks

func (h *IssueHandler) ListSubIssues(w http.ResponseWriter, r *http.Request) {
	crewID := r.PathValue("crewId")
	ident := r.PathValue("identifier")
	wsID := WorkspaceIDFromContext(r.Context())

	// Resolve identifier to ID
	var parentID string
	err := h.db.QueryRowContext(r.Context(),
		`SELECT id FROM missions WHERE identifier = ? AND crew_id = ? AND workspace_id = ?`,
		ident, crewID, wsID).Scan(&parentID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeProblem(w, r, http.StatusNotFound, "Issue not found")
			return
		}
		h.logger.Error("resolve issue for subtasks", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	rows, err := h.db.QueryContext(r.Context(),
		issueSelectColumns+`
		WHERE m.parent_issue_id = ?
		ORDER BY COALESCE(m.sort_order, 0) ASC, m.created_at ASC`, parentID)
	if err != nil {
		h.logger.Error("list sub-issues", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	defer rows.Close()

	var result []issueResponse
	for rows.Next() {
		issue, err := scanIssue(rows)
		if err != nil {
			h.logger.Error("scan sub-issue", "error", err)
			writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
			return
		}
		result = append(result, issue)
	}
	if err := rows.Err(); err != nil {
		h.logger.Error("rows iteration (sub-issues)", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	if result == nil {
		result = []issueResponse{}
	}
	writeJSON(w, http.StatusOK, result)
}
