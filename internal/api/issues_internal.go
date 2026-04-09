package api

import (
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/ws"
)

// InternalIssueHandler handles issue endpoints called by the sidecar
// on behalf of agents. Uses internal token auth, not JWT.
type InternalIssueHandler struct {
	db     *sql.DB
	hub    *ws.Hub
	logger *slog.Logger
}

// NewInternalIssueHandler creates a new InternalIssueHandler.
func NewInternalIssueHandler(db *sql.DB, hub *ws.Hub, logger *slog.Logger) *InternalIssueHandler {
	return &InternalIssueHandler{db: db, hub: hub, logger: logger}
}

// List handles GET /api/v1/internal/issues
// Returns issues for a workspace, filtered by crew_id, status, assignee, etc.
func (h *InternalIssueHandler) List(w http.ResponseWriter, r *http.Request) {
	wsID := r.URL.Query().Get("workspace_id")
	if wsID == "" {
		writeProblem(w, r, http.StatusBadRequest, "workspace_id is required")
		return
	}

	limit, offset := parsePagination(r, 50, 100)

	query := `
		SELECT m.id, m.workspace_id, m.crew_id, COALESCE(c.name, ''), COALESCE(c.slug, ''),
		       m.number, m.identifier, m.title, m.description, m.status,
		       COALESCE(m.priority, 'none'), m.assignee_type, m.assignee_id,
		       m.due_date, COALESCE(m.sort_order, 0), COALESCE(m.mission_type, 'mission'),
		       m.lead_agent_id, m.created_at, m.updated_at, m.completed_at
		FROM missions m
		LEFT JOIN crews c ON m.crew_id = c.id
		WHERE m.workspace_id = ?`
	args := []interface{}{wsID}

	if crewID := r.URL.Query().Get("crew_id"); crewID != "" {
		query += " AND m.crew_id = ?"
		args = append(args, crewID)
	}
	if status := r.URL.Query().Get("status"); status != "" {
		vals := strings.Split(status, ",")
		placeholders := make([]string, len(vals))
		for i, v := range vals {
			placeholders[i] = "?"
			args = append(args, strings.TrimSpace(v))
		}
		query += " AND m.status IN (" + strings.Join(placeholders, ",") + ")"
	}
	if assignee := r.URL.Query().Get("assignee_id"); assignee != "" {
		query += " AND m.assignee_id = ?"
		args = append(args, assignee)
	}
	if mtype := r.URL.Query().Get("mission_type"); mtype != "" {
		query += " AND m.mission_type = ?"
		args = append(args, mtype)
	} else {
		query += " AND COALESCE(m.mission_type, 'orchestration') = 'issue'"
	}

	query += " ORDER BY m.created_at DESC LIMIT ? OFFSET ?"
	args = append(args, limit, offset)

	rows, err := h.db.QueryContext(r.Context(), query, args...)
	if err != nil {
		h.logger.Error("internal list issues", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	defer rows.Close()

	var result []issueResponse
	for rows.Next() {
		var i issueResponse
		if err := rows.Scan(
			&i.ID, &i.WorkspaceID, &i.CrewID, &i.CrewName, &i.CrewSlug,
			&i.Number, &i.Identifier, &i.Title, &i.Description, &i.Status,
			&i.Priority, &i.AssigneeType, &i.AssigneeID,
			&i.DueDate, &i.SortOrder, &i.MissionType,
			&i.LeadAgentID, &i.CreatedAt, &i.UpdatedAt, &i.CompletedAt,
		); err != nil {
			h.logger.Error("scan internal issue", "error", err)
			continue
		}
		i.Labels = []labelResponse{}
		result = append(result, i)
	}
	if result == nil {
		result = []issueResponse{}
	}
	writeJSON(w, http.StatusOK, result)
}

// Get handles GET /api/v1/internal/issues/{identifier}
func (h *InternalIssueHandler) Get(w http.ResponseWriter, r *http.Request) {
	ident := r.PathValue("identifier")
	wsID := r.URL.Query().Get("workspace_id")
	if wsID == "" {
		writeProblem(w, r, http.StatusBadRequest, "workspace_id is required")
		return
	}

	var issue issueResponse
	err := h.db.QueryRowContext(r.Context(), `
		SELECT m.id, m.workspace_id, m.crew_id, COALESCE(c.name, ''), COALESCE(c.slug, ''),
		       m.number, m.identifier, m.title, m.description, m.status,
		       COALESCE(m.priority, 'none'), m.assignee_type, m.assignee_id,
		       m.due_date, COALESCE(m.sort_order, 0), COALESCE(m.mission_type, 'mission'),
		       m.lead_agent_id, m.created_at, m.updated_at, m.completed_at
		FROM missions m
		LEFT JOIN crews c ON m.crew_id = c.id
		WHERE m.identifier = ? AND m.workspace_id = ?`,
		ident, wsID).Scan(
		&issue.ID, &issue.WorkspaceID, &issue.CrewID, &issue.CrewName, &issue.CrewSlug,
		&issue.Number, &issue.Identifier, &issue.Title, &issue.Description, &issue.Status,
		&issue.Priority, &issue.AssigneeType, &issue.AssigneeID,
		&issue.DueDate, &issue.SortOrder, &issue.MissionType,
		&issue.LeadAgentID, &issue.CreatedAt, &issue.UpdatedAt, &issue.CompletedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeProblem(w, r, http.StatusNotFound, "Issue not found")
			return
		}
		h.logger.Error("internal get issue", "error", err)
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

	// Load comment count
	_ = h.db.QueryRowContext(r.Context(),
		`SELECT COUNT(*) FROM mission_comments WHERE mission_id = ?`,
		issue.ID).Scan(&issue.CommentCount)

	writeJSON(w, http.StatusOK, issue)
}

// Create handles POST /api/v1/internal/issues
// Allows agents to create issues via the sidecar.
func (h *InternalIssueHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req struct {
		WorkspaceID  string   `json:"workspace_id"`
		CrewID       string   `json:"crew_id"`
		Title        string   `json:"title"`
		Description  *string  `json:"description"`
		Priority     string   `json:"priority"`
		AssigneeType *string  `json:"assignee_type"`
		AssigneeID   *string  `json:"assignee_id"`
		Labels       []string `json:"labels"`
	}
	if err := readJSON(r, &req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "Invalid JSON body")
		return
	}
	if req.Title == "" || req.CrewID == "" || req.WorkspaceID == "" {
		writeProblem(w, r, http.StatusBadRequest, "title, crew_id, and workspace_id are required")
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
		req.CrewID, req.WorkspaceID).Scan(&issuePrefix, &crewSlug)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeProblem(w, r, http.StatusNotFound, "Crew not found")
			return
		}
		h.logger.Error("get crew for issue", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	prefix := issuePrefix.String
	if prefix == "" {
		slug := strings.ToUpper(crewSlug)
		if len(slug) > 3 {
			slug = slug[:3]
		}
		prefix = slug
	}

	// Atomic counter
	var number int
	err = tx.QueryRowContext(r.Context(),
		`INSERT INTO issue_counters(crew_id, next_number) VALUES(?, 1)
		 ON CONFLICT(crew_id) DO UPDATE SET next_number = issue_counters.next_number + 1
		 RETURNING next_number`, req.CrewID).Scan(&number)
	if err != nil {
		h.logger.Error("issue counter", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	identifier := prefix + "-" + strconv.Itoa(number)

	// Find lead agent
	var leadAgentID string
	err = tx.QueryRowContext(r.Context(),
		`SELECT id FROM agents WHERE crew_id = ? AND agent_role = 'LEAD' AND deleted_at IS NULL LIMIT 1`,
		req.CrewID).Scan(&leadAgentID)
	if err != nil {
		h.logger.Error("find lead agent", "error", err)
		writeProblem(w, r, http.StatusBadRequest, "Crew has no LEAD agent")
		return
	}

	id := generateCUID()
	traceID := "issue-" + generateCUID()
	now := time.Now().UTC().Format(time.RFC3339)

	_, err = tx.ExecContext(r.Context(), `
		INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id,
		                      title, description, status, number, identifier,
		                      priority, assignee_type, assignee_id,
		                      sort_order, mission_type, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, 'BACKLOG', ?, ?, ?, ?, ?, 0, 'issue', ?, ?)`,
		id, req.WorkspaceID, req.CrewID, leadAgentID, traceID,
		req.Title, req.Description, number, identifier,
		req.Priority, req.AssigneeType, req.AssigneeID,
		now, now)
	if err != nil {
		h.logger.Error("insert issue", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	// Labels
	for _, labelID := range req.Labels {
		if _, err := tx.ExecContext(r.Context(),
			`INSERT OR IGNORE INTO mission_labels(mission_id, label_id) VALUES(?, ?)`,
			id, labelID); err != nil {
			h.logger.Error("insert issue label", "issue_id", id, "error", err)
		}
	}

	if err := tx.Commit(); err != nil {
		h.logger.Error("commit issue", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	if h.hub != nil {
		h.hub.Broadcast("workspace:"+req.WorkspaceID, ws.ServerMessage{
			Type:    "issue.created",
			Channel: "workspace:" + req.WorkspaceID,
			Payload: map[string]string{"id": id, "identifier": identifier, "title": req.Title},
		})
	}

	writeJSON(w, http.StatusCreated, map[string]string{
		"id":         id,
		"identifier": identifier,
		"status":     "BACKLOG",
	})
}

// UpdateStatus handles PATCH /api/v1/internal/issues/{identifier}
// Allows agents to update issue status and add comments.
func (h *InternalIssueHandler) UpdateStatus(w http.ResponseWriter, r *http.Request) {
	ident := r.PathValue("identifier")

	var req struct {
		WorkspaceID string  `json:"workspace_id"`
		Status      string  `json:"status"`
		Priority    string  `json:"priority"`
		Comment     *string `json:"comment"`
		AgentID     string  `json:"agent_id"`
	}
	if err := readJSON(r, &req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "Invalid JSON body")
		return
	}
	if req.WorkspaceID == "" {
		writeProblem(w, r, http.StatusBadRequest, "workspace_id is required")
		return
	}

	// Find the issue
	var missionID, currentStatus string
	err := h.db.QueryRowContext(r.Context(),
		`SELECT id, status FROM missions WHERE identifier = ? AND workspace_id = ?`,
		ident, req.WorkspaceID).Scan(&missionID, &currentStatus)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeProblem(w, r, http.StatusNotFound, "Issue not found")
			return
		}
		h.logger.Error("find issue for update", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	ub := newUpdate()

	if req.Status != "" && req.Status != currentStatus {
		allowed := validIssueTransitions[currentStatus]
		valid := false
		for _, a := range allowed {
			if a == req.Status {
				valid = true
				break
			}
		}
		if !valid {
			writeProblem(w, r, http.StatusBadRequest, "Invalid status transition from "+currentStatus+" to "+req.Status)
			return
		}
		ub.Set("status", req.Status)
		if req.Status == "DONE" || req.Status == "CANCELLED" {
			ub.Set("completed_at", time.Now().UTC().Format(time.RFC3339))
		}
	}
	if req.Priority != "" {
		ub.Set("priority", req.Priority)
	}

	if !ub.Empty() {
		query, args := ub.Build("missions", "id = ?", missionID)
		if _, err := h.db.ExecContext(r.Context(), query, args...); err != nil {
			h.logger.Error("update issue", "error", err)
			writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
			return
		}
	}

	// Add comment if provided
	if req.Comment != nil && *req.Comment != "" {
		commentID := generateCUID()
		now := time.Now().UTC().Format(time.RFC3339)
		authorType := "agent"
		authorID := req.AgentID
		if authorID == "" {
			authorID = "system"
		}
		_, err := h.db.ExecContext(r.Context(), `
			INSERT INTO mission_comments (id, mission_id, author_type, author_id, body, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			commentID, missionID, authorType, authorID, *req.Comment, now, now)
		if err != nil {
			h.logger.Error("insert internal comment", "error", err)
		}
	}

	if h.hub != nil {
		h.hub.Broadcast("workspace:"+req.WorkspaceID, ws.ServerMessage{
			Type:    "issue.updated",
			Channel: "workspace:" + req.WorkspaceID,
			Payload: map[string]string{"id": missionID, "identifier": ident},
		})
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// CreateComment handles POST /api/v1/internal/issues/{identifier}/comments
// Allows agents to comment on issues.
func (h *InternalIssueHandler) CreateComment(w http.ResponseWriter, r *http.Request) {
	ident := r.PathValue("identifier")

	var req struct {
		WorkspaceID string `json:"workspace_id"`
		AgentID     string `json:"agent_id"`
		Body        string `json:"body"`
	}
	if err := readJSON(r, &req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "Invalid JSON body")
		return
	}
	if req.Body == "" || req.WorkspaceID == "" {
		writeProblem(w, r, http.StatusBadRequest, "body and workspace_id are required")
		return
	}

	var missionID string
	err := h.db.QueryRowContext(r.Context(),
		`SELECT id FROM missions WHERE identifier = ? AND workspace_id = ?`,
		ident, req.WorkspaceID).Scan(&missionID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeProblem(w, r, http.StatusNotFound, "Issue not found")
			return
		}
		h.logger.Error("find issue for comment", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	commentID := generateCUID()
	now := time.Now().UTC().Format(time.RFC3339)
	authorType := "agent"
	authorID := req.AgentID
	if authorID == "" {
		authorID = "system"
	}

	_, err = h.db.ExecContext(r.Context(), `
		INSERT INTO mission_comments (id, mission_id, author_type, author_id, body, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		commentID, missionID, authorType, authorID, req.Body, now, now)
	if err != nil {
		h.logger.Error("insert comment", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	if h.hub != nil {
		h.hub.Broadcast("workspace:"+req.WorkspaceID, ws.ServerMessage{
			Type:    "issue.updated",
			Channel: "workspace:" + req.WorkspaceID,
			Payload: map[string]string{"id": missionID, "identifier": ident},
		})
	}

	writeJSON(w, http.StatusCreated, commentResponse{
		ID:         commentID,
		MissionID:  missionID,
		AuthorType: authorType,
		AuthorID:   authorID,
		Body:       req.Body,
		CreatedAt:  now,
		UpdatedAt:  now,
	})
}
