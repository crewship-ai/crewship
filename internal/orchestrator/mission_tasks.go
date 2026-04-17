package orchestrator

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/ws"
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

	// Parse structured handoff from FULL output before truncation.
	handoff := parseHandoff(resultSummary)

	// Compress result summary to prevent DB bloat (after handoff parsing).
	if len(resultSummary) > maxResultSummaryLen {
		resultSummary = resultSummary[:maxResultSummaryLen] + "\n...(truncated)"
	}

	if handoff.Parsed {
		e.logger.Info("structured handoff received",
			"task_id", taskID,
			"mission_id", missionID,
			"confidence", handoff.Confidence,
			"summary_len", len(handoff.Summary),
			"artifacts", handoff.Artifacts,
		)
		// Map confidence to numeric value and persist handoff + needs_review in one UPDATE.
		var confVal *float64
		needsReview := 0
		switch strings.ToLower(handoff.Confidence) {
		case "high":
			v := 0.9
			confVal = &v
		case "medium":
			v := 0.7
			confVal = &v
		case "low":
			v := 0.4
			confVal = &v
			needsReview = 1
		}
		if confVal != nil {
			if _, err := e.db.ExecContext(ctx,
				`UPDATE mission_tasks SET confidence = ?, handoff_context = ?, needs_review = ? WHERE id = ?`,
				*confVal, handoff.Summary, needsReview, taskID); err != nil {
				e.logger.Error("persist handoff data", "error", err, "task_id", taskID)
			}
		}
		if needsReview == 1 {
			e.logger.Warn("task flagged for human review (low confidence)",
				"task_id", taskID, "mission_id", missionID)
		}
	} else if taskStatus == "COMPLETED" {
		e.logger.Warn("task completed without structured handoff",
			"task_id", taskID,
			"mission_id", missionID,
		)
	}

	// Auto-post agent comment on the issue with completion summary
	if assignedAgentID.Valid {
		var agentName string
		_ = e.db.QueryRowContext(ctx, `SELECT name FROM agents WHERE id = ?`, assignedAgentID.String).Scan(&agentName)

		var commentBody string
		if handoff.Parsed && handoff.Summary != "" {
			commentBody = fmt.Sprintf("**%s completed their work** (confidence: %s)\n\n%s", agentName, handoff.Confidence, handoff.Summary)
			if handoff.Artifacts != "" {
				commentBody += "\n\n**Artifacts:** " + handoff.Artifacts
			}
		} else if taskStatus == "COMPLETED" {
			fallback := resultSummary
			if len(fallback) > 500 {
				fallback = fallback[:500] + "..."
			}
			if fallback != "" {
				commentBody = fmt.Sprintf("**%s completed their work**\n\n%s", agentName, fallback)
			} else {
				commentBody = fmt.Sprintf("**%s completed their work.**", agentName)
			}
		} else if taskStatus == "FAILED" {
			commentBody = fmt.Sprintf("**%s encountered an issue.**", agentName)
			if errorMessage != "" {
				commentBody += " Error: " + errorMessage
			}
		}
		if commentBody != "" {
			commentID := generateID()
			_, _ = e.db.ExecContext(ctx,
				`INSERT INTO mission_comments (id, mission_id, author_type, author_id, body, created_at, updated_at) VALUES (?, ?, 'agent', ?, ?, ?, ?)`,
				commentID, missionID, assignedAgentID.String, commentBody, now, now)
			// Activity log
			activityID := generateID()
			action := "task_completed"
			if taskStatus == "FAILED" {
				action = "task_failed"
			}
			_, _ = e.db.ExecContext(ctx,
				`INSERT INTO mission_activity (id, mission_id, actor_type, actor_id, action, details, created_at) VALUES (?, ?, 'agent', ?, ?, ?, ?)`,
				activityID, missionID, assignedAgentID.String, action, commentBody, now)
		}
	}

	// Approval gate: check if this task requires human approval before unblocking dependents.
	if taskStatus == "COMPLETED" {
		taskStatus = e.checkApprovalGate(ctx, taskID, missionID)
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

		// Notify workspace about pending approval for dashboard badge.
		if taskStatus == "AWAITING_APPROVAL" && e.hub != nil {
			e.hub.Broadcast("workspace:"+ms.WorkspaceID, ws.ServerMessage{
				Type:    "approval.required",
				Channel: "workspace:" + ms.WorkspaceID,
				Payload: map[string]string{
					"task_id":    taskID,
					"mission_id": missionID,
				},
			})
		}
	}

	e.logger.Info("task updated from assignment",
		"task_id", taskID,
		"mission_id", missionID,
		"status", taskStatus,
	)

	return nil
}

// checkApprovalGate determines whether a completed task should be held for human approval.
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
func (e *MissionEngine) failDependentTasks(ctx context.Context, missionID, failedTaskID, reason string) {
	visited := make(map[string]bool)
	e.failDependentTasksRecurse(ctx, missionID, failedTaskID, reason, visited)
}

func (e *MissionEngine) failDependentTasksRecurse(ctx context.Context, missionID, failedTaskID, reason string, visited map[string]bool) {
	if visited[failedTaskID] {
		return
	}
	visited[failedTaskID] = true

	rows, err := e.db.QueryContext(ctx,
		`SELECT id, depends_on FROM mission_tasks WHERE mission_id = ? AND status = 'BLOCKED'`, missionID)
	if err != nil {
		e.logger.Error("query blocked tasks for cascade", "error", err)
		return
	}

	var toFail []string
	for rows.Next() {
		var id, depsJSON string
		if err := rows.Scan(&id, &depsJSON); err != nil {
			continue
		}
		deps, parseErr := parseDependsOn(depsJSON)
		if parseErr != nil || len(deps) == 0 {
			continue
		}
		for _, d := range deps {
			if d == failedTaskID {
				toFail = append(toFail, id)
				break
			}
		}
	}
	rows.Close()

	now := time.Now().UTC().Format(time.RFC3339)
	e.mu.Lock()
	ms := e.active[missionID]
	e.mu.Unlock()

	for _, id := range toFail {
		if visited[id] {
			continue
		}
		if _, err := e.db.ExecContext(ctx,
			`UPDATE mission_tasks SET status = 'FAILED', error_message = ?, updated_at = ?, completed_at = ? WHERE id = ?`,
			reason, now, now, id); err != nil {
			e.logger.Error("cascade fail task", "task_id", id, "error", err)
			continue
		}
		if ms != nil {
			e.broadcastTaskStatus(ms, id, "FAILED")
		}
		e.failDependentTasksRecurse(ctx, missionID, id, reason, visited)
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
		// No mission_tasks — check if lead planning completed and all assignments are done.
		// This handles the case where lead used /assign (creates assignments, not mission_tasks).
		e.mu.Lock()
		planned := ms.planningDispatched
		e.mu.Unlock()
		if !planned {
			return nil // lead planning hasn't started yet
		}
		// Check if all assignments for this mission are in terminal state
		var pending int
		_ = e.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM assignments WHERE group_id = ? AND status NOT IN ('COMPLETED','FAILED')`,
			ms.ID).Scan(&pending)
		if pending > 0 {
			return nil // assignments still running
		}
		// All assignments done — check if any exist at all
		var total int
		_ = e.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM assignments WHERE group_id = ?`, ms.ID).Scan(&total)
		if total == 0 {
			return nil // no assignments yet — still waiting
		}
		// Lead planning completed, all assignments done → move to REVIEW
		e.logger.Info("lead planning complete, all assignments finished — moving to REVIEW",
			"mission_id", ms.ID, "total_assignments", total)
		now := time.Now().UTC().Format(time.RFC3339)
		_, err = e.db.ExecContext(ctx,
			`UPDATE missions SET status = 'REVIEW', completed_at = ?, updated_at = ? WHERE id = ? AND status = 'IN_PROGRESS'`,
			now, now, ms.ID)
		if err != nil {
			return fmt.Errorf("update mission to REVIEW: %w", err)
		}
		e.broadcastMissionStatus(ms, "REVIEW")
		e.pw.WriteEvent(ms.TraceID, ms.CrewSlug, ProgressEvent{
			Type:      "mission_REVIEW",
			MissionID: ms.ID,
		})
		e.logger.Info("mission completed", "mission_id", ms.ID, "status", "REVIEW")
		return nil
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
	// Load every task's status for this mission in one scan, so dep-readiness checks
	// below are O(1) map lookups instead of one QueryRowContext per dep (was N*K queries).
	statusRows, err := e.db.QueryContext(ctx,
		`SELECT id, status, depends_on FROM mission_tasks WHERE mission_id = ?`,
		missionID)
	if err != nil {
		e.logger.Error("query mission tasks", "error", err)
		return
	}

	type blockedTask struct {
		id   string
		deps []string
	}
	statusByID := make(map[string]string)
	var candidates []blockedTask
	for statusRows.Next() {
		var id, status, depsJSON string
		if err := statusRows.Scan(&id, &status, &depsJSON); err != nil {
			continue
		}
		statusByID[id] = status
		if status != "BLOCKED" {
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
	statusRows.Close()

	now := time.Now().UTC().Format(time.RFC3339)
	for _, bt := range candidates {
		allDone := true
		for _, d := range bt.deps {
			if statusByID[d] != "COMPLETED" {
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

	// Build adjacency list and check for nonexistent deps
	graph := make(map[string][]string, len(tasks)) // taskID → deps
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
