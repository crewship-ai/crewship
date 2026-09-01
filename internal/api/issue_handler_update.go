package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// ── Update — PATCH /api/v1/issues/{id} ──────────────────────────────────────

func (h *IssueHandler) Update(w http.ResponseWriter, r *http.Request) {
	if !requireRole(w, r, "create") {
		return
	}

	crewID := r.PathValue("crewId")
	ident := r.PathValue("identifier")
	wsID := WorkspaceIDFromContext(r.Context())

	var req struct {
		Title        *string `json:"title"`
		Description  *string `json:"description"`
		Status       *string `json:"status"`
		Priority     *string `json:"priority"`
		AssigneeType *string `json:"assignee_type"`
		AssigneeID   *string `json:"assignee_id"`
		DueDate      *string `json:"due_date"`
		ProjectID    *string `json:"project_id"`
		// Raw, not *int: a JSON null decodes into a nil *int, which is
		// indistinguishable from the field being absent — so "clear the
		// estimate" arrived as an empty patch and 400ed on "No fields to
		// update". Raw keeps absent (nil) and null (the four bytes) apart.
		Estimate      json.RawMessage `json:"estimate"`
		ParentIssueID *string         `json:"parent_issue_id"`
		MilestoneID   *string         `json:"milestone_id"`
		SortOrder     *float64        `json:"sort_order"`
		Labels        *[]string       `json:"labels"`
		// Routine binding — pointer + map so the caller can clear it
		// (RoutineID = ""), set it, or leave it untouched (nil).
		// RoutineInputs is treated as a full replacement, not a merge,
		// to keep the inputs schema deterministic.
		RoutineID     *string                 `json:"routine_id"`
		RoutineInputs *map[string]interface{} `json:"routine_inputs"`
	}
	if err := readJSON(r, &req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "Invalid JSON body")
		return
	}

	// Look up current status — and the current description, which is what
	// makes "the description actually changed" answerable. Without it the
	// handler could only see that a description was SENT, and a client that
	// PATCHes the whole issue on every keystroke-blur would stamp an
	// audit row per save with nothing behind it.
	var missionID, currentStatus string
	var currentDescription sql.NullString
	err := h.db.QueryRowContext(r.Context(),
		`SELECT id, status, description FROM missions WHERE identifier = ? AND crew_id = ? AND workspace_id = ?`,
		ident, crewID, wsID).Scan(&missionID, &currentStatus, &currentDescription)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeProblem(w, r, http.StatusNotFound, "Issue not found")
			return
		}
		internalError(w, r, h.logger, "get issue for update", err)
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
	// assigneeTypeSet tracks whether the assignee_id branch below already
	// queued an assignee_type SET clause, so the plain pass-through further
	// down doesn't also queue one — updateBuilder.Set has no dedup, and two
	// "assignee_type = ?" clauses in one UPDATE would leave the outcome to
	// SQL's last-write-wins ordering instead of this handler's own logic.
	assigneeTypeSet := false
	if req.AssigneeID != nil && *req.AssigneeID != "" {
		// Workspace check matches the same gate in Create/parent_issue_id —
		// pre-fix a caller could PATCH assignee_id to point at another
		// workspace's user or agent, and the read path would then hand that
		// foreign identity's display name to anyone viewing the issue.
		var assigneeType string
		if req.AssigneeType != nil {
			assigneeType = *req.AssigneeType
			if assigneeType != "user" && assigneeType != "agent" {
				writeProblem(w, r, http.StatusBadRequest, "assignee_type must be 'user' or 'agent' when assignee_id is set")
				return
			}
			ok, vErr := validateAssigneeWorkspace(r.Context(), h.db, assigneeType, *req.AssigneeID, wsID)
			if vErr != nil {
				internalError(w, r, h.logger, "validate assignee_id", vErr)
				return
			}
			if !ok {
				writeProblem(w, r, http.StatusBadRequest, "assignee_id does not exist in this workspace")
				return
			}
		} else {
			// assignee_type omitted: resolve it instead of trusting the row's
			// stale type. Trusting the stale type was a false-reject bug —
			// reassigning an issue currently held by a user to an agent in
			// the SAME workspace, sending only assignee_id, looked the new
			// agent id up in workspace_members (the user table) under the
			// stale "user" type, found no match, and rejected a valid
			// same-workspace target with a misleading "does not exist"
			// (found by assignee_write_invariant_test.go's review, not by a
			// test — there wasn't one covering this branch).
			var ok bool
			var rErr error
			assigneeType, ok, rErr = resolveAssigneeType(r.Context(), h.db, *req.AssigneeID, wsID)
			if rErr != nil {
				internalError(w, r, h.logger, "resolve assignee_type", rErr)
				return
			}
			if !ok {
				writeProblem(w, r, http.StatusBadRequest, "assignee_id does not exist in this workspace")
				return
			}
		}
		ub.Set("assignee_type", assigneeType)
		assigneeTypeSet = true
		// A10 (I5): route the write to the typed owner or delegate column
		// alongside the legacy pair above — never both, never the other one.
		setOwnerOrDelegate(ub, assigneeType, *req.AssigneeID)
	}
	if req.AssigneeType != nil && !assigneeTypeSet {
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
			// Same fence as parent_issue_id below: without it a caller could
			// PATCH their own issue into another tenant's project, and the
			// listing join then renders that project's name back to them.
			if !fkInWorkspaceOrReject(w, r, h.db, h.logger, "projects", "project_id", *req.ProjectID, wsID) {
				return
			}
			ub.Set("project_id", *req.ProjectID)
		}
	}
	if len(req.Estimate) > 0 {
		if string(req.Estimate) == "null" {
			ub.SetNull("estimate")
		} else {
			// Reading it raw gives up the decoder's type check, so do it
			// here — an agent writes this body itself and will send "eight".
			var pts int
			if err := json.Unmarshal(req.Estimate, &pts); err != nil {
				writeProblem(w, r, http.StatusBadRequest, "estimate must be a number or null")
				return
			}
			ub.Set("estimate", pts)
		}
	}
	if req.ParentIssueID != nil {
		if *req.ParentIssueID == "" {
			ub.SetNull("parent_issue_id")
		} else {
			// Workspace check matches the same gate in Create — pre-fix a
			// caller could PATCH parent_issue_id to point at another
			// workspace's issue, silently linking unrelated tenants.
			// Self-parenting is rejected here for its specific message; the
			// deeper A → B → A case is caught by wouldCycleParent below,
			// the same helper the agent-facing relations endpoint calls.
			if *req.ParentIssueID == missionID {
				writeProblem(w, r, http.StatusBadRequest, "parent_issue_id cannot be the issue itself")
				return
			}
			var parentExists int
			err := h.db.QueryRowContext(r.Context(),
				`SELECT COUNT(*) FROM missions WHERE id = ? AND workspace_id = ?`,
				*req.ParentIssueID, wsID).Scan(&parentExists)
			if err != nil {
				internalError(w, r, h.logger, "validate parent_issue_id", err)
				return
			}
			if parentExists == 0 {
				writeProblem(w, r, http.StatusBadRequest, "parent_issue_id does not exist in this workspace")
				return
			}
			switch cErr := wouldCycleParent(r.Context(), h.db, missionID, *req.ParentIssueID, wsID); {
			case errors.Is(cErr, errParentCycle):
				writeProblem(w, r, http.StatusBadRequest, "parent_issue_id would create a cycle")
				return
			case cErr != nil:
				internalError(w, r, h.logger, "check parent cycle", cErr)
				return
			}
			ub.Set("parent_issue_id", *req.ParentIssueID)
		}
	}
	if req.MilestoneID != nil {
		if *req.MilestoneID == "" {
			ub.SetNull("milestone_id")
		} else {
			if !fkInWorkspaceOrReject(w, r, h.db, h.logger, "milestones", "milestone_id", *req.MilestoneID, wsID) {
				return
			}
			ub.Set("milestone_id", *req.MilestoneID)
		}
	}

	// Routine binding: empty string = clear, non-empty = set (and
	// validate). We resolve the pipeline_id against the workspace so
	// a stale or cross-workspace ID can't sneak in. DB errors stay
	// 500; "no such routine in this workspace" is the only 400 path.
	// Clearing the binding also resets routine_inputs_json so stale
	// inputs don't leak into a future re-bind.
	if req.RoutineID != nil {
		if *req.RoutineID == "" {
			ub.SetNull("routine_id")
			ub.Set("routine_inputs_json", "{}")
		} else {
			var exists int
			err = h.db.QueryRowContext(r.Context(),
				`SELECT COUNT(*) FROM pipelines WHERE id = ? AND workspace_id = ?`,
				*req.RoutineID, wsID).Scan(&exists)
			if err != nil {
				internalError(w, r, h.logger, "validate routine_id", err)
				return
			}
			if exists == 0 {
				writeProblem(w, r, http.StatusBadRequest, "routine_id does not exist in this workspace")
				return
			}
			ub.Set("routine_id", *req.RoutineID)
		}
	}
	if req.RoutineInputs != nil {
		b, mErr := json.Marshal(*req.RoutineInputs)
		if mErr != nil {
			writeProblem(w, r, http.StatusBadRequest, "routine_inputs is not valid JSON")
			return
		}
		ub.Set("routine_inputs_json", string(b))
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

		if newStatus == "DONE" || newStatus == "CANCELLED" || newStatus == "DUPLICATE" {
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
			internalError(w, r, h.logger, "update issue", err)
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

	// Log activity for significant changes. Collected into one batch so the
	// whole PATCH produces exactly one issue.updated broadcast, the way it
	// did when the broadcast was a separate line below.
	user := UserFromContext(r.Context())
	actorType := "user"
	actorID := user.ID

	var evs []issueEvent
	ev := func(action issueAction, details string) {
		evs = append(evs, issueEvent{
			MissionID: missionID, ActorType: actorType, ActorID: actorID,
			Action: action, Details: details,
		})
	}

	if req.Status != nil {
		// The prose AND the fields. Details is what a human reads; from/to
		// are what a matcher can predicate on.
		evs = append(evs, issueEvent{
			MissionID: missionID, ActorType: actorType, ActorID: actorID,
			Action:  actionStatusChanged,
			Details: fmt.Sprintf("%s → %s", currentStatus, *req.Status),
			From:    currentStatus, To: *req.Status,
		})
	}
	if req.AssigneeID != nil {
		ev(actionAssigneeChanged, fmt.Sprintf("assignee_id: %s", *req.AssigneeID))
	}
	if req.Priority != nil {
		ev(actionPriorityChanged, *req.Priority)
	}
	// A description rewrite used to leave NO trace at all: the field was
	// accepted and written a hundred lines above, and none of the three
	// branches over it logged anything. On a board where agents and humans
	// share issues, silently rewriting the statement of the work is exactly
	// the change someone needs to be able to see happened.
	//
	// Gated on the value actually differing — see the SELECT at the top.
	if req.Description != nil && *req.Description != currentDescription.String {
		ev(actionDescriptionChanged, describeDescriptionChange(currentDescription.String, *req.Description))
	}

	h.events().record(r.Context(), wsID, map[string]string{"id": missionID}, evs...)

	// Return updated issue
	issue, err := scanIssueRow(h.db.QueryRowContext(r.Context(),
		issueSelectQuery()+` WHERE m.id = ?`, missionID))
	if err != nil {
		internalError(w, r, h.logger, "read updated issue", err)
		return
	}

	writeJSON(w, http.StatusOK, issue)
}

// ── 5. Delete — DELETE /api/v1/crews/{crewId}/issues/{identifier} ───────────

func (h *IssueHandler) Delete(w http.ResponseWriter, r *http.Request) {
	if !requireRole(w, r, "create") {
		return
	}

	crewID := r.PathValue("crewId")
	ident := r.PathValue("identifier")
	wsID := WorkspaceIDFromContext(r.Context())

	// The digests this delete is ABOUT to orphan, read while the rows still
	// exist (#1768 item 7). After the DELETE they are gone — SQLite cascades
	// them without the application seeing it — so this is the only moment the
	// application can learn what it is orphaning. A failure here is not fatal:
	// the sweep below is the fallback, and the delete itself must not depend on
	// the storage bookkeeping succeeding.
	var orphaned []string
	var orphanErr error
	if h.storagePath != "" {
		orphaned, orphanErr = attachmentDigestsOfIssue(r.Context(), h.db, wsID, crewID, ident)
		if orphanErr != nil {
			h.logger.Warn("read attachment digests before issue delete",
				"identifier", ident, "workspace_id", wsID, "error", orphanErr)
		}
	}

	// Only allow deletion of BACKLOG or CANCELLED issues
	res, err := h.db.ExecContext(r.Context(),
		`DELETE FROM missions WHERE identifier = ? AND crew_id = ? AND workspace_id = ? AND status IN ('BACKLOG', 'CANCELLED')`,
		ident, crewID, wsID)
	if err != nil {
		internalError(w, r, h.logger, "delete issue", err)
		return
	}
	affected, err := res.RowsAffected()
	if err != nil {
		internalError(w, r, h.logger, "delete issue rows affected", err)
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
			internalError(w, r, h.logger, "delete issue follow-up query", qErr)
			return
		}
		writeProblem(w, r, http.StatusBadRequest, "Only BACKLOG or CANCELLED issues can be deleted")
		return
	}

	// Reclaim the attachment blobs the cascade just orphaned (#1768 item 7).
	//
	// The DELETE above cascades attachments' rows away inside SQLite, so the
	// application never sees them go and the refcounted unlink in
	// unlinkAttachmentBlobIfUnreferenced never runs. Without this the stored
	// bytes of every file attached to the issue stay on disk, unreachable and
	// unaccounted for.
	//
	// It reclaims the DIGESTS READ ABOVE rather than sweeping the workspace. The
	// two differ in what they can get wrong: reclaiming a known set asks "does
	// anything still reference this specific blob" and deletes only on a zero
	// answer, while a sweep deletes on absence and therefore has to be defended
	// against every upload in flight. The sweep is now defended (see
	// reclaimAttachmentBlobs), but a delete of one issue has no business walking
	// every blob in the tenant to find three files.
	//
	// The sweep is the fallback for the one case the targeted path cannot cover:
	// the digest read failed, so we do not know what was orphaned.
	//
	// Deliberately best-effort and AFTER the response decision: the delete the
	// user asked for has already committed, and a filesystem hiccup must not turn
	// it into a 500.
	if h.storagePath != "" {
		if orphanErr != nil {
			if n, rerr := reclaimAttachmentBlobs(r.Context(), h.db, h.storagePath, wsID); rerr != nil {
				h.logger.Warn("reclaim attachment blobs after issue delete",
					"identifier", ident, "workspace_id", wsID, "error", rerr)
			} else if n > 0 {
				h.logger.Info("reclaimed attachment blobs after issue delete (workspace sweep)",
					"identifier", ident, "workspace_id", wsID, "blobs", n)
			}
		} else if n := reclaimAttachmentDigests(
			r.Context(), h.db, h.logger, h.storagePath, wsID, orphaned); n > 0 {
			h.logger.Info("reclaimed attachment blobs after issue delete",
				"identifier", ident, "workspace_id", wsID, "blobs", n)
		}
	}

	h.broadcastIssueEvent(wsID, "issue.deleted", map[string]string{"identifier": ident})

	w.WriteHeader(http.StatusNoContent)
}
