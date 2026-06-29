package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// missionSelectColumns is the shared SELECT clause for fetching missions with agent info.
const missionSelectColumns = `
	SELECT m.id, m.workspace_id, m.crew_id, m.lead_agent_id, m.trace_id, m.title,
	       m.description, m.status, m.plan, m.workflow_template,
	       m.total_token_count, m.total_estimated_cost,
	       m.created_at, m.updated_at, m.completed_at,
	       a.name, a.slug
	FROM missions m
	JOIN agents a ON a.id = m.lead_agent_id`

// scanMission scans a row into a missionResponse using the standard column order.
func scanMission(s interface{ Scan(...interface{}) error }) (missionResponse, error) {
	var m missionResponse
	err := s.Scan(
		&m.ID, &m.WorkspaceID, &m.CrewID, &m.LeadAgentID, &m.TraceID, &m.Title,
		&m.Description, &m.Status, &m.Plan, &m.WorkflowTemplate,
		&m.TotalTokenCount, &m.TotalEstimatedCost,
		&m.CreatedAt, &m.UpdatedAt, &m.CompletedAt,
		&m.LeadAgentName, &m.LeadAgentSlug,
	)
	return m, err
}

// Create handles POST /api/v1/crews/{crewId}/missions

func (h *MissionHandler) List(w http.ResponseWriter, r *http.Request) {
	crewID := r.PathValue("crewId")
	wsID := WorkspaceIDFromContext(r.Context())
	status := r.URL.Query().Get("status")

	limit, offset := parsePagination(r, 20, 100)

	query := missionSelectColumns + `
		WHERE m.crew_id = ? AND m.workspace_id = ?`
	args := []interface{}{crewID, wsID}

	if status != "" {
		query += " AND m.status = ?"
		args = append(args, status)
	}
	query += " ORDER BY m.created_at DESC LIMIT ? OFFSET ?"
	args = append(args, limit, offset)

	rows, err := h.db.QueryContext(r.Context(), query, args...)
	if err != nil {
		internalError(w, r, h.logger, "list missions", err)
		return
	}
	defer rows.Close()

	var result []missionResponse
	for rows.Next() {
		m, err := scanMission(rows)
		if err != nil {
			internalError(w, r, h.logger, "scan mission", err)
			return
		}
		result = append(result, m)
	}
	if err := rows.Err(); err != nil {
		internalError(w, r, h.logger, "rows iteration (missions)", err)
		return
	}

	// Load task stats for all missions in a single batch query
	if len(result) > 0 {
		ids := make([]string, len(result))
		for i := range result {
			ids[i] = result[i].ID
		}
		statsMap, err := h.getBatchTaskStats(r, ids)
		if err != nil {
			h.logger.Error("batch get task stats", "error", err)
		} else {
			for i := range result {
				result[i].TaskStats = statsMap[result[i].ID]
			}
		}
	}

	if result == nil {
		result = []missionResponse{}
	}
	writeJSON(w, http.StatusOK, result)
}

// ListAll handles GET /api/v1/missions
func (h *MissionHandler) ListAll(w http.ResponseWriter, r *http.Request) {
	wsID := WorkspaceIDFromContext(r.Context())
	status := r.URL.Query().Get("status")
	includeTasks := r.URL.Query().Get("include_tasks") == "true"

	limit, offset := parsePagination(r, 20, 100)

	query := missionSelectColumns + `
		WHERE m.workspace_id = ?`
	args := []interface{}{wsID}

	if status != "" {
		query += " AND m.status = ?"
		args = append(args, status)
	}
	query += " ORDER BY m.created_at DESC LIMIT ? OFFSET ?"
	args = append(args, limit, offset)

	rows, err := h.db.QueryContext(r.Context(), query, args...)
	if err != nil {
		internalError(w, r, h.logger, "list all missions", err)
		return
	}
	defer rows.Close()

	var result []missionResponse
	for rows.Next() {
		m, err := scanMission(rows)
		if err != nil {
			internalError(w, r, h.logger, "scan mission", err)
			return
		}
		result = append(result, m)
	}
	if err := rows.Err(); err != nil {
		internalError(w, r, h.logger, "rows iteration (missions all)", err)
		return
	}

	// Load task stats for all missions in a single batch query
	if len(result) > 0 {
		ids := make([]string, len(result))
		for i := range result {
			ids[i] = result[i].ID
		}
		statsMap, statsErr := h.getBatchTaskStats(r, ids)
		if statsErr != nil {
			h.logger.Error("batch get task stats", "error", statsErr)
		}
		for i := range result {
			if statsMap != nil {
				result[i].TaskStats = statsMap[result[i].ID]
			}
			if includeTasks {
				tasks, tasksErr := h.loadTasksForMission(r, result[i].ID)
				if tasksErr != nil {
					h.logger.Error("load tasks for mission", "mission_id", result[i].ID, "error", tasksErr)
					result[i].Tasks = []missionTaskResponse{}
				} else {
					result[i].Tasks = tasks
				}
			}
		}
	}

	if result == nil {
		result = []missionResponse{}
	}
	writeJSON(w, http.StatusOK, result)
}

// Get handles GET /api/v1/crews/{crewId}/missions/{missionId}
func (h *MissionHandler) Get(w http.ResponseWriter, r *http.Request) {
	crewID := r.PathValue("crewId")
	missionID := r.PathValue("missionId")
	wsID := WorkspaceIDFromContext(r.Context())

	m, err := scanMission(h.db.QueryRowContext(r.Context(),
		missionSelectColumns+` WHERE m.id = ? AND m.crew_id = ? AND m.workspace_id = ?`,
		missionID, crewID, wsID))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeProblem(w, r, http.StatusNotFound, "Mission not found")
			return
		}
		internalError(w, r, h.logger, "get mission", err)
		return
	}

	// Load tasks
	tasks, tasksErr := h.loadTasksForMission(r, missionID)
	if tasksErr != nil {
		internalError(w, r, h.logger, "get mission tasks", tasksErr)
		return
	}
	m.Tasks = tasks

	stats, statsErr := h.getTaskStats(r, missionID)
	if statsErr != nil {
		h.logger.Error("get task stats", "mission_id", missionID, "error", statsErr)
	}
	m.TaskStats = stats

	writeJSON(w, http.StatusOK, m)
}

// Update handles PATCH /api/v1/crews/{crewId}/missions/{missionId}

func (h *MissionHandler) Start(w http.ResponseWriter, r *http.Request) {
	if !requireRole(w, r, "create") {
		return
	}

	crewID := r.PathValue("crewId")
	missionID := r.PathValue("missionId")
	wsID := WorkspaceIDFromContext(r.Context())

	var currentStatus string
	err := h.db.QueryRowContext(r.Context(),
		`SELECT status FROM missions WHERE id = ? AND crew_id = ? AND workspace_id = ?`,
		missionID, crewID, wsID).Scan(&currentStatus)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeProblem(w, r, http.StatusNotFound, "Mission not found")
			return
		}
		internalError(w, r, h.logger, "get mission for start", err)
		return
	}

	if currentStatus != "PLANNING" {
		writeProblem(w, r, http.StatusBadRequest,
			fmt.Sprintf("cannot start mission in %s state, must be PLANNING", currentStatus))
		return
	}

	// Validate DAG before starting (circular deps, nonexistent dep IDs)
	if h.missionEngine != nil {
		if dagErr := h.missionEngine.ValidateDAG(r.Context(), missionID); dagErr != nil {
			writeProblem(w, r, http.StatusBadRequest, "Invalid task DAG: "+dagErr.Error())
			return
		}
	}

	now := time.Now().UTC().Format(time.RFC3339)
	// Atomic compare-and-swap: only update if still PLANNING (prevents concurrent start race)
	res, err := h.db.ExecContext(r.Context(),
		`UPDATE missions SET status = 'IN_PROGRESS', updated_at = ? WHERE id = ? AND status = 'PLANNING'`,
		now, missionID)
	if err != nil {
		h.logger.Error("update mission status", "error", err)
		writeProblem(w, r, http.StatusInternalServerError, "Failed to start mission")
		return
	}
	rows, err := res.RowsAffected()
	if err != nil {
		internalError(w, r, h.logger, "check rows affected", err)
		return
	}
	if rows == 0 {
		writeProblem(w, r, http.StatusConflict, "Mission was already started by another request")
		return
	}

	if h.missionEngine != nil {
		if err := h.missionEngine.StartMission(context.Background(), missionID); err != nil {
			h.logger.Error("mission engine start failed, rolling back to PLANNING", "error", err, "mission_id", missionID)
			if _, rbErr := h.db.ExecContext(r.Context(),
				`UPDATE missions SET status = 'PLANNING', updated_at = ? WHERE id = ?`,
				now, missionID); rbErr != nil {
				h.logger.Error("rollback mission status", "error", rbErr, "mission_id", missionID)
			}
			writeProblem(w, r, http.StatusInternalServerError, "Failed to start mission engine")
			return
		}
	}

	broadcastChannelEvent(h.hub, "mission", missionID, "mission.updated",
		map[string]interface{}{"id": missionID, "status": "IN_PROGRESS"})
	broadcastWorkspaceEvent(h.hub, wsID, "mission.updated",
		map[string]interface{}{"id": missionID, "crew_id": crewID, "status": "IN_PROGRESS"})

	writeJSON(w, http.StatusOK, map[string]string{"id": missionID, "status": "IN_PROGRESS"})
}

// Metrics handles GET /api/v1/mission-metrics
func (h *MissionHandler) Metrics(w http.ResponseWriter, r *http.Request) {
	wsID := WorkspaceIDFromContext(r.Context())

	type metricsResponse struct {
		TotalMissions       int     `json:"total_missions"`
		ActiveMissions      int     `json:"active_missions"`
		Completed24h        int     `json:"completed_24h"`
		Failed24h           int     `json:"failed_24h"`
		TotalTokens24h      int     `json:"total_tokens_24h"`
		TotalCost24h        float64 `json:"total_cost_24h"`
		AvgCompletionTimeMs int     `json:"avg_completion_time_ms"`
		TasksCompleted24h   int     `json:"tasks_completed_24h"`
		TasksFailed24h      int     `json:"tasks_failed_24h"`
	}

	var m metricsResponse
	cutoff := time.Now().Add(-24 * time.Hour).UTC().Format(time.RFC3339)

	// Total and active missions
	err := h.db.QueryRowContext(r.Context(), `
		SELECT
			COUNT(*),
			COUNT(*) FILTER (WHERE status IN ('IN_PROGRESS', 'PLANNING', 'REVIEW'))
		FROM missions WHERE workspace_id = ?`, wsID).Scan(&m.TotalMissions, &m.ActiveMissions)
	if err != nil {
		// SQLite doesn't support FILTER — fallback to CASE.
		// COALESCE needed because SUM returns NULL for empty workspaces.
		err = h.db.QueryRowContext(r.Context(), `
			SELECT
				COUNT(*),
				COALESCE(SUM(CASE WHEN status IN ('IN_PROGRESS', 'PLANNING', 'REVIEW') THEN 1 ELSE 0 END), 0)
			FROM missions WHERE workspace_id = ?`, wsID).Scan(&m.TotalMissions, &m.ActiveMissions)
		if err != nil {
			internalError(w, r, h.logger, "mission metrics: totals", err)
			return
		}
	}

	// 24h mission counts (completed_at for COMPLETED, updated_at for FAILED since failed missions may lack completed_at).
	// COALESCE needed because SUM returns NULL for empty workspaces.
	if err := h.db.QueryRowContext(r.Context(), `
		SELECT
			COALESCE(SUM(CASE WHEN status = 'COMPLETED' AND completed_at >= ? THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN status = 'FAILED' AND updated_at >= ? THEN 1 ELSE 0 END), 0)
		FROM missions WHERE workspace_id = ?`,
		cutoff, cutoff, wsID).Scan(&m.Completed24h, &m.Failed24h); err != nil {
		h.logger.Warn("mission metrics: 24h mission counts query failed", "error", err)
	}

	// 24h token/cost from tasks completed in the window (avoids counting lifetime totals of long-running missions)
	if err := h.db.QueryRowContext(r.Context(), `
		SELECT
			COALESCE(SUM(COALESCE(mt.tokens_used, mt.token_count, 0)), 0),
			COALESCE(SUM(COALESCE(mt.estimated_cost, 0)), 0)
		FROM mission_tasks mt
		JOIN missions m ON m.id = mt.mission_id
		WHERE m.workspace_id = ? AND mt.completed_at >= ?`,
		wsID, cutoff).Scan(&m.TotalTokens24h, &m.TotalCost24h); err != nil {
		h.logger.Warn("mission metrics: 24h token/cost query failed", "error", err)
	}

	// Average completion time (completed missions in last 24h)
	if err := h.db.QueryRowContext(r.Context(), `
		SELECT COALESCE(AVG(
			CAST((julianday(completed_at) - julianday(created_at)) * 86400000 AS INTEGER)
		), 0)
		FROM missions
		WHERE workspace_id = ? AND status = 'COMPLETED' AND completed_at >= ?`,
		wsID, cutoff).Scan(&m.AvgCompletionTimeMs); err != nil {
		h.logger.Warn("mission metrics: avg completion time query failed", "error", err)
	}

	// 24h task stats — COALESCE needed because SUM returns NULL for workspaces with no tasks.
	if err := h.db.QueryRowContext(r.Context(), `
		SELECT
			COALESCE(SUM(CASE WHEN mt.status = 'COMPLETED' AND mt.completed_at >= ? THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN mt.status = 'FAILED' AND mt.updated_at >= ? THEN 1 ELSE 0 END), 0)
		FROM mission_tasks mt
		JOIN missions m ON m.id = mt.mission_id
		WHERE m.workspace_id = ?`,
		cutoff, cutoff, wsID).Scan(&m.TasksCompleted24h, &m.TasksFailed24h); err != nil {
		h.logger.Warn("mission metrics: task stats query failed", "error", err)
	}

	writeJSON(w, http.StatusOK, m)
}
