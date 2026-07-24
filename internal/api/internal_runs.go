package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/featureflags"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/runverdict"
)

// runVerdictFlagKey is the feature_flags row seeded by migration v164
// (migrate_consts_v164_run_verdict_flag.go) — gates the post-run
// outcome verdict (#1403) per workspace.
const runVerdictFlagKey = "run_verdict_summaries"

// CreateRun records a new agent run started by the sidecar.
// POST /api/v1/internal/runs
func (h *InternalHandler) CreateRun(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID          string          `json:"id"`
		AgentID     string          `json:"agent_id"`
		ChatID      string          `json:"chat_id"`
		WorkspaceID string          `json:"workspace_id"`
		TriggerType string          `json:"trigger_type"`
		Metadata    json.RawMessage `json:"metadata"`
	}
	if err := readJSON(r, &body); err != nil {
		replyError(w, http.StatusBadRequest, "Invalid JSON")
		return
	}
	if body.ID == "" || body.AgentID == "" || body.WorkspaceID == "" {
		replyError(w, http.StatusBadRequest, "id, agent_id, workspace_id required")
		return
	}
	if body.TriggerType == "" {
		body.TriggerType = "USER"
	}

	// Tenancy guard: the internal token authenticates the *sidecar*, not a
	// workspace. A caller could otherwise post their own workspace_id with
	// another workspace's agent_id and mutate that agent. Confirm the agent
	// actually belongs to the claimed workspace BEFORE emitting any journal
	// entry or flipping status — see proxy.go's `WHERE id=? AND workspace_id=?`.
	var agentWorkspaceID string
	switch err := h.db.QueryRowContext(r.Context(),
		"SELECT workspace_id FROM agents WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL",
		body.AgentID, body.WorkspaceID).Scan(&agentWorkspaceID); {
	case err == sql.ErrNoRows:
		replyError(w, http.StatusNotFound, "Agent not found in workspace")
		return
	case err != nil:
		h.logger.Error("create run: agent workspace check", "error", err, "agent_id", body.AgentID)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)

	// Emit run.started FIRST — this is the source of truth for runs
	// (post-J migration). If we flip the agent to RUNNING before the
	// journal entry is durable and the emit then fails, the agent is
	// stuck in RUNNING with no trace anywhere; nothing in the recovery
	// loop knows about it.
	{
		payload := map[string]any{"trigger_type": body.TriggerType}
		if body.ChatID != "" {
			payload["chat_id"] = body.ChatID
		}
		if body.Metadata != nil {
			var md map[string]any
			if err := json.Unmarshal(body.Metadata, &md); err == nil && md != nil {
				payload["metadata"] = md
			}
		}
		if _, err := h.journal.Emit(r.Context(), journal.Entry{
			WorkspaceID: body.WorkspaceID,
			AgentID:     body.AgentID,
			Type:        journal.EntryRunStarted,
			Severity:    journal.SeverityInfo,
			ActorType:   journal.ActorSidecar,
			Summary:     fmt.Sprintf("run %s started", shortRunID(body.ID)),
			Payload:     payload,
			TraceID:     body.ID,
		}); err != nil {
			replyInternalError(w, h.logger, "create run: emit run.started", err)
			return
		}
	}

	// Now flip the agent to RUNNING. Failure is debug-only because the
	// run trace already exists — recoverOrphanedRuns will clean up.
	if _, err := h.db.ExecContext(r.Context(),
		"UPDATE agents SET status = 'RUNNING', updated_at = ? WHERE id = ? AND workspace_id = ?", now, body.AgentID, body.WorkspaceID); err != nil {
		h.logger.Debug("update agent status on run create", "error", err, "agent_id", body.AgentID)
	}

	// Broadcast real-time events
	if h.hub != nil {
		var agentName string
		if err := h.db.QueryRowContext(r.Context(), "SELECT name FROM agents WHERE id = ?", body.AgentID).Scan(&agentName); err != nil {
			h.logger.Debug("fetch agent name for broadcast", "error", err, "agent_id", body.AgentID)
		}

		broadcastWorkspaceEvent(h.hub, body.WorkspaceID, "run.started",
			map[string]string{
				"run_id":     body.ID,
				"agent_id":   body.AgentID,
				"agent_name": agentName,
				"status":     "RUNNING",
			})
		broadcastWorkspaceEvent(h.hub, body.WorkspaceID, "agent.status",
			map[string]string{
				"agent_id":   body.AgentID,
				"agent_name": agentName,
				"status":     "RUNNING",
			})
	}

	writeJSON(w, http.StatusCreated, map[string]string{"id": body.ID, "status": "RUNNING"})
}

// UpdateRun records the terminal state of an agent run by emitting
// the matching run.* journal entry. Also refreshes the agent's status
// and broadcasts real-time events for the dashboard.
// PATCH /api/v1/internal/runs/{runId}
//
// Post Phase J of unified-journal: there is no agent_runs row to UPDATE;
// the run is fully reconstructed from journal entries grouped by
// trace_id (== runID). Workspace + agent context is read from the
// run.started entry that was previously emitted by CreateRun.
func (h *InternalHandler) UpdateRun(w http.ResponseWriter, r *http.Request) {
	runID := r.PathValue("runId")
	var body struct {
		Status       string          `json:"status"`
		ExitCode     *int            `json:"exit_code"`
		ErrorMessage *string         `json:"error_message"`
		Metadata     json.RawMessage `json:"metadata"`
	}
	if err := readJSON(r, &body); err != nil {
		replyError(w, http.StatusBadRequest, "Invalid JSON")
		return
	}

	validStatuses := map[string]bool{
		"RUNNING": true, "COMPLETED": true, "FAILED": true, "CANCELLED": true, "TIMEOUT": true,
	}
	if !validStatuses[body.Status] {
		replyError(w, http.StatusBadRequest, "Invalid status")
		return
	}

	terminal := map[string]bool{"COMPLETED": true, "FAILED": true, "CANCELLED": true, "TIMEOUT": true}
	if !terminal[body.Status] {
		// Non-terminal updates (i.e. status=RUNNING) are no-ops post-J:
		// the run is already RUNNING from CreateRun's run.started entry.
		writeJSON(w, http.StatusOK, map[string]string{"id": runID, "status": body.Status})
		return
	}

	// Look up workspace_id + agent_id from the run.started journal entry
	// belonging to this trace. Without this we can't broadcast events.
	var agentID, workspaceID string
	var agentName sql.NullString
	if err := h.db.QueryRowContext(r.Context(),
		`SELECT je.workspace_id, je.agent_id, a.name
		 FROM journal_entries je
		 LEFT JOIN agents a ON a.id = je.agent_id
		 WHERE je.trace_id = ? AND je.entry_type = 'run.started'
		 LIMIT 1`, runID,
	).Scan(&workspaceID, &agentID, &agentName); err != nil {
		if err == sql.ErrNoRows {
			replyError(w, http.StatusNotFound, "run not found")
			return
		}
		h.logger.Error("update run: lookup", "error", err, "run_id", runID)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	// Tenant scope (PR-F24 F-5). The run's owning workspace comes from
	// its run.started journal entry, not from the request. A bound token
	// may only finalize runs in its own workspace; otherwise an agent
	// that enumerated a run id could flip a foreign tenant's run status
	// (and trip its consolidator / dashboard events). Master-token
	// (host-side) callers have an empty scope and are unaffected. 404,
	// not 403, so we don't confirm the run exists in another tenant.
	if scope := InternalTokenWorkspaceFromContext(r.Context()); scope != "" && scope != workspaceID {
		replyError(w, http.StatusNotFound, "run not found")
		return
	}

	// Idempotency guard: if a terminal run.* entry already exists for this
	// trace, treat the call as a no-op success. Sidecar retries (network
	// blip, 503 retry) would otherwise append duplicate run.completed/
	// run.failed/... rows, polluting the timeline and double-counting the
	// run in KPIs.
	var existingTerminal sql.NullString
	if err := h.db.QueryRowContext(r.Context(),
		`SELECT entry_type FROM journal_entries
		 WHERE trace_id = ? AND entry_type IN ('run.completed','run.failed','run.cancelled','run.timeout')
		 LIMIT 1`, runID,
	).Scan(&existingTerminal); err == nil && existingTerminal.Valid {
		// Already terminal — acknowledge with the already-recorded status
		// rather than the new one, so retries don't appear to "succeed"
		// at flipping a finished run.
		statusFromEntry := strings.ToUpper(strings.TrimPrefix(existingTerminal.String, "run."))
		writeJSON(w, http.StatusOK, map[string]string{"id": runID, "status": statusFromEntry})
		return
	} else if err != nil && err != sql.ErrNoRows {
		h.logger.Error("update run: terminal-exists check", "error", err, "run_id", runID)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	// Emit the terminal journal entry. This is the source-of-truth write.
	entryType := terminalEntryType(body.Status)
	severity := journal.SeverityInfo
	if body.Status == "FAILED" || body.Status == "TIMEOUT" {
		severity = journal.SeverityError
	}
	payload := map[string]any{}
	if body.ExitCode != nil {
		payload["exit_code"] = *body.ExitCode
	}
	if body.ErrorMessage != nil && *body.ErrorMessage != "" {
		payload["error_message"] = *body.ErrorMessage
	}
	if body.Metadata != nil {
		var md map[string]any
		if err := json.Unmarshal(body.Metadata, &md); err == nil && md != nil {
			payload["metadata"] = md
		}
	}
	if _, err := h.journal.Emit(r.Context(), journal.Entry{
		WorkspaceID: workspaceID,
		AgentID:     agentID,
		Type:        entryType,
		Severity:    severity,
		ActorType:   journal.ActorSidecar,
		Summary:     fmt.Sprintf("run %s %s", shortRunID(runID), entryType[len("run."):]),
		Payload:     payload,
		TraceID:     runID,
	}); err != nil {
		h.logger.Error("update run: emit terminal", "error", err, "run_id", runID)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	// Post-run outcome verdict (#1403): fire-and-forget, same pattern as
	// the sleep-time consolidator trigger below — never blocks the
	// response, never fails the run it narrates. Skipped for CANCELLED
	// (a user-aborted run has no goal outcome to assess) and whenever
	// the run_summary aux slot has no buildable provider (SetRunVerdict
	// wired nil) or the workspace has the feature flag off.
	if body.Status != "CANCELLED" && h.runVerdictProvider != nil {
		h.verdictWG.Add(1)
		go func() {
			defer h.verdictWG.Done()
			h.generateRunVerdict(context.Background(), workspaceID, agentID, runID)
		}()
	}

	// Refresh the agent's atomic status. RUNNING when another run for
	// the same agent is still active (no terminal yet for that trace),
	// IDLE / ERROR otherwise. The subquery counts active runs in the
	// journal: traces with a run.started but no terminal entry.
	now := time.Now().UTC().Format(time.RFC3339)
	failedStatus := "IDLE"
	if body.Status == "FAILED" || body.Status == "TIMEOUT" {
		failedStatus = "ERROR"
	}
	agentStatus := failedStatus
	if agentID != "" {
		if _, err := h.db.ExecContext(r.Context(), `
			UPDATE agents SET status = CASE
				WHEN (
					SELECT COUNT(DISTINCT je1.trace_id)
					FROM journal_entries je1
					WHERE je1.workspace_id = ?
					  AND je1.agent_id = ?
					  AND je1.entry_type = 'run.started'
					  AND je1.trace_id != ?
					  AND NOT EXISTS (
					    SELECT 1 FROM journal_entries je2
					    WHERE je2.workspace_id = je1.workspace_id
					      AND je2.trace_id = je1.trace_id
					      AND je2.entry_type IN ('run.completed','run.failed','run.cancelled','run.timeout')
					  )
				) > 0 THEN 'RUNNING'
				ELSE ?
			END, updated_at = ? WHERE id = ?`,
			workspaceID, agentID, runID, failedStatus, now, agentID); err != nil {
			h.logger.Debug("update agent status on run completion", "error", err, "agent_id", agentID)
		}
		var readBack string
		if err := h.db.QueryRowContext(r.Context(), "SELECT status FROM agents WHERE id = ?", agentID).Scan(&readBack); err == nil {
			agentStatus = readBack
		}
	}

	// Sleep-time trigger: when an agent run completes successfully,
	// notify the post-run consolidator trigger so it can fire the
	// extraction pass while the agent is between tasks (PRD §8.1).
	// Only on COMPLETED — failed / cancelled / timeout runs don't
	// produce stable signal for consolidation, and triggering on
	// them would just generate noisy proposals.
	//
	// The trigger does its own per-(workspace, crew) debouncing and
	// fires the consolidation pass asynchronously, so this call
	// never blocks the response. agentID → crew_id lookup is a tiny
	// extra SELECT, but only happens once per run completion.
	if body.Status == "COMPLETED" && h.postRunTrigger != nil && agentID != "" {
		var crewID, crewSlug sql.NullString
		if err := h.db.QueryRowContext(r.Context(), `
			SELECT c.id, c.slug
			FROM agents a
			LEFT JOIN crews c ON c.id = a.crew_id
			WHERE a.id = ?`, agentID).Scan(&crewID, &crewSlug); err != nil {
			h.logger.Debug("post-run trigger: agent → crew lookup",
				"error", err, "agent_id", agentID)
		} else if crewID.Valid && crewSlug.Valid {
			// Fire on a fresh background context so the trigger
			// outlives the HTTP request that birthed it; the
			// trigger itself spawns a goroutine for the actual
			// consolidator run.
			h.postRunTrigger.OnRunCompleted(context.Background(),
				workspaceID, crewID.String, crewSlug.String)
		}
	}

	// Broadcast — same event names and payloads the dashboard already
	// listens to, so frontends don't need rewiring.
	eventType := "run.completed"
	if body.Status == "FAILED" || body.Status == "CANCELLED" || body.Status == "TIMEOUT" {
		eventType = "run.failed"
	}
	broadcastWorkspaceEvent(h.hub, workspaceID, eventType,
		map[string]string{
			"run_id":     runID,
			"agent_id":   agentID,
			"agent_name": agentName.String,
			"status":     body.Status,
		})
	broadcastWorkspaceEvent(h.hub, workspaceID, "agent.status",
		map[string]string{
			"agent_id":   agentID,
			"agent_name": agentName.String,
			"status":     agentStatus,
		})

	writeJSON(w, http.StatusOK, map[string]string{"id": runID, "status": body.Status})
}

// generateRunVerdict fetches the run's journal entries and asks
// runverdict to produce+emit an outcome verdict (#1403). Called on a
// background context (outlives the HTTP request that spawned it, same
// pattern as OnRunCompleted below); swallows its own errors at Debug
// level since a verdict is a best-effort narrative aid, never allowed
// to surface as a caller-visible failure.
func (h *InternalHandler) generateRunVerdict(ctx context.Context, workspaceID, agentID, runID string) {
	enabled, err := featureflags.IsEnabled(ctx, h.db, workspaceID, runVerdictFlagKey)
	if err != nil {
		h.logger.Debug("run verdict: feature flag check", "error", err, "run_id", runID)
		return
	}
	if !enabled {
		return
	}

	// Emit queues entries for async background write (internal/journal
	// emit.go's Writer.Emit returns as soon as the entry is queued, not
	// once it's durable). The terminal entry this goroutine was spawned
	// right after may not have hit h.db yet — force the drain before
	// reading it back, or the entry count check below can under-count
	// and skip a run that genuinely has activity.
	if err := h.journal.Flush(ctx); err != nil {
		h.logger.Debug("run verdict: flush before read", "error", err, "run_id", runID)
	}

	entries, _, err := journal.List(ctx, h.db, journal.Query{WorkspaceID: workspaceID, TraceID: runID, Limit: 500})
	if err != nil {
		h.logger.Debug("run verdict: fetch entries", "error", err, "run_id", runID)
		return
	}

	var crewID sql.NullString
	if agentID != "" {
		if err := h.db.QueryRowContext(ctx, "SELECT crew_id FROM agents WHERE id = ?", agentID).Scan(&crewID); err != nil {
			h.logger.Debug("run verdict: crew lookup", "error", err, "agent_id", agentID)
		}
	}

	base := journal.Entry{
		WorkspaceID: workspaceID,
		CrewID:      crewID.String,
		AgentID:     agentID,
		TraceID:     runID,
	}
	if err := runverdict.GenerateAndEmit(ctx, h.journal, h.runVerdictProvider, h.runVerdictModel, base, entries); err != nil {
		h.logger.Debug("run verdict: generate", "error", err, "run_id", runID)
	}
	// Same async-queue caveat as above: drain the verdict entry itself
	// so it's immediately visible to readers (UI polling right after
	// this goroutine completes, tests reading h.db directly).
	if err := h.journal.Flush(ctx); err != nil {
		h.logger.Debug("run verdict: flush after emit", "error", err, "run_id", runID)
	}
}

// shortRunID returns the first 8 characters of a run id for use in
// human-readable summaries; falls back to the full id when shorter.
func shortRunID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}

// terminalEntryType maps an agent_runs.status (legacy enum) to the
// equivalent journal EntryType. Caller is expected to have already
// checked the status is terminal.
func terminalEntryType(status string) journal.EntryType {
	switch status {
	case "COMPLETED":
		return journal.EntryRunCompleted
	case "FAILED":
		return journal.EntryRunFailed
	case "CANCELLED":
		return journal.EntryRunCancelled
	case "TIMEOUT":
		return journal.EntryRunTimeout
	default:
		return journal.EntryRunFailed
	}
}
