package api

import (
	"database/sql"
	"log/slog"
	"net/http"
	"strconv"
)

type KeeperLogHandler struct {
	db     *sql.DB
	logger *slog.Logger
}

func NewKeeperLogHandler(db *sql.DB, logger *slog.Logger) *KeeperLogHandler {
	return &KeeperLogHandler{db: db, logger: logger}
}

type keeperLogEntry struct {
	ID               string  `json:"id"`
	AgentID          string  `json:"agent_id"`
	AgentName        string  `json:"agent_name"`
	CrewID           string  `json:"crew_id"`
	CredentialID     string  `json:"credential_id"`
	CredName         string  `json:"credential_name"`
	Intent           string  `json:"intent"`
	RequestType      string  `json:"request_type"`
	Command          *string `json:"command,omitempty"`
	Decision         *string `json:"decision"`
	Reason           *string `json:"reason"`
	RiskScore        *int    `json:"risk_score"`
	ExitCode         *int    `json:"exit_code,omitempty"`
	OllamaPrompt     *string `json:"ollama_prompt,omitempty"`
	OllamaRawResponse *string `json:"ollama_raw_response,omitempty"`
	CreatedAt        string  `json:"created_at"`
	DecidedAt        *string `json:"decided_at"`
}

// List returns the most recent keeper requests with agent and credential names.
// GET /api/v1/admin/keeper/requests?limit=50&offset=0
func (h *KeeperLogHandler) List(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	if user == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "Unauthorized"})
		return
	}
	// Require ADMIN+ to view Keeper security logs
	role := RoleFromContext(r.Context())
	if !canRole(role, "manage") {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Forbidden: ADMIN or OWNER only"})
		return
	}

	workspaceID := WorkspaceIDFromContext(r.Context())
	if workspaceID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "workspace context required"})
		return
	}

	limit := 50
	offset := 0
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			offset = n
		}
	}

	rows, err := h.db.QueryContext(r.Context(), `
		SELECT
			kr.id, kr.requesting_agent_id, COALESCE(a.name,'Unknown'),
			kr.requesting_crew_id, kr.credential_id, COALESCE(c.name,'Unknown'),
			kr.intent, kr.request_type, kr.command,
			kr.decision, kr.reason, kr.risk_score, kr.exit_code,
			kr.ollama_prompt, kr.ollama_raw_response,
			kr.created_at, kr.decided_at
		FROM keeper_requests kr
		LEFT JOIN agents a ON a.id = kr.requesting_agent_id
		LEFT JOIN credentials c ON c.id = kr.credential_id
		WHERE kr.requesting_agent_id IN (SELECT id FROM agents WHERE workspace_id = ?)
		ORDER BY kr.created_at DESC
		LIMIT ? OFFSET ?`, workspaceID, limit, offset)
	if err != nil {
		h.logger.Error("keeper log: query failed", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal error"})
		return
	}
	defer rows.Close()

	var entries []keeperLogEntry
	for rows.Next() {
		var e keeperLogEntry
		if err := rows.Scan(
			&e.ID, &e.AgentID, &e.AgentName,
			&e.CrewID, &e.CredentialID, &e.CredName,
			&e.Intent, &e.RequestType, &e.Command,
			&e.Decision, &e.Reason, &e.RiskScore, &e.ExitCode,
			&e.OllamaPrompt, &e.OllamaRawResponse,
			&e.CreatedAt, &e.DecidedAt,
		); err != nil {
			h.logger.Error("keeper log: scan failed", "error", err)
			continue
		}
		entries = append(entries, e)
	}

	if entries == nil {
		entries = []keeperLogEntry{}
	}

	writeJSON(w, http.StatusOK, entries)
}
