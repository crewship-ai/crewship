package api

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
)

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
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid JSON"})
		return
	}
	if body.ID == "" || body.AgentID == "" || body.WorkspaceID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id, agent_id, workspace_id required"})
		return
	}
	if body.TriggerType == "" {
		body.TriggerType = "USER"
	}

	now := time.Now().UTC().Format(time.RFC3339)

	// Update agent status to RUNNING. The run lifecycle itself is
	// recorded as a run.started journal entry below — agent_runs was
	// dropped in migration v61.
	if _, err := h.db.ExecContext(r.Context(),
		"UPDATE agents SET status = 'RUNNING', updated_at = ? WHERE id = ?", now, body.AgentID); err != nil {
		h.logger.Debug("update agent status on run create", "error", err, "agent_id", body.AgentID)
	}

	// Emit run.started — the source of truth for runs. trace_id == run.id
	// so subsequent in-run journal entries (LLM call, exec, etc.) group
	// under the same trace via journal.WithRunID.
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
			h.logger.Error("create run: emit run.started", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
			return
		}
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
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid JSON"})
		return
	}

	validStatuses := map[string]bool{
		"RUNNING": true, "COMPLETED": true, "FAILED": true, "CANCELLED": true, "TIMEOUT": true,
	}
	if !validStatuses[body.Status] {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid status"})
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
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "run not found"})
			return
		}
		h.logger.Error("update run: lookup", "error", err, "run_id", runID)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
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
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
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
					WHERE je1.agent_id = ?
					  AND je1.entry_type = 'run.started'
					  AND je1.trace_id != ?
					  AND NOT EXISTS (
					    SELECT 1 FROM journal_entries je2
					    WHERE je2.trace_id = je1.trace_id
					      AND je2.entry_type IN ('run.completed','run.failed','run.cancelled','run.timeout')
					  )
				) > 0 THEN 'RUNNING'
				ELSE ?
			END, updated_at = ? WHERE id = ?`,
			agentID, runID, failedStatus, now, agentID); err != nil {
			h.logger.Debug("update agent status on run completion", "error", err, "agent_id", agentID)
		}
		var readBack string
		if err := h.db.QueryRowContext(r.Context(), "SELECT status FROM agents WHERE id = ?", agentID).Scan(&readBack); err == nil {
			agentStatus = readBack
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
