package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/ws"
)

// Restart resets a FAILED/CANCELLED/REVIEW/COMPLETED mission: resets non-completed tasks,
// increments their iteration, and re-starts. Completed tasks stay completed (no re-run).
func (h *MissionHandler) Restart(w http.ResponseWriter, r *http.Request) {
	role := RoleFromContext(r.Context())
	if !canRole(role, "create") {
		writeProblem(w, r, http.StatusForbidden, "Forbidden")
		return
	}

	crewID := r.PathValue("crewId")
	missionID := r.PathValue("missionId")
	wsID := WorkspaceIDFromContext(r.Context())

	now := time.Now().UTC().Format(time.RFC3339)

	// Atomic CAS: only restart from terminal states (prevents restarting transient states like RESUMING)
	res, err := h.db.ExecContext(r.Context(),
		`UPDATE missions SET status = 'PLANNING', updated_at = ?, completed_at = NULL
		 WHERE id = ? AND crew_id = ? AND workspace_id = ? AND status IN ('COMPLETED', 'FAILED', 'CANCELLED')`,
		now, missionID, crewID, wsID)
	if err != nil {
		h.logger.Error("restart: claim mission", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	claimed, err := res.RowsAffected()
	if err != nil {
		h.logger.Error("restart: check rows affected", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	if claimed == 0 {
		var currentStatus string
		qErr := h.db.QueryRowContext(r.Context(),
			`SELECT status FROM missions WHERE id = ? AND crew_id = ? AND workspace_id = ?`,
			missionID, crewID, wsID).Scan(&currentStatus)
		if qErr != nil {
			writeProblem(w, r, http.StatusNotFound, "Mission not found")
			return
		}
		writeProblem(w, r, http.StatusConflict,
			fmt.Sprintf("cannot restart mission in %s state", currentStatus))
		return
	}

	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	defer tx.Rollback() //nolint:errcheck

	// Reset FAILED/PENDING/BLOCKED/SKIPPED tasks; increment iteration; clear errors
	// (mission status already set to PLANNING by atomic CAS above)
	if _, err = tx.ExecContext(r.Context(),
		`UPDATE mission_tasks SET
			status = CASE WHEN depends_on = '[]' OR depends_on IS NULL THEN 'PENDING' ELSE 'BLOCKED' END,
			iteration = COALESCE(iteration, 0) + 1,
			error_message = NULL,
			result_summary = CASE WHEN status = 'COMPLETED' THEN result_summary ELSE NULL END,
			started_at = NULL,
			completed_at = NULL,
			duration_ms = NULL,
			assignment_id = NULL,
			updated_at = ?
		WHERE mission_id = ? AND status != 'COMPLETED'`,
		now, missionID); err != nil {
		writeProblem(w, r, http.StatusInternalServerError, "Failed to reset tasks")
		return
	}

	if err = tx.Commit(); err != nil {
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	// Post-reset: unblock tasks whose dependencies are all COMPLETED.
	// The SQL above blindly sets tasks with deps to BLOCKED, but some deps
	// may already be COMPLETED (they were not reset). Unblock those now.
	h.unblockCompletedDeps(r, missionID)

	if h.hub != nil {
		wsChannel := "workspace:" + wsID
		h.hub.Broadcast(wsChannel, ws.ServerMessage{
			Type:    "mission.updated",
			Channel: wsChannel,
			Payload: map[string]interface{}{"id": missionID, "crew_id": crewID, "status": "PLANNING"},
		})
	}

	writeJSON(w, http.StatusOK, map[string]string{"id": missionID, "status": "PLANNING"})
}

// Resume restarts a FAILED mission from the point of failure. Only the FAILED
// task(s) and their downstream dependents are reset; COMPLETED tasks stay.
// The DAG engine is started automatically — no separate Start call needed.
func (h *MissionHandler) Resume(w http.ResponseWriter, r *http.Request) {
	role := RoleFromContext(r.Context())
	if !canRole(role, "create") {
		writeProblem(w, r, http.StatusForbidden, "Forbidden")
		return
	}

	crewID := r.PathValue("crewId")
	missionID := r.PathValue("missionId")
	wsID := WorkspaceIDFromContext(r.Context())

	// Atomic compare-and-swap: claim the FAILED mission before reading tasks.
	// Prevents two concurrent Resume requests from both resetting the same tasks.
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := h.db.ExecContext(r.Context(),
		`UPDATE missions SET status = 'RESUMING', updated_at = ? WHERE id = ? AND crew_id = ? AND workspace_id = ? AND status = 'FAILED'`,
		now, missionID, crewID, wsID)
	if err != nil {
		h.logger.Error("claim mission for resume", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	claimed, err := res.RowsAffected()
	if err != nil {
		h.logger.Error("resume: check rows affected", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	if claimed == 0 {
		// Either not found or not in FAILED state — check which
		var currentStatus string
		qErr := h.db.QueryRowContext(r.Context(),
			`SELECT status FROM missions WHERE id = ? AND crew_id = ? AND workspace_id = ?`,
			missionID, crewID, wsID).Scan(&currentStatus)
		if qErr != nil {
			writeProblem(w, r, http.StatusNotFound, "Mission not found")
			return
		}
		writeProblem(w, r, http.StatusConflict,
			fmt.Sprintf("can only resume FAILED missions, current status: %s", currentStatus))
		return
	}

	// After claiming RESUMING, any error must restore FAILED status so the
	// mission doesn't get stuck in the unrecoverable RESUMING state.
	resumeOK := false
	defer func() {
		if !resumeOK {
			if _, rbErr := h.db.ExecContext(context.Background(),
				`UPDATE missions SET status = 'FAILED', updated_at = ? WHERE id = ? AND status = 'RESUMING'`,
				now, missionID); rbErr != nil {
				h.logger.Error("resume: rollback mission to FAILED", "error", rbErr, "mission_id", missionID)
			}
		}
	}()

	// Collect all tasks to build dependency graph
	type taskRow struct {
		ID        string
		Status    string
		DependsOn string
	}
	rows, err := h.db.QueryContext(r.Context(),
		`SELECT id, status, COALESCE(depends_on, '[]') FROM mission_tasks WHERE mission_id = ?`, missionID)
	if err != nil {
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	var tasks []taskRow
	for rows.Next() {
		var t taskRow
		if err := rows.Scan(&t.ID, &t.Status, &t.DependsOn); err != nil {
			rows.Close()
			writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
			return
		}
		tasks = append(tasks, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	// Build reverse dependency map: taskID -> list of tasks that depend on it
	reverseDeps := make(map[string][]string)
	for _, t := range tasks {
		deps := parseDependencyJSON(t.DependsOn)
		for _, dep := range deps {
			reverseDeps[dep] = append(reverseDeps[dep], t.ID)
		}
	}

	// Find FAILED tasks and cascade downstream via BFS
	toReset := make(map[string]bool)
	queue := []string{}
	for _, t := range tasks {
		if t.Status == "FAILED" {
			toReset[t.ID] = true
			queue = append(queue, t.ID)
		}
	}
	for len(queue) > 0 {
		curr := queue[0]
		queue = queue[1:]
		for _, child := range reverseDeps[curr] {
			if !toReset[child] {
				toReset[child] = true
				queue = append(queue, child)
			}
		}
	}

	if len(toReset) == 0 {
		writeProblem(w, r, http.StatusBadRequest, "No failed tasks to resume from")
		return
	}

	// Validate DAG before modifying tasks (deps may have been modified while FAILED)
	if h.missionEngine != nil {
		if dagErr := h.missionEngine.ValidateDAG(r.Context(), missionID); dagErr != nil {
			h.logger.Error("resume: invalid DAG", "error", dagErr, "mission_id", missionID)
			writeProblem(w, r, http.StatusBadRequest, "Invalid task DAG: "+dagErr.Error())
			return
		}
	}

	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	defer tx.Rollback() //nolint:errcheck

	// Reset the identified tasks
	resetIDs := make([]string, 0, len(toReset))
	for id := range toReset {
		resetIDs = append(resetIDs, id)
	}

	// Build task status map for checking deps
	statusMap := make(map[string]string)
	for _, t := range tasks {
		if toReset[t.ID] {
			statusMap[t.ID] = "RESET"
		} else {
			statusMap[t.ID] = t.Status
		}
	}

	// Parse deps map
	depsMap := make(map[string][]string)
	for _, t := range tasks {
		depsMap[t.ID] = parseDependencyJSON(t.DependsOn)
	}

	resetStatusMap := make(map[string]string, len(resetIDs))
	for _, id := range resetIDs {
		// Determine correct initial status: PENDING if all deps are COMPLETED, BLOCKED otherwise
		newStatus := "PENDING"
		for _, dep := range depsMap[id] {
			if statusMap[dep] != "COMPLETED" {
				newStatus = "BLOCKED"
				break
			}
		}
		resetStatusMap[id] = newStatus
		if _, err = tx.ExecContext(r.Context(),
			`UPDATE mission_tasks SET
				status = ?,
				iteration = COALESCE(iteration, 0) + 1,
				error_message = NULL,
				result_summary = NULL,
				started_at = NULL,
				completed_at = NULL,
				duration_ms = NULL,
				assignment_id = NULL,
				updated_at = ?
			WHERE id = ?`,
			newStatus, now, id); err != nil {
			writeProblem(w, r, http.StatusInternalServerError, "Failed to reset task")
			return
		}
	}

	// Transition from RESUMING to IN_PROGRESS
	if _, err = tx.ExecContext(r.Context(),
		`UPDATE missions SET status = 'IN_PROGRESS', updated_at = ?, completed_at = NULL WHERE id = ?`,
		now, missionID); err != nil {
		writeProblem(w, r, http.StatusInternalServerError, "Failed to update mission")
		return
	}

	if err = tx.Commit(); err != nil {
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	// Start DAG engine immediately. If this fails, roll back to FAILED.
	if h.missionEngine != nil {
		if err := h.missionEngine.StartMission(context.Background(), missionID); err != nil {
			h.logger.Error("resume: mission engine start failed, rolling back", "error", err, "mission_id", missionID)
			// Restore FAILED (the deferred rollback checks for RESUMING, but we
			// already committed IN_PROGRESS — restore explicitly here).
			if _, rbErr := h.db.ExecContext(context.Background(),
				`UPDATE missions SET status = 'FAILED', updated_at = ? WHERE id = ?`,
				now, missionID); rbErr != nil {
				h.logger.Error("resume: rollback mission status after engine failure", "error", rbErr)
			}
			// Mark as OK so the deferred rollback doesn't also fire (status is no longer RESUMING).
			resumeOK = true
			writeProblem(w, r, http.StatusInternalServerError, "Failed to start mission engine")
			return
		}
	}

	// All steps succeeded — prevent deferred rollback.
	resumeOK = true

	if h.hub != nil {
		wsChannel := "workspace:" + wsID
		h.hub.Broadcast(wsChannel, ws.ServerMessage{
			Type:    "mission.updated",
			Channel: wsChannel,
			Payload: map[string]interface{}{"id": missionID, "crew_id": crewID, "status": "IN_PROGRESS"},
		})
		for _, id := range resetIDs {
			h.hub.Broadcast(wsChannel, ws.ServerMessage{
				Type:    "task.updated",
				Channel: wsChannel,
				Payload: map[string]string{"id": id, "mission_id": missionID, "status": resetStatusMap[id]},
			})
		}
	}

	h.logger.Info("mission resumed from failure",
		"mission_id", missionID,
		"reset_tasks", len(toReset),
	)

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"id":          missionID,
		"status":      "IN_PROGRESS",
		"reset_tasks": len(toReset),
	})
}

// Clone creates a deep copy of a mission with all its tasks, assigning new IDs.
func (h *MissionHandler) Clone(w http.ResponseWriter, r *http.Request) {
	role := RoleFromContext(r.Context())
	if !canRole(role, "create") {
		writeProblem(w, r, http.StatusForbidden, "Forbidden")
		return
	}

	crewID := r.PathValue("crewId")
	missionID := r.PathValue("missionId")
	wsID := WorkspaceIDFromContext(r.Context())

	// Read original mission
	var m struct {
		Title       string
		Description sql.NullString
		LeadAgentID string
		Template    sql.NullString
	}
	err := h.db.QueryRowContext(r.Context(),
		`SELECT title, description, lead_agent_id, workflow_template
		 FROM missions WHERE id = ? AND crew_id = ? AND workspace_id = ?`,
		missionID, crewID, wsID).Scan(&m.Title, &m.Description, &m.LeadAgentID, &m.Template)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeProblem(w, r, http.StatusNotFound, "Mission not found")
			return
		}
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	// Read original tasks
	type taskRow struct {
		ID          string
		AgentID     sql.NullString
		Title       string
		Description sql.NullString
		TaskOrder   int
		DependsOn   string
		MaxIter     sql.NullInt64
	}
	rows, err := h.db.QueryContext(r.Context(),
		`SELECT id, assigned_agent_id, title, description, task_order, COALESCE(depends_on, '[]'), max_iterations
		 FROM mission_tasks WHERE mission_id = ? ORDER BY task_order`, missionID)
	if err != nil {
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	var origTasks []taskRow
	for rows.Next() {
		var t taskRow
		if err := rows.Scan(&t.ID, &t.AgentID, &t.Title, &t.Description, &t.TaskOrder, &t.DependsOn, &t.MaxIter); err != nil {
			rows.Close()
			writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
			return
		}
		origTasks = append(origTasks, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	// Create new IDs and remap dependencies
	newMissionID := generateCUID()
	idMap := make(map[string]string) // oldID -> newID
	for _, t := range origTasks {
		idMap[t.ID] = generateCUID()
	}

	now := time.Now().UTC().Format(time.RFC3339)

	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}
	defer tx.Rollback() //nolint:errcheck

	// Create synthetic chat (same pattern as Create)
	if _, err = tx.ExecContext(r.Context(),
		`INSERT INTO chats (id, agent_id, workspace_id, title, mode, status, started_at, created_at, updated_at)
		 VALUES (?, ?, ?, ?, 'MISSION', 'ACTIVE', ?, ?, ?)`,
		newMissionID, m.LeadAgentID, wsID, "Mission: "+m.Title+" (clone)", now, now, now); err != nil {
		h.logger.Error("create synthetic chat for clone", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Failed to create mission")
		return
	}

	// Insert new mission
	traceID := "mission-" + generateCUID()
	if _, err = tx.ExecContext(r.Context(),
		`INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, description,
		 status, workflow_template, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, 'PLANNING', ?, ?, ?)`,
		newMissionID, wsID, crewID, m.LeadAgentID, traceID,
		m.Title+" (copy)", m.Description, m.Template, now, now); err != nil {
		h.logger.Error("clone mission insert", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Failed to clone mission")
		return
	}

	// Insert cloned tasks with remapped dependencies
	for _, t := range origTasks {
		newTaskID := idMap[t.ID]
		newDeps := remapDependencies(t.DependsOn, idMap)
		status := "PENDING"
		if newDeps != "[]" {
			status = "BLOCKED"
		}
		if _, err = tx.ExecContext(r.Context(),
			`INSERT INTO mission_tasks (id, mission_id, assigned_agent_id, title, description,
			 status, task_order, depends_on, max_iterations, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			newTaskID, newMissionID, t.AgentID, t.Title, t.Description,
			status, t.TaskOrder, newDeps, t.MaxIter, now, now); err != nil {
			h.logger.Error("clone task insert", "error", err)
			writeProblem(w, r, http.StatusInternalServerError, "Failed to clone task")
			return
		}
	}

	if err = tx.Commit(); err != nil {
		writeProblem(w, r, http.StatusInternalServerError, "Internal server error")
		return
	}

	if h.hub != nil {
		wsChannel := "workspace:" + wsID
		h.hub.Broadcast(wsChannel, ws.ServerMessage{
			Type:    "mission.created",
			Channel: wsChannel,
			Payload: map[string]interface{}{"id": newMissionID, "crew_id": crewID, "status": "PLANNING"},
		})
	}

	writeJSON(w, http.StatusCreated, map[string]string{"id": newMissionID, "status": "PLANNING"})
}
