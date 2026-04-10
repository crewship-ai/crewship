package api

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

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

	query := issueSelectQuery() + `
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
		issue, err := scanIssueRow(rows)
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

	h.broadcastIssueEvent(wsID, "issue.created", map[string]string{"id": id})

	writeJSON(w, http.StatusCreated, resp)
}

// ── 3. Get — GET /api/v1/crews/{crewId}/issues/{identifier} ────────────────

func (h *IssueHandler) Get(w http.ResponseWriter, r *http.Request) {
	crewID := r.PathValue("crewId")
	ident := r.PathValue("identifier")
	wsID := WorkspaceIDFromContext(r.Context())

	issue, err := scanIssueRow(h.db.QueryRowContext(r.Context(),
		issueSelectQuery()+` WHERE m.identifier = ? AND m.crew_id = ? AND m.workspace_id = ?`,
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

	// Load labels
	issue.Labels = h.loadIssueLabels(r.Context(), issue.ID)

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

	issue, err := scanIssueRow(h.db.QueryRowContext(r.Context(),
		issueSelectQuery()+` WHERE m.identifier = ? AND m.workspace_id = ?`,
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
	issue.Labels = h.loadIssueLabels(r.Context(), issue.ID)

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
		if !h.validateStatusTransition(currentStatus, newStatus) {
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

	if ub.Empty() && req.Labels == nil {
		writeProblem(w, r, http.StatusBadRequest, "No fields to update")
		return
	}

	if !ub.Empty() {
		query, args := ub.Build("missions", "id = ?", missionID)
		if _, err := h.db.ExecContext(r.Context(), query, args...); err != nil {
			h.logger.Error("update issue", "error", err)
			writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
			return
		}
	}

	// Update labels if provided
	if req.Labels != nil {
		// Replace all labels: delete existing, insert new
		if _, err := h.db.ExecContext(r.Context(), `DELETE FROM mission_labels WHERE mission_id = ?`, missionID); err != nil {
			h.logger.Error("delete issue labels", "error", err)
		}
		for _, labelID := range *req.Labels {
			if _, err := h.db.ExecContext(r.Context(),
				`INSERT OR IGNORE INTO mission_labels (mission_id, label_id) VALUES (?, ?)`,
				missionID, labelID); err != nil {
				h.logger.Error("insert mission label", "error", err, "mission_id", missionID, "label_id", labelID)
			}
		}
	}

	// Log activity for significant changes
	user := UserFromContext(r.Context())
	actorType := "user"
	actorID := user.ID

	if req.Status != nil {
		h.logActivity(r.Context(), missionID, actorType, actorID, "status_changed",
			fmt.Sprintf("%s → %s", currentStatus, *req.Status))
	}
	if req.AssigneeID != nil {
		h.logActivity(r.Context(), missionID, actorType, actorID, "assignee_changed",
			fmt.Sprintf("assignee_id: %s", *req.AssigneeID))
	}
	if req.Priority != nil {
		h.logActivity(r.Context(), missionID, actorType, actorID, "priority_changed", *req.Priority)
	}

	h.broadcastIssueEvent(wsID, "issue.updated", map[string]string{"id": missionID})

	// Return updated issue
	issue, err := scanIssueRow(h.db.QueryRowContext(r.Context(),
		issueSelectQuery()+` WHERE m.id = ?`, missionID))
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

	h.broadcastIssueEvent(wsID, "issue.deleted", map[string]string{"identifier": ident})

	w.WriteHeader(http.StatusNoContent)
}
