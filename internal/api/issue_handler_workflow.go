package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"
)

// ── Review — POST /api/v1/crews/{crewId}/issues/{identifier}/review ────────

func (h *IssueHandler) Review(w http.ResponseWriter, r *http.Request) {
	if !requireRole(w, r, "create") {
		return
	}

	crewID := r.PathValue("crewId")
	ident := r.PathValue("identifier")
	wsID := WorkspaceIDFromContext(r.Context())
	user := UserFromContext(r.Context())

	var req struct {
		Action     string  `json:"action"` // "approve" or "request_changes"
		Comment    string  `json:"comment"`
		ReassignTo *string `json:"reassign_to"` // agent slug for request_changes
	}
	if err := readJSON(r, &req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "Invalid JSON body")
		return
	}
	if req.Action != "approve" && req.Action != "request_changes" {
		writeProblem(w, r, http.StatusBadRequest, "action must be 'approve' or 'request_changes'")
		return
	}

	// Get issue
	var missionID, status string
	err := h.db.QueryRowContext(r.Context(),
		`SELECT id, status FROM missions WHERE identifier = ? AND crew_id = ? AND workspace_id = ?`,
		ident, crewID, wsID).Scan(&missionID, &status)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeProblem(w, r, http.StatusNotFound, "Issue not found")
			return
		}
		internalError(w, r, h.logger, "review: load issue", err)
		return
	}

	if status != "REVIEW" && status != "IN_PROGRESS" {
		writeProblem(w, r, http.StatusBadRequest, "Issue must be in REVIEW or IN_PROGRESS to review (current: "+status+")")
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)

	if req.Action == "approve" {
		// REVIEW → DONE
		_, err = h.db.ExecContext(r.Context(),
			`UPDATE missions SET status = 'DONE', completed_at = ?, updated_at = ? WHERE id = ?`,
			now, now, missionID)
		if err != nil {
			internalError(w, r, h.logger, "review: approve", err)
			return
		}

		// Add comment
		commentBody := "Approved"
		if req.Comment != "" {
			commentBody = "Approved: " + req.Comment
		}
		h.addIssueComment(r.Context(), missionID, "user", user.ID, commentBody)

		// Activity
		h.logActivity(r.Context(), missionID, "user", user.ID, "review_approved", commentBody)

		// F4.5 mission outcomes → crew memory.
		emitMissionOutcomeLessonAsync(r.Context(), h.db, h.storagePath, missionID, "DONE", h.logger)

	} else {
		// request_changes → TODO
		ub := newUpdate()
		ub.Set("status", "TODO")

		if req.ReassignTo != nil && *req.ReassignTo != "" {
			// Resolve agent slug to ID
			var agentID string
			err := h.db.QueryRowContext(r.Context(),
				`SELECT id FROM agents WHERE slug = ? AND deleted_at IS NULL LIMIT 1`,
				*req.ReassignTo).Scan(&agentID)
			if err == nil {
				ub.Set("assignee_type", "agent")
				ub.Set("assignee_id", agentID)
			}
		}

		query, args := ub.Build("missions", "id = ?", missionID)
		_, err = h.db.ExecContext(r.Context(), query, args...)
		if err != nil {
			internalError(w, r, h.logger, "review: request_changes", err)
			return
		}

		// Add comment
		commentBody := "Changes requested"
		if req.Comment != "" {
			commentBody = "Changes requested: " + req.Comment
		}
		h.addIssueComment(r.Context(), missionID, "user", user.ID, commentBody)

		// Activity
		h.logActivity(r.Context(), missionID, "user", user.ID, "review_changes_requested", commentBody)
	}

	h.broadcastIssueEvent(wsID, "issue.updated", map[string]string{"id": missionID, "identifier": ident})

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "action": req.Action})
}

// ── ListActivity — GET /api/v1/crews/{crewId}/issues/{identifier}/activity

func (h *IssueHandler) ListActivity(w http.ResponseWriter, r *http.Request) {
	crewID := r.PathValue("crewId")
	ident := r.PathValue("identifier")
	wsID := WorkspaceIDFromContext(r.Context())

	missionID, err := h.resolveMissionID(r.Context(), ident, crewID, wsID)
	if err != nil {
		writeProblem(w, r, http.StatusNotFound, "Issue not found")
		return
	}

	rows, err := h.db.QueryContext(r.Context(), `
		SELECT a.id, a.mission_id, a.actor_type, a.actor_id, a.action, a.details, a.created_at,
			CASE
				WHEN a.actor_type = 'user' THEN (SELECT full_name FROM users WHERE id = a.actor_id)
				WHEN a.actor_type = 'agent' THEN (SELECT name FROM agents WHERE id = a.actor_id)
				ELSE 'System'
			END AS actor_name
		FROM mission_activity a
		WHERE a.mission_id = ?
		ORDER BY a.created_at DESC
		LIMIT 50`, missionID)
	if err != nil {
		internalError(w, r, h.logger, "list activity", err)
		return
	}
	defer rows.Close()

	var result []activityResponse
	for rows.Next() {
		var a activityResponse
		if err := rows.Scan(&a.ID, &a.MissionID, &a.ActorType, &a.ActorID, &a.Action, &a.Details, &a.CreatedAt, &a.ActorName); err != nil {
			h.logger.Error("scan activity", "error", err)
			continue
		}
		result = append(result, a)
	}
	if result == nil {
		result = []activityResponse{}
	}
	writeJSON(w, http.StatusOK, result)
}

// ── 15. Start — POST /api/v1/crews/{crewId}/issues/{identifier}/start ──────

func (h *IssueHandler) Start(w http.ResponseWriter, r *http.Request) {
	if !requireRole(w, r, "create") {
		return
	}

	crewID := r.PathValue("crewId")
	ident := r.PathValue("identifier")
	wsID := WorkspaceIDFromContext(r.Context())

	// 1. Load issue
	var missionID, status, title, leadAgentID string
	var description, assigneeID sql.NullString
	err := h.db.QueryRowContext(r.Context(), `
		SELECT id, status, title, description, assignee_id, lead_agent_id
		FROM missions WHERE identifier = ? AND crew_id = ? AND workspace_id = ?`,
		ident, crewID, wsID).Scan(&missionID, &status, &title, &description, &assigneeID, &leadAgentID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeProblem(w, r, http.StatusNotFound, "Issue not found")
			return
		}
		internalError(w, r, h.logger, "start issue: load", err)
		return
	}

	// 2. Validate status
	if status != "BACKLOG" && status != "TODO" {
		writeProblem(w, r, http.StatusBadRequest, "Issue must be in BACKLOG or TODO to start (current: "+status+")")
		return
	}

	// 3. Validate assignee
	if !assigneeID.Valid || assigneeID.String == "" {
		writeProblem(w, r, http.StatusBadRequest, "Issue must have an assignee before starting")
		return
	}

	// 3b. Create synthetic chat so assignments can reference it (FK on chat_id)
	var chatExists int
	_ = h.db.QueryRowContext(r.Context(), `SELECT 1 FROM chats WHERE id = ?`, missionID).Scan(&chatExists)
	if chatExists == 0 {
		chatNow := time.Now().UTC().Format(time.RFC3339)
		_, err = h.db.ExecContext(r.Context(), `
			INSERT INTO chats (id, agent_id, workspace_id, title, mode, status, started_at, created_at, updated_at)
			VALUES (?, ?, ?, ?, 'MISSION', 'ACTIVE', ?, ?, ?)`,
			missionID, leadAgentID, wsID, "Issue: "+title, chatNow, chatNow, chatNow)
		if err != nil {
			h.logger.Error("start issue: create synthetic chat", "error", err)
			writeProblem(w, r, http.StatusInternalServerError, "Failed to prepare issue execution")
			return
		}
	}

	// 4. Reset existing tasks to PENDING or create new one
	var taskCount int
	_ = h.db.QueryRowContext(r.Context(), `SELECT COUNT(*) FROM mission_tasks WHERE mission_id = ?`, missionID).Scan(&taskCount)
	if taskCount > 0 {
		// Reset existing tasks for re-run
		resetNow := time.Now().UTC().Format(time.RFC3339)
		_, _ = h.db.ExecContext(r.Context(), `
			UPDATE mission_tasks SET status = 'PENDING', started_at = NULL, completed_at = NULL,
			duration_ms = NULL, result_summary = NULL, error_message = NULL, assignment_id = NULL,
			iteration = COALESCE(iteration, 0) + 1, updated_at = ?
			WHERE mission_id = ?`, resetNow, missionID)
	} else {
		// If assignee is a LEAD agent, skip creating a default task so the mission engine
		// triggers lead planning (with sidecar and crew context for delegation).
		var assigneeRole string
		_ = h.db.QueryRowContext(r.Context(),
			`SELECT agent_role FROM agents WHERE id = ? AND deleted_at IS NULL`,
			assigneeID.String).Scan(&assigneeRole)

		if assigneeRole == "LEAD" {
			h.logger.Info("start issue: LEAD assignee — skipping default task for lead planning",
				"issue", ident, "assignee", assigneeID.String)
		} else {
			taskID := generateCUID()
			now := time.Now().UTC().Format(time.RFC3339)
			desc := ""
			if description.Valid {
				desc = description.String
			}
			_, err = h.db.ExecContext(r.Context(), `
				INSERT INTO mission_tasks (id, mission_id, assigned_agent_id, title, description, status, task_order, depends_on, created_at, updated_at)
				VALUES (?, ?, ?, ?, ?, 'PENDING', 1, '[]', ?, ?)`,
				taskID, missionID, assigneeID.String, title, desc, now, now)
			if err != nil {
				h.logger.Error("start issue: create task", "error", err)
				writeProblem(w, r, http.StatusInternalServerError, "Failed to create task")
				return
			}
		}
	}

	// 5. Update status → IN_PROGRESS (atomic CAS)
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := h.db.ExecContext(r.Context(), `
		UPDATE missions SET status = 'IN_PROGRESS', updated_at = ? WHERE id = ? AND status IN ('BACKLOG', 'TODO')`,
		now, missionID)
	if err != nil {
		internalError(w, r, h.logger, "start issue: update status", err)
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		writeProblem(w, r, http.StatusConflict, "Issue was already started by another request")
		return
	}

	// 6. Start mission engine (async)
	if h.missionEngine != nil {
		go func() {
			if err := h.missionEngine.StartMission(context.Background(), missionID); err != nil {
				h.logger.Error("start issue: engine", "error", err, "issue", ident)
			}
		}()
	}

	// 7. Broadcast
	h.broadcastIssueEvent(wsID, "issue.started", map[string]string{"id": missionID, "identifier": ident, "status": "IN_PROGRESS"})

	h.logger.Info("issue started", "identifier", ident, "agent", assigneeID.String)
	writeJSON(w, http.StatusOK, map[string]string{"status": "IN_PROGRESS", "identifier": ident})
}

// ── 16. Stop — POST /api/v1/crews/{crewId}/issues/{identifier}/stop ────────

func (h *IssueHandler) Stop(w http.ResponseWriter, r *http.Request) {
	if !requireRole(w, r, "create") {
		return
	}

	crewID := r.PathValue("crewId")
	ident := r.PathValue("identifier")
	wsID := WorkspaceIDFromContext(r.Context())

	var missionID, status string
	err := h.db.QueryRowContext(r.Context(), `
		SELECT id, status FROM missions WHERE identifier = ? AND crew_id = ? AND workspace_id = ?`,
		ident, crewID, wsID).Scan(&missionID, &status)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeProblem(w, r, http.StatusNotFound, "Issue not found")
			return
		}
		internalError(w, r, h.logger, "stop issue: load", err)
		return
	}

	if status != "IN_PROGRESS" && status != "REVIEW" {
		writeProblem(w, r, http.StatusBadRequest, "Issue must be IN_PROGRESS or REVIEW to stop (current: "+status+")")
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)

	// Cancel running/pending tasks
	_, _ = h.db.ExecContext(r.Context(), `
		UPDATE mission_tasks SET status = 'CANCELLED', updated_at = ? WHERE mission_id = ? AND status IN ('PENDING', 'IN_PROGRESS', 'BLOCKED')`,
		now, missionID)

	// Update issue status → CANCELLED
	_, err = h.db.ExecContext(r.Context(), `
		UPDATE missions SET status = 'CANCELLED', completed_at = ?, updated_at = ? WHERE id = ?`,
		now, now, missionID)
	if err != nil {
		internalError(w, r, h.logger, "stop issue: update", err)
		return
	}

	h.broadcastIssueEvent(wsID, "issue.updated", map[string]string{"id": missionID, "identifier": ident, "status": "CANCELLED"})

	// F4.5 mission outcomes → crew memory. CANCELLED maps to neutral.
	emitMissionOutcomeLessonAsync(r.Context(), h.db, h.storagePath, missionID, "CANCELLED", h.logger)

	h.logger.Info("issue stopped", "identifier", ident)
	writeJSON(w, http.StatusOK, map[string]string{"status": "CANCELLED", "identifier": ident})
}
