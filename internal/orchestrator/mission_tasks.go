package orchestrator

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ResolveReadyTasks returns tasks that have all dependencies completed
// and are in PENDING status (ready to be scheduled).
// It also self-heals BLOCKED tasks whose dependencies are all COMPLETED
// (e.g. after a mission restart that blindly set dep-tasks to BLOCKED).
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

	// Self-heal: promote BLOCKED tasks whose deps are all COMPLETED to PENDING.
	now := time.Now().UTC().Format(time.RFC3339)
	for i, t := range tasks {
		if t.Status != "BLOCKED" {
			continue
		}
		deps, err := parseDependsOn(t.DependsOn)
		if err != nil || len(deps) == 0 {
			continue
		}
		allDone := true
		for _, dep := range deps {
			if !completed[dep] {
				allDone = false
				break
			}
		}
		if allDone {
			if _, err := e.db.ExecContext(ctx,
				`UPDATE mission_tasks SET status = 'PENDING', updated_at = ? WHERE id = ? AND status = 'BLOCKED'`,
				now, t.ID); err != nil {
				e.logger.Error("self-heal BLOCKED→PENDING failed", "task_id", t.ID, "error", err)
				continue
			}
			tasks[i].Status = "PENDING"
			e.logger.Info("self-healed BLOCKED→PENDING", "task_id", t.ID, "mission_id", missionID)
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

	// Find non-LEAD agents, pick the one with fewest assigned tasks in this mission (round-robin)
	rows, err := e.db.QueryContext(ctx, `
		SELECT a.id, a.slug, COUNT(mt.id) AS task_count
		FROM agents a
		LEFT JOIN mission_tasks mt ON mt.assigned_agent_id = a.id AND mt.mission_id = ?
		WHERE a.crew_id = ? AND a.deleted_at IS NULL AND a.id != ?
		GROUP BY a.id, a.slug
		ORDER BY task_count ASC, a.name ASC`, missionID, crewID, leadAgentID)
	if err != nil {
		return "", "", fmt.Errorf("query crew agents: %w", err)
	}
	var candidates []struct{ id, slug string }
	for rows.Next() {
		var c struct{ id, slug string }
		var cnt int
		if err := rows.Scan(&c.id, &c.slug, &cnt); err == nil {
			candidates = append(candidates, c)
		}
	}
	rows.Close()

	var agentID, agentSlug string
	if len(candidates) > 0 {
		// First candidate has the fewest tasks (round-robin / least-loaded)
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
//
// The format is designed to prevent agents from asking clarifying questions —
// dependency outputs appear BEFORE the task instructions with explicit directives
// to use them as input.

func (e *MissionEngine) buildMissionBrief(ctx context.Context, ms *missionState, task TaskInfo, allTasks []TaskInfo) string {
	var b strings.Builder

	// Collect dependency outputs first — we need to know if they exist for the preamble.
	// Prefer structured handoff summary when available (concise, designed for next agent).
	deps, _ := parseDependsOn(task.DependsOn)
	depOutputs := make([]string, 0)
	for _, depID := range deps {
		for _, t := range allTasks {
			if t.ID == depID && t.ResultSummary != nil && *t.ResultSummary != "" {
				agentLabel := "unknown"
				if t.AgentSlug != nil {
					agentLabel = "@" + *t.AgentSlug
				}

				// Try to extract structured handoff — more concise and targeted
				handoff := parseHandoff(*t.ResultSummary)
				var summary string
				if handoff.Parsed && handoff.Summary != "" {
					summary = handoff.Summary
					if handoff.Artifacts != "" && handoff.Artifacts != "none" {
						summary += "\nArtifacts: " + handoff.Artifacts
					}
					if handoff.Confidence != "" {
						summary += "\nConfidence: " + handoff.Confidence
					}
				} else {
					summary = *t.ResultSummary
					if len(summary) > maxDepOutputLen {
						summary = summary[:maxDepOutputLen] + "\n...(truncated)"
					}
				}

				depOutputs = append(depOutputs,
					fmt.Sprintf("--- Output from Task #%d \"%s\" (by %s) ---\n%s", t.TaskOrder, t.Title, agentLabel, summary))
			}
		}
	}

	// Assertive preamble — prevents "I need more info" responses
	if len(depOutputs) > 0 {
		b.WriteString("IMPORTANT: You are part of a multi-agent mission pipeline. ")
		b.WriteString("Previous tasks have already been completed and their outputs are provided below. ")
		b.WriteString("DO NOT ask for additional information or clarification — everything you need is in this prompt. ")
		b.WriteString("Use the dependency outputs below as your input and execute your task immediately.\n\n")
	}

	// Mission overview
	var missionTitle, missionDesc sql.NullString
	e.db.QueryRowContext(ctx,
		`SELECT title, description FROM missions WHERE id = ?`, ms.ID,
	).Scan(&missionTitle, &missionDesc)

	b.WriteString("[MISSION]\n")
	if missionTitle.Valid {
		// fmt.Fprintf streams into the Builder directly; the previous
		// b.WriteString(fmt.Sprintf(...)) allocated an intermediate string
		// per call just to copy it into the same buffer.
		fmt.Fprintf(&b, "Name: %s\n", missionTitle.String)
	}
	if missionDesc.Valid && missionDesc.String != "" {
		fmt.Fprintf(&b, "Goal: %s\n", missionDesc.String)
	}

	// DAG overview — list all tasks so the agent knows the bigger picture
	fmt.Fprintf(&b, "Tasks in pipeline: %d\n", len(allTasks))
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
		fmt.Fprintf(&b, "  %s#%d %s (%s, %s)\n", marker, t.TaskOrder, t.Title, agentLabel, t.Status)
	}
	b.WriteString("\n")

	// Dependency outputs — BEFORE the task assignment so agent reads context first
	if len(depOutputs) > 0 {
		b.WriteString("[INPUT FROM PREVIOUS TASKS]\n")
		b.WriteString("The following outputs were produced by tasks that yours depends on.\n")
		b.WriteString("You MUST use this information to complete your task:\n\n")
		b.WriteString(strings.Join(depOutputs, "\n\n"))
		b.WriteString("\n\n")
	}

	// Issue comments — so the agent has full context
	if rows, err := e.db.QueryContext(ctx, `SELECT COALESCE(CASE mc.author_type WHEN 'agent' THEN (SELECT name FROM agents WHERE id = mc.author_id) WHEN 'user' THEN (SELECT COALESCE(name, email) FROM users WHERE id = mc.author_id) ELSE 'System' END, 'Unknown'), mc.body FROM mission_comments mc WHERE mc.mission_id = ? ORDER BY mc.created_at ASC LIMIT 30`, ms.ID); err == nil {
		var hdr bool
		for rows.Next() {
			var n, bd string
			if rows.Scan(&n, &bd) != nil {
				continue
			}
			if !hdr {
				b.WriteString("[ISSUE COMMENTS]\n")
				hdr = true
			}
			if len(bd) > 500 {
				bd = bd[:500] + "..."
			}
			b.WriteString(fmt.Sprintf("@%s: %s\n\n", n, bd))
		}
		rows.Close()
	}

	// Current task details — the actual assignment
	b.WriteString("[YOUR ASSIGNMENT]\n")
	b.WriteString(fmt.Sprintf("Task: %s\n", task.Title))
	if task.Description != nil && *task.Description != "" {
		b.WriteString(fmt.Sprintf("Instructions: %s\n", *task.Description))
	}
	if task.Iteration > 1 {
		b.WriteString(fmt.Sprintf("Iteration: %d — this is a retry. Fix the issues from the previous attempt.\n", task.Iteration))
	}

	// Structured handoff instructions — agent MUST format output this way
	b.WriteString("\n[OUTPUT FORMAT]\n")
	b.WriteString("When you complete this task, end your response with a structured summary block:\n")
	b.WriteString("---HANDOFF---\n")
	b.WriteString("summary: <1-3 sentences describing what you did and the result>\n")
	b.WriteString("confidence: <low|medium|high>\n")
	b.WriteString("artifacts: <comma-separated list of files created/modified, or \"none\">\n")
	b.WriteString("---END HANDOFF---\n")
	b.WriteString("This block is REQUIRED. It helps the next agent in the pipeline understand your output.\n")

	// Closing directive
	if len(depOutputs) > 0 {
		b.WriteString("\nExecute this task NOW using the input from previous tasks above. Do not ask questions.")
	}

	result := b.String()
	if len(result) > maxBriefTotalLen {
		result = result[:maxBriefTotalLen] + "\n...(brief truncated to 32KB)"
	}
	return result
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
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("assigned agent %s not found (deleted or invalid)", *task.AssignedAgentID)
		}
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

	// Transition task to IN_PROGRESS (idempotency: only if still PENDING)
	res, err := e.db.ExecContext(ctx,
		`UPDATE mission_tasks SET status = 'IN_PROGRESS', started_at = ?, updated_at = ? WHERE id = ? AND status = 'PENDING'`,
		now, now, task.ID)
	if err != nil {
		return fmt.Errorf("update task status: %w", err)
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return nil // already claimed by another tick — skip silently
	}

	e.broadcastTaskStatus(ms, task.ID, "IN_PROGRESS")
	e.pw.WriteEvent(ms.TraceID, ms.CrewSlug, ProgressEvent{
		Type:      "task_started",
		TaskID:    task.ID,
		AgentSlug: agentSlug,
		Title:     task.Title,
	})

	// Build rich mission brief with full context for the agent
	taskBrief := e.buildMissionBrief(ctx, ms, task, allTasks)

	e.logger.Info("mission brief built",
		"task_id", task.ID,
		"brief_len", len(taskBrief),
		"has_input_section", strings.Contains(taskBrief, "[INPUT FROM PREVIOUS TASKS]"),
		"has_assignment", strings.Contains(taskBrief, "[YOUR ASSIGNMENT]"),
	)

	// Create assignment record — store full brief for audit trail
	assignmentID := generateID()
	_, err = e.db.ExecContext(ctx, `
		INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status, group_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?, 'PENDING', ?, ?)`,
		assignmentID, ms.WorkspaceID, ms.ID, ms.LeadAgentID, *task.AssignedAgentID,
		taskBrief,
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
				// Use Background ctx — parent ctx may be cancelled by the time this goroutine runs
				e.updateTaskStatus(context.Background(), ms, task.ID, "FAILED", dispatchErr.Error())
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
