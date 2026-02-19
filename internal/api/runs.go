package api

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"strconv"
)

type RunHandler struct {
	db     *sql.DB
	logger *slog.Logger
}

func NewRunHandler(db *sql.DB, logger *slog.Logger) *RunHandler {
	return &RunHandler{db: db, logger: logger}
}

type runResponse struct {
	ID           string  `json:"id"`
	AgentID      string  `json:"agent_id"`
	ChatID       *string `json:"chat_id"`
	WorkspaceID  string  `json:"workspace_id"`
	TriggeredBy  *string `json:"triggered_by"`
	TriggerType  string  `json:"trigger_type"`
	Status       string  `json:"status"`
	StartedAt    *string `json:"started_at"`
	FinishedAt   *string `json:"finished_at"`
	ErrorMessage *string `json:"error_message"`
	ExitCode     *int             `json:"exit_code"`
	Metadata     json.RawMessage  `json:"metadata"`
	CreatedAt    string           `json:"created_at"`
	AgentName    *string          `json:"agent_name,omitempty"`
	AgentSlug    *string          `json:"agent_slug,omitempty"`
	CrewName     *string          `json:"crew_name,omitempty"`
}

type runListResponse struct {
	Data       []runResponse `json:"data"`
	Stats      runStats      `json:"stats"`
	Pagination pagination    `json:"pagination"`
}

type runStats struct {
	Running int `json:"running"`
	Today   int `json:"today"`
	Failed  int `json:"failed"`
}

type pagination struct {
	Page       int `json:"page"`
	Limit      int `json:"limit"`
	Total      int `json:"total"`
	TotalPages int `json:"total_pages"`
}

func (h *RunHandler) List(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())

	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit < 1 || limit > 100 {
		limit = 50
	}
	offset := (page - 1) * limit

	status := r.URL.Query().Get("status")
	agentID := r.URL.Query().Get("agent_id")
	trigger := r.URL.Query().Get("trigger")

	tag := r.URL.Query().Get("tag")

	query := `
		SELECT r.id, r.agent_id, r.chat_id, r.workspace_id, r.triggered_by,
			r.trigger_type, r.status, r.started_at, r.finished_at,
			r.error_message, r.exit_code, r.metadata, r.created_at,
			a.name, a.slug,
			c.name
		FROM agent_runs r
		LEFT JOIN agents a ON a.id = r.agent_id
		LEFT JOIN crews c ON c.id = a.crew_id
		WHERE r.workspace_id = ?`
	countQuery := `SELECT COUNT(*) FROM agent_runs WHERE workspace_id = ?`
	args := []interface{}{workspaceID}
	countArgs := []interface{}{workspaceID}

	if status != "" {
		query += " AND r.status = ?"
		countQuery += " AND status = ?"
		args = append(args, status)
		countArgs = append(countArgs, status)
	}
	if agentID != "" {
		query += " AND r.agent_id = ?"
		countQuery += " AND agent_id = ?"
		args = append(args, agentID)
		countArgs = append(countArgs, agentID)
	}
	if trigger != "" {
		query += " AND r.trigger_type = ?"
		countQuery += " AND trigger_type = ?"
		args = append(args, trigger)
		countArgs = append(countArgs, trigger)
	}
	if tag != "" {
		// Search for tag in JSON metadata: {"tags":["tag1","tag2"]}
		tagPattern := fmt.Sprintf("%%\"%s\"%%", tag)
		query += " AND r.metadata LIKE ?"
		countQuery += " AND metadata LIKE ?"
		args = append(args, tagPattern)
		countArgs = append(countArgs, tagPattern)
	}

	query += fmt.Sprintf(" ORDER BY r.created_at DESC LIMIT %d OFFSET %d", limit, offset)

	rows, err := h.db.QueryContext(r.Context(), query, args...)
	if err != nil {
		h.logger.Error("list runs", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	defer rows.Close()

	var runs []runResponse
	for rows.Next() {
		var run runResponse
		var metadataStr sql.NullString
		if err := rows.Scan(&run.ID, &run.AgentID, &run.ChatID, &run.WorkspaceID,
			&run.TriggeredBy, &run.TriggerType, &run.Status,
			&run.StartedAt, &run.FinishedAt, &run.ErrorMessage, &run.ExitCode,
			&metadataStr, &run.CreatedAt, &run.AgentName, &run.AgentSlug, &run.CrewName); err != nil {
			h.logger.Error("scan run", "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
			return
		}
		if metadataStr.Valid {
			run.Metadata = json.RawMessage(metadataStr.String)
		}
		runs = append(runs, run)
	}
	if err := rows.Err(); err != nil {
		h.logger.Error("rows iteration (runs)", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	if runs == nil {
		runs = []runResponse{}
	}

	var total int
	if err := h.db.QueryRowContext(r.Context(), countQuery, countArgs...).Scan(&total); err != nil {
		h.logger.Error("count runs", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	var running, today, failed int
	if err := h.db.QueryRowContext(r.Context(),
		"SELECT COUNT(*) FROM agent_runs WHERE workspace_id = ? AND status = 'RUNNING'", workspaceID).Scan(&running); err != nil {
		h.logger.Error("count running runs", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	if err := h.db.QueryRowContext(r.Context(),
		"SELECT COUNT(*) FROM agent_runs WHERE workspace_id = ? AND date(created_at) = date('now')", workspaceID).Scan(&today); err != nil {
		h.logger.Error("count today runs", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}
	if err := h.db.QueryRowContext(r.Context(),
		"SELECT COUNT(*) FROM agent_runs WHERE workspace_id = ? AND status = 'FAILED' AND date(created_at) = date('now')", workspaceID).Scan(&failed); err != nil {
		h.logger.Error("count failed runs", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Internal server error"})
		return
	}

	writeJSON(w, http.StatusOK, runListResponse{
		Data:  runs,
		Stats: runStats{Running: running, Today: today, Failed: failed},
		Pagination: pagination{
			Page:       page,
			Limit:      limit,
			Total:      total,
			TotalPages: int(math.Ceil(float64(total) / float64(limit))),
		},
	})
}
