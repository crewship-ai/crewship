package api

import (
	"database/sql"
	"log/slog"
	"net/http"
	"strconv"
)

// KeeperLogHandler provides endpoints for querying the Keeper decision audit log.
type KeeperLogHandler struct {
	db     *sql.DB
	logger *slog.Logger
}

// NewKeeperLogHandler creates a KeeperLogHandler with the given database and logger.
func NewKeeperLogHandler(db *sql.DB, logger *slog.Logger) *KeeperLogHandler {
	return &KeeperLogHandler{db: db, logger: logger}
}

type keeperLogEntry struct {
	ID                string  `json:"id"`
	AgentID           string  `json:"agent_id"`
	AgentName         string  `json:"agent_name"`
	CrewID            string  `json:"crew_id"`
	CredentialID      string  `json:"credential_id"`
	CredName          string  `json:"credential_name"`
	Intent            string  `json:"intent"`
	RequestType       string  `json:"request_type"`
	Command           *string `json:"command,omitempty"`
	Decision          *string `json:"decision"`
	Reason            *string `json:"reason"`
	RiskScore         *int    `json:"risk_score"`
	ExitCode          *int    `json:"exit_code,omitempty"`
	OllamaPrompt      *string `json:"ollama_prompt,omitempty"`
	OllamaRawResponse *string `json:"ollama_raw_response,omitempty"`
	CreatedAt         string  `json:"created_at"`
	DecidedAt         *string `json:"decided_at"`
}

// List returns the most recent keeper requests with agent and credential names.
// GET /api/v1/admin/keeper/requests?limit=50&offset=0
func (h *KeeperLogHandler) List(w http.ResponseWriter, r *http.Request) {
	user := UserFromContext(r.Context())
	if user == nil {
		replyError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	// Require ADMIN+ to view Keeper security logs
	role := RoleFromContext(r.Context())
	if !canRole(role, "manage") {
		replyError(w, http.StatusForbidden, "Forbidden: ADMIN or OWNER only")
		return
	}

	workspaceID := WorkspaceIDFromContext(r.Context())
	if workspaceID == "" {
		replyError(w, http.StatusBadRequest, "workspace context required")
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
		replyError(w, http.StatusInternalServerError, "internal error")
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

// keeperRequestEventEntry is one row of the append-only transition ledger
// (#1369). Distinct from keeperLogEntry: that one is the CURRENT state of a
// request, this is the history of how it got there.
type keeperRequestEventEntry struct {
	Seq          int     `json:"seq"`
	State        string  `json:"state"`
	RequestType  *string `json:"request_type,omitempty"`
	AgentID      *string `json:"requesting_agent_id,omitempty"`
	AgentName    string  `json:"agent_name,omitempty"`
	CrewID       *string `json:"requesting_crew_id,omitempty"`
	CredentialID *string `json:"credential_id,omitempty"`
	CredName     string  `json:"credential_name,omitempty"`
	Intent       *string `json:"intent,omitempty"`
	Command      *string `json:"command,omitempty"`
	Reason       *string `json:"reason,omitempty"`
	RiskScore    *int    `json:"risk_score,omitempty"`
	ExitCode     *int    `json:"exit_code,omitempty"`
	ActorType    string  `json:"actor_type"`
	ActorID      *string `json:"actor_id,omitempty"`
	RecordedAt   string  `json:"recorded_at"`
}

// ListEvents returns the append-only transition history for one keeper request.
// GET /api/v1/admin/keeper/requests/{requestId}/events
//
// keeper_requests answers "what was decided". This answers "how did it get
// there" — the question the in-place UPDATE used to destroy: was it pending, for
// how long, and was the decision ever rewritten (which would show up as a third
// transition rather than a silently changed column).
//
// Tenant scoping mirrors List: the ledger carries workspace_id, but a legacy or
// Phase-2 row may have it NULL, so the agent-membership subquery is kept as the
// authoritative filter and the workspace column is used as an additional
// narrowing. Requests belonging to another workspace return an empty list rather
// than a 404, so this endpoint cannot be used to probe which request ids exist.
func (h *KeeperLogHandler) ListEvents(w http.ResponseWriter, r *http.Request) {
	if user := UserFromContext(r.Context()); user == nil {
		replyError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if !canRole(RoleFromContext(r.Context()), "manage") {
		replyError(w, http.StatusForbidden, "Forbidden: ADMIN or OWNER only")
		return
	}
	workspaceID := WorkspaceIDFromContext(r.Context())
	if workspaceID == "" {
		replyError(w, http.StatusBadRequest, "workspace context required")
		return
	}
	requestID := r.PathValue("requestId")
	if requestID == "" {
		replyError(w, http.StatusBadRequest, "request id required")
		return
	}

	rows, err := h.db.QueryContext(r.Context(), `
		SELECT e.seq, e.state, e.request_type, e.requesting_agent_id, COALESCE(a.name,''),
		       e.requesting_crew_id, e.credential_id, COALESCE(c.name,''),
		       e.intent, e.command, e.reason, e.risk_score, e.exit_code,
		       e.actor_type, e.actor_id, e.recorded_at
		FROM keeper_request_events e
		LEFT JOIN agents a ON a.id = e.requesting_agent_id
		LEFT JOIN credentials c ON c.id = e.credential_id
		WHERE e.request_id = ?
		  AND (e.workspace_id = ?
		       OR e.requesting_agent_id IN (SELECT id FROM agents WHERE workspace_id = ?))
		ORDER BY e.seq ASC`, requestID, workspaceID, workspaceID)
	if err != nil {
		h.logger.Error("keeper request events: query failed", "error", err, "request_id", requestID)
		replyError(w, http.StatusInternalServerError, "internal error")
		return
	}
	defer rows.Close()

	var events []keeperRequestEventEntry
	for rows.Next() {
		var e keeperRequestEventEntry
		if err := rows.Scan(&e.Seq, &e.State, &e.RequestType, &e.AgentID, &e.AgentName,
			&e.CrewID, &e.CredentialID, &e.CredName, &e.Intent, &e.Command,
			&e.Reason, &e.RiskScore, &e.ExitCode, &e.ActorType, &e.ActorID,
			&e.RecordedAt); err != nil {
			// Unlike List, do NOT swallow a scan error here: this endpoint's whole
			// value is completeness, and quietly dropping a transition would make a
			// gap look like it never happened.
			h.logger.Error("keeper request events: scan failed", "error", err, "request_id", requestID)
			replyError(w, http.StatusInternalServerError, "internal error")
			return
		}
		events = append(events, e)
	}
	if err := rows.Err(); err != nil {
		h.logger.Error("keeper request events: rows iteration", "error", err, "request_id", requestID)
		replyError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if events == nil {
		events = []keeperRequestEventEntry{}
	}
	writeJSON(w, http.StatusOK, events)
}
