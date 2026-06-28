package api

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// ── 17. BulkUpdate — POST /api/v1/issues/bulk-update ──────────────────────

func (h *IssueHandler) BulkUpdate(w http.ResponseWriter, r *http.Request) {
	if !requireRole(w, r, "create") {
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
			if !h.validateStatusTransition(currentStatus, newStatus) {
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
			if _, err := h.db.ExecContext(r.Context(), `DELETE FROM mission_labels WHERE mission_id = ?`, issueID); err != nil {
				h.logger.Error("bulk delete issue labels", "error", err, "issue_id", issueID)
			}
			for _, labelID := range *req.Updates.Labels {
				if _, err := h.db.ExecContext(r.Context(),
					`INSERT OR IGNORE INTO mission_labels (mission_id, label_id) VALUES (?, ?)`,
					issueID, labelID); err != nil {
					h.logger.Error("bulk insert issue label", "error", err, "issue_id", issueID, "label_id", labelID)
				}
			}
		}

		// Activity log
		if req.Updates.Status != nil {
			h.logActivity(r.Context(), issueID, "user", user.ID, "status_changed",
				fmt.Sprintf("%s -> %s (bulk)", currentStatus, *req.Updates.Status))
		}

		updated++
	}

	if updated > 0 {
		h.broadcastIssueEvent(wsID, "issues.bulk_updated", map[string]string{"count": fmt.Sprintf("%d", updated)})
	}

	writeJSON(w, http.StatusOK, map[string]int{"updated": updated})
}

// ── 18. ListSubIssues — GET /api/v1/crews/{crewId}/issues/{identifier}/subtasks

func (h *IssueHandler) ListSubIssues(w http.ResponseWriter, r *http.Request) {
	crewID := r.PathValue("crewId")
	ident := r.PathValue("identifier")
	wsID := WorkspaceIDFromContext(r.Context())

	// Resolve identifier to ID
	parentID, err := h.resolveMissionID(r.Context(), ident, crewID, wsID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeProblem(w, r, http.StatusNotFound, "Issue not found")
			return
		}
		internalError(w, r, h.logger, "resolve issue for subtasks", err)
		return
	}

	rows, err := h.db.QueryContext(r.Context(),
		issueSelectQuery()+`
		WHERE m.parent_issue_id = ?
		ORDER BY COALESCE(m.sort_order, 0) ASC, m.created_at ASC`, parentID)
	if err != nil {
		internalError(w, r, h.logger, "list sub-issues", err)
		return
	}
	defer rows.Close()

	var result []issueResponse
	for rows.Next() {
		issue, err := scanIssueRow(rows)
		if err != nil {
			internalError(w, r, h.logger, "scan sub-issue", err)
			return
		}
		result = append(result, issue)
	}
	if err := rows.Err(); err != nil {
		internalError(w, r, h.logger, "rows iteration (sub-issues)", err)
		return
	}

	if result == nil {
		result = []issueResponse{}
	}
	writeJSON(w, http.StatusOK, result)
}
