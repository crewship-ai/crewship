package api

import (
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/statuses"
	"github.com/crewship-ai/crewship/internal/ws"
)

// MissionHandler provides endpoints for managing missions and their tasks.
type MissionHandler struct {
	db            *sql.DB
	hub           *ws.Hub
	missionEngine *orchestrator.MissionEngine
	logger        *slog.Logger
}

// NewMissionHandler creates a MissionHandler with the given dependencies.
func NewMissionHandler(db *sql.DB, hub *ws.Hub, me *orchestrator.MissionEngine, logger *slog.Logger) *MissionHandler {
	return &MissionHandler{db: db, hub: hub, missionEngine: me, logger: logger}
}

type missionResponse struct {
	ID                 string                `json:"id"`
	WorkspaceID        string                `json:"workspace_id"`
	CrewID             string                `json:"crew_id"`
	LeadAgentID        string                `json:"lead_agent_id"`
	LeadAgentName      string                `json:"lead_agent_name,omitempty"`
	LeadAgentSlug      string                `json:"lead_agent_slug,omitempty"`
	TraceID            string                `json:"trace_id"`
	Title              string                `json:"title"`
	Description        *string               `json:"description"`
	Status             string                `json:"status"`
	Plan               *string               `json:"plan"`
	WorkflowTemplate   *string               `json:"workflow_template"`
	TotalTokenCount    *int                  `json:"total_token_count"`
	TotalEstimatedCost *float64              `json:"total_estimated_cost"`
	CreatedAt          string                `json:"created_at"`
	UpdatedAt          string                `json:"updated_at"`
	CompletedAt        *string               `json:"completed_at"`
	TaskStats          *taskStats            `json:"task_stats,omitempty"`
	Tasks              []missionTaskResponse `json:"tasks,omitempty"`
}

type missionTaskResponse struct {
	ID               string   `json:"id"`
	MissionID        string   `json:"mission_id"`
	AssignedAgentID  *string  `json:"assigned_agent_id"`
	AgentName        *string  `json:"agent_name,omitempty"`
	AgentSlug        *string  `json:"agent_slug,omitempty"`
	Title            string   `json:"title"`
	Description      *string  `json:"description"`
	Status           string   `json:"status"`
	TaskOrder        int      `json:"task_order"`
	DependsOn        string   `json:"depends_on"`
	Iteration        *int     `json:"iteration"`
	MaxIterations    *int     `json:"max_iterations"`
	ResultSummary    *string  `json:"result_summary"`
	OutputPath       *string  `json:"output_path"`
	ErrorMessage     *string  `json:"error_message"`
	AssignmentID     *string  `json:"assignment_id"`
	TokenCount       *int     `json:"token_count"`
	EstimatedCost    *float64 `json:"estimated_cost"`
	StartedAt        *string  `json:"started_at"`
	CompletedAt      *string  `json:"completed_at"`
	DurationMs       *int     `json:"duration_ms"`
	CreatedAt        string   `json:"created_at"`
	UpdatedAt        string   `json:"updated_at"`
	Confidence       *float64 `json:"confidence"`
	NeedsReview      bool     `json:"needs_review"`
	HandoffContext   *string  `json:"handoff_context"`
	EvaluationStatus *string  `json:"evaluation_status"`
	EvaluationNotes  *string  `json:"evaluation_notes"`
	ApprovalRequired bool     `json:"approval_required"`
	ApprovalStatus   *string  `json:"approval_status"`
	ApprovedBy       *string  `json:"approved_by"`
	ApprovedAt       *string  `json:"approved_at"`
}

type taskStats struct {
	Total            int `json:"total"`
	Pending          int `json:"pending"`
	Blocked          int `json:"blocked"`
	InProgress       int `json:"in_progress"`
	Completed        int `json:"completed"`
	Failed           int `json:"failed"`
	Skipped          int `json:"skipped"`
	AwaitingApproval int `json:"awaiting_approval"`
}

// validMissionTransitions and validTaskTransitions reference the canonical
// transition maps from the statuses package.
var validMissionTransitions = statuses.ValidMissionTransitions
var validTaskTransitions = statuses.ValidTaskTransitions

// parseDependencyJSON unmarshals a JSON string array of task dependency IDs.
// Returns nil if the input is empty, invalid, or contains no entries.
func parseDependencyJSON(raw string) []string {
	var deps []string
	if err := json.Unmarshal([]byte(raw), &deps); err != nil {
		return nil
	}
	return deps
}

func writeProblem(w http.ResponseWriter, r *http.Request, status int, detail string) {
	writeJSON(w, status, map[string]interface{}{
		"type":     "about:blank",
		"title":    http.StatusText(status),
		"status":   status,
		"detail":   detail,
		"instance": r.URL.Path,
	})
}

// loadTasksForMission loads all tasks for a mission with agent info.
func (h *MissionHandler) loadTasksForMission(r *http.Request, missionID string) ([]missionTaskResponse, error) {
	taskRows, err := h.db.QueryContext(r.Context(), `
		SELECT mt.id, mt.mission_id, mt.assigned_agent_id, mt.title, mt.description,
		       mt.status, mt.task_order, mt.depends_on, mt.iteration, mt.max_iterations,
		       mt.result_summary, mt.output_path, mt.error_message, mt.assignment_id,
		       mt.token_count, mt.estimated_cost, mt.started_at, mt.completed_at,
		       mt.duration_ms, mt.created_at, mt.updated_at,
		       ag.name, ag.slug,
		       mt.confidence, COALESCE(mt.needs_review, 0), mt.handoff_context,
		       mt.evaluation_status, mt.evaluation_notes,
		       COALESCE(mt.approval_required, 0), mt.approval_status, mt.approved_by, mt.approved_at
		FROM mission_tasks mt
		LEFT JOIN agents ag ON ag.id = mt.assigned_agent_id
		WHERE mt.mission_id = ?
		ORDER BY mt.task_order ASC`, missionID)
	if err != nil {
		return nil, err
	}
	defer taskRows.Close()

	tasks := []missionTaskResponse{}
	for taskRows.Next() {
		var t missionTaskResponse
		if err := taskRows.Scan(
			&t.ID, &t.MissionID, &t.AssignedAgentID, &t.Title, &t.Description,
			&t.Status, &t.TaskOrder, &t.DependsOn, &t.Iteration, &t.MaxIterations,
			&t.ResultSummary, &t.OutputPath, &t.ErrorMessage, &t.AssignmentID,
			&t.TokenCount, &t.EstimatedCost, &t.StartedAt, &t.CompletedAt,
			&t.DurationMs, &t.CreatedAt, &t.UpdatedAt,
			&t.AgentName, &t.AgentSlug,
			&t.Confidence, &t.NeedsReview, &t.HandoffContext,
			&t.EvaluationStatus, &t.EvaluationNotes,
			&t.ApprovalRequired, &t.ApprovalStatus, &t.ApprovedBy, &t.ApprovedAt,
		); err != nil {
			return nil, err
		}
		tasks = append(tasks, t)
	}
	return tasks, taskRows.Err()
}

func (h *MissionHandler) getTaskStats(r *http.Request, missionID string) (*taskStats, error) {
	rows, err := h.db.QueryContext(r.Context(),
		`SELECT status, COUNT(*) FROM mission_tasks WHERE mission_id = ? GROUP BY status`,
		missionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	stats := &taskStats{}
	for rows.Next() {
		var status string
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return nil, err
		}
		applyTaskStatCount(stats, status, count)
	}
	return stats, rows.Err()
}

// getBatchTaskStats returns task stats for multiple missions in a single query.
func (h *MissionHandler) getBatchTaskStats(r *http.Request, missionIDs []string) (map[string]*taskStats, error) {
	if len(missionIDs) == 0 {
		return map[string]*taskStats{}, nil
	}

	args := make([]interface{}, len(missionIDs))
	for i, id := range missionIDs {
		args[i] = id
	}

	rows, err := h.db.QueryContext(r.Context(),
		`SELECT mission_id, status, COUNT(*) FROM mission_tasks WHERE mission_id IN (`+
			sqlPlaceholders(len(missionIDs))+`) GROUP BY mission_id, status`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string]*taskStats, len(missionIDs))
	for rows.Next() {
		var missionID, status string
		var count int
		if err := rows.Scan(&missionID, &status, &count); err != nil {
			return nil, err
		}
		s, ok := result[missionID]
		if !ok {
			s = &taskStats{}
			result[missionID] = s
		}
		applyTaskStatCount(s, status, count)
	}
	return result, rows.Err()
}

func applyTaskStatCount(s *taskStats, status string, count int) {
	s.Total += count
	switch status {
	case "PENDING":
		s.Pending = count
	case "BLOCKED":
		s.Blocked = count
	case "IN_PROGRESS":
		s.InProgress = count
	case "COMPLETED":
		s.Completed = count
	case "FAILED":
		s.Failed = count
	case "SKIPPED":
		s.Skipped = count
	case "AWAITING_APPROVAL":
		s.AwaitingApproval = count
	}
}
