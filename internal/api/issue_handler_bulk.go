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
		if func() bool {
			tx, err := h.db.BeginTx(r.Context(), nil)
			if err != nil {
				h.logger.Error("bulk begin issue update", "error", err)
				return false
			}
			defer tx.Rollback()

			// Verify issue belongs to workspace
			var currentStatus string
			err = tx.QueryRowContext(r.Context(),
				`SELECT status FROM missions WHERE id = ? AND workspace_id = ?`,
				issueID, wsID).Scan(&currentStatus)
			if err != nil {
				return false // skip missing issues
			}

			ub := newUpdate()

			if req.Updates.Status != nil {
				newStatus := *req.Updates.Status
				if !h.validateStatusTransition(currentStatus, newStatus) {
					return false // skip invalid transitions
				}
				ub.Set("status", newStatus)
				if newStatus == "DONE" || newStatus == "CANCELLED" || newStatus == "DUPLICATE" {
					ub.Set("completed_at", now)
				}
			}
			if req.Updates.Priority != nil {
				ub.Set("priority", *req.Updates.Priority)
			}
			// assigneeTypeSet tracks whether the assignee_id branch below already
			// queued an assignee_type SET clause, so the plain pass-through
			// further down doesn't also queue one — see issue_handler_update.go's
			// Update for the same guard and the reason it's needed
			// (updateBuilder.Set has no dedup).
			assigneeTypeSet := false
			if req.Updates.AssigneeID != nil && *req.Updates.AssigneeID != "" {
				// Same workspace-scoping as Create/Update (issue_handler_create.go,
				// issue_handler_update.go): pre-fix this went straight into the
				// UPDATE, so a bulk request could assign issues to a guessed or
				// enumerated cross-workspace user/agent ID.
				var assigneeType string
				if req.Updates.AssigneeType != nil {
					assigneeType = *req.Updates.AssigneeType
					if assigneeType != "user" && assigneeType != "agent" {
						return false // skip: invalid/unrecognized assignee_type, same as any other bad per-item field
					}
					ok, vErr := validateAssigneeWorkspace(r.Context(), tx, assigneeType, *req.Updates.AssigneeID, wsID)
					if vErr != nil {
						h.logger.Error("bulk validate assignee_id", "error", vErr, "issue_id", issueID)
						return false
					}
					if !ok {
						return false // skip: cross-workspace assignee_id, consistent with the invalid-transition skip above
					}
				} else {
					// assignee_type omitted: resolve it instead of trusting the
					// row's stale type — see issue_handler_update.go's Update for
					// the false-reject this used to cause (same shape, same
					// fix). In bulk, the old behavior didn't just misreport the
					// error: it silently skipped the ENTIRE item (assignee,
					// status, everything) via this same `return false`, and the
					// caller only ever saw the aggregate `{"updated": N}` count
					// — no way to tell a wrong-table miss apart from a real
					// cross-workspace rejection.
					var ok bool
					var rErr error
					assigneeType, ok, rErr = resolveAssigneeType(r.Context(), tx, *req.Updates.AssigneeID, wsID)
					if rErr != nil {
						h.logger.Error("bulk resolve assignee_type", "error", rErr, "issue_id", issueID)
						return false
					}
					if !ok {
						return false // skip: assignee_id doesn't exist in this workspace under either type
					}
				}
				ub.Set("assignee_type", assigneeType)
				assigneeTypeSet = true
				// A10 (I5): route the write to the typed owner or delegate
				// column alongside the legacy pair above.
				setOwnerOrDelegate(ub, assigneeType, *req.Updates.AssigneeID)
			}
			if req.Updates.AssigneeType != nil && !assigneeTypeSet {
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

			if ub.Empty() && req.Updates.Labels == nil {
				return false
			}

			if req.Updates.ProjectID != nil && *req.Updates.ProjectID != "" {
				var exists bool
				if err := tx.QueryRowContext(r.Context(), `SELECT EXISTS(SELECT 1 FROM projects WHERE id=? AND workspace_id=?)`, *req.Updates.ProjectID, wsID).Scan(&exists); err != nil || !exists {
					return false
				}
			}
			if req.Updates.Labels != nil {
				for _, label := range *req.Updates.Labels {
					var exists bool
					if err := tx.QueryRowContext(r.Context(), `SELECT EXISTS(SELECT 1 FROM labels WHERE id=? AND workspace_id=?)`, label, wsID).Scan(&exists); err != nil || !exists {
						return false
					}
				}
			}
			if !ub.Empty() {
				query, args := ub.Build("missions", "id = ? AND workspace_id = ?", issueID, wsID)
				if _, err := tx.ExecContext(r.Context(), query, args...); err != nil {
					h.logger.Error("bulk update issue", "error", err, "issue_id", issueID)
					return false
				}
			}
			if req.Updates.Labels != nil {
				if err := replaceIssueLabelsTx(r.Context(), tx, issueID, *req.Updates.Labels); err != nil {
					return false
				}
			}
			var evs []issueEvent
			if req.Updates.Status != nil && currentStatus != *req.Updates.Status {
				evs = append(evs, issueEvent{MissionID: issueID, ActorType: "user", ActorID: user.ID, Action: actionStatusChanged, Details: fmt.Sprintf("%s -> %s (bulk)", currentStatus, *req.Updates.Status), From: currentStatus, To: *req.Updates.Status})
			}
			if req.Updates.Priority != nil {
				evs = append(evs, issueEvent{MissionID: issueID, ActorType: "user", ActorID: user.ID, Action: actionPriorityChanged, Details: *req.Updates.Priority})
			}
			if req.Updates.AssigneeID != nil {
				evs = append(evs, issueEvent{MissionID: issueID, ActorType: "user", ActorID: user.ID, Action: actionAssigneeChanged, Details: "assignee_id: " + *req.Updates.AssigneeID})
			}
			for _, ev := range evs {
				if _, err := persistIssueActivityTx(r.Context(), tx, ev); err != nil {
					h.logger.Error("bulk persist issue activity", "error", err)
					return false
				}
			}
			if err := tx.Commit(); err != nil {
				h.logger.Error("bulk commit issue", "error", err)
				return false
			}
			return true

		}() {
			updated++
		}
	}

	if updated > 0 {
		h.drainCommittedIssueMutation(wsID)
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
