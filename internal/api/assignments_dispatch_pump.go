package api

// Phase 1B of the queue mechanism (.claude/context/prd/QUEUE-MECHANISM-2026.md).
// This file owns the "I have an assignment id, run it" path that Phase
// 1A's primitives lacked — DispatchAssignment was the only door into
// runAssignment, but the queue pump fires from the completion path
// where we hold an id and nothing else. dispatchByID reconstructs
// target/creds/body from the row and calls runAssignment; the existing
// DispatchAssignment shrinks to "load + claim + delegate".

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// dispatchByID loads every input runAssignment needs straight from the
// assignment row (and joined tables) and starts the run. Used by the
// completion-path pump after claimCrewSlot/pumpCrewQueue have already
// flipped the row to RUNNING — runAssignment's own UPDATE-to-RUNNING
// at the top of the function is a no-op in that case (status already
// matches, started_at coalesces).
//
// Returns an error so the pump can log per-id failures without
// crashing the whole pump cycle. Errors here mean the row is in an
// inconsistent state (RUNNING in DB but never actually executed) —
// the sweeper described in the PRD's "failure modes" section will
// eventually re-pump those, but that's Phase 2; today such failures
// are rare-but-logged.
func (h *AssignmentHandler) dispatchByID(ctx context.Context, assignmentID string) error {
	var (
		workspaceID     string
		chatID          string
		assignedByID    string
		assignedToID    string
		task            string
		missionID       sql.NullString
		authorAgentID   sql.NullString
		createdByUserID sql.NullString
		leadPlanning    bool
	)
	err := h.db.QueryRowContext(ctx, `
		SELECT workspace_id, chat_id, assigned_by_id, assigned_to_id, task,
		       mission_id, author_agent_id, created_by_user_id, lead_planning
		FROM assignments
		WHERE id = ?`, assignmentID).Scan(&workspaceID, &chatID, &assignedByID, &assignedToID, &task,
		&missionID, &authorAgentID, &createdByUserID, &leadPlanning)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("dispatchByID: assignment %s not found", assignmentID)
		}
		return fmt.Errorf("dispatchByID: load assignment %s: %w", assignmentID, err)
	}

	// Cross-surface exclusivity pre-check (#2269 follow-up, defect 4 —
	// "pumpCrewQueue is agent-blind"): pumpCrewQueue's SQL CAS claims purely
	// on crew budget, so it can flip a row to RUNNING for an agent that is
	// ACTUALLY busy elsewhere (a chat send, or another assignment holding
	// chatbridge.AgentRunLock) — it has no visibility into that in-memory
	// state. Checked HERE, before the target-agent load below (the more
	// expensive of the two queries this function runs), so a doomed
	// dispatch doesn't pay for it. This is an optimization layered on top
	// of runAssignment's own TryStart check below (still the source of
	// truth: InFlight and TryStart are not one atomic operation, so a race
	// where the lock is claimed between this check and TryStart is still
	// caught there, just after paying for the load).
	if h.agentRunLock != nil && h.agentRunLock.InFlight(assignedToID) {
		crewID, cerr := h.crewIDForAssignment(ctx, assignmentID)
		if cerr == nil && crewID != "" {
			h.logger.Info("dispatchByID: agent busy (cross-surface), requeuing without loading target",
				"assignment_id", assignmentID, "agent_id", assignedToID)
			h.requeueLockLossAndMaybeDrain(ctx, assignmentID, crewID, assignedToID, chatID, workspaceID, "")
			return nil
		}
		// crewIDForAssignment failed or found none — fall through to the
		// normal path. runAssignment's own TryStart catches the busy lock
		// regardless; the only cost of not short-circuiting here is the
		// redundant target load below, not a correctness gap.
	}

	var target targetAgentInfo
	var crewID string
	err = h.db.QueryRowContext(ctx, `
		SELECT a.id, a.slug, a.name, COALESCE(a.role_title,''), COALESCE(a.system_prompt_legacy,''),
		       a.cli_adapter, COALESCE(a.llm_model,''), a.tool_profile, a.timeout_seconds,
		       a.memory_enabled, c.slug, c.id
		FROM agents a
		JOIN crews c ON c.id = a.crew_id
		WHERE a.id = ? AND a.deleted_at IS NULL
	`, assignedToID).Scan(
		&target.ID, &target.Slug, &target.Name, &target.RoleTitle,
		&target.SystemPrompt, &target.CLIAdapter, &target.LLMModel,
		&target.ToolProfile, &target.TimeoutSeconds, &target.MemoryEnabled,
		&target.CrewSlug, &crewID,
	)
	if err != nil {
		return fmt.Errorf("dispatchByID: load target agent for %s: %w", assignmentID, err)
	}

	body := createAssignmentBody{
		TargetSlug:  target.Slug,
		Task:        task,
		CrewID:      crewID,
		WorkspaceID: workspaceID,
		ChatID:      chatID,
		// mission_id, author_agent_id, created_by_user_id, lead_planning are
		// read straight off the row rather than assumed from group_id (see
		// the 20260901221102 migration's doc for why that assumption was
		// wrong for a Create-originated /assign row) or hard-coded false.
		// This is what lets a lock-loss requeue re-dispatch an EXACT copy
		// of the original door instead of a degraded one.
		MissionID:       missionID.String,
		CrewMembers:     h.loadCrewMembers(ctx, crewID, target.ID),
		LeadPlanning:    leadPlanning,
		AuthorAgentID:   authorAgentID.String,
		CreatedByUserID: createdByUserID.String,
	}

	h.logger.Info("dispatching queued assignment",
		"assignment_id", assignmentID,
		"crew", target.CrewSlug,
		"target", target.Slug,
		"lead_planning", body.LeadPlanning,
	)

	h.runAssignment(ctx, assignmentID, body, target)
	return nil
}

// requeueLockLossAndMaybeDrain is the shared tail of both places that can
// discover chatbridge.AgentRunLock is already held by someone else for an
// assignment's target agent: runAssignment's own TryStart check
// (assignments_run.go — a row already flipped RUNNING by claimCrewSlot/
// pumpCrewQueue, or a still-PENDING LeadPlanning row that bypassed the CAS)
// and dispatchByID's InFlight pre-check above. Both requeue the row via
// requeueForLockLoss and both need the SAME answer to "should this trigger
// a fresh pump" — see existsQueuedForOtherAgent's doc for why that is
// conditional. Centralising it here means the livelock-avoidance logic
// exists exactly once.
func (h *AssignmentHandler) requeueLockLossAndMaybeDrain(
	ctx context.Context,
	assignmentID, crewID, agentID, chatID, workspaceID, targetSlug string,
) {
	requeued, rqErr := requeueForLockLoss(ctx, h.db, assignmentID)
	switch {
	case rqErr != nil:
		h.logger.Error("assignment lock loss: requeue failed",
			"assignment_id", assignmentID, "agent_id", agentID, "error", rqErr)
		return
	case !requeued:
		// Row had already left PENDING/RUNNING (raced a cancel, the
		// stuck-RUNNING/stuck-QUEUED sweeper, or a terminal write) before we
		// could requeue it. Nothing to drop or drain — whoever moved it
		// already owns its fate.
		h.logger.Warn("assignment lock loss: row was no longer PENDING/RUNNING, not requeued",
			"assignment_id", assignmentID, "agent_id", agentID)
		return
	}
	h.emitAssignmentQueued(ctx, assignmentID, chatID, workspaceID, crewID, targetSlug)

	other, oerr := existsQueuedForOtherAgent(ctx, h.db, crewID, agentID)
	if oerr != nil {
		h.logger.Warn("assignment lock loss: could not check for other queued work; not pumping",
			"assignment_id", assignmentID, "crew_id", crewID, "error", oerr)
		return
	}
	if !other {
		// The only queued work in this crew targets the SAME busy agent —
		// pumping now would just reclaim this row again immediately. Leave
		// it for the normal drain triggers (an unrelated completion in this
		// crew, or the stuck-QUEUED sweeper) once the agent actually frees.
		return
	}
	// context.Background(), same as finishAssignment's own post-completion
	// pump: the dispatched goroutines this spawns must outlive whichever
	// request/goroutine ctx belongs to.
	if _, perr := h.pumpAndDispatch(context.Background(), crewID); perr != nil {
		h.logger.Warn("post-lock-loss queue pump failed",
			"assignment_id", assignmentID, "crew_id", crewID, "error", perr)
	}
}

// pumpAndDispatch is the completion-path entry point: claim as many
// QUEUED rows for this crew as the budget allows, then spawn a
// goroutine per claimed row to actually execute the agent. Designed
// to be called from the assignment-completion path WITHOUT blocking
// the caller — the pumped dispatches each take their own goroutine
// because runAssignment is long-running.
//
// Returns the number of assignments claimed (for logging) and any
// error from the pump itself. Per-dispatch errors are logged inside
// each spawned goroutine and do not propagate up — a single bad
// pumped row must not stop the queue from draining.
//
// Context handling: we deliberately use context.Background for the
// spawned dispatches, NOT the caller's ctx. The caller's ctx is the
// HTTP request that triggered the completion (or the orchestrator's
// mission-engine ctx); cancelling it would kill the pumped run
// mid-flight. The dispatched run owns its own lifetime via its own
// runID + timeoutSeconds.
func (h *AssignmentHandler) pumpAndDispatch(ctx context.Context, crewID string) (int, error) {
	if crewID == "" {
		return 0, nil
	}
	budget, err := computeCrewBudget(ctx, h.db, crewID)
	if err != nil {
		// Don't fall back to budget=1 silently — at completion time
		// we'd rather skip the pump and let the next completion try
		// again than dispatch with an artificially constrained
		// budget. The caller's completion still succeeded; the queue
		// just won't drain this tick.
		return 0, fmt.Errorf("pumpAndDispatch: compute budget for %s: %w", crewID, err)
	}
	claimed, err := pumpCrewQueue(ctx, h.db, crewID, budget)
	if err != nil {
		return 0, fmt.Errorf("pumpAndDispatch: pump crew %s: %w", crewID, err)
	}
	for _, id := range claimed {
		h.dispatchWG.Add(1)
		finish := beginBackgroundWork()
		go func(assignmentID string) {
			defer finish()
			defer h.dispatchWG.Done()
			if derr := h.dispatchByID(context.Background(), assignmentID); derr != nil {
				h.logger.Error("pumped dispatch failed",
					"assignment_id", assignmentID,
					"crew_id", crewID,
					"error", derr,
				)
			}
		}(id)
	}
	return len(claimed), nil
}

// PumpForAgent drains agentID's crew queue after chatbridge.AgentRunLock's
// per-agent claim is released by the OTHER door that can hold it — a chat
// send (bridge.go's HandleChatMessage). Every existing pump trigger fires
// from THIS package's own release points (finishAssignment's post-
// completion pump, requeueLockLossAndMaybeDrain), so an assignment queued
// behind a chat turn used to wait for an unrelated crew completion or the
// stuck-QUEUED sweeper even after the chat turn had long finished (#2269
// follow-up, defect 5). chatbridge.Bridge calls this — via the
// AssignmentPumper seam SetAssignmentPumper wires at boot (cmd_start.go) —
// from a deferred call alongside its own AgentRunLock.End.
//
// Takes an AGENT id, not a crew id (unlike pumpAndDispatch), because the
// chat door only knows which agent it just freed — it has no assignment row
// to hang a crew id off of the way the completion path does.
func (h *AssignmentHandler) PumpForAgent(ctx context.Context, agentID string) {
	if agentID == "" {
		return
	}
	crewID, err := h.crewIDForAgent(ctx, agentID)
	if err != nil || crewID == "" {
		return
	}
	if _, perr := h.pumpAndDispatch(ctx, crewID); perr != nil {
		h.logger.Warn("post-chat-release queue pump failed", "agent_id", agentID, "crew_id", crewID, "error", perr)
	}
}

// crewIDForAgent looks up an agent's crew directly (no assignment row
// involved) — PumpForAgent's input. Returns ("", nil) for a deleted/missing
// agent, same "no pump needed, not an error" convention as
// crewIDForAssignment below.
func (h *AssignmentHandler) crewIDForAgent(ctx context.Context, agentID string) (string, error) {
	var crewID sql.NullString
	err := h.db.QueryRowContext(ctx, `SELECT crew_id FROM agents WHERE id = ? AND deleted_at IS NULL`, agentID).Scan(&crewID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return "", fmt.Errorf("crewIDForAgent: %w", err)
	}
	if !crewID.Valid {
		return "", nil
	}
	return crewID.String, nil
}

// crewIDForAssignment looks up the crew of an assignment's target
// agent. Called from the completion path which only has the
// assignment id in scope. Returns ("", nil) when the row or agent has
// been deleted — caller should treat that as "no pump needed" rather
// than an error.
func (h *AssignmentHandler) crewIDForAssignment(ctx context.Context, assignmentID string) (string, error) {
	var crewID sql.NullString
	err := h.db.QueryRowContext(ctx, `
		SELECT a.crew_id
		FROM assignments asn
		JOIN agents a ON a.id = asn.assigned_to_id
		WHERE asn.id = ?`, assignmentID).Scan(&crewID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		return "", fmt.Errorf("crewIDForAssignment: %w", err)
	}
	if !crewID.Valid {
		return "", nil
	}
	return crewID.String, nil
}

// emitAssignmentQueued broadcasts the assignment_queued event on the
// workspace channel + a session-level event for the chat that owns
// the assignment. Mirrors the broadcastChannelEvent/broadcastWorkspaceEvent
// pattern that assignment_running / assignment_completed already use.
//
// Payload keys are stable: the dashboard reads "ahead_of" to render
// "you're #3 in the queue". queue_depth includes this assignment in
// the count (the operator sees "3 ahead" when they're at position 3).
func (h *AssignmentHandler) emitAssignmentQueued(ctx context.Context, assignmentID, chatID, workspaceID, crewID, targetSlug string) {
	depth, derr := queueDepth(ctx, h.db, crewID)
	if derr != nil {
		// Non-fatal — broadcast the event without the ahead-of hint.
		// The UI degrades to "queued" without a position. Don't lose
		// the entire event because a count query failed.
		h.logger.Warn("queueDepth for assignment_queued payload failed", "crew_id", crewID, "error", derr)
		depth = 0
	}
	payload := map[string]any{
		"id":          assignmentID,
		"crew_id":     crewID,
		"target":      targetSlug,
		"ahead_of":    depth,
		"queue_depth": depth,
	}
	if chatID != "" {
		broadcastChannelEvent(h.hub, "session", chatID, "assignment_queued", payload)
	}
	broadcastWorkspaceEvent(h.hub, workspaceID, "assignment_queued", payload)
}

// emitAssignmentUnqueued broadcasts the assignment_unqueued event
// when the pump promotes a QUEUED row to RUNNING. Distinct from
// assignment_running because Phase 1B may emit BOTH for the same
// transition (unqueued lets the UI animate the dequeue specifically,
// then assignment_running fires from runAssignment). Phase 2 might
// collapse them once the UI semantics settle.
func (h *AssignmentHandler) emitAssignmentUnqueued(ctx context.Context, assignmentID, chatID, workspaceID, crewID string) {
	payload := map[string]any{
		"id":      assignmentID,
		"crew_id": crewID,
	}
	if chatID != "" {
		broadcastChannelEvent(h.hub, "session", chatID, "assignment_unqueued", payload)
	}
	broadcastWorkspaceEvent(h.hub, workspaceID, "assignment_unqueued", payload)
}
