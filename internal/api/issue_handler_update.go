package api

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// ── Update — PATCH /api/v1/issues/{id} ──────────────────────────────────────

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
