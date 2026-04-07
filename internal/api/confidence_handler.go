package api

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/ws"
)

// ReportConfidence handles POST /api/v1/internal/report-confidence.
func (h *QueryHandler) ReportConfidence(w http.ResponseWriter, r *http.Request) {
	var body struct {
		AgentID     string  `json:"agent_id"`
		CrewID      string  `json:"crew_id"`
		WorkspaceID string  `json:"workspace_id"`
		ChatID      string  `json:"chat_id"`
		Confidence  float64 `json:"confidence"`
		Reason      string  `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}
	if body.AgentID == "" || body.Confidence < 0 || body.Confidence > 1 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "agent_id required, confidence must be 0-1"})
		return
	}

	var taskID, missionID string
	err := h.db.QueryRowContext(r.Context(),
		`SELECT mt.id, mt.mission_id FROM mission_tasks mt
		 JOIN missions m ON m.id = mt.mission_id
		 WHERE mt.assigned_agent_id = ? AND mt.status = 'IN_PROGRESS'
		   AND m.crew_id = ? AND m.status = 'IN_PROGRESS'
		 ORDER BY mt.updated_at DESC LIMIT 1`,
		body.AgentID, body.CrewID).Scan(&taskID, &missionID)
	if err != nil {
		if err == sql.ErrNoRows {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "no active task found for agent"})
			return
		}
		h.logger.Error("lookup active task for confidence report", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal server error"})
		return
	}

	if _, err := h.db.ExecContext(r.Context(),
		`UPDATE mission_tasks SET confidence = ? WHERE id = ?`,
		body.Confidence, taskID); err != nil {
		h.logger.Error("update task confidence", "error", err, "task_id", taskID)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal server error"})
		return
	}

	var configJSON sql.NullString
	if err := h.db.QueryRowContext(r.Context(),
		`SELECT c.escalation_config FROM crews c JOIN missions m ON m.crew_id = c.id WHERE m.id = ?`,
		missionID).Scan(&configJSON); err != nil && err != sql.ErrNoRows {
		h.logger.Error("load escalation config for confidence", "error", err)
	}

	action := "none"
	if configJSON.Valid && configJSON.String != "" {
		var cfg orchestrator.EscalationConfig
		if err := json.Unmarshal([]byte(configJSON.String), &cfg); err == nil {
			if cfg.RequireApprovalBelow > 0 && body.Confidence < cfg.RequireApprovalBelow {
				action = "escalated"
				h.autoEscalateForConfidence(r, body.AgentID, body.CrewID, body.WorkspaceID, body.ChatID,
					taskID, body.Confidence, body.Reason)
			} else if cfg.NotifyThreshold > 0 && body.Confidence < cfg.NotifyThreshold {
				action = "notified"
				if h.hub != nil {
					h.hub.Broadcast("workspace:"+body.WorkspaceID, ws.ServerMessage{
						Type:    "confidence.low",
						Channel: "workspace:" + body.WorkspaceID,
						Payload: map[string]interface{}{
							"task_id": taskID, "mission_id": missionID,
							"confidence": body.Confidence, "reason": body.Reason,
						},
					})
				}
			}
		}
	}

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"task_id": taskID, "confidence": body.Confidence, "action": action,
	})
}

func (h *QueryHandler) autoEscalateForConfidence(r *http.Request, agentID, crewID, workspaceID, chatID, taskID string, confidence float64, reason string) {
	// NOTE: json_extract is SQLite-specific. Crewship uses modernc.org/sqlite exclusively.
	var existing int
	if err := h.db.QueryRowContext(r.Context(),
		`SELECT 1 FROM escalations
		 WHERE crew_id = ? AND status = 'PENDING'
		   AND json_extract(metadata, '$.task_id') = ?
		   AND json_extract(metadata, '$.source') = 'auto_confidence_gate'`,
		crewID, taskID).Scan(&existing); err == nil {
		return
	}

	escalationID := generateCUID()
	if reason == "" {
		reason = "Low confidence reported"
	}
	metadata, _ := json.Marshal(map[string]string{"task_id": taskID, "source": "auto_confidence_gate"})

	_, err := h.db.ExecContext(r.Context(),
		`INSERT INTO escalations (id, workspace_id, crew_id, chat_id, from_agent_id, reason, context, type, metadata, status, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, 'TEXT', ?, 'PENDING', datetime('now'))`,
		escalationID, workspaceID, crewID, chatID, agentID,
		reason, fmt.Sprintf("Auto-escalated: confidence %.0f%% below threshold", confidence*100),
		string(metadata))
	if err != nil {
		h.logger.Error("create auto-escalation", "error", err, "task_id", taskID)
		return
	}

	if h.hub != nil {
		h.hub.Broadcast("workspace:"+workspaceID, ws.ServerMessage{
			Type:    "escalation.created",
			Channel: "workspace:" + workspaceID,
			Payload: map[string]string{"id": escalationID, "task_id": taskID, "reason": reason},
		})
	}
}
