package api

// Assignment runtime — Create handler + the goroutine that actually
// runs an assignment (runAssignment) and the post-run state
// reconciliation (finishAssignment). Extracted from assignments.go
// for readability; types and read paths stay in the main file.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/ws"
)

type createAssignmentBody struct {
	TargetSlug   string                    `json:"target_slug"`
	Task         string                    `json:"task"`
	CrewID       string                    `json:"crew_id"`
	WorkspaceID  string                    `json:"workspace_id"`
	ChatID       string                    `json:"chat_id"`
	MissionID    string                    `json:"mission_id,omitempty"` // optional — mission this assignment belongs to; threaded into journal entries so Cartographer checkpoints can anchor on per-mission journal cursors.
	CrewMembers  []orchestrator.CrewMember `json:"-"`                    // populated internally for mission dispatches
	LeadPlanning bool                      `json:"-"`                    // when true, run as LEAD with sidecar
}

// Create handles POST /api/v1/internal/assignments.
// Called by the sidecar when a lead agent invokes `curl localhost:9119/assign`.

func (h *AssignmentHandler) Create(w http.ResponseWriter, r *http.Request) {
	var body createAssignmentBody
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid JSON"})
		return
	}
	if body.TargetSlug == "" || body.Task == "" || body.CrewID == "" || body.WorkspaceID == "" || body.ChatID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "target_slug, task, crew_id, workspace_id, chat_id required",
		})
		return
	}

	// Look up the assigning agent from the parent chat
	var assignedByID string
	err := h.db.QueryRowContext(r.Context(), `SELECT agent_id FROM chats WHERE id = ?`, body.ChatID).Scan(&assignedByID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "chat not found"})
			return
		}
		h.logger.Error("lookup chat for assignment", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	// Cross-crew connection check: if the assigning agent's crew differs
	// from the target crew, verify an active crew connection exists.
	var assignerCrewID string
	if err := h.db.QueryRowContext(r.Context(),
		`SELECT a.crew_id FROM agents a JOIN chats ch ON ch.agent_id = a.id WHERE ch.id = ?`,
		body.ChatID).Scan(&assignerCrewID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// Cannot determine the assigner's crew; deny cross-crew assignments.
			h.logger.Warn("could not resolve assigner crew from chat", "chat_id", body.ChatID)
			writeJSON(w, http.StatusForbidden, map[string]string{
				"error": "cannot verify crew connection — assigner crew not found",
			})
			return
		}
		h.logger.Error("lookup assigner crew for connection check", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	if assignerCrewID != body.CrewID {
		connected, connErr := AreCrewsConnected(r.Context(), h.db, assignerCrewID, body.CrewID)
		if connErr != nil {
			h.logger.Error("check crew connection for assignment", "error", connErr)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
			return
		}
		if !connected {
			writeJSON(w, http.StatusForbidden, map[string]string{
				"error": "crews are not connected — create a crew connection first",
			})
			return
		}
	}

	// Look up the target agent by slug + crew_id
	var target targetAgentInfo
	err = h.db.QueryRowContext(r.Context(), `
		SELECT a.id, a.slug, a.name, COALESCE(a.role_title,''), COALESCE(a.system_prompt,''),
		       a.cli_adapter, COALESCE(a.llm_model,''), a.tool_profile, a.timeout_seconds, a.memory_enabled, c.slug
		FROM agents a
		JOIN crews c ON c.id = a.crew_id
		WHERE a.slug = ? AND a.crew_id = ? AND a.deleted_at IS NULL
	`, body.TargetSlug, body.CrewID).Scan(
		&target.ID, &target.Slug, &target.Name, &target.RoleTitle,
		&target.SystemPrompt, &target.CLIAdapter, &target.LLMModel,
		&target.ToolProfile, &target.TimeoutSeconds, &target.MemoryEnabled, &target.CrewSlug,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "target agent not found"})
			return
		}
		h.logger.Error("lookup target agent", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	// Load target agent credentials
	creds, err := h.loadAgentCredentials(r.Context(), target.ID)
	if err != nil {
		h.logger.Error("load agent credentials", "agent_id", target.ID, "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	// Create assignment record in PENDING state (group_id = chat_id for mission linkage)
	assignmentID := generateCUID()
	now := time.Now().UTC().Format(time.RFC3339)
	_, err = h.db.ExecContext(r.Context(), `
		INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status, group_id, created_at)
		VALUES (?, ?, ?, ?, ?, ?, 'PENDING', ?, ?)
	`, assignmentID, body.WorkspaceID, body.ChatID, assignedByID, target.ID, body.Task, body.ChatID, now)
	if err != nil {
		h.logger.Error("create assignment", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	// Broadcast assignment_created event to the session channel
	broadcastChannelEvent(h.hub, "session", body.ChatID, "assignment_created",
		map[string]string{
			"id":     assignmentID,
			"target": body.TargetSlug,
			"task":   body.Task,
		})

	// Mirror to the journal so the assignment lifecycle (created →
	// running → completed/failed) shows up in the unified Timeline.
	// Severity stays at info because creation is routine — escalate to
	// warn/error only on failure terminal states.
	taskPreviewForSummary := body.Task
	if len(taskPreviewForSummary) > 120 {
		taskPreviewForSummary = taskPreviewForSummary[:120] + "…"
	}
	// MissionID intentionally NOT set — body.ChatID is a chat session
	// id, which only sometimes corresponds to a row in `missions`
	// (group_id linkage). Setting it would FK-fail under tests + any
	// non-mission assignment. chat_id lives in payload + refs instead.
	if _, jerr := h.journal.Emit(r.Context(), journal.Entry{
		WorkspaceID: body.WorkspaceID,
		CrewID:      body.CrewID,
		AgentID:     target.ID,
		Type:        journal.EntryAssignmentCreate,
		Severity:    journal.SeverityInfo,
		ActorType:   journal.ActorAgent,
		ActorID:     assignedByID,
		Summary:     fmt.Sprintf("assigned %s → %s: %s", body.TargetSlug, target.Name, taskPreviewForSummary),
		Payload: map[string]any{
			"assignment_id": assignmentID,
			"chat_id":       body.ChatID,
			"target_slug":   body.TargetSlug,
			"target_id":     target.ID,
			"task":          body.Task,
		},
		Refs: map[string]any{"assignment_id": assignmentID, "chat_id": body.ChatID},
	}); jerr != nil {
		h.logger.Warn("assignment journal emit (create) failed", "error", jerr, "assignment_id", assignmentID)
	}

	h.logger.Info("assignment created",
		"assignment_id", assignmentID,
		"target", body.TargetSlug,
		"crew_id", body.CrewID,
	)

	// Post comment, update assignee, and log activity on the linked issue
	var missionExists int
	_ = h.db.QueryRowContext(r.Context(), `SELECT 1 FROM missions WHERE id = ?`, body.ChatID).Scan(&missionExists)
	if missionExists == 1 {
		var assignerName string
		_ = h.db.QueryRowContext(r.Context(), `SELECT name FROM agents WHERE id = ?`, assignedByID).Scan(&assignerName)
		if assignerName == "" {
			assignerName = "Lead"
		}

		taskPreview := body.Task
		if len(taskPreview) > 300 {
			taskPreview = taskPreview[:300] + "..."
		}

		// Comment
		commentID := generateCUID()
		commentBody := fmt.Sprintf("**%s assigned work to %s**\n\n%s", assignerName, target.Name, taskPreview)
		_, _ = h.db.ExecContext(r.Context(),
			`INSERT INTO mission_comments (id, mission_id, author_type, author_id, body, created_at, updated_at) VALUES (?, ?, 'agent', ?, ?, ?, ?)`,
			commentID, body.ChatID, assignedByID, commentBody, now, now)

		// Mirror the comment into the journal so mission discussion
		// shows up in the unified Timeline alongside operational
		// events. Mission comments today are sparse (auto-generated
		// "X assigned work to Y") so volume is bounded.
		commentSummary := commentBody
		if len(commentSummary) > 200 {
			commentSummary = commentSummary[:200] + "…"
		}
		// Mission comment emit lands under the existing missions row
		// (we already verified `missionExists == 1` above), so passing
		// MissionID is safe here. Other branches in this file
		// deliberately skip MissionID because their ChatID is not
		// guaranteed to have a corresponding missions row.
		if _, jerr := h.journal.Emit(r.Context(), journal.Entry{
			WorkspaceID: body.WorkspaceID,
			AgentID:     assignedByID,
			MissionID:   body.ChatID,
			Type:        journal.EntryMissionComment,
			Severity:    journal.SeverityInfo,
			ActorType:   journal.ActorAgent,
			ActorID:     assignedByID,
			Summary:     commentSummary,
			Payload: map[string]any{
				"comment_id":  commentID,
				"mission_id":  body.ChatID,
				"author_name": assignerName,
				"target_name": target.Name,
				"target_slug": target.Slug,
				"body":        commentBody,
			},
			Refs: map[string]any{"comment_id": commentID, "mission_id": body.ChatID},
		}); jerr != nil {
			h.logger.Warn("mission comment journal emit failed", "error", jerr, "comment_id", commentID)
		}

		// Update assignee on the issue to the target agent
		_, _ = h.db.ExecContext(r.Context(),
			`UPDATE missions SET assignee_id = ?, assignee_type = 'agent', updated_at = ? WHERE id = ?`,
			target.ID, now, body.ChatID)

		// Activity
		activityID := generateCUID()
		_, _ = h.db.ExecContext(r.Context(),
			`INSERT INTO mission_activity (id, mission_id, actor_type, actor_id, action, details, created_at) VALUES (?, ?, 'agent', ?, ?, ?, ?)`,
			activityID, body.ChatID, assignedByID, "assignee_changed",
			fmt.Sprintf("%s assigned to %s (@%s)", assignerName, target.Name, target.Slug), now)

		// Broadcast update
		if h.hub != nil {
			h.hub.Broadcast("workspace:"+body.WorkspaceID, ws.ServerMessage{
				Type: "issue.updated", Channel: "workspace:" + body.WorkspaceID,
				Payload: map[string]string{"id": body.ChatID, "assignee": target.Slug},
			})
		}
	}

	// Run the sub-agent asynchronously
	go h.runAssignment(context.Background(), assignmentID, body, target, creds)

	writeJSON(w, http.StatusCreated, map[string]string{
		"assignment_id": assignmentID,
		"status":        "PENDING",
	})
}

// targetAgentInfo holds the agent fields needed to run an assignment.

func (h *AssignmentHandler) runAssignment(
	ctx context.Context,
	assignmentID string,
	body createAssignmentBody,
	target targetAgentInfo,
	creds []orchestrator.Credential,
) {
	now := time.Now().UTC().Format(time.RFC3339)

	// Record the sub-agent run via the journal — this is the source of
	// truth for "run started" post Phase J. trace_id == runID groups
	// every entry the assignment will produce.
	runID := generateCUID()
	if _, err := h.journal.Emit(ctx, journal.Entry{
		WorkspaceID: body.WorkspaceID,
		AgentID:     target.ID,
		Type:        journal.EntryRunStarted,
		Severity:    journal.SeverityInfo,
		ActorType:   journal.ActorOrchestrator,
		Summary:     fmt.Sprintf("run %s started (assignment)", shortRunID(runID)),
		Payload: map[string]any{
			"trigger_type":     "ASSIGNMENT",
			"chat_id":          body.ChatID,
			"assignment_id":    assignmentID,
			"assigned_by_chat": body.ChatID,
			"target_slug":      body.TargetSlug,
		},
		Refs:    map[string]any{"assignment_id": assignmentID},
		TraceID: runID,
	}); err != nil {
		h.logger.Error("create run record for assignment", "error", err, "assignment_id", assignmentID)
		runID = "" // prevent finishAssignment from emitting a terminal entry
	}

	// Stamp the run id onto ctx so downstream journal emits inside this
	// assignment (LLM calls, exec, network egress, etc.) inherit the
	// same trace_id without each callsite having to pass runID by hand.
	if runID != "" {
		ctx = journal.WithRunID(ctx, runID)
	}

	// Mark assignment as RUNNING
	if _, err := h.db.ExecContext(ctx,
		`UPDATE assignments SET status='RUNNING', started_at=? WHERE id=?`, now, assignmentID); err != nil {
		h.logger.Error("update assignment to running", "error", err, "assignment_id", assignmentID)
	}
	// Mirror RUNNING to the journal so the Timeline records the
	// kick-off. Without this the gap between "created" and the first
	// exec.command can be seconds-to-minutes (image pull, container
	// boot) and the user has no signal that work is actually happening.
	// As with assignment.created above, omit MissionID to avoid the FK
	// failure when body.ChatID is a chat session that has no missions
	// row. trace_id ties the entry back to the run; chat_id lives in
	// payload + refs.
	if _, jerr := h.journal.Emit(ctx, journal.Entry{
		WorkspaceID: body.WorkspaceID,
		AgentID:     target.ID,
		Type:        journal.EntryAssignmentRun,
		Severity:    journal.SeverityInfo,
		ActorType:   journal.ActorOrchestrator,
		Summary:     fmt.Sprintf("assignment %s running on %s", shortRunID(assignmentID), body.TargetSlug),
		Payload: map[string]any{
			"assignment_id": assignmentID,
			"target_slug":   body.TargetSlug,
			"target_id":     target.ID,
			"started_at":    now,
		},
		Refs:    map[string]any{"assignment_id": assignmentID, "chat_id": body.ChatID},
		TraceID: runID,
	}); jerr != nil {
		h.logger.Warn("assignment journal emit (running) failed", "error", jerr, "assignment_id", assignmentID)
	}
	broadcastChannelEvent(h.hub, "session", body.ChatID, "assignment_running",
		map[string]string{
			"id":     assignmentID,
			"target": body.TargetSlug,
		})
	broadcastWorkspaceEvent(h.hub, body.WorkspaceID, "assignment.updated",
		map[string]string{
			"id":     assignmentID,
			"status": "RUNNING",
			"target": body.TargetSlug,
		})

	if h.orch == nil {
		h.finishAssignment(ctx, assignmentID, runID, body.ChatID, body.TargetSlug, body.WorkspaceID, "", "orchestrator not available")
		return
	}

	// Ensure crew container is running
	containerID, err := h.orch.GetOrCreateContainer(ctx, target.CrewSlug, body.CrewID, body.WorkspaceID)
	if err != nil {
		h.logger.Error("get container for assignment", "error", err, "assignment_id", assignmentID)
		h.finishAssignment(ctx, assignmentID, runID, body.ChatID, body.TargetSlug, body.WorkspaceID, "",
			fmt.Sprintf("container error: %v", err))
		return
	}

	// Collect agent output
	var outputParts []string
	handler := func(event orchestrator.AgentEvent) {
		if event.Type == "text" && event.Content != "" {
			outputParts = append(outputParts, event.Content)
		}
	}

	agentRole := "AGENT"
	skipSidecar := true
	if body.LeadPlanning {
		agentRole = "LEAD" // Lead planning: full LEAD privileges with sidecar
		skipSidecar = false
	}

	req := orchestrator.AgentRunRequest{
		AgentID:         target.ID,
		AgentSlug:       target.Slug,
		AgentRole:       agentRole,
		CrewID:          body.CrewID,
		CrewSlug:        target.CrewSlug,
		MissionID:       body.MissionID,
		WorkspaceID:     body.WorkspaceID,
		ChatID:          body.ChatID,
		ContainerID:     containerID,
		CLIAdapter:      target.CLIAdapter,
		LLMModel:        target.LLMModel,
		SystemPrompt:    target.SystemPrompt,
		UserMessage:     body.Task,
		ToolProfile:     target.ToolProfile,
		Credentials:     creds,
		TimeoutSecs:     target.TimeoutSeconds,
		MemoryEnabled:   target.MemoryEnabled,
		CrewMembers:     body.CrewMembers,
		SkipSidecar:     skipSidecar,
		SkipConvHistory: true,
	}

	// Load workspace language preference
	var lang string
	_ = h.db.QueryRowContext(ctx,
		`SELECT COALESCE(preferred_language, '') FROM workspaces WHERE id = ?`,
		body.WorkspaceID).Scan(&lang)
	req.PreferredLanguage = lang

	// Refuse the run if a backup currently holds the workspace's
	// advisory lock. Otherwise this execution would write agent
	// state that the in-flight dump has already passed and therefore
	// miss from the bundle.
	guardRelease, guardErr := refuseIfBackupInProgress(ctx, h.db, body.WorkspaceID)
	if guardErr != nil {
		h.logger.Warn("assignment refused — backup in progress", "assignment_id", assignmentID, "workspace_id", body.WorkspaceID)
		h.finishAssignment(ctx, assignmentID, runID, body.ChatID, body.TargetSlug, body.WorkspaceID, "", guardErr.Error())
		return
	}
	defer guardRelease()

	if err := h.orch.RunAgentForAssignment(ctx, req, handler); err != nil {
		h.logger.Error("assignment execution failed", "error", err, "assignment_id", assignmentID)
		h.finishAssignment(ctx, assignmentID, runID, body.ChatID, body.TargetSlug, body.WorkspaceID, "",
			fmt.Sprintf("execution error: %v", err))
		return
	}

	// Build result from collected output (cap at 10k chars)
	result := ""
	for _, part := range outputParts {
		result += part
	}
	if len(result) > 10000 {
		result = result[:10000] + "...(truncated)"
	}

	h.finishAssignment(ctx, assignmentID, runID, body.ChatID, body.TargetSlug, body.WorkspaceID, result, "")
}

// finishAssignment updates the assignment and run records, then broadcasts the final event.

func (h *AssignmentHandler) finishAssignment(
	ctx context.Context,
	assignmentID, runID, chatID, targetSlug, workspaceID, result, errMsg string,
) {
	now := time.Now().UTC().Format(time.RFC3339)
	status := "COMPLETED"
	if errMsg != "" {
		status = "FAILED"
	}

	var resultVal, errVal interface{}
	if result != "" {
		resultVal = result
	}
	if errMsg != "" {
		errVal = errMsg
	}

	if _, err := h.db.ExecContext(ctx,
		`UPDATE assignments SET status=?, result_summary=?, error_message=?, finished_at=? WHERE id=?`,
		status, resultVal, errVal, now, assignmentID); err != nil {
		h.logger.Error("update assignment status", "error", err, "assignment_id", assignmentID)
	}

	// Emit the terminal run.* journal entry — the source of truth post
	// Phase J. trace_id == runID joins it with the run.started entry.
	if runID != "" {
		entryType := terminalEntryType(status)
		severity := journal.SeverityInfo
		if status == "FAILED" {
			severity = journal.SeverityError
		}
		payload := map[string]any{}
		if errMsg != "" {
			payload["error_message"] = errMsg
		}
		if status == "COMPLETED" {
			payload["exit_code"] = 0
		}
		if _, err := h.journal.Emit(ctx, journal.Entry{
			WorkspaceID: workspaceID,
			Type:        entryType,
			Severity:    severity,
			ActorType:   journal.ActorOrchestrator,
			Summary:     fmt.Sprintf("run %s %s (assignment)", shortRunID(runID), entryType[len("run."):]),
			Payload:     payload,
			Refs:        map[string]any{"assignment_id": assignmentID},
			TraceID:     runID,
		}); err != nil {
			h.logger.Error("emit terminal run entry for assignment", "error", err, "run_id", runID)
		}
	}

	// Notify MissionEngine first — must run regardless of websocket availability
	if h.missionCallback != nil {
		if err := h.missionCallback.OnAssignmentCompleted(ctx, assignmentID, status, result, errMsg); err != nil {
			h.logger.Error("mission callback failed", "error", err, "assignment_id", assignmentID)
		}
	}

	// Post completion comment for assignments linked to a mission (via group_id or chat_id).
	// This covers sub-agent assignments from sidecar /assign and lead planning assignments.
	// Unique to feat/code-quality (commit 686c6c2) — main does not post these comments.
	{
		var groupID sql.NullString
		var agentID string
		_ = h.db.QueryRowContext(ctx,
			`SELECT COALESCE(group_id, chat_id), assigned_to_id FROM assignments WHERE id = ?`,
			assignmentID).Scan(&groupID, &agentID)
		if groupID.Valid && groupID.String != "" {
			var missionExists, hasLinkedTask int
			_ = h.db.QueryRowContext(ctx, `SELECT 1 FROM missions WHERE id = ?`, groupID.String).Scan(&missionExists)
			_ = h.db.QueryRowContext(ctx, `SELECT 1 FROM mission_tasks WHERE assignment_id = ?`, assignmentID).Scan(&hasLinkedTask)

			// Only post if this is mission-linked and NOT already handled by OnAssignmentCompleted (which handles mission_tasks)
			if missionExists == 1 && hasLinkedTask == 0 {
				var agentName string
				_ = h.db.QueryRowContext(ctx, `SELECT name FROM agents WHERE id = ?`, agentID).Scan(&agentName)

				var commentBody string
				if errMsg != "" {
					commentBody = fmt.Sprintf("**%s encountered an issue.** Error: %s", agentName, errMsg)
				} else if result != "" {
					handoff := orchestrator.ParseHandoff(result)
					if handoff.Parsed && handoff.Summary != "" {
						commentBody = fmt.Sprintf("**%s completed their work** (confidence: %s)\n\n%s", agentName, handoff.Confidence, handoff.Summary)
						if handoff.Artifacts != "" {
							commentBody += "\n\n**Artifacts:** " + handoff.Artifacts
						}
					} else {
						summary := result
						if len(summary) > 500 {
							summary = summary[:500] + "..."
						}
						commentBody = fmt.Sprintf("**%s completed their work**\n\n%s", agentName, summary)
					}
				}
				if commentBody != "" {
					cid := generateCUID()
					_, _ = h.db.ExecContext(ctx,
						`INSERT INTO mission_comments (id, mission_id, author_type, author_id, body, created_at, updated_at) VALUES (?, ?, 'agent', ?, ?, ?, ?)`,
						cid, groupID.String, agentID, commentBody, now, now)
					aid := generateCUID()
					action := "task_completed"
					if errMsg != "" {
						action = "task_failed"
					}
					_, _ = h.db.ExecContext(ctx,
						`INSERT INTO mission_activity (id, mission_id, actor_type, actor_id, action, details, created_at) VALUES (?, ?, 'agent', ?, ?, ?, ?)`,
						aid, groupID.String, agentID, action, fmt.Sprintf("%s finished work", agentName), now)
				}
			}
		}
	}

	if h.hub == nil {
		return
	}

	if errMsg != "" {
		broadcastChannelEvent(h.hub, "session", chatID, "assignment_failed",
			map[string]string{
				"id":     assignmentID,
				"target": targetSlug,
				"error":  errMsg,
			})
	} else {
		broadcastChannelEvent(h.hub, "session", chatID, "assignment_completed",
			map[string]string{
				"id":     assignmentID,
				"target": targetSlug,
				"result": result,
			})
	}

	// Broadcast to workspace channel for real-time dashboard updates
	if workspaceID != "" {
		broadcastWorkspaceEvent(h.hub, workspaceID, "assignment.updated",
			map[string]string{
				"id":     assignmentID,
				"status": status,
				"target": targetSlug,
			})
	}

	h.logger.Info("assignment finished", "assignment_id", assignmentID, "status", status)
}

// List handles GET /api/v1/crews/{crewId}/assignments.
// Returns all assignments for agents in this crew, sorted by created_at DESC.
