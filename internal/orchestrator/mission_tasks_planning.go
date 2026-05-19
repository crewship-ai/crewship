package orchestrator

// Approval gating, lead-planning dispatch, and DAG validation —
// extracted from mission_tasks.go for readability. All public
// signatures (ApproveTask, ValidateDAG) are preserved.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/ws"
)

func (e *MissionEngine) checkApprovalGate(ctx context.Context, taskID, missionID string) string {
	var approvalRequired int
	var confRaw sql.NullFloat64
	var configJSON sql.NullString

	err := e.db.QueryRowContext(ctx,
		`SELECT COALESCE(mt.approval_required, 0), mt.confidence, c.escalation_config
		 FROM mission_tasks mt
		 JOIN missions m ON m.id = mt.mission_id
		 JOIN crews c ON c.id = m.crew_id
		 WHERE mt.id = ?`, taskID).Scan(&approvalRequired, &confRaw, &configJSON)
	if err != nil {
		e.logger.Error("check approval gate", "error", err, "task_id", taskID)
		return "COMPLETED"
	}

	var cfg EscalationConfig
	hasConfig := false
	if configJSON.Valid && configJSON.String != "" {
		if err := json.Unmarshal([]byte(configJSON.String), &cfg); err != nil {
			e.logger.Error("parse escalation_config", "error", err, "mission_id", missionID)
		} else {
			hasConfig = true
		}
	}

	hasConf := confRaw.Valid
	conf := float64(0)
	if hasConf {
		conf = confRaw.Float64
	}

	if hasConfig && hasConf && cfg.AutoApproveThreshold > 0 && conf >= cfg.AutoApproveThreshold {
		return "COMPLETED"
	}

	if approvalRequired == 1 {
		e.logger.Info("task held for approval (explicit flag)", "task_id", taskID, "mission_id", missionID)
		return "AWAITING_APPROVAL"
	}

	if !hasConfig || !hasConf {
		return "COMPLETED"
	}

	if cfg.RequireApprovalBelow > 0 && conf < cfg.RequireApprovalBelow {
		e.logger.Info("task held for approval (confidence below threshold)", "task_id", taskID, "confidence", conf)
		return "AWAITING_APPROVAL"
	}

	if cfg.NotifyThreshold > 0 && conf < cfg.NotifyThreshold {
		e.mu.Lock()
		ms := e.active[missionID]
		e.mu.Unlock()
		if ms != nil && e.hub != nil {
			e.hub.Broadcast("workspace:"+ms.WorkspaceID, ws.ServerMessage{
				Type:    "confidence.low",
				Channel: "workspace:" + ms.WorkspaceID,
				Payload: map[string]string{"task_id": taskID, "mission_id": missionID, "level": "notify"},
			})
		}
	}

	return "COMPLETED"
}

// ApproveTask approves or rejects a task in AWAITING_APPROVAL status.

func (e *MissionEngine) ApproveTask(ctx context.Context, taskID, userID string, approved bool, notes string) error {
	if userID == "" {
		return fmt.Errorf("userID is required for approval audit trail")
	}

	var currentStatus, missionID, missionStatus string
	if err := e.db.QueryRowContext(ctx,
		`SELECT mt.status, mt.mission_id, m.status
		 FROM mission_tasks mt JOIN missions m ON m.id = mt.mission_id
		 WHERE mt.id = ?`, taskID).Scan(&currentStatus, &missionID, &missionStatus); err != nil {
		return fmt.Errorf("lookup task %s: %w", taskID, err)
	}

	if missionStatus != "IN_PROGRESS" {
		return fmt.Errorf("%w: mission is %s, approvals only allowed when IN_PROGRESS", ErrInvalidTaskStatus, missionStatus)
	}
	if currentStatus != "AWAITING_APPROVAL" {
		return fmt.Errorf("%w: task %s is %s, expected AWAITING_APPROVAL", ErrInvalidTaskStatus, taskID, currentStatus)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	newStatus := "COMPLETED"
	approvalStatus := "APPROVED"
	if !approved {
		newStatus = "FAILED"
		approvalStatus = "REJECTED"
	}

	res, err := e.db.ExecContext(ctx,
		`UPDATE mission_tasks SET status = ?, approval_status = ?, approved_by = ?, approved_at = ?,
		 evaluation_notes = ?, completed_at = ?, updated_at = ? WHERE id = ? AND status = 'AWAITING_APPROVAL'`,
		newStatus, approvalStatus, userID, now, notes, now, now, taskID)
	if err != nil {
		return fmt.Errorf("update task %s approval: %w", taskID, err)
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return fmt.Errorf("%w: task %s was already approved or rejected by another user", ErrInvalidTaskStatus, taskID)
	}

	if approved {
		e.unblockDependentTasks(ctx, missionID, taskID)
	} else {
		e.failDependentTasks(ctx, missionID, taskID, "upstream task rejected")
	}

	e.mu.Lock()
	ms := e.active[missionID]
	e.mu.Unlock()

	if ms != nil {
		e.broadcastTaskStatus(ms, taskID, newStatus)
		if e.hub != nil {
			e.hub.Broadcast("workspace:"+ms.WorkspaceID, ws.ServerMessage{
				Type:    "approval.resolved",
				Channel: "workspace:" + ms.WorkspaceID,
				Payload: map[string]string{"task_id": taskID, "mission_id": missionID, "action": approvalStatus},
			})
		}
		e.checkMissionCompletion(ctx, ms) //nolint:errcheck
	}

	e.logger.Info("task approval decision", "task_id", taskID, "approved", approved, "user_id", userID)
	return nil
}

// failDependentTasks cascades failure to BLOCKED tasks when a dependency is rejected.
// Uses a visited set to prevent infinite recursion on circular dependencies.

func (e *MissionEngine) dispatchLeadPlanning(ctx context.Context, ms *missionState) error {
	// Load mission details for the planning prompt
	var title, desc sql.NullString
	e.db.QueryRowContext(ctx,
		`SELECT title, description FROM missions WHERE id = ?`, ms.ID).Scan(&title, &desc)

	// Resolve lead agent details and check lead_mode
	var agentSlug string
	var leadMode sql.NullString
	err := e.db.QueryRowContext(ctx,
		`SELECT slug, lead_mode FROM agents WHERE id = ? AND deleted_at IS NULL`,
		ms.LeadAgentID).Scan(&agentSlug, &leadMode)
	if err != nil {
		e.logger.Error("lead planning: resolve lead agent", "error", err, "mission_id", ms.ID)
		return fmt.Errorf("resolve lead agent: %w", err)
	}

	// Passive lead mode: skip autonomous planning — human manages tasks manually.
	if leadMode.Valid && leadMode.String == "passive" {
		e.logger.Info("lead is passive, skipping autonomous planning", "mission_id", ms.ID, "agent", agentSlug)
		return nil
	}

	// Build the planning prompt. Pre-size and use fmt.Fprintf so the three
	// dynamic lines don't each pay a Sprintf intermediate-string allocation
	// on top of the Builder's own growth. Output bytes are byte-identical
	// to the previous WriteString(Sprintf(...)) shape.
	var b strings.Builder
	b.Grow(3072)
	b.WriteString("[MISSION PLANNING REQUEST]\n")
	b.WriteString("You are the Lead agent for this crew. A new mission has been assigned to you WITHOUT pre-defined tasks.\n")
	b.WriteString("Your job is to analyze the objective, break it down into concrete tasks, and assign them to your crew members.\n\n")
	fmt.Fprintf(&b, "Mission: %s\n", title.String)
	if desc.Valid && desc.String != "" {
		fmt.Fprintf(&b, "Description: %s\n", desc.String)
	}
	fmt.Fprintf(&b, "Mission ID: %s\n\n", ms.ID)
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
	b.WriteString("  For each task, run:\n")
	b.WriteString("  curl -s -X POST http://localhost:9119/assign \\\n")
	b.WriteString("    -H 'Content-Type: application/json' \\\n")
	b.WriteString("    -d '{\"target\":\"<agent_slug>\",\"task\":\"<detailed task description>\"}'\n\n")
	b.WriteString("Option B — If you prefer structured mission with dependencies:\n")
	b.WriteString("  Create a new sub-mission with dependency DAG:\n")
	b.WriteString("  curl -s -X POST http://localhost:9119/mission/create \\\n")
	b.WriteString("    -H 'Content-Type: application/json' \\\n")
	b.WriteString("    -d '{\"title\":\"...\",\"tasks\":[...]}'\n")
	b.WriteString("  Then start it: curl -s -X POST http://localhost:9119/mission/<id>/start\n\n")
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

	// Build reverse adjacency (parent → children) and in-degree counts in
	// one pass over the task list, parsing each task's depends_on JSON
	// exactly once. The Kahn loop below then walks dependents[node] in
	// O(1) per edge instead of rescanning every task and re-parsing JSON
	// per visit — the previous implementation was O(n²·d) with JSON
	// parses on the hot path.
	dependents := make(map[string][]string, len(tasks))
	inDegree := make(map[string]int, len(tasks))
	for _, t := range tasks {
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
			dependents[dep] = append(dependents[dep], t.ID)
			inDegree[t.ID]++
		}
	}

	// Kahn's algorithm for cycle detection — O(V+E).
	queue := make([]string, 0, len(tasks))
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
		for _, child := range dependents[node] {
			inDegree[child]--
			if inDegree[child] == 0 {
				queue = append(queue, child)
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
