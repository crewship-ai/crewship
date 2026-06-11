package orchestrator

// Mission task completion + dependency cascade — handles
// OnAssignmentCompleted, dep-task failure propagation, and the
// final mission-completion check. Extracted from mission_tasks.go
// for readability; no behavioral change.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/inbox"
	"github.com/crewship-ai/crewship/internal/ws"
)

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
	return e.checkMissionCompletionWithTasks(ctx, ms, tasks)
}

// checkMissionCompletionWithTasks is checkMissionCompletion over an
// already-loaded task snapshot, so the tick loop can load mission_tasks
// once and share the slice with the deadlock check. It only mutates the
// missions table (never mission_tasks rows), which is what makes sharing
// that snapshot with the subsequent deadlock check safe.
func (e *MissionEngine) checkMissionCompletionWithTasks(ctx context.Context, ms *missionState, tasks []TaskInfo) error {
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
		if _, err := e.db.ExecContext(ctx,
			`UPDATE missions SET status = 'REVIEW', completed_at = ?, updated_at = ? WHERE id = ? AND status = 'IN_PROGRESS'`,
			now, now, ms.ID); err != nil {
			return fmt.Errorf("update mission to REVIEW: %w", err)
		}
		e.broadcastMissionStatus(ms, "REVIEW")
		e.pw.WriteEvent(ms.TraceID, ms.CrewSlug, ProgressEvent{
			Type:      "mission_REVIEW",
			MissionID: ms.ID,
		})
		e.logger.Info("mission completed", "mission_id", ms.ID, "status", "REVIEW")
		e.fireIssueReviewInboxNotification(ctx, ms)
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

	if _, err := e.db.ExecContext(ctx,
		`UPDATE missions SET status = ?, completed_at = ?, updated_at = ? WHERE id = ? AND status = 'IN_PROGRESS'`,
		newStatus, completedAt, now, ms.ID); err != nil {
		return fmt.Errorf("update mission status: %w", err)
	}

	e.broadcastMissionStatus(ms, newStatus)
	e.pw.WriteEvent(ms.TraceID, ms.CrewSlug, ProgressEvent{
		Type:      "mission_" + newStatus,
		MissionID: ms.ID,
	})

	e.logger.Info("mission completed", "mission_id", ms.ID, "status", newStatus)
	if newStatus == "REVIEW" {
		// allTerminal+REVIEW path also lands in the user's "I need
		// to look at this" set. Original implementation only fired
		// the inbox notification on the lead-planning fast path
		// above; missions reaching REVIEW through this branch were
		// silently skipped. Same helper now covers both.
		e.fireIssueReviewInboxNotification(ctx, ms)
	}
	return nil
}

// fireIssueReviewInboxNotification creates the kind=message inbox row
// when an issue mission transitions to REVIEW, so the user gets a
// single jump-off point next to waitpoints and escalations. Best-
// effort: a SQL failure is logged + swallowed (the missions row is
// already updated; the inbox is a projection). Skips for non-issue
// mission types and missions without an identifier — neither has a
// meaningful "open the issue page" path.
func (e *MissionEngine) fireIssueReviewInboxNotification(ctx context.Context, ms *missionState) {
	var (
		missionTitle, missionIdentifier sql.NullString
		missionType                     sql.NullString
	)
	if err := e.db.QueryRowContext(ctx,
		`SELECT title, identifier, mission_type FROM missions WHERE id = ?`,
		ms.ID).Scan(&missionTitle, &missionIdentifier, &missionType); err != nil {
		e.logger.Warn("review inbox: lookup mission", "mission_id", ms.ID, "error", err)
		return
	}
	if !missionType.Valid || missionType.String != "issue" || !missionIdentifier.Valid {
		return
	}
	title := fmt.Sprintf("%s ready for review", missionIdentifier.String)
	body := ""
	if missionTitle.Valid {
		body = missionTitle.String
	}
	inbox.Insert(ctx, e.db, e.logger, inbox.Item{
		WorkspaceID: ms.WorkspaceID,
		Kind:        "message",
		SourceID:    "issue_review_" + ms.ID,
		TargetRole:  "MANAGER",
		Title:       title,
		BodyMD:      body,
		SenderType:  "system",
		SenderName:  "Mission engine",
		Priority:    "medium",
		Blocking:    false,
		Payload: map[string]interface{}{
			"mission_id":       ms.ID,
			"issue_identifier": missionIdentifier.String,
			"new_status":       "REVIEW",
		},
	})
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
