package api

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
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
		internalError(w, r, h.logger, "list issues", err)
		return
	}
	defer rows.Close()

	var result []issueResponse
	var issueIDs []string
	for rows.Next() {
		issue, err := scanIssueRow(rows)
		if err != nil {
			internalError(w, r, h.logger, "scan issue", err)
			return
		}
		result = append(result, issue)
		issueIDs = append(issueIDs, issue.ID)
	}
	if err := rows.Err(); err != nil {
		internalError(w, r, h.logger, "rows iteration (issues)", err)
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
		internalError(w, r, h.logger, "get issue", err)
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
		internalError(w, r, h.logger, "get issue by identifier", err)
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
