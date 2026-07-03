package api

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
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

// logActivity mirrors IssueHandler.logActivity's mission_activity insert so
// agent-driven changes leave the same audit trail humans do. Best-effort:
// errors are logged, never returned — the mutation itself already landed.
// (No journal emit here; the internal handler has no journal wired, and the
// activity row is what the issue UI's feed reads.)
func (h *InternalIssueHandler) logActivity(ctx context.Context, missionID, actorType, actorID, action, details string) {
	actID := generateCUID()
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := h.db.ExecContext(ctx,
		`INSERT INTO mission_activity (id, mission_id, actor_type, actor_id, action, details, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		actID, missionID, actorType, actorID, action, details, now); err != nil {
		h.logger.Error("insert mission activity", "action", action, "mission_id", missionID, "error", err)
	}
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
		       m.lead_agent_id, m.created_at, m.updated_at, m.completed_at,
		       m.author_agent_id, m.created_by_user_id, m.authored_via,
		       CASE
		           WHEN m.author_agent_id IS NOT NULL THEN (SELECT name FROM agents WHERE id = m.author_agent_id)
		           WHEN m.created_by_user_id IS NOT NULL THEN (SELECT full_name FROM users WHERE id = m.created_by_user_id)
		       END
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
		for _, v := range vals {
			args = append(args, strings.TrimSpace(v))
		}
		query += " AND m.status IN (" + sqlPlaceholders(len(vals)) + ")"
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
		internalError(w, r, h.logger, "internal list issues", err)
		return
	}
	defer rows.Close()

	var result []issueResponse
	for rows.Next() {
		var i issueResponse
		var authorAgentID, createdByUserID, authoredVia, creatorName sql.NullString
		if err := rows.Scan(
			&i.ID, &i.WorkspaceID, &i.CrewID, &i.CrewName, &i.CrewSlug,
			&i.Number, &i.Identifier, &i.Title, &i.Description, &i.Status,
			&i.Priority, &i.AssigneeType, &i.AssigneeID,
			&i.DueDate, &i.SortOrder, &i.MissionType,
			&i.LeadAgentID, &i.CreatedAt, &i.UpdatedAt, &i.CompletedAt,
			&authorAgentID, &createdByUserID, &authoredVia, &creatorName,
		); err != nil {
			h.logger.Error("scan internal issue", "error", err)
			continue
		}
		i.Labels = []labelResponse{}
		i.CreatedBy = buildIssueCreator(authorAgentID, createdByUserID, creatorName)
		if authoredVia.Valid && authoredVia.String != "" {
			i.AuthoredVia = &authoredVia.String
		}
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
	var authorAgentID, createdByUserID, authoredVia, creatorName sql.NullString
	err := h.db.QueryRowContext(r.Context(), `
		SELECT m.id, m.workspace_id, m.crew_id, COALESCE(c.name, ''), COALESCE(c.slug, ''),
		       m.number, m.identifier, m.title, m.description, m.status,
		       COALESCE(m.priority, 'none'), m.assignee_type, m.assignee_id,
		       m.due_date, COALESCE(m.sort_order, 0), COALESCE(m.mission_type, 'mission'),
		       m.lead_agent_id, m.created_at, m.updated_at, m.completed_at,
		       m.author_agent_id, m.created_by_user_id, m.authored_via,
		       CASE
		           WHEN m.author_agent_id IS NOT NULL THEN (SELECT name FROM agents WHERE id = m.author_agent_id)
		           WHEN m.created_by_user_id IS NOT NULL THEN (SELECT full_name FROM users WHERE id = m.created_by_user_id)
		       END
		FROM missions m
		LEFT JOIN crews c ON m.crew_id = c.id
		WHERE m.identifier = ? AND m.workspace_id = ?`,
		ident, wsID).Scan(
		&issue.ID, &issue.WorkspaceID, &issue.CrewID, &issue.CrewName, &issue.CrewSlug,
		&issue.Number, &issue.Identifier, &issue.Title, &issue.Description, &issue.Status,
		&issue.Priority, &issue.AssigneeType, &issue.AssigneeID,
		&issue.DueDate, &issue.SortOrder, &issue.MissionType,
		&issue.LeadAgentID, &issue.CreatedAt, &issue.UpdatedAt, &issue.CompletedAt,
		&authorAgentID, &createdByUserID, &authoredVia, &creatorName,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeProblem(w, r, http.StatusNotFound, "Issue not found")
			return
		}
		internalError(w, r, h.logger, "internal get issue", err)
		return
	}
	issue.CreatedBy = buildIssueCreator(authorAgentID, createdByUserID, creatorName)
	if authoredVia.Valid && authoredVia.String != "" {
		issue.AuthoredVia = &authoredVia.String
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
	if err := h.db.QueryRowContext(r.Context(),
		`SELECT COUNT(*) FROM mission_comments WHERE mission_id = ?`,
		issue.ID).Scan(&issue.CommentCount); err != nil {
		h.logger.Error("load comment count", "issue_id", issue.ID, "error", err)
	}

	writeJSON(w, http.StatusOK, issue)
}

// Create handles POST /api/v1/internal/issues
// Allows agents to create issues via the sidecar.
func (h *InternalIssueHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req struct {
		WorkspaceID   string   `json:"workspace_id"`
		CrewID        string   `json:"crew_id"`
		Title         string   `json:"title"`
		Description   *string  `json:"description"`
		Priority      string   `json:"priority"`
		AssigneeType  *string  `json:"assignee_type"`
		AssigneeID    *string  `json:"assignee_id"`
		AuthorAgentID string   `json:"author_agent_id"`
		AuthorChatID  string   `json:"author_chat_id"`
		AuthorRunID   string   `json:"author_run_id"`
		Labels        []string `json:"labels"`
	}
	if err := readJSON(r, &req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "Invalid JSON body")
		return
	}
	if req.Title == "" || req.CrewID == "" || req.WorkspaceID == "" {
		writeProblem(w, r, http.StatusBadRequest, "title, crew_id, and workspace_id are required")
		return
	}
	// PR-F24 F-4: a bound token may only write into its own workspace.
	// requireInternal sees only the query string; this guards the
	// body-carried workspace_id (403 on a foreign tenant).
	if !assertInternalTokenWorkspace(w, r, req.WorkspaceID) {
		return
	}
	if req.Priority == "" {
		req.Priority = "none"
	}

	// SECURITY (defense-in-depth): when an author agent is supplied, verify it
	// actually belongs to the supplied crew+workspace. Without this, a
	// compromised agent could create an issue in another crew (cross-crew
	// override). The sidecar always forwards its trusted IPC agent identity.
	if req.AuthorAgentID != "" {
		var exists int
		err := h.db.QueryRowContext(r.Context(),
			`SELECT 1 FROM agents WHERE id = ? AND crew_id = ? AND workspace_id = ? AND deleted_at IS NULL`,
			req.AuthorAgentID, req.CrewID, req.WorkspaceID).Scan(&exists)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				writeProblem(w, r, http.StatusBadRequest, "author agent does not belong to the specified crew/workspace")
				return
			}
			internalError(w, r, h.logger, "validate author agent", err)
			return
		}
	}

	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		internalError(w, r, h.logger, "begin tx", err)
		return
	}
	defer tx.Rollback() //nolint:errcheck

	// Creator attribution (v129): this endpoint is only reachable through
	// the sidecar's trusted IPC identity, so a supplied author_agent_id
	// (validated against crew+workspace above) is THE creator and the
	// channel is always the agent tool call. Chat/run provenance rides
	// along when the sidecar has it (v108 columns). All issue creation goes
	// through insertIssueTx — the single chokepoint shared with the
	// recurring-issue dispatcher.
	id, identifier, err := insertIssueTx(r.Context(), tx, h.logger, issueSpec{
		WorkspaceID:   req.WorkspaceID,
		CrewID:        req.CrewID,
		Title:         req.Title,
		Description:   req.Description,
		Priority:      req.Priority,
		AssigneeType:  req.AssigneeType,
		AssigneeID:    req.AssigneeID,
		Labels:        req.Labels,
		AuthoredVia:   "agent_tool_call",
		AuthorAgentID: req.AuthorAgentID,
		AuthorChatID:  req.AuthorChatID,
		AuthorRunID:   req.AuthorRunID,
	})
	switch {
	case errors.Is(err, errIssueCrewNotFound):
		writeProblem(w, r, http.StatusNotFound, "Crew not found")
		return
	case errors.Is(err, errIssueNoLeadAgent):
		h.logger.Error("find lead agent", "crew_id", req.CrewID)
		writeProblem(w, r, http.StatusBadRequest, "Crew has no LEAD agent")
		return
	case err != nil:
		internalError(w, r, h.logger, "insert issue", err)
		return
	}

	if err := tx.Commit(); err != nil {
		internalError(w, r, h.logger, "commit issue", err)
		return
	}

	broadcastWorkspaceEvent(h.hub, req.WorkspaceID, "issue.created", map[string]string{"id": id, "identifier": identifier, "title": req.Title})

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
	// PR-F24 F-4: bound token may only update its own workspace's issues.
	if !assertInternalTokenWorkspace(w, r, req.WorkspaceID) {
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
		internalError(w, r, h.logger, "find issue for update", err)
		return
	}

	// Comments must carry a real author. mission_comments' CHECK only
	// allows ('user','agent'), so an empty agent_id can't be attributed —
	// pre-fix it was misfiled as an agent literally named "system".
	// Reject up front, before any mutation lands.
	if req.Comment != nil && *req.Comment != "" && req.AgentID == "" {
		writeProblem(w, r, http.StatusBadRequest, "agent_id is required when adding a comment")
		return
	}

	// Actor identity for the audit trail. The sidecar forwards its trusted
	// IPC agent identity as agent_id; an empty value means a non-agent
	// internal caller, attributed to "system" (mission_activity's CHECK
	// allows it) rather than misfiled as an agent named "system".
	actorType := "agent"
	actorID := req.AgentID
	if actorID == "" {
		actorType = "system"
		actorID = "system"
	}

	ub := newUpdate()
	statusChanged := false

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
		statusChanged = true
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
			internalError(w, r, h.logger, "update issue", err)
			return
		}

		// Audit trail: mirror the human handlers' logActivity rows so
		// agent-driven changes are just as visible in the activity feed.
		// Best-effort — the update itself already committed.
		if statusChanged {
			h.logActivity(r.Context(), missionID, actorType, actorID,
				"status_changed", currentStatus+" → "+req.Status)
		}
		if req.Priority != "" {
			h.logActivity(r.Context(), missionID, actorType, actorID,
				"priority_changed", req.Priority)
		}
	}

	// Add comment if provided. agent_id presence was validated above, so
	// the author is always a real agent here.
	if req.Comment != nil && *req.Comment != "" {
		commentID := generateCUID()
		now := time.Now().UTC().Format(time.RFC3339)
		_, err := h.db.ExecContext(r.Context(), `
			INSERT INTO mission_comments (id, mission_id, author_type, author_id, body, created_at, updated_at)
			VALUES (?, ?, 'agent', ?, ?, ?, ?)`,
			commentID, missionID, req.AgentID, *req.Comment, now, now)
		if err != nil {
			h.logger.Error("insert internal comment", "error", err)
		}
	}

	broadcastWorkspaceEvent(h.hub, req.WorkspaceID, "issue.updated", map[string]string{"id": missionID, "identifier": ident})

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
	// Comments must carry a real author: mission_comments' CHECK only
	// allows ('user','agent'). Pre-fix, an empty agent_id was misfiled as
	// an agent literally named "system" — reject instead.
	if req.AgentID == "" {
		writeProblem(w, r, http.StatusBadRequest, "agent_id is required")
		return
	}
	// PR-F24 F-4: bound token may only comment on its own workspace's issues.
	if !assertInternalTokenWorkspace(w, r, req.WorkspaceID) {
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
		internalError(w, r, h.logger, "find issue for comment", err)
		return
	}

	commentID := generateCUID()
	now := time.Now().UTC().Format(time.RFC3339)
	// agent_id presence was validated above — the author is always a real agent.
	authorType := "agent"
	authorID := req.AgentID

	_, err = h.db.ExecContext(r.Context(), `
		INSERT INTO mission_comments (id, mission_id, author_type, author_id, body, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		commentID, missionID, authorType, authorID, req.Body, now, now)
	if err != nil {
		internalError(w, r, h.logger, "insert comment", err)
		return
	}

	broadcastWorkspaceEvent(h.hub, req.WorkspaceID, "issue.updated", map[string]string{"id": missionID, "identifier": ident})

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
