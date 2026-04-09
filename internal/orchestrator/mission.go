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
	LeadPlanning bool   // when true, dispatch as LEAD with sidecar (for task planning phase)
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

// ErrInvalidTaskStatus is returned when a task is not in the expected status for an operation.
var ErrInvalidTaskStatus = errors.New("invalid task status")

// EscalationConfig holds tiered escalation thresholds per crew.
type EscalationConfig struct {
	AutoApproveThreshold float64 `json:"auto_approve_threshold"`
	NotifyThreshold      float64 `json:"notify_threshold"`
	RequireApprovalBelow float64 `json:"require_approval_below"`
}

const (
	circuitBreakerThreshold = 3     // consecutive failures before tripping
	maxResultSummaryLen     = 8000
	maxBriefTotalLen        = 32000 // total brief size cap (bytes) to avoid LLM token budget issues
	maxDepOutputLen         = 4000  // per-dependency output truncation
	missionTimeoutDefault   = 2 * time.Hour
)

// HandoffData represents parsed structured handoff from an agent's output.
type HandoffData struct {
	Summary    string `json:"summary"`
	Confidence string `json:"confidence"` // low, medium, high
	Artifacts  string `json:"artifacts"`
	Parsed     bool   `json:"parsed"` // true if handoff block was found
}

// ParseHandoff extracts structured handoff data from an agent's result summary.
// Looks for ---HANDOFF--- ... ---END HANDOFF--- block at the end of the output.
func ParseHandoff(resultSummary string) HandoffData {
	return parseHandoff(resultSummary)
}

func parseHandoff(resultSummary string) HandoffData {
	const startMarker = "---HANDOFF---"
	const endMarker = "---END HANDOFF---"

	startIdx := strings.LastIndex(resultSummary, startMarker)
	if startIdx < 0 {
		return HandoffData{Parsed: false}
	}
	endIdx := strings.Index(resultSummary[startIdx:], endMarker)
	if endIdx < 0 {
		return HandoffData{Parsed: false}
	}

	block := resultSummary[startIdx+len(startMarker) : startIdx+endIdx]
	hd := HandoffData{}

	for _, line := range strings.Split(block, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "summary:") {
			hd.Summary = strings.TrimSpace(strings.TrimPrefix(line, "summary:"))
		} else if strings.HasPrefix(line, "confidence:") {
			hd.Confidence = strings.TrimSpace(strings.TrimPrefix(line, "confidence:"))
		} else if strings.HasPrefix(line, "artifacts:") {
			hd.Artifacts = strings.TrimSpace(strings.TrimPrefix(line, "artifacts:"))
		}
	}

	// Require summary and confidence for a valid handoff — partial blocks
	// (e.g. summary-only) are treated as unparsed so callers don't skip review.
	hd.Parsed = hd.Summary != "" && hd.Confidence != ""
	return hd
}

// SetDispatcher registers the assignment dispatcher.
func (e *MissionEngine) SetDispatcher(d TaskDispatcher) {
	e.dispatcher = d
}

type missionState struct {
	ID                 string
	Title              string
	CrewID             string
	CrewSlug           string
	LeadAgentID        string
	TraceID            string
	WorkspaceID        string
	cancel             context.CancelFunc
	planningDispatched bool // true after lead planning dispatch (prevents re-dispatch)
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
	// Insert sentinel to prevent concurrent starts (TOCTOU race)
	e.active[missionID] = &missionState{ID: missionID}
	e.mu.Unlock()

	var ms missionState
	var crewSlug string
	err := e.db.QueryRowContext(ctx, `
		SELECT m.id, m.title, m.crew_id, m.lead_agent_id, m.trace_id, m.workspace_id, c.slug
		FROM missions m
		JOIN crews c ON c.id = m.crew_id
		WHERE m.id = ?`, missionID).Scan(
		&ms.ID, &ms.Title, &ms.CrewID, &ms.LeadAgentID, &ms.TraceID, &ms.WorkspaceID, &crewSlug,
	)
	if err != nil {
		e.mu.Lock()
		delete(e.active, missionID)
		e.mu.Unlock()
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
	e.broadcastMissionStatus(&ms, "STARTED")

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
			// Fail any AWAITING_APPROVAL tasks that were never resolved.
			e.db.ExecContext(context.Background(),
				`UPDATE mission_tasks SET status = 'FAILED', error_message = 'mission timed out', updated_at = ?, completed_at = ?
				 WHERE mission_id = ? AND status = 'AWAITING_APPROVAL'`,
				now, now, ms.ID)
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

			// Lead planning phase: if mission has 0 tasks, dispatch to lead
			// so they can plan and create tasks autonomously.
			e.mu.Lock()
			alreadyPlanning := ms.planningDispatched
			e.mu.Unlock()
			if !alreadyPlanning {
				taskCount, countErr := e.countTasks(ctx, ms.ID)
				if countErr != nil {
					e.logger.Error("count tasks", "mission_id", ms.ID, "error", countErr)
				} else if taskCount == 0 {
					if planErr := e.dispatchLeadPlanning(ctx, ms); planErr != nil {
						e.logger.Error("lead planning failed", "mission_id", ms.ID, "error", planErr)
					} else {
						e.mu.Lock()
						ms.planningDispatched = true
						e.mu.Unlock()
					}
					continue // wait for lead to create tasks
				}
			}

			if err := e.scheduleReadyTasks(ctx, ms); err != nil {
				e.logger.Error("schedule ready tasks", "mission_id", ms.ID, "error", err)
			}

			if err := e.checkMissionCompletion(ctx, ms); err != nil {
				e.logger.Error("check mission completion", "mission_id", ms.ID, "error", err)
			}

			// Deadlock detection: all tasks BLOCKED with nothing making progress
			if e.detectDeadlock(ctx, ms.ID) {
				e.logger.Error("deadlock detected — all tasks BLOCKED with no progress possible",
					"mission_id", ms.ID)
				now := time.Now().UTC().Format(time.RFC3339)
				e.db.ExecContext(context.Background(),
					`UPDATE missions SET status = 'FAILED', updated_at = ?, completed_at = ? WHERE id = ? AND status = 'IN_PROGRESS'`,
					now, now, ms.ID)
				e.broadcastMissionStatus(ms, "FAILED")
				e.pw.WriteEvent(ms.TraceID, ms.CrewSlug, ProgressEvent{
					Type:      "mission_deadlock",
					MissionID: ms.ID,
				})
				return
			}
		}
	}
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

// ValidateDAG checks all mission tasks for:
// 1. Circular dependencies (topological sort)
// 2. References to nonexistent task IDs
// Returns nil if the DAG is valid.
func (e *MissionEngine) ValidateDAG(ctx context.Context, missionID string) error {
	tasks, err := e.loadTasks(ctx, missionID)
	if err != nil {
		return fmt.Errorf("load tasks: %w", err)
	}
	if len(tasks) == 0 {
		return nil // empty mission — lead will plan
	}

	taskIDs := make(map[string]bool, len(tasks))
	for _, t := range tasks {
		taskIDs[t.ID] = true
	}

	// Build adjacency list and check for nonexistent deps
	graph := make(map[string][]string, len(tasks))     // taskID → deps
	inDegree := make(map[string]int, len(tasks))
	for _, t := range tasks {
		graph[t.ID] = nil
		inDegree[t.ID] = 0
	}
	for _, t := range tasks {
		deps, parseErr := parseDependsOn(t.DependsOn)
		if parseErr != nil {
			return fmt.Errorf("task %s has invalid depends_on: %w", t.ID, parseErr)
		}
		for _, dep := range deps {
			if !taskIDs[dep] {
				return fmt.Errorf("task %q depends on nonexistent task %q", t.Title, dep)
			}
			graph[t.ID] = append(graph[t.ID], dep)
			inDegree[t.ID]++
		}
	}

	// Kahn's algorithm for cycle detection
	var queue []string
	for id, deg := range inDegree {
		if deg == 0 {
			queue = append(queue, id)
		}
	}

	visited := 0
	for len(queue) > 0 {
		node := queue[0]
		queue = queue[1:]
		visited++
		// Find tasks that depend on this node
		for _, t := range tasks {
			deps, _ := parseDependsOn(t.DependsOn)
			for _, dep := range deps {
				if dep == node {
					inDegree[t.ID]--
					if inDegree[t.ID] == 0 {
						queue = append(queue, t.ID)
					}
				}
			}
		}
	}

	if visited != len(tasks) {
		return fmt.Errorf("circular dependency detected: %d tasks involved in cycle", len(tasks)-visited)
	}
	return nil
}

// detectDeadlock checks if a mission is deadlocked: all tasks are BLOCKED
// with no PENDING or IN_PROGRESS tasks to make progress. Returns true if deadlocked.
func (e *MissionEngine) detectDeadlock(ctx context.Context, missionID string) bool {
	tasks, err := e.loadTasks(ctx, missionID)
	if err != nil || len(tasks) == 0 {
		return false
	}
	for _, t := range tasks {
		switch t.Status {
		case "PENDING", "IN_PROGRESS", "AWAITING_APPROVAL":
			return false // can still make progress (AWAITING_APPROVAL = waiting for human)
		case "COMPLETED", "SKIPPED":
			continue // terminal, OK
		case "FAILED":
			continue // terminal, handled elsewhere
		case "BLOCKED":
			continue // potential deadlock member
		}
	}
	// All tasks are terminal or BLOCKED. If any are BLOCKED, it's a deadlock.
	for _, t := range tasks {
		if t.Status == "BLOCKED" {
			return true
		}
	}
	return false
}

// dispatchLeadPlanning sends the mission to the lead agent for autonomous task planning.
// The lead runs with full LEAD privileges (sidecar, crew context) so they can:
// 1. Analyze the mission objective
// 2. Break it into tasks using /mission/create or /assign
// 3. Assign tasks to crew members based on their skills
// The engine then picks up the created tasks on the next loop iteration.
func (e *MissionEngine) dispatchLeadPlanning(ctx context.Context, ms *missionState) error {
	// Load mission details for the planning prompt
	var title, desc sql.NullString
	e.db.QueryRowContext(ctx,
		`SELECT title, description FROM missions WHERE id = ?`, ms.ID).Scan(&title, &desc)

	// Resolve lead agent details
	var agentSlug string
	err := e.db.QueryRowContext(ctx,
		`SELECT slug FROM agents WHERE id = ? AND deleted_at IS NULL`,
		ms.LeadAgentID).Scan(&agentSlug)
	if err != nil {
		e.logger.Error("lead planning: resolve lead agent", "error", err, "mission_id", ms.ID)
		return fmt.Errorf("resolve lead agent: %w", err)
	}

	// Build the planning prompt
	var b strings.Builder
	b.WriteString("[MISSION PLANNING REQUEST]\n")
	b.WriteString("You are the Lead agent for this crew. A new mission has been assigned to you WITHOUT pre-defined tasks.\n")
	b.WriteString("Your job is to analyze the objective, break it down into concrete tasks, and assign them to your crew members.\n\n")
	b.WriteString(fmt.Sprintf("Mission: %s\n", title.String))
	if desc.Valid && desc.String != "" {
		b.WriteString(fmt.Sprintf("Description: %s\n", desc.String))
	}
	b.WriteString(fmt.Sprintf("Mission ID: %s\n\n", ms.ID))
	b.WriteString("SCALING RULES — classify before planning:\n")
	b.WriteString("  SIMPLE  (fact-finding, single op):    1 agent, 3-10 tool calls, ~5 min\n")
	b.WriteString("  MEDIUM  (multi-step, 1-2 files):      1-2 agents, 10-15 tool calls, ~15 min\n")
	b.WriteString("  COMPLEX (research, multi-file):        2-4 agents, 15+ tool calls, ~30 min\n")
	b.WriteString("Match effort to complexity. Do NOT create missions for SIMPLE tasks — use /assign directly.\n\n")

	b.WriteString("INSTRUCTIONS:\n")
	b.WriteString("1. Assess mission complexity (SIMPLE/MEDIUM/COMPLEX) first\n")
	b.WriteString("2. Review the mission objective and your crew members' capabilities\n")
	b.WriteString("3. Break the work into specific, actionable tasks\n")
	b.WriteString("4. Assign each task to the most suitable crew member (or yourself if solo)\n")
	b.WriteString("5. Define task dependencies (which tasks must complete before others start)\n")
	b.WriteString("6. Create the tasks using the mission API:\n\n")
	b.WriteString("Option A — Add tasks to this existing mission:\n")
	b.WriteString(fmt.Sprintf("  For each task, run:\n"))
	b.WriteString(fmt.Sprintf("  curl -s -X POST http://localhost:9119/assign \\\n"))
	b.WriteString(fmt.Sprintf("    -H 'Content-Type: application/json' \\\n"))
	b.WriteString(fmt.Sprintf("    -d '{\"target\":\"<agent_slug>\",\"task\":\"<detailed task description>\"}'\n\n"))
	b.WriteString("Option B — If you prefer structured mission with dependencies:\n")
	b.WriteString(fmt.Sprintf("  Create a new sub-mission with dependency DAG:\n"))
	b.WriteString(fmt.Sprintf("  curl -s -X POST http://localhost:9119/mission/create \\\n"))
	b.WriteString(fmt.Sprintf("    -H 'Content-Type: application/json' \\\n"))
	b.WriteString(fmt.Sprintf("    -d '{\"title\":\"...\",\"tasks\":[...]}'\n"))
	b.WriteString(fmt.Sprintf("  Then start it: curl -s -X POST http://localhost:9119/mission/<id>/start\n\n"))
	b.WriteString("Option C — If you can handle this yourself (solo crew / simple task):\n")
	b.WriteString("  Just do the work directly and produce the result.\n\n")
	b.WriteString("After creating tasks or completing the work, the system will handle the rest.\n")
	b.WriteString("[END PLANNING REQUEST]")

	// Create a planning assignment
	now := time.Now().UTC().Format(time.RFC3339)
	assignmentID := generateID()
	_, err = e.db.ExecContext(ctx, `
		INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status, group_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?, 'PENDING', ?, ?)`,
		assignmentID, ms.WorkspaceID, ms.ID, ms.LeadAgentID, ms.LeadAgentID,
		"[PLANNING] "+title.String,
		ms.ID,
		now,
	)
	if err != nil {
		e.logger.Error("create planning assignment", "error", err, "mission_id", ms.ID)
		return fmt.Errorf("create planning assignment: %w", err)
	}

	e.logger.Info("dispatching lead planning",
		"mission_id", ms.ID,
		"lead", agentSlug,
		"assignment_id", assignmentID,
	)

	e.pw.WriteEvent(ms.TraceID, ms.CrewSlug, ProgressEvent{
		Type:      "lead_planning",
		MissionID: ms.ID,
		AgentSlug: agentSlug,
		Title:     title.String,
	})

	// Dispatch as LEAD (with sidecar and crew context)
	if e.dispatcher != nil {
		go func() {
			dispatchErr := e.dispatcher.DispatchAssignment(context.Background(), DispatchRequest{
				AssignmentID: assignmentID,
				AgentID:      ms.LeadAgentID,
				AgentSlug:    agentSlug,
				CrewID:       ms.CrewID,
				CrewSlug:     ms.CrewSlug,
				WorkspaceID:  ms.WorkspaceID,
				ChatID:       ms.ID,
				Task:         b.String(),
				TraceID:      ms.TraceID,
				MissionID:    ms.ID,
				LeadPlanning: true, // run as LEAD with sidecar enabled
			})
			if dispatchErr != nil {
				e.logger.Error("lead planning dispatch failed",
					"assignment_id", assignmentID,
					"error", dispatchErr,
				)
				// Reset planningDispatched so the loop will retry on next tick
				e.mu.Lock()
				ms.planningDispatched = false
				e.mu.Unlock()
			}
		}()
	}

	e.broadcastMissionStatus(ms, "IN_PROGRESS")
	return nil
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
		Payload: map[string]string{"id": ms.ID, "title": ms.Title, "status": status},
	})
	wsChannel := "workspace:" + ms.WorkspaceID
	e.hub.Broadcast(wsChannel, ws.ServerMessage{
		Type:    "mission.updated",
		Channel: wsChannel,
		Payload: map[string]string{"id": ms.ID, "crew_id": ms.CrewID, "title": ms.Title, "status": status},
	})
}

// countTasks returns the number of tasks in a mission.
func (e *MissionEngine) countTasks(ctx context.Context, missionID string) (int, error) {
	var count int
	err := e.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM mission_tasks WHERE mission_id = ?`, missionID).Scan(&count)
	return count, err
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
