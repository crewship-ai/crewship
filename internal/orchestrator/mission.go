package orchestrator

import (
	"context"
	crand "crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/crewship-ai/crewship/internal/ws"
)

// TaskDispatcher runs an assignment for a given agent. Implemented by the
// AssignmentHandler in the api package to reuse credential loading, container
// management, and the orchestrator's RunAgentForAssignment flow.
type TaskDispatcher interface {
	DispatchAssignment(ctx context.Context, req DispatchRequest) error
}

// DispatchRequest contains everything needed to dispatch a mission task.
type DispatchRequest struct {
	AssignmentID string
	AgentID      string
	AgentSlug    string
	CrewID       string
	CrewSlug     string
	WorkspaceID  string
	ChatID       string // mission ID used as pseudo-chat for grouping
	Task         string
	TraceID      string // mission trace ID for end-to-end observability
	MissionID    string
}

// MissionEngine manages the lifecycle of missions and their tasks.
// It bridges the mission model (DB) with the assignment system (orchestrator)
// by resolving task dependencies, scheduling ready tasks, and tracking completion.
type MissionEngine struct {
	db         *sql.DB
	orch       *Orchestrator
	hub        *ws.Hub
	pw         *ProgressWriter
	lc         *LoopController
	dispatcher TaskDispatcher
	logger     *slog.Logger

	mu       sync.Mutex
	active   map[string]*missionState // missionID -> state
	stopping bool

	// Circuit breaker: tracks consecutive failures per agent
	cbMu     sync.Mutex
	failures map[string]int // agentID -> consecutive failure count
}

const (
	circuitBreakerThreshold = 3 // consecutive failures before tripping
	maxResultSummaryLen     = 8000
	missionTimeoutDefault   = 2 * time.Hour
)

// SetDispatcher registers the assignment dispatcher.
func (e *MissionEngine) SetDispatcher(d TaskDispatcher) {
	e.dispatcher = d
}

type missionState struct {
	ID          string
	CrewID      string
	CrewSlug    string
	LeadAgentID string
	TraceID     string
	WorkspaceID string
	cancel      context.CancelFunc
}

func NewMissionEngine(db *sql.DB, orch *Orchestrator, hub *ws.Hub, logger *slog.Logger) *MissionEngine {
	pw := NewProgressWriter()
	return &MissionEngine{
		db:       db,
		orch:     orch,
		hub:      hub,
		pw:       pw,
		lc:       NewLoopController(db, pw, logger),
		logger:   logger,
		active:   make(map[string]*missionState),
		failures: make(map[string]int),
	}
}

// MissionTaskDef represents a task definition from a mission plan.
type MissionTaskDef struct {
	Title         string   `json:"title"`
	Description   string   `json:"description,omitempty"`
	AgentSlug     string   `json:"agent"`
	Order         int      `json:"order"`
	DependsOn     []string `json:"depends_on,omitempty"`
	MaxIterations *int     `json:"max_iterations,omitempty"`
}

// MissionPlan is the structured plan created by the lead agent.
type MissionPlan struct {
	Tasks []MissionTaskDef `json:"tasks"`
}

// TaskInfo holds task data read from the database for scheduling decisions.
type TaskInfo struct {
	ID              string
	MissionID       string
	AssignedAgentID *string
	AgentSlug       *string
	Title           string
	Description     *string
	Status          string
	TaskOrder       int
	DependsOn       string // JSON array of task IDs
	Iteration       int
	MaxIterations   *int
	ResultSummary   *string // populated for completed tasks (context propagation)
}

// StartMission begins orchestrating a mission that is in IN_PROGRESS status.
// It resolves ready tasks and schedules them as assignments.
func (e *MissionEngine) StartMission(ctx context.Context, missionID string) error {
	e.mu.Lock()
	if e.stopping {
		e.mu.Unlock()
		return fmt.Errorf("mission engine is shutting down")
	}
	if _, exists := e.active[missionID]; exists {
		e.mu.Unlock()
		return fmt.Errorf("mission %s is already active", missionID)
	}
	e.mu.Unlock()

	var ms missionState
	var crewSlug string
	err := e.db.QueryRowContext(ctx, `
		SELECT m.id, m.crew_id, m.lead_agent_id, m.trace_id, m.workspace_id, c.slug
		FROM missions m
		JOIN crews c ON c.id = m.crew_id
		WHERE m.id = ?`, missionID).Scan(
		&ms.ID, &ms.CrewID, &ms.LeadAgentID, &ms.TraceID, &ms.WorkspaceID, &crewSlug,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("mission not found: %s", missionID)
		}
		return fmt.Errorf("load mission: %w", err)
	}
	ms.CrewSlug = crewSlug

	mCtx, cancel := context.WithTimeout(ctx, missionTimeoutDefault)
	ms.cancel = cancel

	e.mu.Lock()
	e.active[missionID] = &ms
	e.mu.Unlock()

	e.logger.Info("mission started", "mission_id", missionID, "crew", crewSlug)
	e.pw.WriteEvent(ms.TraceID, ms.CrewSlug, ProgressEvent{
		Type:      "mission_started",
		MissionID: missionID,
	})

	go e.runMissionLoop(mCtx, &ms)
	return nil
}

// StopMission cancels an active mission's orchestration loop.
func (e *MissionEngine) StopMission(missionID string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if ms, ok := e.active[missionID]; ok {
		ms.cancel()
		delete(e.active, missionID)
		e.logger.Info("mission stopped", "mission_id", missionID)
	}
}

// Shutdown stops all active missions gracefully.
func (e *MissionEngine) Shutdown() {
	e.mu.Lock()
	e.stopping = true
	for id, ms := range e.active {
		ms.cancel()
		delete(e.active, id)
	}
	e.mu.Unlock()
	e.logger.Info("mission engine shut down")
}

// runMissionLoop is the main orchestration loop for a single mission.
// It periodically checks for ready tasks and schedules them.
func (e *MissionEngine) runMissionLoop(ctx context.Context, ms *missionState) {
	defer func() {
		// If context timed out, mark mission as FAILED
		if ctx.Err() == context.DeadlineExceeded {
			e.logger.Warn("mission timed out", "mission_id", ms.ID)
			now := time.Now().UTC().Format(time.RFC3339)
			e.db.ExecContext(context.Background(),
				`UPDATE missions SET status = 'FAILED', updated_at = ?, completed_at = ? WHERE id = ? AND status = 'IN_PROGRESS'`,
				now, now, ms.ID)
			e.broadcastMissionStatus(ms, "FAILED")
			e.pw.WriteEvent(ms.TraceID, ms.CrewSlug, ProgressEvent{
				Type:      "mission_timeout",
				MissionID: ms.ID,
			})
		}
		e.mu.Lock()
		delete(e.active, ms.ID)
		e.mu.Unlock()
	}()

	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			status, err := e.getMissionStatus(ctx, ms.ID)
			if err != nil {
				e.logger.Error("check mission status", "mission_id", ms.ID, "error", err)
				continue
			}

			// Mission is no longer in progress -- stop orchestrating
			if status != "IN_PROGRESS" {
				e.logger.Info("mission no longer in progress", "mission_id", ms.ID, "status", status)
				return
			}

			if err := e.scheduleReadyTasks(ctx, ms); err != nil {
				e.logger.Error("schedule ready tasks", "mission_id", ms.ID, "error", err)
			}

			if err := e.checkMissionCompletion(ctx, ms); err != nil {
				e.logger.Error("check mission completion", "mission_id", ms.ID, "error", err)
			}
		}
	}
}

// ResolveReadyTasks returns tasks that have all dependencies completed
// and are in PENDING status (ready to be scheduled).
// Unassigned tasks are auto-assigned to an available crew member or the lead agent.
func (e *MissionEngine) ResolveReadyTasks(ctx context.Context, missionID string) ([]TaskInfo, error) {
	tasks, err := e.loadTasks(ctx, missionID)
	if err != nil {
		return nil, err
	}

	completed := make(map[string]bool)
	for _, t := range tasks {
		if t.Status == "COMPLETED" {
			completed[t.ID] = true
		}
	}

	var ready []TaskInfo
	for i, t := range tasks {
		if t.Status != "PENDING" {
			continue
		}

		deps, err := parseDependsOn(t.DependsOn)
		if err != nil {
			e.logger.Warn("invalid depends_on", "task_id", t.ID, "error", err)
			continue
		}

		allDone := true
		for _, dep := range deps {
			if !completed[dep] {
				allDone = false
				break
			}
		}
		if !allDone {
			continue
		}

		// Auto-assign unassigned tasks
		if t.AssignedAgentID == nil {
			agentID, agentSlug, autoErr := e.autoAssignTask(ctx, missionID, t.ID)
			if autoErr != nil {
				e.logger.Error("auto-assign failed, marking task FAILED",
					"task_id", t.ID, "error", autoErr)
				e.mu.Lock()
				ms := e.active[missionID]
				e.mu.Unlock()
				if ms != nil {
					e.updateTaskStatus(ctx, ms, t.ID, "FAILED",
						"No agent assigned and auto-assignment failed: "+autoErr.Error())
				}
				continue
			}
			tasks[i].AssignedAgentID = &agentID
			tasks[i].AgentSlug = &agentSlug
			t = tasks[i]
			e.logger.Info("task auto-assigned",
				"task_id", t.ID, "agent", agentSlug)
		}

		ready = append(ready, t)
	}
	return ready, nil
}

// autoAssignTask picks an available agent from the mission's crew for an unassigned task.
// Priority: non-LEAD agents first, then the LEAD agent as fallback.
func (e *MissionEngine) autoAssignTask(ctx context.Context, missionID, taskID string) (string, string, error) {
	var crewID, leadAgentID string
	err := e.db.QueryRowContext(ctx,
		`SELECT crew_id, lead_agent_id FROM missions WHERE id = ?`, missionID,
	).Scan(&crewID, &leadAgentID)
	if err != nil {
		return "", "", fmt.Errorf("lookup mission: %w", err)
	}

	// Try to find a non-LEAD agent in the crew
	rows, err := e.db.QueryContext(ctx, `
		SELECT a.id, a.slug FROM agents a
		WHERE a.crew_id = ? AND a.deleted_at IS NULL AND a.id != ?
		ORDER BY a.name ASC`, crewID, leadAgentID)
	if err != nil {
		return "", "", fmt.Errorf("query crew agents: %w", err)
	}
	var candidates []struct{ id, slug string }
	for rows.Next() {
		var c struct{ id, slug string }
		if err := rows.Scan(&c.id, &c.slug); err == nil {
			candidates = append(candidates, c)
		}
	}
	rows.Close()

	var agentID, agentSlug string
	if len(candidates) > 0 {
		// Pick the first available (could be improved with load balancing)
		agentID = candidates[0].id
		agentSlug = candidates[0].slug
	} else {
		// Fallback: assign to the lead agent
		err = e.db.QueryRowContext(ctx,
			`SELECT id, slug FROM agents WHERE id = ? AND deleted_at IS NULL`, leadAgentID,
		).Scan(&agentID, &agentSlug)
		if err != nil {
			return "", "", fmt.Errorf("lead agent not found: %w", err)
		}
	}

	// Persist the assignment
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := e.db.ExecContext(ctx,
		`UPDATE mission_tasks SET assigned_agent_id = ?, updated_at = ? WHERE id = ?`,
		agentID, now, taskID); err != nil {
		return "", "", fmt.Errorf("persist auto-assignment: %w", err)
	}

	return agentID, agentSlug, nil
}

// buildMissionBrief constructs a rich context prompt for an agent executing a mission task.
// It includes: mission overview, the specific task, all sibling tasks (DAG awareness),
// and the output from completed dependency tasks (cross-task context propagation).
func (e *MissionEngine) buildMissionBrief(ctx context.Context, ms *missionState, task TaskInfo, allTasks []TaskInfo) string {
	var b strings.Builder

	// Mission overview
	var missionTitle, missionDesc sql.NullString
	e.db.QueryRowContext(ctx,
		`SELECT title, description FROM missions WHERE id = ?`, ms.ID,
	).Scan(&missionTitle, &missionDesc)

	b.WriteString("[MISSION CONTEXT]\n")
	if missionTitle.Valid {
		b.WriteString(fmt.Sprintf("Mission: %s\n", missionTitle.String))
	}
	if missionDesc.Valid && missionDesc.String != "" {
		b.WriteString(fmt.Sprintf("Description: %s\n", missionDesc.String))
	}

	// DAG overview — list all tasks so the agent knows the bigger picture
	b.WriteString(fmt.Sprintf("\nTotal tasks: %d\n", len(allTasks)))
	for _, t := range allTasks {
		marker := "  "
		switch t.Status {
		case "COMPLETED":
			marker = "✓ "
		case "IN_PROGRESS":
			marker = "► "
		case "FAILED":
			marker = "✗ "
		}
		agentLabel := "unassigned"
		if t.AgentSlug != nil {
			agentLabel = "@" + *t.AgentSlug
		}
		b.WriteString(fmt.Sprintf("  %s#%d %s (%s, %s)\n", marker, t.TaskOrder, t.Title, agentLabel, t.Status))
	}

	// Current task details
	b.WriteString(fmt.Sprintf("\n[YOUR TASK]\n"))
	b.WriteString(fmt.Sprintf("Title: %s\n", task.Title))
	if task.Description != nil && *task.Description != "" {
		b.WriteString(fmt.Sprintf("Description: %s\n", *task.Description))
	}
	if task.Iteration > 1 {
		b.WriteString(fmt.Sprintf("Iteration: %d (this is a retry — fix the issues from the previous attempt)\n", task.Iteration))
	}

	// Completed dependency outputs — critical for context chain
	deps, _ := parseDependsOn(task.DependsOn)
	if len(deps) > 0 {
		depOutputs := make([]string, 0)
		for _, depID := range deps {
			for _, t := range allTasks {
				if t.ID == depID && t.ResultSummary != nil && *t.ResultSummary != "" {
					agentLabel := "unknown"
					if t.AgentSlug != nil {
						agentLabel = "@" + *t.AgentSlug
					}
					summary := *t.ResultSummary
					if len(summary) > 4000 {
						summary = summary[:4000] + "\n...(truncated)"
					}
					depOutputs = append(depOutputs,
						fmt.Sprintf("Task #%d \"%s\" (%s):\n%s", t.TaskOrder, t.Title, agentLabel, summary))
				}
			}
		}
		if len(depOutputs) > 0 {
			b.WriteString("\n[COMPLETED DEPENDENCIES — use these results as input]\n")
			b.WriteString(strings.Join(depOutputs, "\n---\n"))
			b.WriteString("\n")
		}
	}

	b.WriteString("[END MISSION CONTEXT]\n\n")
	b.WriteString(task.Title)
	if task.Description != nil && *task.Description != "" {
		b.WriteString("\n\n" + *task.Description)
	}

	return b.String()
}

// scheduleReadyTasks finds PENDING tasks with completed dependencies and creates assignments.
func (e *MissionEngine) scheduleReadyTasks(ctx context.Context, ms *missionState) error {
	ready, err := e.ResolveReadyTasks(ctx, ms.ID)
	if err != nil {
		return fmt.Errorf("resolve ready tasks: %w", err)
	}

	// Load all tasks once for mission brief context
	allTasks, briefErr := e.loadTasks(ctx, ms.ID)
	if briefErr != nil {
		e.logger.Warn("load tasks for brief failed, continuing without context", "error", briefErr)
	}

	for _, task := range ready {
		if err := e.scheduleTask(ctx, ms, task, allTasks); err != nil {
			e.logger.Error("schedule task", "task_id", task.ID, "error", err)
			// Mark task as FAILED so the loop doesn't retry endlessly
			e.updateTaskStatus(ctx, ms, task.ID, "FAILED", err.Error())
		}
	}
	return nil
}

// scheduleTask transitions a task to IN_PROGRESS and creates an assignment.
// It resolves the target agent's crew (which may differ from the mission's
// crew for cross-crew tasks) and dispatches the work via the TaskDispatcher.
// allTasks is used to build the mission brief context for the agent.
func (e *MissionEngine) scheduleTask(ctx context.Context, ms *missionState, task TaskInfo, allTasks []TaskInfo) error {
	// Circuit breaker: skip agent if it has failed too many times consecutively
	if task.AssignedAgentID != nil {
		e.cbMu.Lock()
		failCount := e.failures[*task.AssignedAgentID]
		e.cbMu.Unlock()
		if failCount >= circuitBreakerThreshold {
			return fmt.Errorf("circuit breaker: agent has %d consecutive failures (threshold: %d)", failCount, circuitBreakerThreshold)
		}
	}

	now := time.Now().UTC().Format(time.RFC3339)

	// Resolve the target agent's crew for cross-crew support
	var agentCrewID, agentCrewSlug, agentSlug string
	err := e.db.QueryRowContext(ctx, `
		SELECT a.slug, a.crew_id, c.slug
		FROM agents a
		JOIN crews c ON c.id = a.crew_id
		WHERE a.id = ? AND a.deleted_at IS NULL`,
		*task.AssignedAgentID).Scan(&agentSlug, &agentCrewID, &agentCrewSlug)
	if err != nil {
		return fmt.Errorf("resolve agent crew: %w", err)
	}

	// For cross-crew tasks, verify the crews are connected
	if agentCrewID != ms.CrewID {
		connected, connErr := e.areCrewsConnected(ctx, ms.CrewID, agentCrewID)
		if connErr != nil {
			return fmt.Errorf("check crew connection: %w", connErr)
		}
		if !connected {
			return fmt.Errorf("crew %s is not connected to crew %s — create a crew connection first", ms.CrewSlug, agentCrewSlug)
		}
		e.logger.Info("cross-crew task dispatch",
			"mission_crew", ms.CrewSlug,
			"target_crew", agentCrewSlug,
			"agent", agentSlug,
		)
	}

	// Transition task to IN_PROGRESS
	_, err = e.db.ExecContext(ctx,
		`UPDATE mission_tasks SET status = 'IN_PROGRESS', started_at = ?, updated_at = ? WHERE id = ? AND status = 'PENDING'`,
		now, now, task.ID)
	if err != nil {
		return fmt.Errorf("update task status: %w", err)
	}

	e.broadcastTaskStatus(ms, task.ID, "IN_PROGRESS")
	e.pw.WriteEvent(ms.TraceID, ms.CrewSlug, ProgressEvent{
		Type:      "task_started",
		TaskID:    task.ID,
		AgentSlug: agentSlug,
		Title:     task.Title,
	})

	// Create assignment record
	assignmentID := generateID()
	_, err = e.db.ExecContext(ctx, `
		INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status, group_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?, 'PENDING', ?, ?)`,
		assignmentID, ms.WorkspaceID, ms.ID, ms.LeadAgentID, *task.AssignedAgentID,
		task.Title,
		ms.ID, // group_id = mission_id for grouping
		now,
	)
	if err != nil {
		return fmt.Errorf("create assignment: %w", err)
	}

	// Link assignment to the mission task
	_, err = e.db.ExecContext(ctx,
		`UPDATE mission_tasks SET assignment_id = ?, updated_at = ? WHERE id = ?`,
		assignmentID, now, task.ID)
	if err != nil {
		e.logger.Warn("link assignment to task", "task_id", task.ID, "error", err)
	}

	// Build rich mission brief with full context for the agent
	taskBrief := e.buildMissionBrief(ctx, ms, task, allTasks)

	// Dispatch the assignment to the correct crew's container
	if e.dispatcher != nil {
		go func() {
			dispatchErr := e.dispatcher.DispatchAssignment(context.Background(), DispatchRequest{
				AssignmentID: assignmentID,
				AgentID:      *task.AssignedAgentID,
				AgentSlug:    agentSlug,
				CrewID:       agentCrewID,
				CrewSlug:     agentCrewSlug,
				WorkspaceID:  ms.WorkspaceID,
				ChatID:       ms.ID,
				Task:         taskBrief,
				TraceID:      ms.TraceID,
				MissionID:    ms.ID,
			})
			if dispatchErr != nil {
				e.logger.Error("dispatch assignment failed",
					"assignment_id", assignmentID,
					"error", dispatchErr,
				)
				e.updateTaskStatus(ctx, ms, task.ID, "FAILED", dispatchErr.Error())
			}
		}()
	}

	e.logger.Info("task scheduled",
		"mission_id", ms.ID,
		"task_id", task.ID,
		"assignment_id", assignmentID,
		"agent_slug", agentSlug,
		"agent_crew", agentCrewSlug,
	)

	return nil
}

// areCrewsConnected checks if two crews have an active connection.
func (e *MissionEngine) areCrewsConnected(ctx context.Context, crewA, crewB string) (bool, error) {
	var exists bool
	err := e.db.QueryRowContext(ctx, `
		SELECT 1 FROM crew_connections
		WHERE status = 'active' AND (
			(from_crew_id = ? AND to_crew_id = ?)
			OR (from_crew_id = ? AND to_crew_id = ? AND direction = 'bidirectional')
		)`, crewA, crewB, crewB, crewA).Scan(&exists)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// OnAssignmentCompleted is called when an assignment finishes.
// It updates the corresponding mission task status, tracks circuit breaker
// state, and compresses output to prevent DB bloat.
func (e *MissionEngine) OnAssignmentCompleted(ctx context.Context, assignmentID, status, resultSummary, errorMessage string) error {
	var taskID, missionID string
	var assignedAgentID sql.NullString
	err := e.db.QueryRowContext(ctx,
		`SELECT id, mission_id, assigned_agent_id FROM mission_tasks WHERE assignment_id = ?`,
		assignmentID).Scan(&taskID, &missionID, &assignedAgentID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil // assignment not linked to a mission task
		}
		return fmt.Errorf("lookup task for assignment %s: %w", assignmentID, err)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	taskStatus := "COMPLETED"
	if status == "FAILED" || status == "TIMEOUT" {
		taskStatus = "FAILED"
	}

	// Circuit breaker: track consecutive failures per agent
	if assignedAgentID.Valid {
		e.cbMu.Lock()
		if taskStatus == "FAILED" {
			e.failures[assignedAgentID.String]++
			e.logger.Warn("agent failure tracked",
				"agent_id", assignedAgentID.String,
				"consecutive_failures", e.failures[assignedAgentID.String],
			)
		} else {
			delete(e.failures, assignedAgentID.String) // reset on success
		}
		e.cbMu.Unlock()
	}

	// Compress result summary to prevent DB bloat
	if len(resultSummary) > maxResultSummaryLen {
		resultSummary = resultSummary[:maxResultSummaryLen] + "\n...(truncated)"
	}

	// Calculate task duration
	var startedAt sql.NullString
	e.db.QueryRowContext(ctx, `SELECT started_at FROM mission_tasks WHERE id = ?`, taskID).Scan(&startedAt)
	var durationMs int64
	if startedAt.Valid {
		if st, err := time.Parse(time.RFC3339, startedAt.String); err == nil {
			durationMs = time.Since(st).Milliseconds()
		}
	}

	updates := `status = ?, updated_at = ?, completed_at = ?`
	args := []interface{}{taskStatus, now, now}
	if resultSummary != "" {
		updates += `, result_summary = ?`
		args = append(args, resultSummary)
	}
	if errorMessage != "" {
		updates += `, error_message = ?`
		args = append(args, errorMessage)
	}
	if durationMs > 0 {
		updates += `, duration_ms = ?`
		args = append(args, durationMs)
	}
	args = append(args, taskID)

	if _, err = e.db.ExecContext(ctx,
		`UPDATE mission_tasks SET `+updates+` WHERE id = ?`, args...); err != nil {
		return fmt.Errorf("update task %s: %w", taskID, err)
	}

	// If the task failed, attempt retry via LoopController before proceeding
	if taskStatus == "FAILED" && e.lc != nil {
		retried, retryErr := e.lc.RetryLoopBack(ctx, taskID, missionID)
		if retryErr != nil {
			e.logger.Error("loop controller retry check failed", "error", retryErr, "task_id", taskID)
		}
		if retried {
			e.logger.Info("task retry initiated by loop controller",
				"task_id", taskID, "mission_id", missionID)
			// Task was reset to PENDING — broadcast and return without checking completion
			e.mu.Lock()
			ms := e.active[missionID]
			e.mu.Unlock()
			if ms != nil {
				e.broadcastTaskStatus(ms, taskID, "PENDING")
				e.pw.WriteEvent(ms.TraceID, ms.CrewSlug, ProgressEvent{
					Type:   "task_retry",
					TaskID: taskID,
					Error:  errorMessage,
				})
			}
			return nil
		}
	}

	// Unblock dependent tasks (only for completed tasks)
	if taskStatus == "COMPLETED" {
		e.unblockDependentTasks(ctx, missionID, taskID)
	}

	// Get mission state for broadcasting
	e.mu.Lock()
	ms := e.active[missionID]
	e.mu.Unlock()

	if ms != nil {
		e.broadcastTaskStatus(ms, taskID, taskStatus)

		agentSlug := ""
		e.db.QueryRowContext(ctx, `SELECT a.slug FROM mission_tasks mt JOIN agents a ON a.id = mt.assigned_agent_id WHERE mt.id = ?`, taskID).Scan(&agentSlug)

		e.pw.WriteEvent(ms.TraceID, ms.CrewSlug, ProgressEvent{
			Type:      "task_" + taskStatus,
			TaskID:    taskID,
			AgentSlug: agentSlug,
			Summary:   resultSummary,
			Error:     errorMessage,
		})
	}

	e.logger.Info("task updated from assignment",
		"task_id", taskID,
		"mission_id", missionID,
		"status", taskStatus,
	)

	return nil
}

// checkMissionCompletion checks if all tasks are in a terminal state
// and transitions the mission to REVIEW or FAILED accordingly.
func (e *MissionEngine) checkMissionCompletion(ctx context.Context, ms *missionState) error {
	tasks, err := e.loadTasks(ctx, ms.ID)
	if err != nil {
		return err
	}

	if len(tasks) == 0 {
		return nil // no tasks yet
	}

	allTerminal := true
	anyFailed := false
	for _, t := range tasks {
		switch t.Status {
		case "COMPLETED", "FAILED", "SKIPPED":
			if t.Status == "FAILED" {
				anyFailed = true
			}
		default:
			allTerminal = false
		}
	}

	if !allTerminal {
		return nil
	}

	newStatus := "REVIEW"
	if anyFailed {
		newStatus = "FAILED"
	}

	now := time.Now().UTC().Format(time.RFC3339)
	completedAt := sql.NullString{String: now, Valid: true}

	_, err = e.db.ExecContext(ctx,
		`UPDATE missions SET status = ?, completed_at = ?, updated_at = ? WHERE id = ? AND status = 'IN_PROGRESS'`,
		newStatus, completedAt, now, ms.ID)
	if err != nil {
		return fmt.Errorf("update mission status: %w", err)
	}

	e.broadcastMissionStatus(ms, newStatus)
	e.pw.WriteEvent(ms.TraceID, ms.CrewSlug, ProgressEvent{
		Type:      "mission_" + newStatus,
		MissionID: ms.ID,
	})

	e.logger.Info("mission completed", "mission_id", ms.ID, "status", newStatus)
	return nil
}

// unblockDependentTasks transitions BLOCKED tasks to PENDING when all deps are done.
func (e *MissionEngine) unblockDependentTasks(ctx context.Context, missionID, completedTaskID string) {
	// Collect blocked tasks first to avoid nested queries on the same SQLite connection.
	rows, err := e.db.QueryContext(ctx,
		`SELECT id, depends_on FROM mission_tasks WHERE mission_id = ? AND status = 'BLOCKED'`,
		missionID)
	if err != nil {
		e.logger.Error("query blocked tasks", "error", err)
		return
	}

	type blockedTask struct {
		id      string
		deps    []string
	}
	var candidates []blockedTask
	for rows.Next() {
		var id, depsJSON string
		if err := rows.Scan(&id, &depsJSON); err != nil {
			continue
		}
		deps, err := parseDependsOn(depsJSON)
		if err != nil || len(deps) == 0 {
			continue
		}
		hasDep := false
		for _, d := range deps {
			if d == completedTaskID {
				hasDep = true
				break
			}
		}
		if hasDep {
			candidates = append(candidates, blockedTask{id: id, deps: deps})
		}
	}
	rows.Close()

	now := time.Now().UTC().Format(time.RFC3339)
	for _, bt := range candidates {
		allDone := true
		for _, d := range bt.deps {
			var s string
			if err := e.db.QueryRowContext(ctx, `SELECT status FROM mission_tasks WHERE id = ?`, d).Scan(&s); err != nil || s != "COMPLETED" {
				allDone = false
				break
			}
		}

		if allDone {
			if _, err := e.db.ExecContext(ctx,
				`UPDATE mission_tasks SET status = 'PENDING', updated_at = ? WHERE id = ?`,
				now, bt.id); err != nil {
				e.logger.Error("unblock task", "task_id", bt.id, "error", err)
			}

			e.mu.Lock()
			ms := e.active[missionID]
			e.mu.Unlock()
			if ms != nil {
				e.broadcastTaskStatus(ms, bt.id, "PENDING")
				e.pw.WriteEvent(ms.TraceID, ms.CrewSlug, ProgressEvent{
					Type:   "task_unblocked",
					TaskID: bt.id,
				})
			}
		}
	}
}

func (e *MissionEngine) updateTaskStatus(ctx context.Context, ms *missionState, taskID, status, errMsg string) {
	now := time.Now().UTC().Format(time.RFC3339)
	query := `UPDATE mission_tasks SET status = ?, updated_at = ?`
	args := []interface{}{status, now}
	if errMsg != "" {
		query += `, error_message = ?`
		args = append(args, errMsg)
	}
	if status == "COMPLETED" || status == "FAILED" || status == "SKIPPED" {
		query += `, completed_at = ?`
		args = append(args, now)
	}
	query += ` WHERE id = ?`
	args = append(args, taskID)
	if _, err := e.db.ExecContext(ctx, query, args...); err != nil {
		e.logger.Error("update task status", "task_id", taskID, "error", err)
	}
	e.broadcastTaskStatus(ms, taskID, status)
}

func (e *MissionEngine) getMissionStatus(ctx context.Context, missionID string) (string, error) {
	var status string
	err := e.db.QueryRowContext(ctx, `SELECT status FROM missions WHERE id = ?`, missionID).Scan(&status)
	return status, err
}

func (e *MissionEngine) loadTasks(ctx context.Context, missionID string) ([]TaskInfo, error) {
	rows, err := e.db.QueryContext(ctx, `
		SELECT mt.id, mt.mission_id, mt.assigned_agent_id, a.slug, mt.title, mt.description,
		       mt.status, mt.task_order, mt.depends_on, COALESCE(mt.iteration, 1),
		       mt.max_iterations, mt.result_summary
		FROM mission_tasks mt
		LEFT JOIN agents a ON a.id = mt.assigned_agent_id
		WHERE mt.mission_id = ?
		ORDER BY mt.task_order ASC`, missionID)
	if err != nil {
		return nil, fmt.Errorf("query tasks: %w", err)
	}
	defer rows.Close()

	var tasks []TaskInfo
	for rows.Next() {
		var t TaskInfo
		if err := rows.Scan(&t.ID, &t.MissionID, &t.AssignedAgentID, &t.AgentSlug,
			&t.Title, &t.Description, &t.Status, &t.TaskOrder, &t.DependsOn,
			&t.Iteration, &t.MaxIterations, &t.ResultSummary); err != nil {
			return nil, fmt.Errorf("scan task: %w", err)
		}
		tasks = append(tasks, t)
	}
	return tasks, rows.Err()
}

func (e *MissionEngine) broadcastTaskStatus(ms *missionState, taskID, status string) {
	if e.hub == nil {
		return
	}
	e.hub.Broadcast("mission:"+ms.ID, ws.ServerMessage{
		Type:    "task.status",
		Channel: "mission:" + ms.ID,
		Payload: map[string]string{"id": taskID, "status": status},
	})
	wsChannel := "workspace:" + ms.WorkspaceID
	e.hub.Broadcast(wsChannel, ws.ServerMessage{
		Type:    "task.updated",
		Channel: wsChannel,
		Payload: map[string]string{"id": taskID, "mission_id": ms.ID, "status": status},
	})
}

func (e *MissionEngine) broadcastMissionStatus(ms *missionState, status string) {
	if e.hub == nil {
		return
	}
	e.hub.Broadcast("mission:"+ms.ID, ws.ServerMessage{
		Type:    "mission.status",
		Channel: "mission:" + ms.ID,
		Payload: map[string]string{"id": ms.ID, "status": status},
	})
	wsChannel := "workspace:" + ms.WorkspaceID
	e.hub.Broadcast(wsChannel, ws.ServerMessage{
		Type:    "mission.updated",
		Channel: wsChannel,
		Payload: map[string]string{"id": ms.ID, "crew_id": ms.CrewID, "status": status},
	})
}

func parseDependsOn(raw string) ([]string, error) {
	if raw == "" || raw == "[]" {
		return nil, nil
	}
	var deps []string
	if err := json.Unmarshal([]byte(raw), &deps); err != nil {
		return nil, err
	}
	return deps, nil
}

func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func generateID() string {
	b := make([]byte, 8)
	if _, err := crand.Read(b); err != nil {
		return fmt.Sprintf("m_%d", time.Now().UnixNano())
	}
	return fmt.Sprintf("m_%x%x", time.Now().UnixMilli(), b[:6])
}
