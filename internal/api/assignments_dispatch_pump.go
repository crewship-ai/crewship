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
		workspaceID  string
		chatID       string
		assignedByID string
		assignedToID string
		task         string
		groupID      sql.NullString
	)
	err := h.db.QueryRowContext(ctx, `
		SELECT workspace_id, chat_id, assigned_by_id, assigned_to_id, task, group_id
		FROM assignments
		WHERE id = ?`, assignmentID).Scan(&workspaceID, &chatID, &assignedByID, &assignedToID, &task, &groupID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("dispatchByID: assignment %s not found", assignmentID)
		}
		return fmt.Errorf("dispatchByID: load assignment %s: %w", assignmentID, err)
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

	creds, err := h.loadAgentCredentials(ctx, target.ID)
	if err != nil {
		return fmt.Errorf("dispatchByID: load credentials for %s: %w", assignmentID, err)
	}

	body := createAssignmentBody{
		TargetSlug:  target.Slug,
		Task:        task,
		CrewID:      crewID,
		WorkspaceID: workspaceID,
		ChatID:      chatID,
		// MissionID from group_id when present. Pumped assignments
		// preserve the mission link so the completion comment posts
		// back to the right issue. NULL group_id → empty MissionID,
		// runAssignment then skips the mission-linked branch (same
		// as a non-mission assignment created via /assign).
		MissionID:   groupID.String,
		CrewMembers: h.loadCrewMembers(ctx, crewID, target.ID),
		// LeadPlanning is intentionally false on pumped dispatches.
		// Lead-planning assignments are originated by the lead-agent
		// flow, which never goes through claim/queue (they bypass
		// the per-crew budget — a stuck lead deadlocks the whole
		// mission). If a lead-planning assignment ever did get
		// queued, that's an upstream bug; pumping it without the
		// flag re-creates the lead in non-lead mode which is wrong
		// but recoverable, vs. blocking the pump forever.
		LeadPlanning: false,
	}

	h.logger.Info("dispatching queued assignment",
		"assignment_id", assignmentID,
		"crew", target.CrewSlug,
		"target", target.Slug,
	)

	h.runAssignment(ctx, assignmentID, body, target, creds)
	return nil
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
		go func(assignmentID string) {
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
