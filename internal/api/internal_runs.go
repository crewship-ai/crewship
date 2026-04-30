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
	var metadataVal interface{}
	if body.Metadata != nil {
		metadataVal = string(body.Metadata)
	}
	_, err := h.db.ExecContext(r.Context(), `
		INSERT INTO agent_runs (id, agent_id, chat_id, workspace_id, trigger_type, status, metadata, started_at, created_at)
		VALUES (?, ?, ?, ?, ?, 'RUNNING', ?, ?, ?)`,
		body.ID, body.AgentID, body.ChatID, body.WorkspaceID, body.TriggerType, metadataVal, now, now)
	if err != nil {
		h.logger.Error("create run", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	// Update agent status to RUNNING
	if _, err := h.db.ExecContext(r.Context(),
		"UPDATE agents SET status = 'RUNNING', updated_at = ? WHERE id = ?", now, body.AgentID); err != nil {
		h.logger.Debug("update agent status on run create", "error", err, "agent_id", body.AgentID)
	}

	// Mirror into journal — Phase J of unified-journal will drop agent_runs
	// and leave this as the single source of truth. Until then the run is
	// dual-written so a half-deployed migration can roll back without data
	// loss.
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
		_, _ = h.journal.Emit(r.Context(), journal.Entry{
			WorkspaceID: body.WorkspaceID,
			AgentID:     body.AgentID,
			Type:        journal.EntryRunStarted,
			Severity:    journal.SeverityInfo,
			ActorType:   journal.ActorSidecar,
			Summary:     fmt.Sprintf("run %s started", shortRunID(body.ID)),
			Payload:     payload,
			TraceID:     body.ID,
		})
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

// UpdateRun updates the status of an agent run (e.g. COMPLETED, FAILED) when it finishes.
// PATCH /api/v1/internal/runs/{runId}
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
		"RUNNING": true, "COMPLETED": true, "FAILED": true, "CANCELLED": true,
	}
	if !validStatuses[body.Status] {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid status"})
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)
	terminal := map[string]bool{"COMPLETED": true, "FAILED": true, "CANCELLED": true}
	query := "UPDATE agent_runs SET status = ?"
	args := []interface{}{body.Status}
	if terminal[body.Status] {
		query += ", finished_at = ?"
		args = append(args, now)
	}

	if body.ExitCode != nil {
		query += ", exit_code = ?"
		args = append(args, *body.ExitCode)
	}
	if body.ErrorMessage != nil {
		query += ", error_message = ?"
		args = append(args, *body.ErrorMessage)
	}
	if body.Metadata != nil {
		query += ", metadata = ?"
		args = append(args, string(body.Metadata))
	}
	query += " WHERE id = ?"
	args = append(args, runID)

	_, err := h.db.ExecContext(r.Context(), query, args...)
	if err != nil {
		h.logger.Error("update run", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	// Update agent status and broadcast events for terminal states
	if terminal[body.Status] {
		var agentID, workspaceID string
		var agentName sql.NullString
		if err := h.db.QueryRowContext(r.Context(),
			`SELECT r.agent_id, r.workspace_id, a.name FROM agent_runs r
			 LEFT JOIN agents a ON a.id = r.agent_id WHERE r.id = ?`, runID,
		).Scan(&agentID, &workspaceID, &agentName); err != nil {
			h.logger.Debug("fetch run details for broadcast", "error", err, "run_id", runID)
		}

		// Mirror terminal state into journal (dual-write — see CreateRun).
		// Severity is error for FAILED so it surfaces in the warn/error
		// filter; CANCELLED stays info because it's user-initiated.
		if workspaceID != "" {
			entryType := terminalEntryType(body.Status)
			severity := journal.SeverityInfo
			if body.Status == "FAILED" {
				severity = journal.SeverityError
			}
			payload := map[string]any{}
			if body.ExitCode != nil {
				payload["exit_code"] = *body.ExitCode
			}
			if body.ErrorMessage != nil && *body.ErrorMessage != "" {
				payload["error_message"] = *body.ErrorMessage
			}
			_, _ = h.journal.Emit(r.Context(), journal.Entry{
				WorkspaceID: workspaceID,
				AgentID:     agentID,
				Type:        entryType,
				Severity:    severity,
				ActorType:   journal.ActorSidecar,
				Summary:     fmt.Sprintf("run %s %s", shortRunID(runID), entryType[len("run."):]),
				Payload:     payload,
				TraceID:     runID,
			})
		}

		// Atomic agent status update: always runs regardless of hub presence
		agentStatus := "IDLE"
		if agentID != "" {
			failedStatus := "IDLE"
			if body.Status == "FAILED" {
				failedStatus = "ERROR"
			}
			if _, err := h.db.ExecContext(r.Context(), `
				UPDATE agents SET status = CASE
					WHEN (SELECT COUNT(*) FROM agent_runs WHERE agent_id = ? AND status = 'RUNNING' AND id != ?) > 0 THEN 'RUNNING'
					ELSE ?
				END, updated_at = ? WHERE id = ?`,
				agentID, runID, failedStatus, now, agentID); err != nil {
				h.logger.Debug("update agent status on run completion", "error", err, "agent_id", agentID)
			}

			// Read back actual status
			agentStatus = failedStatus
			var readBack string
			if err := h.db.QueryRowContext(r.Context(), "SELECT status FROM agents WHERE id = ?", agentID).Scan(&readBack); err == nil {
				agentStatus = readBack
			}
		}

		// Broadcast real-time events (only when hub is available)
		if workspaceID != "" {
			eventType := "run.completed"
			if body.Status == "FAILED" || body.Status == "CANCELLED" {
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
		}
	}

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
