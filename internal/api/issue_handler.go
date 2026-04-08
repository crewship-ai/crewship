package api

import (
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
type IssueHandler struct {
	db     *sql.DB
	hub    *ws.Hub
	logger *slog.Logger
}

// NewIssueHandler creates a new IssueHandler.
func NewIssueHandler(db *sql.DB, hub *ws.Hub, logger *slog.Logger) *IssueHandler {
	return &IssueHandler{db: db, hub: hub, logger: logger}
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
	Labels       []labelResponse `json:"labels"`
	CommentCount int             `json:"comment_count"`
}

type labelResponse struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	Color      string  `json:"color"`
	LabelGroup *string `json:"label_group"`
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
	"REVIEW":      {"DONE", "IN_PROGRESS", "FAILED", "CANCELLED"},
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

	query := `
		SELECT m.id, m.workspace_id, m.crew_id, COALESCE(c.name, ''), COALESCE(c.slug, ''),
		       m.number, m.identifier, m.title, m.description, m.status,
		       COALESCE(m.priority, 'none'), m.assignee_type, m.assignee_id,
		       CASE
		         WHEN m.assignee_type = 'user' THEN (SELECT full_name FROM users WHERE id = m.assignee_id)
		         WHEN m.assignee_type = 'agent' THEN (SELECT name FROM agents WHERE id = m.assignee_id)
		       END,
		       m.due_date, COALESCE(m.sort_order, 0), COALESCE(m.mission_type, 'mission'),
		       m.lead_agent_id, m.created_at, m.updated_at, m.completed_at
		FROM missions m
		LEFT JOIN crews c ON m.crew_id = c.id
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
		var issue issueResponse
		if err := rows.Scan(
			&issue.ID, &issue.WorkspaceID, &issue.CrewID, &issue.CrewName, &issue.CrewSlug,
			&issue.Number, &issue.Identifier, &issue.Title, &issue.Description, &issue.Status,
			&issue.Priority, &issue.AssigneeType, &issue.AssigneeID, &issue.AssigneeName,
			&issue.DueDate, &issue.SortOrder, &issue.MissionType,
			&issue.LeadAgentID, &issue.CreatedAt, &issue.UpdatedAt, &issue.CompletedAt,
		); err != nil {
			h.logger.Error("scan issue", "error", err)
			writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
			return
		}
		issue.Labels = []labelResponse{}
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
		Title        string   `json:"title"`
		Description  *string  `json:"description"`
		Priority     string   `json:"priority"`
		AssigneeType *string  `json:"assignee_type"`
		AssigneeID   *string  `json:"assignee_id"`
		DueDate      *string  `json:"due_date"`
		Labels       []string `json:"labels"`
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
		    assignee_type, assignee_id, due_date, sort_order, mission_type,
		    created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, 'BACKLOG', ?, ?, ?, ?, ?, ?, 0, 'issue', ?, ?)`,
		id, wsID, crewID, leadAgentID, traceID,
		req.Title, req.Description, issueNumber, identifier, req.Priority,
		req.AssigneeType, req.AssigneeID, req.DueDate,
		now, now)
	if err != nil {
		h.logger.Error("insert issue", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	// Insert label associations
	for _, labelID := range req.Labels {
		_, err = tx.ExecContext(r.Context(),
			`INSERT INTO mission_labels (id, mission_id, label_id, created_at) VALUES (?, ?, ?, ?)`,
			generateCUID(), id, labelID, now)
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
		ID:           id,
		WorkspaceID:  wsID,
		CrewID:       crewID,
		Number:       &issueNumber,
		Identifier:   &identifier,
		Title:        req.Title,
		Description:  req.Description,
		Status:       "BACKLOG",
		Priority:     req.Priority,
		AssigneeType: req.AssigneeType,
		AssigneeID:   req.AssigneeID,
		DueDate:      req.DueDate,
		SortOrder:    0,
		MissionType:  "issue",
		LeadAgentID:  leadAgentID,
		CreatedAt:    now,
		UpdatedAt:    now,
		Labels:       []labelResponse{},
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

	var issue issueResponse
	err := h.db.QueryRowContext(r.Context(), `
		SELECT m.id, m.workspace_id, m.crew_id, COALESCE(c.name, ''), COALESCE(c.slug, ''),
		       m.number, m.identifier, m.title, m.description, m.status,
		       COALESCE(m.priority, 'none'), m.assignee_type, m.assignee_id,
		       CASE
		         WHEN m.assignee_type = 'user' THEN (SELECT full_name FROM users WHERE id = m.assignee_id)
		         WHEN m.assignee_type = 'agent' THEN (SELECT name FROM agents WHERE id = m.assignee_id)
		       END,
		       m.due_date, COALESCE(m.sort_order, 0), COALESCE(m.mission_type, 'mission'),
		       m.lead_agent_id, m.created_at, m.updated_at, m.completed_at
		FROM missions m
		LEFT JOIN crews c ON m.crew_id = c.id
		WHERE m.identifier = ? AND m.crew_id = ? AND m.workspace_id = ?`,
		ident, crewID, wsID).Scan(
		&issue.ID, &issue.WorkspaceID, &issue.CrewID, &issue.CrewName, &issue.CrewSlug,
		&issue.Number, &issue.Identifier, &issue.Title, &issue.Description, &issue.Status,
		&issue.Priority, &issue.AssigneeType, &issue.AssigneeID, &issue.AssigneeName,
		&issue.DueDate, &issue.SortOrder, &issue.MissionType,
		&issue.LeadAgentID, &issue.CreatedAt, &issue.UpdatedAt, &issue.CompletedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeProblem(w, r, http.StatusNotFound, "Issue not found")
			return
		}
		h.logger.Error("get issue", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	// Load labels
	issue.Labels = []labelResponse{}
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

	var issue issueResponse
	err := h.db.QueryRowContext(r.Context(), `
		SELECT m.id, m.workspace_id, m.crew_id, COALESCE(c.name, ''), COALESCE(c.slug, ''),
		       m.number, m.identifier, m.title, m.description, m.status,
		       COALESCE(m.priority, 'none'), m.assignee_type, m.assignee_id,
		       CASE
		         WHEN m.assignee_type = 'user' THEN (SELECT full_name FROM users WHERE id = m.assignee_id)
		         WHEN m.assignee_type = 'agent' THEN (SELECT name FROM agents WHERE id = m.assignee_id)
		       END,
		       m.due_date, COALESCE(m.sort_order, 0), COALESCE(m.mission_type, 'mission'),
		       m.lead_agent_id, m.created_at, m.updated_at, m.completed_at
		FROM missions m
		LEFT JOIN crews c ON m.crew_id = c.id
		WHERE m.identifier = ? AND m.workspace_id = ?`,
		ident, wsID).Scan(
		&issue.ID, &issue.WorkspaceID, &issue.CrewID, &issue.CrewName, &issue.CrewSlug,
		&issue.Number, &issue.Identifier, &issue.Title, &issue.Description, &issue.Status,
		&issue.Priority, &issue.AssigneeType, &issue.AssigneeID, &issue.AssigneeName,
		&issue.DueDate, &issue.SortOrder, &issue.MissionType,
		&issue.LeadAgentID, &issue.CreatedAt, &issue.UpdatedAt, &issue.CompletedAt,
	)
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
		Title        *string  `json:"title"`
		Description  *string  `json:"description"`
		Status       *string  `json:"status"`
		Priority     *string  `json:"priority"`
		AssigneeType *string  `json:"assignee_type"`
		AssigneeID   *string  `json:"assignee_id"`
		DueDate      *string  `json:"due_date"`
		SortOrder    *float64 `json:"sort_order"`
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

	if h.hub != nil {
		h.hub.Broadcast("workspace:"+wsID, ws.ServerMessage{
			Type:    "issue.updated",
			Channel: "workspace:" + wsID,
			Payload: map[string]string{"id": missionID},
		})
	}

	// Return updated issue
	var issue issueResponse
	err = h.db.QueryRowContext(r.Context(), `
		SELECT m.id, m.workspace_id, m.crew_id, COALESCE(c.name, ''), COALESCE(c.slug, ''),
		       m.number, m.identifier, m.title, m.description, m.status,
		       COALESCE(m.priority, 'none'), m.assignee_type, m.assignee_id,
		       CASE
		         WHEN m.assignee_type = 'user' THEN (SELECT full_name FROM users WHERE id = m.assignee_id)
		         WHEN m.assignee_type = 'agent' THEN (SELECT name FROM agents WHERE id = m.assignee_id)
		       END,
		       m.due_date, COALESCE(m.sort_order, 0), COALESCE(m.mission_type, 'mission'),
		       m.lead_agent_id, m.created_at, m.updated_at, m.completed_at
		FROM missions m
		LEFT JOIN crews c ON m.crew_id = c.id
		WHERE m.id = ?`, missionID).Scan(
		&issue.ID, &issue.WorkspaceID, &issue.CrewID, &issue.CrewName, &issue.CrewSlug,
		&issue.Number, &issue.Identifier, &issue.Title, &issue.Description, &issue.Status,
		&issue.Priority, &issue.AssigneeType, &issue.AssigneeID, &issue.AssigneeName,
		&issue.DueDate, &issue.SortOrder, &issue.MissionType,
		&issue.LeadAgentID, &issue.CreatedAt, &issue.UpdatedAt, &issue.CompletedAt,
	)
	if err != nil {
		h.logger.Error("read updated issue", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	issue.Labels = []labelResponse{}

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
		`INSERT INTO labels (id, workspace_id, name, color, label_group, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, wsID, req.Name, req.Color, req.LabelGroup, now, now)
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
