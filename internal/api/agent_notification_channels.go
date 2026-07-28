package api

import (
	"database/sql"
	"log/slog"
	"net/http"

	"github.com/crewship-ai/crewship/internal/notify"
)

// AgentNotifyChannelsHandler serves GET
// /api/v1/agents/{agentId}/notification-channels — the mirror of
// /notification-channels/{id}/agents.
//
// The channel side could always answer "who may post here?". The agent side
// could not answer "what can this agent reach?", even though the store has had
// ListForAgent since the notify_send discovery path. Building that view from
// the client meant fetching every channel and asking each one about this agent
// — an N+1 that also silently misses channels the caller cannot list, so the
// answer would be wrong rather than slow.
//
// Readable by any workspace member, matching the channel side. That is safe
// because the response says THAT a channel exists and of what kind, never
// where it points: ListForAgent deliberately leaves config_json unparsed.
type AgentNotifyChannelsHandler struct {
	pairings *notify.PairingStore
	logger   *slog.Logger
}

func NewAgentNotifyChannelsHandler(db *sql.DB, logger *slog.Logger) *AgentNotifyChannelsHandler {
	return &AgentNotifyChannelsHandler{pairings: notify.NewPairingStore(db), logger: logger}
}

// agentChannelResponse is the trimmed channel shape this route returns.
//
// Explicitly listed rather than serialising notify.Channel, so a field added
// to that struct later — a destination, a secret — cannot start leaking here
// just because someone widened a type somewhere else.
type agentChannelResponse struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Provider string `json:"provider,omitempty"`
	Enabled  bool   `json:"enabled"`
}

// List serves GET /api/v1/agents/{agentId}/notification-channels.
func (h *AgentNotifyChannelsHandler) List(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	if workspaceID == "" {
		replyError(w, http.StatusUnauthorized, "workspace required")
		return
	}
	agentID := r.PathValue("agentId")
	if agentID == "" {
		replyError(w, http.StatusBadRequest, "agent id required")
		return
	}

	channels, err := h.pairings.ListForAgent(r.Context(), workspaceID, agentID)
	if err != nil {
		h.logger.Error("notify: list agent channels", "err", err, "agent_id", agentID)
		replyError(w, http.StatusInternalServerError, "list failed")
		return
	}

	out := make([]agentChannelResponse, 0, len(channels))
	for _, c := range channels {
		out = append(out, agentChannelResponse{
			ID:       c.ID,
			Type:     string(c.Type),
			Provider: c.Provider,
			Enabled:  c.Enabled,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"channels": out})
}
