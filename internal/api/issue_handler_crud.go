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

	// Pagination — the S1 convention: `?limit=&offset=`, clamped, with the
	// total published in X-Total-Count so a client never has to count what
	// it received (the board used to say "100 issues" at 1 015).
	limit, offset := parsePagination(r, 50, 500)

	// The WHERE clause is built once and used twice: for the page and for
	// the COUNT(*) that feeds X-Total-Count. Keeping them apart is how the
	// two drift — a filter that only reaches one of them makes the count
	// lie about the list.
	where := ` WHERE m.workspace_id = ?`
	args := []interface{}{wsID}

	// Default filter: only issues unless explicitly overridden
	missionType := r.URL.Query().Get("mission_type")
	if missionType == "" {
		missionType = "issue"
	}
	where += " AND COALESCE(m.mission_type, 'mission') = ?"
	args = append(args, missionType)

	// Status filter (comma-separated)
	if statusParam := r.URL.Query().Get("status"); statusParam != "" {
		statuses := strings.Split(statusParam, ",")
		placeholders := make([]string, len(statuses))
		for i, s := range statuses {
			placeholders[i] = "?"
			whereArgs = append(whereArgs, strings.TrimSpace(s))
		}
		where += " AND m.status IN (" + strings.Join(placeholders, ",") + ")"
	}

	// Priority filter (comma-separated)
	if priorityParam := r.URL.Query().Get("priority"); priorityParam != "" {
		priorities := strings.Split(priorityParam, ",")
		placeholders := make([]string, len(priorities))
		for i, p := range priorities {
			placeholders[i] = "?"
			whereArgs = append(whereArgs, strings.TrimSpace(p))
		}
		where += " AND m.priority IN (" + strings.Join(placeholders, ",") + ")"
	}

	// Project filter
	if projectID := r.URL.Query().Get("project_id"); projectID != "" {
		where += " AND m.project_id = ?"
		args = append(args, projectID)
	}

	// Crew filter
	if crewID := r.URL.Query().Get("crew_id"); crewID != "" {
		where += " AND m.crew_id = ?"
		args = append(args, crewID)
	}

	// Assignee filter
	if assigneeID := r.URL.Query().Get("assignee_id"); assigneeID != "" {
		where += " AND m.assignee_id = ?"
		args = append(args, assigneeID)
	}

	// Label filter
	if labelName := r.URL.Query().Get("label"); labelName != "" {
		where += " AND m.id IN (SELECT ml.mission_id FROM mission_labels ml JOIN labels l ON ml.label_id = l.id WHERE l.name = ?)"
		args = append(args, labelName)
	}

	// Search. `q` is the S1 spelling shared by every list; `search` stays
	// as the older alias. It matches the title and the identifier, so a
	// person who types "ENG-4" or "launch" finds the issue either way —
	// the board's search box used to filter only the 100 rows it had loaded.
	if search := issueSearchTerm(r); search != "" {
		where += " AND (m.title LIKE ? OR m.identifier LIKE ?)"
		args = append(args, "%"+search+"%", "%"+search+"%")
	}

	var total int
	if err := h.db.QueryRowContext(r.Context(),
		"SELECT COUNT(*) FROM missions m"+where, args...).Scan(&total); err != nil {
		internalError(w, r, h.logger, "count issues", err)
		return
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
	query := issueSelectQuery() + where + " ORDER BY " + sortCol + " DESC LIMIT ? OFFSET ?"
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
	writeListMeta(w, total, limit, offset)
	// #2286 shipped X-Has-More next to X-Total-Count before the S1 headers
	// existed; it stays, so a client that only reads that boolean keeps working.
	w.Header().Set("X-Has-More", strconv.FormatBool(offset+len(result) < total))
	writeJSON(w, http.StatusOK, result)
}

// issueSearchTerm reads the free-text search of an issue list: `q` first,
// then the older `search`, trimmed. Empty means no search.
func issueSearchTerm(r *http.Request) string {
	if q := strings.TrimSpace(r.URL.Query().Get("q")); q != "" {
		return q
	}
	return strings.TrimSpace(r.URL.Query().Get("search"))
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
