package orchestrator

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/untrusted"
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
	return e.resolveReadyFromTasks(ctx, missionID, tasks)
}

// resolveReadyFromTasks is ResolveReadyTasks' self-heal + ready-filter logic,
// operating on an already-loaded task snapshot instead of querying its own.
// scheduleReadyTasks calls this directly with the snapshot it already loaded
// for the mission-brief context, so a tick issues one mission_tasks query
// here instead of two (#1255 item 4).
func (e *MissionEngine) resolveReadyFromTasks(ctx context.Context, missionID string, tasks []TaskInfo) ([]TaskInfo, error) {
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
	depOutputs := make([]string, 0, len(deps))
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
		// Mission goal originates from an external issue/ticket body — fence it
		// as untrusted data before the agent reads it (#808 M1).
		fmt.Fprintf(&b, "Goal: %s\n", untrusted.Wrap("mission_task", missionDesc.String))
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
			// Issue/mission comment bodies are external, lower-trust content —
			// fence each one so a comment can't smuggle an instruction override
			// into the agent's prompt (#808 M1). The author handle @n stays
			// unfenced (it's a short structural label, not free-form body text).
			fmt.Fprintf(&b, "@%s: %s\n\n", n, untrusted.Wrap("mission_comment", bd))
		}
		rows.Close()
	}

	// Current task details — the actual assignment
	b.WriteString("[YOUR ASSIGNMENT]\n")
	b.WriteString(fmt.Sprintf("Task: %s\n", task.Title))
	if task.Description != nil && *task.Description != "" {
		// The task description is the untrusted issue/ticket body the agent is
		// told to act on — the single widest-open ingress site (#808 M1). Fence
		// it as data so a body that says "ignore previous instructions" is
		// examined, never obeyed. Task.Title stays unfenced (short structural
		// label, surfaced in the DAG list and headers).
		fmt.Fprintf(&b, "Instructions: %s\n", untrusted.Wrap("mission_task", *task.Description))
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
	b.WriteString("outcome: <NO_CHANGE|SUCCEEDED|WORK_CREATED|PARTIAL|NEEDS_HUMAN|FAILED>\n")
	b.WriteString("---END HANDOFF---\n")
	b.WriteString("This block is REQUIRED. It helps the next agent in the pipeline understand your output.\n")
	b.WriteString("outcome tells the system what to do next: NO_CHANGE (nothing to do), SUCCEEDED (did the work,\n")
	b.WriteString("nothing further needed), WORK_CREATED (produced or updated an issue), PARTIAL (some done,\n")
	b.WriteString("some failed, no human needed yet), NEEDS_HUMAN (blocked on a decision, input or credential —\n")
	b.WriteString("goes to a human's inbox), or FAILED (could not complete). Omitting it is treated as FAILED.\n")

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
	// Single snapshot shared between ready-resolution and the mission-brief
	// context below — resolveReadyFromTasks self-heals/auto-assigns in place
	// on this slice, so allTasks already reflects those mutations without a
	// second query (#1255 item 4).
	allTasks, err := e.loadTasks(ctx, ms.ID)
	if err != nil {
		return fmt.Errorf("resolve ready tasks: %w", err)
	}
	ready, err := e.resolveReadyFromTasks(ctx, ms.ID, allTasks)
	if err != nil {
		return fmt.Errorf("resolve ready tasks: %w", err)
	}

	for _, task := range ready {
		err := e.scheduleTask(ctx, ms, task, allTasks)
		if err == nil {
			continue
		}
		// A DEFERRAL is not a failure. The target is staged for an operator's
		// decision — under guided autonomy that is the ephemeral-hire flow
		// working as designed — so the task stays exactly where it was
		// (PENDING, unlinked, no assignment row) and the next tick asks
		// again. Failing it here made the approval arrive at a task that had
		// already been marked terminal, which retries nothing. See
		// agent_hold.go.
		if isDeferredDispatch(err) {
			e.logger.Info("task deferred — target agent is held for an operator's approval",
				"task_id", task.ID, "mission_id", ms.ID, "reason", err.Error())
			continue
		}
		e.logger.Error("schedule task", "task_id", task.ID, "error", err)
		// Mark task as FAILED so the loop doesn't retry endlessly
		e.updateTaskStatus(ctx, ms, task.ID, "FAILED", err.Error())
	}
	return nil
}

// ensureMissionChat guarantees a chats row exists at ms.ID before this
// engine writes an assignment against it (assignments.chat_id is
// NOT NULL REFERENCES chats(id), and every assignment this engine inserts
// uses the mission's own id as chat_id).
//
// Mission creation is supposed to stamp this row itself — three of the four
// mission-creating doors do (api/mission_handler_mutate.go's Create,
// api/issue_handler_workflow.go's Start, internal/cartographer/fork.go's
// Fork). But two of those three learned it the hard way, as a FOREIGN KEY
// constraint failure on the mission's first task dispatch, because nothing
// here required it: api/missions_internal.go's Create (the door an agent
// uses to plan its own mission) and cartographer's Fork each independently
// forgot the row before this fix and #2128 respectively. A per-path
// eager insert is correctness at the source but does not close the class —
// a fifth door can forget it just as easily. This is that class fix: the
// one place every mission's assignments are actually written checks for
// itself rather than trusting whoever created the mission.
//
// Idempotent (SELECT-then-INSERT OR IGNORE), so a mission that already has
// its chat row — the common case — pays one indexed lookup and nothing
// else, and a second task dispatched into the same tick never double-inserts.
func (e *MissionEngine) ensureMissionChat(ctx context.Context, ms *missionState) error {
	var exists int
	if err := e.db.QueryRowContext(ctx, `SELECT 1 FROM chats WHERE id = ?`, ms.ID).Scan(&exists); err == nil {
		return nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("look up mission chat: %w", err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	title := ms.Title
	if title == "" {
		title = ms.ID
	}
	if _, err := e.db.ExecContext(ctx, `
		INSERT OR IGNORE INTO chats (id, agent_id, workspace_id, title, mode, status, started_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'MISSION', 'ACTIVE', ?, ?, ?)`,
		ms.ID, ms.LeadAgentID, ms.WorkspaceID, "Mission: "+title, now, now, now); err != nil {
		return fmt.Errorf("create synthetic chat for mission: %w", err)
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

	// Resolve the target agent's crew for cross-crew support. agents.status
	// comes along because this is where the work is ADMITTED — the row below
	// and the IN_PROGRESS flip above it are what a hold has to prevent, and
	// asking here costs nothing on a query that already runs.
	var agentCrewID, agentCrewSlug, agentSlug, agentStatus string
	err := e.db.QueryRowContext(ctx, `
		SELECT a.slug, a.crew_id, c.slug, COALESCE(a.status,'')
		FROM agents a
		JOIN crews c ON c.id = a.crew_id
		WHERE a.id = ? AND a.deleted_at IS NULL`,
		*task.AssignedAgentID).Scan(&agentSlug, &agentCrewID, &agentCrewSlug, &agentStatus)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("assigned agent %s not found (deleted or invalid)", *task.AssignedAgentID)
		}
		return fmt.Errorf("resolve agent crew: %w", err)
	}

	// The target is staged for an operator's decision. Return BEFORE the
	// IN_PROGRESS flip and BEFORE the assignment INSERT, so a hold that
	// stands for an hour costs zero rows and leaves the task exactly as the
	// next tick expects to find it. Callers read this as "wait", never as
	// "fail" — agent_hold.go explains why that third answer is the only
	// correct one here.
	if agentHeldForDispatch(agentStatus) {
		return &heldAgentDeferral{msg: fmt.Sprintf(
			"agent %s is %s: it was created or hired by an agent and is held until an operator "+
				"approves it, so this task waits rather than failing. Approve it from the inbox "+
				"(or with `crewship hire approve <agent-id>`) and the next tick dispatches it.",
			agentSlug, AgentStatusPendingReview)}
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

	// Create assignment record — store full brief for audit trail.
	//
	// depth is written EXPLICITLY as 0 rather than left to the column default.
	// It is the discriminator delegation_limits.go uses to tell a mission row
	// from one of its own capped doors' rows (`depth > 0`): a mission task is
	// one authored plan, not a delegation hop, and counting it as one made a
	// busy lead unmentionable. Pinned by
	// TestMissionAssignmentRowsCarryDepthZero — do not stamp a depth here
	// without changing that file too.
	//
	// The row and the link that makes it findable are ONE transaction. Split,
	// a failure (or a crash) between them left an assignment nothing pointed
	// at while the task sat IN_PROGRESS — and resolveReadyFromTasks only
	// re-picks PENDING/BLOCKED, so that task was stranded forever with no
	// operator-visible symptom. A rollback here instead returns an error the
	// caller turns into a FAILED task, which an operator can see and retry.
	//
	// Everything that is not one of these two writes stays OUTSIDE: SQLite
	// has a single database-wide writer, so the brief build above and the
	// dispatch below must never be inside this window.
	if err := e.ensureMissionChat(ctx, ms); err != nil {
		return fmt.Errorf("ensure mission chat: %w", err)
	}
	assignmentID := generateID()
	// mission_id/author_agent_id/created_by_user_id persisted on the row for
	// the same reason the planning insert (mission_tasks_planning.go) does —
	// dispatchByID's lock-loss re-dispatch rebuilds from the row alone.
	// lead_planning is left at its column DEFAULT 0: this insert is a
	// regular worker task, never the lead's own planning turn.
	nullIfEmptyStr := func(s string) any {
		if s == "" {
			return nil
		}
		return s
	}
	if err := database.WithTx(ctx, e.db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status, group_id, depth, created_at, mission_id, author_agent_id, created_by_user_id)
			VALUES (?, ?, ?, ?, ?, ?, 'PENDING', ?, 0, ?, ?, ?, ?)`,
			assignmentID, ms.WorkspaceID, ms.ID, ms.LeadAgentID, *task.AssignedAgentID,
			taskBrief,
			ms.ID, // group_id = mission_id for grouping
			now,
			ms.ID, nullIfEmptyStr(ms.AuthorAgentID), nullIfEmptyStr(ms.CreatedByUserID),
		); err != nil {
			return fmt.Errorf("create assignment: %w", err)
		}
		// Link assignment to the mission task.
		if _, err := tx.ExecContext(ctx,
			`UPDATE mission_tasks SET assignment_id = ?, updated_at = ? WHERE id = ?`,
			assignmentID, now, task.ID); err != nil {
			return fmt.Errorf("link assignment to task: %w", err)
		}
		return nil
	}); err != nil {
		e.logger.Error("create assignment", "task_id", task.ID, "error", err)
		return err
	}

	// Dispatch the assignment to the correct crew's container.
	// Audit #481 follow-up: WithoutCancel preserves the scheduler's
	// OTel trace + auth values so the dispatched assignment surfaces
	// under the same trace ID, while the parent request's
	// cancellation does not propagate (the scheduler tick has
	// returned to its loop before this goroutine runs).
	dispatchCtx := context.WithoutCancel(ctx)
	if e.dispatcher != nil {
		go func() {
			dispatchErr := e.dispatcher.DispatchAssignment(dispatchCtx, DispatchRequest{
				AssignmentID:    assignmentID,
				AgentID:         *task.AssignedAgentID,
				AgentSlug:       agentSlug,
				CrewID:          agentCrewID,
				CrewSlug:        agentCrewSlug,
				WorkspaceID:     ms.WorkspaceID,
				ChatID:          ms.ID,
				Task:            taskBrief,
				TraceID:         ms.TraceID,
				MissionID:       ms.ID,
				AuthorAgentID:   ms.AuthorAgentID,
				CreatedByUserID: ms.CreatedByUserID,
			})
			if dispatchErr != nil {
				// The door said "wait for an operator". The admission check
				// above already refuses a hold it can see, but the read and
				// this write are not one statement, so an agent staged in
				// between arrives here — with the row and the IN_PROGRESS
				// flip already done. Unwind both: the task goes back to the
				// state the next tick expects, and the row does not linger
				// PENDING in the ledger forever.
				if isDeferredDispatch(dispatchErr) {
					e.logger.Info("mission dispatch deferred — target agent is held for an operator's approval",
						"assignment_id", assignmentID, "task_id", task.ID, "reason", dispatchErr.Error())
					e.unwindDeferredTaskDispatch(dispatchCtx, ms, task.ID, assignmentID, dispatchErr.Error())
					return
				}
				e.logger.Error("dispatch assignment failed",
					"assignment_id", assignmentID,
					"error", dispatchErr,
				)
				// Audit #481 follow-up: was context.Background() with
				// a comment about parent-ctx cancellation. dispatchCtx
				// (WithoutCancel above) keeps trace + auth values;
				// cancellation is shed regardless.
				e.updateTaskStatus(dispatchCtx, ms, task.ID, "FAILED", dispatchErr.Error())
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

// unwindDeferredTaskDispatch puts a task back the way scheduleTask found it
// after the dispatch door deferred (agent_hold.go).
//
// Both halves are load-bearing:
//
//   - the assignment row is CANCELLED, not left PENDING. A PENDING row that
//     nothing will ever run is a permanent entry in the chat's ledger and, on
//     the /assign side, was the row that quietly consumed a slot of the
//     fan-out budget forever.
//   - the task goes back to PENDING with its started_at and assignment_id
//     cleared, which is exactly the shape resolveReadyFromTasks re-selects.
//     Leaving it IN_PROGRESS would park the mission until the two-hour
//     timeout with nothing running.
//
// Both writes are conditional on the state this function believes it is
// unwinding, so a run that started concurrently (or a completion that raced
// in) is never clobbered.
func (e *MissionEngine) unwindDeferredTaskDispatch(ctx context.Context, ms *missionState, taskID, assignmentID, reason string) {
	now := time.Now().UTC().Format(time.RFC3339)
	e.cancelDeferredAssignment(ctx, assignmentID, reason)
	res, err := e.db.ExecContext(ctx, `
		UPDATE mission_tasks
		   SET status = 'PENDING', started_at = NULL, assignment_id = NULL, updated_at = ?
		 WHERE id = ? AND status = 'IN_PROGRESS'`, now, taskID)
	if err != nil {
		e.logger.Error("unwind deferred task", "task_id", taskID, "error", err)
		return
	}
	if n, _ := res.RowsAffected(); n == 1 {
		e.broadcastTaskStatus(ms, taskID, "PENDING")
	}
}

// cancelDeferredAssignment retires the PENDING row a deferred dispatch left
// behind. Conditional on PENDING so a row a concurrent claim already moved to
// RUNNING is never cancelled out from under a live run.
func (e *MissionEngine) cancelDeferredAssignment(ctx context.Context, assignmentID, reason string) {
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := e.db.ExecContext(ctx, `
		UPDATE assignments
		   SET status = 'CANCELLED', finished_at = ?, error_message = ?
		 WHERE id = ? AND status = 'PENDING'`, now, reason, assignmentID); err != nil {
		e.logger.Error("cancel deferred assignment", "assignment_id", assignmentID, "error", err)
	}
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
