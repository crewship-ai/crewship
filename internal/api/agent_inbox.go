package api

import (
	"database/sql"
	"log/slog"
	"net/http"
	"time"
)

// AgentInboxHandler serves GET /api/v1/agents/{agentId}/inbox — a
// consolidated "what's waiting on this agent" payload used by the Crews
// preview panel (Phase 10). One round-trip instead of four parallel
// fetches from the client.
type AgentInboxHandler struct {
	db     *sql.DB
	logger *slog.Logger
}

func NewAgentInboxHandler(db *sql.DB, logger *slog.Logger) *AgentInboxHandler {
	return &AgentInboxHandler{db: db, logger: logger}
}

type agentInboxResponse struct {
	ApprovalsPending    int              `json:"approvals_pending"`
	AssignmentsOpen     int              `json:"assignments_open"`
	EscalationsOpen     int              `json:"escalations_open"`
	PeerMessages        []peerMessageRow `json:"peer_messages"`
	CostUSDThisMonth    float64          `json:"cost_usd_this_month"`
	LLMCallsThisMonth   int              `json:"llm_calls_this_month"`
	TokensUsedThisMonth int64            `json:"tokens_used_this_month"`
}

type peerMessageRow struct {
	ID            string  `json:"id"`
	FromAgentName string  `json:"from_agent_name"`
	FromAgentSlug string  `json:"from_agent_slug"`
	ToAgentName   *string `json:"to_agent_name,omitempty"`
	ToAgentSlug   *string `json:"to_agent_slug,omitempty"`
	Question      string  `json:"question"`
	Response      *string `json:"response,omitempty"`
	Escalated     bool    `json:"escalated"`
	DurationMs    *int64  `json:"duration_ms,omitempty"`
	Status        string  `json:"status"`
	CreatedAt     string  `json:"created_at"`
	Direction     string  `json:"direction"` // "incoming" or "outgoing"
}

// Handle serves the consolidated inbox. Agent must belong to the caller's
// workspace; cross-tenant reads return 404 (indistinguishable from
// "agent doesn't exist").
func (h *AgentInboxHandler) Handle(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	if workspaceID == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "workspace required"})
		return
	}
	agentID := r.PathValue("agentId")
	if agentID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "agentId required"})
		return
	}

	var crewID sql.NullString
	err := h.db.QueryRowContext(r.Context(),
		`SELECT crew_id FROM agents WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL`,
		agentID, workspaceID).Scan(&crewID)
	if err == sql.ErrNoRows {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "agent not found"})
		return
	}
	if err != nil {
		h.logger.Error("inbox: lookup agent", "err", err, "agent_id", agentID)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "lookup failed"})
		return
	}

	resp := agentInboxResponse{PeerMessages: []peerMessageRow{}}

	// Approvals pending
	if err := h.db.QueryRowContext(r.Context(),
		`SELECT COUNT(*) FROM approvals_queue WHERE workspace_id = ? AND agent_id = ? AND status = 'pending'`,
		workspaceID, agentID).Scan(&resp.ApprovalsPending); err != nil {
		h.logger.Warn("inbox: approvals count", "err", err, "agent_id", agentID)
	}

	// Assignments open (this agent is the recipient, still running or queued)
	if err := h.db.QueryRowContext(r.Context(),
		`SELECT COUNT(*) FROM assignments WHERE workspace_id = ? AND assigned_to_id = ? AND status IN ('queued', 'running')`,
		workspaceID, agentID).Scan(&resp.AssignmentsOpen); err != nil {
		h.logger.Warn("inbox: assignments count", "err", err, "agent_id", agentID)
	}

	// Escalations: this agent raised them, not yet resolved
	if err := h.db.QueryRowContext(r.Context(),
		`SELECT COUNT(*) FROM escalations WHERE workspace_id = ? AND from_agent_id = ? AND status IN ('pending', 'open')`,
		workspaceID, agentID).Scan(&resp.EscalationsOpen); err != nil {
		h.logger.Warn("inbox: escalations count", "err", err, "agent_id", agentID)
	}

	// Peer messages involving this agent (either direction), 3 most recent
	if crewID.Valid && crewID.String != "" {
		rows, err := h.db.QueryContext(r.Context(), `
			SELECT pc.id,
			       from_a.name, from_a.slug,
			       to_a.name, to_a.slug,
			       pc.question, pc.response, pc.status, pc.created_at,
			       pc.from_agent_id, pc.escalated, pc.duration_ms
			FROM peer_conversations pc
			JOIN agents from_a ON from_a.id = pc.from_agent_id
			LEFT JOIN agents to_a ON to_a.id = pc.to_agent_id
			WHERE pc.workspace_id = ? AND pc.crew_id = ?
			  AND (pc.from_agent_id = ? OR pc.to_agent_id = ?)
			ORDER BY pc.created_at DESC
			LIMIT 20
		`, workspaceID, crewID.String, agentID, agentID)
		if err != nil {
			h.logger.Warn("inbox: peer messages", "err", err, "agent_id", agentID)
		} else {
			defer rows.Close()
			for rows.Next() {
				var pm peerMessageRow
				var fromID string
				var escalated sql.NullInt64
				var duration sql.NullInt64
				var response sql.NullString
				if err := rows.Scan(&pm.ID, &pm.FromAgentName, &pm.FromAgentSlug,
					&pm.ToAgentName, &pm.ToAgentSlug,
					&pm.Question, &response, &pm.Status, &pm.CreatedAt,
					&fromID, &escalated, &duration); err != nil {
					continue
				}
				if fromID == agentID {
					pm.Direction = "outgoing"
				} else {
					pm.Direction = "incoming"
				}
				if response.Valid && response.String != "" {
					s := response.String
					pm.Response = &s
				}
				pm.Escalated = escalated.Valid && escalated.Int64 != 0
				if duration.Valid {
					d := duration.Int64
					pm.DurationMs = &d
				}
				resp.PeerMessages = append(resp.PeerMessages, pm)
			}
		}
	}

	// Cost aggregation for the current calendar month (paymaster ledger).
	// SUM over cost_ledger rows where agent_id matches. Table may not exist
	// in older workspaces — tolerate the failure silently rather than 500
	// the whole inbox.
	monthStart := time.Now().UTC().Format("2006-01") + "-01T00:00:00Z"
	var costUSD sql.NullFloat64
	var callCount sql.NullInt64
	var tokenTotal sql.NullInt64
	if err := h.db.QueryRowContext(r.Context(), `
		SELECT COALESCE(SUM(cost_usd), 0), COUNT(*), COALESCE(SUM(input_tokens + output_tokens), 0)
		FROM cost_ledger
		WHERE workspace_id = ? AND agent_id = ? AND created_at >= ?
	`, workspaceID, agentID, monthStart).Scan(&costUSD, &callCount, &tokenTotal); err != nil {
		h.logger.Debug("inbox: cost ledger (may be missing table)", "err", err, "agent_id", agentID)
	} else {
		resp.CostUSDThisMonth = costUSD.Float64
		resp.LLMCallsThisMonth = int(callCount.Int64)
		resp.TokensUsedThisMonth = tokenTotal.Int64
	}

	writeJSON(w, http.StatusOK, resp)
}
