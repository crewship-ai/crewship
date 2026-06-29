package api

import (
	"database/sql"
	"errors"
	"net/http"
	"time"
)

func (h *MissionHandler) Create(w http.ResponseWriter, r *http.Request) {
	if !requireRole(w, r, "create") {
		return
	}

	crewID := r.PathValue("crewId")
	wsID := WorkspaceIDFromContext(r.Context())

	var req struct {
		Title            string  `json:"title"`
		Description      *string `json:"description"`
		LeadAgentID      string  `json:"lead_agent_id"`
		WorkflowTemplate *string `json:"workflow_template"`
	}
	if err := readJSON(r, &req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "Invalid JSON body")
		return
	}
	if req.Title == "" {
		writeProblem(w, r, http.StatusBadRequest, "title is required")
		return
	}
	if req.LeadAgentID == "" {
		writeProblem(w, r, http.StatusBadRequest, "lead_agent_id is required")
		return
	}

	// Validate lead agent exists in crew with LEAD role
	var agentRole string
	err := h.db.QueryRowContext(r.Context(),
		`SELECT agent_role FROM agents WHERE id = ? AND crew_id = ? AND deleted_at IS NULL`,
		req.LeadAgentID, crewID).Scan(&agentRole)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeProblem(w, r, http.StatusBadRequest, "lead agent not found in crew")
			return
		}
		internalError(w, r, h.logger, "lookup lead agent", err)
		return
	}
	if agentRole != "LEAD" {
		writeProblem(w, r, http.StatusBadRequest, "agent must have LEAD role")
		return
	}

	id := generateCUID()
	traceID := "mission-" + generateCUID()
	now := time.Now().UTC().Format(time.RFC3339)

	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		internalError(w, r, h.logger, "begin tx", err)
		return
	}
	defer tx.Rollback() //nolint:errcheck

	_, err = tx.ExecContext(r.Context(), `
		INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, description, status, workflow_template, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, 'PLANNING', ?, ?, ?)`,
		id, wsID, crewID, req.LeadAgentID, traceID, req.Title, req.Description, req.WorkflowTemplate, now, now)
	if err != nil {
		internalError(w, r, h.logger, "create mission", err)
		return
	}

	// Create a synthetic chat so assignments can reference it (FK on chat_id)
	_, err = tx.ExecContext(r.Context(), `
		INSERT INTO chats (id, agent_id, workspace_id, title, mode, status, started_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'MISSION', 'ACTIVE', ?, ?, ?)`,
		id, req.LeadAgentID, wsID, "Mission: "+req.Title, now, now, now)
	if err != nil {
		internalError(w, r, h.logger, "create synthetic chat for mission", err)
		return
	}

	if err := tx.Commit(); err != nil {
		internalError(w, r, h.logger, "commit mission", err)
		return
	}

	resp := missionResponse{
		ID:               id,
		WorkspaceID:      wsID,
		CrewID:           crewID,
		LeadAgentID:      req.LeadAgentID,
		TraceID:          traceID,
		Title:            req.Title,
		Description:      req.Description,
		Status:           "PLANNING",
		WorkflowTemplate: req.WorkflowTemplate,
		CreatedAt:        now,
		UpdatedAt:        now,
	}

	broadcastChannelEvent(h.hub, "crew", crewID, "mission.created",
		map[string]string{"id": id, "title": req.Title})
	broadcastWorkspaceEvent(h.hub, wsID, "mission.updated",
		map[string]string{"id": id, "crew_id": crewID, "status": "PLANNING"})

	writeJSON(w, http.StatusCreated, resp)
}

// List handles GET /api/v1/crews/{crewId}/missions

func (h *MissionHandler) Update(w http.ResponseWriter, r *http.Request) {
	if !requireRole(w, r, "create") {
		return
	}

	crewID := r.PathValue("crewId")
	missionID := r.PathValue("missionId")
	wsID := WorkspaceIDFromContext(r.Context())

	var req struct {
		Status      *string `json:"status"`
		Title       *string `json:"title"`
		Description *string `json:"description"`
		Plan        *string `json:"plan"`
	}
	if err := readJSON(r, &req); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "Invalid JSON body")
		return
	}

	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		internalError(w, r, h.logger, "begin transaction", err)
		return
	}
	defer tx.Rollback() //nolint:errcheck

	// Get current status
	var currentStatus string
	err = tx.QueryRowContext(r.Context(),
		`SELECT status FROM missions WHERE id = ? AND crew_id = ? AND workspace_id = ?`,
		missionID, crewID, wsID).Scan(&currentStatus)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeProblem(w, r, http.StatusNotFound, "Mission not found")
			return
		}
		internalError(w, r, h.logger, "get mission for update", err)
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)

	// committedTerminalStatus carries the terminal status (if any) past
	// the tx commit so the F4.5 mission-outcomes-to-crew-memory hook can
	// fire AFTER persistence — see emitMissionOutcomeLessonAsync. Empty
	// means either no status change or a non-terminal transition.
	var committedTerminalStatus string

	// Validate status transition
	if req.Status != nil {
		newStatus := *req.Status
		allowed := validMissionTransitions[currentStatus]
		valid := false
		for _, s := range allowed {
			if s == newStatus {
				valid = true
				break
			}
		}
		if !valid {
			writeProblem(w, r, http.StatusBadRequest, "Invalid status transition from "+currentStatus+" to "+newStatus)
			return
		}

		completedAt := sql.NullString{}
		if newStatus == "COMPLETED" || newStatus == "FAILED" || newStatus == "CANCELLED" {
			completedAt = sql.NullString{String: now, Valid: true}
		}

		if _, err = tx.ExecContext(r.Context(),
			`UPDATE missions SET status = ?, completed_at = ?, updated_at = ? WHERE id = ?`,
			newStatus, completedAt, now, missionID); err != nil {
			internalError(w, r, h.logger, "update mission status", err)
			return
		}
		if _, terminal := terminalStatusToLessonKindLocal(newStatus); terminal {
			committedTerminalStatus = newStatus
		}
	}

	if req.Title != nil {
		if _, err = tx.ExecContext(r.Context(), `UPDATE missions SET title = ?, updated_at = ? WHERE id = ?`, *req.Title, now, missionID); err != nil {
			internalError(w, r, h.logger, "update mission title", err)
			return
		}
	}
	if req.Description != nil {
		if _, err = tx.ExecContext(r.Context(), `UPDATE missions SET description = ?, updated_at = ? WHERE id = ?`, *req.Description, now, missionID); err != nil {
			internalError(w, r, h.logger, "update mission description", err)
			return
		}
	}
	if req.Plan != nil {
		if _, err = tx.ExecContext(r.Context(), `UPDATE missions SET plan = ?, updated_at = ? WHERE id = ?`, *req.Plan, now, missionID); err != nil {
			internalError(w, r, h.logger, "update mission plan", err)
			return
		}
	}

	if err = tx.Commit(); err != nil {
		internalError(w, r, h.logger, "commit mission update", err)
		return
	}

	// F4.5 mission outcomes → crew memory. Fires only when the
	// transition was to a terminal state; the helper is a no-op
	// otherwise. Runs in a detached goroutine so a slow filesystem
	// can't stall this response (see emitMissionOutcomeLessonAsync).
	if committedTerminalStatus != "" {
		emitMissionOutcomeLessonAsync(r.Context(), h.db, h.storagePath, missionID, committedTerminalStatus, h.logger)
	}

	// Return updated mission
	m, err := scanMission(h.db.QueryRowContext(r.Context(),
		missionSelectColumns+` WHERE m.id = ?`, missionID))
	if err != nil {
		internalError(w, r, h.logger, "read updated mission", err)
		return
	}

	if req.Status != nil {
		broadcastChannelEvent(h.hub, "mission", missionID, "mission.status",
			map[string]string{"id": missionID, "status": *req.Status})
		// Broadcast to workspace for dashboard visibility
		broadcastWorkspaceEvent(h.hub, m.WorkspaceID, "mission.updated",
			map[string]string{"id": missionID, "crew_id": crewID, "status": *req.Status})
	}

	writeJSON(w, http.StatusOK, m)
}

// Delete handles DELETE /api/v1/crews/{crewId}/missions/{missionId}

func (h *MissionHandler) Delete(w http.ResponseWriter, r *http.Request) {
	if !requireRole(w, r, "create") {
		return
	}

	crewID := r.PathValue("crewId")
	missionID := r.PathValue("missionId")
	wsID := WorkspaceIDFromContext(r.Context())

	// Atomic delete with status guard — prevents TOCTOU race where a concurrent
	// Start flips the row to IN_PROGRESS between a separate check and delete.
	res, err := h.db.ExecContext(r.Context(),
		`DELETE FROM missions WHERE id = ? AND crew_id = ? AND workspace_id = ? AND status IN ('PLANNING', 'CANCELLED')`,
		missionID, crewID, wsID)
	if err != nil {
		internalError(w, r, h.logger, "delete mission", err)
		return
	}
	affected, err := res.RowsAffected()
	if err != nil {
		internalError(w, r, h.logger, "delete mission rows affected", err)
		return
	}
	if affected == 0 {
		// Distinguish "not found" from "exists but wrong status"
		var currentStatus string
		qErr := h.db.QueryRowContext(r.Context(),
			`SELECT status FROM missions WHERE id = ? AND crew_id = ? AND workspace_id = ?`,
			missionID, crewID, wsID).Scan(&currentStatus)
		if qErr != nil {
			if errors.Is(qErr, sql.ErrNoRows) {
				writeProblem(w, r, http.StatusNotFound, "Mission not found")
				return
			}
			internalError(w, r, h.logger, "delete mission follow-up query", qErr)
			return
		}
		writeProblem(w, r, http.StatusBadRequest, "Only PLANNING or CANCELLED missions can be deleted")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// Start handles POST /api/v1/crews/{crewId}/missions/{missionId}/start
// Transitions a PLANNING mission to IN_PROGRESS and kicks off the MissionEngine.
