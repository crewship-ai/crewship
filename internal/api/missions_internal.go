package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/ws"
)

// InternalMissionHandler handles mission endpoints called by the sidecar
// on behalf of lead agents. Uses internal token auth, not JWT.
type InternalMissionHandler struct {
	db            *sql.DB
	hub           *ws.Hub
	missionEngine *orchestrator.MissionEngine
	logger        *slog.Logger
}

// NewInternalMissionHandler creates an InternalMissionHandler for sidecar-facing mission endpoints.
func NewInternalMissionHandler(db *sql.DB, hub *ws.Hub, me *orchestrator.MissionEngine, logger *slog.Logger) *InternalMissionHandler {
	return &InternalMissionHandler{db: db, hub: hub, missionEngine: me, logger: logger}
}

// Create handles POST /api/v1/internal/missions
// Creates a mission and optionally its tasks in one call.
func (h *InternalMissionHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Title            string  `json:"title"`
		Description      *string `json:"description"`
		LeadAgentID      string  `json:"lead_agent_id"`
		CrewID           string  `json:"crew_id"`
		WorkspaceID      string  `json:"workspace_id"`
		Plan             *string `json:"plan"`
		WorkflowTemplate *string `json:"workflow_template"`
		Tasks            []struct {
			Title           string   `json:"title"`
			Description     *string  `json:"description"`
			AssignedAgentID *string  `json:"assigned_agent_id"`
			TaskOrder       int      `json:"task_order"`
			DependsOn       []string `json:"depends_on"`
			MaxIterations   *int     `json:"max_iterations"`
		} `json:"tasks"`
	}
	if err := readJSON(r, &req); err != nil {
		replyError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.Title == "" || req.LeadAgentID == "" || req.CrewID == "" || req.WorkspaceID == "" {
		replyError(w, http.StatusBadRequest, "title, lead_agent_id, crew_id, workspace_id required")
		return
	}
	// PR-F24 F-4: a bound token may only create missions in its own
	// workspace; the body-carried workspace_id is checked here because
	// requireInternal cannot inspect request bodies.
	if !assertInternalTokenWorkspace(w, r, req.WorkspaceID) {
		return
	}

	// SECURITY (defense-in-depth): verify the lead agent actually belongs to
	// the supplied crew+workspace. Without this, a compromised agent could
	// create a mission in another crew with itself as lead (cross-crew override).
	var exists int
	err := h.db.QueryRowContext(r.Context(),
		`SELECT 1 FROM agents WHERE id = ? AND crew_id = ? AND workspace_id = ? AND deleted_at IS NULL`,
		req.LeadAgentID, req.CrewID, req.WorkspaceID).Scan(&exists)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			replyError(w, http.StatusBadRequest, "lead agent does not belong to the specified crew/workspace")
			return
		}
		h.logger.Error("validate lead agent", "error", err)
		replyError(w, http.StatusInternalServerError, "internal error")
		return
	}

	id := generateCUID()
	traceID := "mission-" + generateCUID()
	now := time.Now().UTC().Format(time.RFC3339)

	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		h.logger.Error("begin tx", "error", err)
		replyError(w, http.StatusInternalServerError, "internal error")
		return
	}
	defer tx.Rollback()

	_, err = tx.ExecContext(r.Context(), `
		INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title, description, status, plan, workflow_template, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, 'PLANNING', ?, ?, ?, ?)`,
		id, req.WorkspaceID, req.CrewID, req.LeadAgentID, traceID,
		req.Title, req.Description, req.Plan, req.WorkflowTemplate, now, now)
	if err != nil {
		h.logger.Error("create mission", "error", err)
		replyError(w, http.StatusInternalServerError, "failed to create mission")
		return
	}

	// Create tasks if provided (batch creation)
	// Task IDs are generated server-side; depends_on references use temp IDs
	// that map to task_order for resolution.
	taskIDs := make(map[int]string) // task_order -> generated ID
	for _, t := range req.Tasks {
		taskID := generateCUID()
		taskIDs[t.TaskOrder] = taskID

		depsJSON := "[]"
		if len(t.DependsOn) > 0 {
			b, _ := json.Marshal(t.DependsOn)
			depsJSON = string(b)
		}

		status := "PENDING"
		if len(t.DependsOn) > 0 {
			status = "BLOCKED"
		}

		_, err = tx.ExecContext(r.Context(), `
			INSERT INTO mission_tasks (id, mission_id, assigned_agent_id, title, description, status, task_order, depends_on, max_iterations, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			taskID, id, t.AssignedAgentID, t.Title, t.Description, status, t.TaskOrder, depsJSON, t.MaxIterations, now, now)
		if err != nil {
			h.logger.Error("create mission task", "error", err)
			replyError(w, http.StatusInternalServerError, "failed to create task: "+t.Title)
			return
		}
	}

	if err := tx.Commit(); err != nil {
		h.logger.Error("commit tx", "error", err)
		replyError(w, http.StatusInternalServerError, "internal error")
		return
	}

	broadcastChannelEvent(h.hub, "crew", req.CrewID, "mission.created",
		map[string]string{"id": id, "title": req.Title})

	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"id":       id,
		"trace_id": traceID,
		"status":   "PLANNING",
		"tasks":    taskIDs,
	})
}

// Start handles POST /api/v1/internal/missions/{missionId}/start
// Transitions a PLANNING mission to IN_PROGRESS and kicks off the MissionEngine.
func (h *InternalMissionHandler) Start(w http.ResponseWriter, r *http.Request) {
	missionID := r.PathValue("missionId")
	if missionID == "" {
		// Try extracting from URL path directly
		parts := strings.Split(r.URL.Path, "/")
		for i, p := range parts {
			if p == "missions" && i+1 < len(parts) {
				missionID = parts[i+1]
				break
			}
		}
	}

	// SECURITY (defense-in-depth): scope the mission lookup to the caller's
	// workspace (and crew, when supplied). Without this, a compromised sidecar
	// or an agent that enumerated a mission id could start a mission belonging
	// to another crew/workspace. Scope is sourced from the trusted IPC identity
	// the sidecar forwards as query params (workspace_id required, crew_id
	// optional), mirroring InternalIssueHandler.Get.
	wsID := r.URL.Query().Get("workspace_id")
	if wsID == "" {
		replyError(w, http.StatusBadRequest, "workspace_id is required")
		return
	}
	crewID := r.URL.Query().Get("crew_id")

	selArgs := []any{missionID, wsID}
	selQuery := `SELECT status FROM missions WHERE id = ? AND workspace_id = ?`
	if crewID != "" {
		selQuery = `SELECT status FROM missions WHERE id = ? AND workspace_id = ? AND crew_id = ?`
		selArgs = []any{missionID, wsID, crewID}
	}

	var currentStatus string
	err := h.db.QueryRowContext(r.Context(), selQuery, selArgs...).Scan(&currentStatus)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			replyError(w, http.StatusNotFound, "mission not found")
			return
		}
		h.logger.Error("get mission", "error", err)
		replyError(w, http.StatusInternalServerError, "internal error")
		return
	}

	if currentStatus != "PLANNING" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": fmt.Sprintf("cannot start mission in %s state, must be PLANNING", currentStatus),
		})
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)
	updQuery := `UPDATE missions SET status = 'IN_PROGRESS', updated_at = ? WHERE id = ? AND workspace_id = ?`
	updArgs := []any{now, missionID, wsID}
	if crewID != "" {
		updQuery = `UPDATE missions SET status = 'IN_PROGRESS', updated_at = ? WHERE id = ? AND workspace_id = ? AND crew_id = ?`
		updArgs = []any{now, missionID, wsID, crewID}
	}
	if _, err := h.db.ExecContext(r.Context(), updQuery, updArgs...); err != nil {
		h.logger.Error("update mission status", "error", err)
		replyError(w, http.StatusInternalServerError, "failed to start mission")
		return
	}

	// Start the MissionEngine loop for this mission
	if h.missionEngine != nil {
		if err := h.missionEngine.StartMission(context.Background(), missionID); err != nil {
			h.logger.Error("mission engine start failed", "error", err, "mission_id", missionID)
			// Don't fail the request -- the mission is IN_PROGRESS in DB, engine can catch up
		}
	}

	writeJSON(w, http.StatusOK, map[string]string{"id": missionID, "status": "IN_PROGRESS"})
}

// Get handles GET /api/v1/internal/missions/{missionId}
// Returns mission with tasks and task stats.
func (h *InternalMissionHandler) Get(w http.ResponseWriter, r *http.Request) {
	missionID := r.PathValue("missionId")
	if missionID == "" {
		parts := strings.Split(r.URL.Path, "/")
		for i, p := range parts {
			if p == "missions" && i+1 < len(parts) {
				missionID = parts[i+1]
				break
			}
		}
	}

	// SECURITY (defense-in-depth): scope the mission lookup to the caller's
	// workspace (and crew, when supplied) so an enumerated mission id from
	// another crew/workspace cannot be read. Mirrors InternalIssueHandler.Get.
	wsID := r.URL.Query().Get("workspace_id")
	if wsID == "" {
		replyError(w, http.StatusBadRequest, "workspace_id is required")
		return
	}
	crewID := r.URL.Query().Get("crew_id")

	var m struct {
		ID          string  `json:"id"`
		TraceID     string  `json:"trace_id"`
		Title       string  `json:"title"`
		Description *string `json:"description"`
		Status      string  `json:"status"`
		CreatedAt   string  `json:"created_at"`
	}
	getQuery := `SELECT id, trace_id, title, description, status, created_at FROM missions WHERE id = ? AND workspace_id = ?`
	getArgs := []any{missionID, wsID}
	if crewID != "" {
		getQuery = `SELECT id, trace_id, title, description, status, created_at FROM missions WHERE id = ? AND workspace_id = ? AND crew_id = ?`
		getArgs = []any{missionID, wsID, crewID}
	}
	err := h.db.QueryRowContext(r.Context(), getQuery, getArgs...).Scan(&m.ID, &m.TraceID, &m.Title, &m.Description, &m.Status, &m.CreatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			replyError(w, http.StatusNotFound, "mission not found")
			return
		}
		h.logger.Error("get mission", "error", err)
		replyError(w, http.StatusInternalServerError, "internal error")
		return
	}

	// Load tasks
	rows, err := h.db.QueryContext(r.Context(), `
		SELECT id, title, status, assigned_agent_id, depends_on, result_summary, error_message, task_order
		FROM mission_tasks WHERE mission_id = ? ORDER BY task_order`,
		missionID)
	if err != nil {
		h.logger.Error("get tasks", "error", err)
		replyError(w, http.StatusInternalServerError, "internal error")
		return
	}
	defer rows.Close()

	type taskSummary struct {
		ID              string  `json:"id"`
		Title           string  `json:"title"`
		Status          string  `json:"status"`
		AssignedAgentID *string `json:"assigned_agent_id"`
		DependsOn       string  `json:"depends_on"`
		ResultSummary   *string `json:"result_summary"`
		ErrorMessage    *string `json:"error_message"`
		TaskOrder       int     `json:"task_order"`
	}
	var tasks []taskSummary
	for rows.Next() {
		var t taskSummary
		if err := rows.Scan(&t.ID, &t.Title, &t.Status, &t.AssignedAgentID, &t.DependsOn, &t.ResultSummary, &t.ErrorMessage, &t.TaskOrder); err != nil {
			continue
		}
		tasks = append(tasks, t)
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"mission": m,
		"tasks":   tasks,
	})
}
