package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/crewship-ai/crewship/internal/notify"
)

// NotifyChannelAgentsHandler manages which agents may post to a notification
// channel of their own accord (see internal/notify/agent_pairing.go).
//
// Granting is a human decision, so it lives on the public, role-gated API
// rather than the internal one an agent can reach. An agent cannot pair
// itself; that is the entire point of the table.
type NotifyChannelAgentsHandler struct {
	db       *sql.DB
	channels *notify.ChannelStore
	pairings *notify.PairingStore
	logger   *slog.Logger
}

func NewNotifyChannelAgentsHandler(db *sql.DB, logger *slog.Logger) *NotifyChannelAgentsHandler {
	return &NotifyChannelAgentsHandler{
		db:       db,
		channels: notify.NewChannelStore(db),
		pairings: notify.NewPairingStore(db),
		logger:   logger,
	}
}

// agentPairingResponse enriches a pairing with the agent's name, so the list
// answers "who can post here?" in the terms an admin thinks in rather than
// making them resolve ids by hand.
type agentPairingResponse struct {
	notify.AgentPairing
	AgentName string `json:"agent_name,omitempty"`
	AgentSlug string `json:"agent_slug,omitempty"`
}

// List serves GET /api/v1/notification-channels/{id}/agents.
func (h *NotifyChannelAgentsHandler) List(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	if workspaceID == "" {
		replyError(w, http.StatusUnauthorized, "workspace required")
		return
	}
	channelID := r.PathValue("id")
	if channelID == "" {
		replyError(w, http.StatusBadRequest, "channel id required")
		return
	}
	if _, ok := h.resolveChannel(w, r, workspaceID, channelID); !ok {
		return
	}
	pairings, err := h.pairings.ListForChannel(r.Context(), workspaceID, channelID)
	if err != nil {
		h.logger.Error("notify: list channel agents", "err", err, "channel_id", channelID)
		replyError(w, http.StatusInternalServerError, "list failed")
		return
	}
	out := make([]agentPairingResponse, 0, len(pairings))
	for _, p := range pairings {
		row := agentPairingResponse{AgentPairing: p}
		var name, slug sql.NullString
		_ = h.db.QueryRowContext(r.Context(),
			`SELECT name, slug FROM agents WHERE id = ?`, p.AgentID).Scan(&name, &slug)
		row.AgentName = name.String
		row.AgentSlug = slug.String
		out = append(out, row)
	}
	writeJSON(w, http.StatusOK, map[string]any{"agents": out})
}

type pairAgentRequest struct {
	AgentID string `json:"agent_id"`
}

// Allow serves POST /api/v1/notification-channels/{id}/agents — grant an
// agent permission to post to this channel.
func (h *NotifyChannelAgentsHandler) Allow(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	if workspaceID == "" {
		replyError(w, http.StatusUnauthorized, "workspace required")
		return
	}
	channelID := r.PathValue("id")
	if _, ok := h.resolveChannel(w, r, workspaceID, channelID); !ok {
		return
	}
	var body pairAgentRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		replyError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	agentID := strings.TrimSpace(body.AgentID)
	if agentID == "" {
		replyError(w, http.StatusBadRequest, "agent_id required")
		return
	}
	// The agent must live in this workspace. Without the check an admin could
	// pair an id from another tenant, and the send path — which only asks "is
	// this pair present?" — would honour it.
	if !h.agentInWorkspace(r, workspaceID, agentID) {
		replyError(w, http.StatusBadRequest, "that agent does not exist in this workspace")
		return
	}
	grantedBy := ""
	if u := UserFromContext(r.Context()); u != nil {
		grantedBy = u.ID
	}
	if err := h.pairings.Allow(r.Context(), workspaceID, channelID, agentID, grantedBy); err != nil {
		h.logger.Error("notify: pair agent", "err", err, "channel_id", channelID, "agent_id", agentID)
		replyError(w, http.StatusInternalServerError, "pairing failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"channel_id": channelID, "agent_id": agentID, "allowed": true})
}

// Deny serves DELETE /api/v1/notification-channels/{id}/agents/{agentId} —
// revoke the grant.
func (h *NotifyChannelAgentsHandler) Deny(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	if workspaceID == "" {
		replyError(w, http.StatusUnauthorized, "workspace required")
		return
	}
	channelID := r.PathValue("id")
	agentID := r.PathValue("agentId")
	if channelID == "" || agentID == "" {
		replyError(w, http.StatusBadRequest, "channel id and agent id required")
		return
	}
	if _, ok := h.resolveChannel(w, r, workspaceID, channelID); !ok {
		return
	}
	removed, err := h.pairings.Deny(r.Context(), workspaceID, channelID, agentID)
	if err != nil {
		h.logger.Error("notify: unpair agent", "err", err, "channel_id", channelID, "agent_id", agentID)
		replyError(w, http.StatusInternalServerError, "unpairing failed")
		return
	}
	if !removed {
		replyError(w, http.StatusNotFound, "that agent was not paired with this channel")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"channel_id": channelID, "agent_id": agentID, "allowed": false})
}

// resolveChannel loads the channel and enforces that the caller may manage
// it. Pairing an agent to a channel is a change to who can speak on it, so it
// takes the same authority as editing the channel: MANAGER+ for a workspace
// channel, ownership for a personal one.
func (h *NotifyChannelAgentsHandler) resolveChannel(w http.ResponseWriter, r *http.Request, workspaceID, channelID string) (notify.Channel, bool) {
	ch, err := h.channels.Get(r.Context(), workspaceID, channelID)
	if err != nil {
		if errors.Is(err, notify.ErrNotFound) {
			replyError(w, http.StatusNotFound, "channel not found")
		} else {
			h.logger.Error("notify: resolve channel", "err", err, "id", channelID)
			replyError(w, http.StatusInternalServerError, "resolve failed")
		}
		return notify.Channel{}, false
	}
	if ch.Scope == notify.ScopeUser {
		userID := ""
		if u := UserFromContext(r.Context()); u != nil {
			userID = u.ID
		}
		if userID == "" || ch.OwnerUserID != userID {
			writeProblem(w, r, http.StatusForbidden, "Forbidden")
			return notify.Channel{}, false
		}
		return ch, true
	}
	if !canRole(RoleFromContext(r.Context()), "manage") {
		writeProblem(w, r, http.StatusForbidden, "Forbidden")
		return notify.Channel{}, false
	}
	return ch, true
}

func (h *NotifyChannelAgentsHandler) agentInWorkspace(r *http.Request, workspaceID, agentID string) bool {
	var one int
	err := h.db.QueryRowContext(r.Context(), `
		SELECT 1 FROM agents a JOIN crews c ON c.id = a.crew_id
		 WHERE a.id = ? AND c.workspace_id = ? AND a.deleted_at IS NULL`,
		agentID, workspaceID).Scan(&one)
	return err == nil
}
