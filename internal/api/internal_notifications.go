package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/mailer"
	"github.com/crewship-ai/crewship/internal/notify"
	"github.com/crewship-ai/crewship/internal/notifyroute"
)

// AgentNotifyHandler serves the agent-initiated send path: an agent inside a
// container asking Crewship to deliver a message to a notification channel it
// has been paired with.
//
// Four gates, in order, because each one fails for a different reason and the
// agent should be told which:
//
//  1. The agent exists in the claimed workspace and the calling token is
//     bound to its crew — an agent cannot send as someone else.
//  2. The channel is paired to THIS agent. Default-deny; a human grants it.
//  3. The per-agent rate gate. One chatty agent must not be able to flood a
//     Slack channel the whole team reads.
//  4. The message goes through the secret scrubber, because agent-authored
//     text is untrusted output that is about to leave the instance.
type AgentNotifyHandler struct {
	db         *sql.DB
	channels   *notify.ChannelStore
	pairings   *notify.PairingStore
	dispatcher *notify.Dispatcher
	limiter    *notifyroute.RateLimiter
	journal    journal.Emitter
	logger     *slog.Logger
}

// NewAgentNotifyHandler wires the handler.
//
// The rate limiter is deliberately much tighter than the human-facing one
// (which is burst 5, refill 1/30s per recipient×channel×category). An agent
// in a loop can call a tool hundreds of times a minute without noticing, and
// the blast radius is a channel other people are reading. Burst 3, refill
// 1/60s gives an agent room to report a result and a follow-up, and stops
// anything resembling a loop within a minute.
func NewAgentNotifyHandler(db *sql.DB, mail mailer.Mailer, j journal.Emitter, logger *slog.Logger) *AgentNotifyHandler {
	if mail == nil {
		mail = mailer.Disabled{}
	}
	store := notify.NewChannelStore(db)
	return &AgentNotifyHandler{
		db:         db,
		channels:   store,
		pairings:   notify.NewPairingStore(db),
		dispatcher: notify.NewDispatcher(store, mail, logger, db),
		limiter:    notifyroute.NewRateLimiter(3, 1.0/60.0),
		journal:    j,
		logger:     logger,
	}
}

type agentNotifySendRequest struct {
	WorkspaceID string `json:"workspace_id"`
	AgentID     string `json:"agent_id"`
	CrewID      string `json:"crew_id"`
	ChannelID   string `json:"channel_id"`
	Title       string `json:"title"`
	Body        string `json:"body"`
}

// agentNotifyBodyCap bounds the message an agent can send. Generous enough
// for a real status report, small enough that a runaway agent cannot page a
// megabyte into someone's phone.
const agentNotifyBodyCap = 8 << 10 // 8 KiB

// ListChannels serves GET /api/v1/internal/notifications/channels — the
// channels the calling agent may post to.
//
// Discovery exists so an agent is TOLD what it has instead of guessing a
// channel id and learning from a permission error. It returns ids, types and
// provider names only: an agent has no business knowing a channel's
// destination address, just that one exists.
func (h *AgentNotifyHandler) ListChannels(w http.ResponseWriter, r *http.Request) {
	workspaceID := strings.TrimSpace(r.URL.Query().Get("workspace_id"))
	agentID := strings.TrimSpace(r.URL.Query().Get("agent_id"))
	crewID := strings.TrimSpace(r.URL.Query().Get("crew_id"))
	if workspaceID == "" || agentID == "" {
		replyError(w, http.StatusBadRequest, "workspace_id and agent_id are required")
		return
	}
	if !h.assertAgentIdentity(w, r, workspaceID, crewID, agentID) {
		return
	}
	channels, err := h.pairings.ListForAgent(r.Context(), workspaceID, agentID)
	if err != nil {
		h.logger.Error("notify: list agent channels", "err", err, "agent_id", agentID)
		replyError(w, http.StatusInternalServerError, "internal")
		return
	}
	out := make([]map[string]any, 0, len(channels))
	for _, c := range channels {
		out = append(out, map[string]any{
			"channel_id": c.ID,
			"type":       string(c.Type),
			"provider":   c.Provider,
			"enabled":    c.Enabled,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"channels": out})
}

// Send serves POST /api/v1/internal/notifications/send.
func (h *AgentNotifyHandler) Send(w http.ResponseWriter, r *http.Request) {
	// /api/v1/internal/* bypasses the global body cap.
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var req agentNotifySendRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		replyError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	req.Title = strings.TrimSpace(req.Title)
	req.Body = strings.TrimSpace(req.Body)

	if req.WorkspaceID == "" || req.AgentID == "" || req.ChannelID == "" {
		replyError(w, http.StatusBadRequest, "workspace_id, agent_id and channel_id are required")
		return
	}
	if req.Title == "" {
		replyError(w, http.StatusBadRequest, "title is required")
		return
	}
	if len(req.Body) > agentNotifyBodyCap {
		replyError(w, http.StatusBadRequest, "body too large (max 8KB)")
		return
	}

	// Gate 1 — identity.
	if !h.assertAgentIdentity(w, r, req.WorkspaceID, req.CrewID, req.AgentID) {
		return
	}

	// Gate 2 — pairing. Default-deny, and the error says exactly what a human
	// has to do, because the agent cannot fix this itself.
	paired, err := h.pairings.IsPaired(r.Context(), req.WorkspaceID, req.ChannelID, req.AgentID)
	if err != nil {
		h.logger.Error("notify: check agent pairing", "err", err, "agent_id", req.AgentID)
		replyError(w, http.StatusInternalServerError, "internal")
		return
	}
	if !paired {
		replyError(w, http.StatusForbidden,
			"this agent is not paired with that notification channel — a workspace admin must grant it "+
				"(crewship notifychannel agents allow <channel-id> --agent <agent-id>)")
		return
	}

	// Gate 3 — rate. Keyed on the agent rather than a recipient: the thing
	// being protected is the channel's readers, and every agent send to a
	// given channel competes for the same bucket regardless of who reads it.
	if !h.limiter.Allow(req.AgentID, req.ChannelID, "agent") {
		replyError(w, http.StatusTooManyRequests,
			"too many notifications from this agent to this channel; wait before sending again")
		return
	}

	ch, err := h.channels.GetForDispatch(r.Context(), req.WorkspaceID, req.ChannelID)
	if err != nil {
		if errors.Is(err, notify.ErrNotFound) {
			replyError(w, http.StatusNotFound, "channel not found")
			return
		}
		h.logger.Error("notify: resolve channel for agent send", "err", err, "channel_id", req.ChannelID)
		replyError(w, http.StatusInternalServerError, "internal")
		return
	}
	if !ch.Enabled {
		replyError(w, http.StatusBadRequest, "that channel is disabled")
		return
	}

	// Gate 4 — scrub. DeliverCategoryMessage scrubs the body; the TITLE is
	// agent-authored too and would otherwise leave unscrubbed, so it is
	// scrubbed here rather than trusting the delivery path to cover a field
	// it was never asked to.
	msg := notify.CategoryMessage{
		WorkspaceID: req.WorkspaceID,
		Category:    notify.CategoryAgentsMessage,
		Title:       notify.ScrubText(req.Title),
		Body:        req.Body,
		Priority:    "medium",
		SourceKind:  "agent",
		SourceID:    req.AgentID,
	}

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	if err := h.dispatcher.DeliverCategoryMessage(ctx, ch, msg); err != nil {
		h.emitJournal(ctx, req, ch, journal.EntryNotificationFailed, journal.SeverityError, err.Error())
		replyError(w, http.StatusBadGateway, "send failed: "+err.Error())
		return
	}
	h.emitJournal(ctx, req, ch, journal.EntryNotificationDelivered, journal.SeverityInfo, "")
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "channel_id": req.ChannelID})
}

// assertAgentIdentity verifies the agent exists in the claimed workspace and,
// when the calling token is crew-bound, that the agent belongs to that crew.
//
// Without this an agent could send as any agent id it cared to name, which
// would make the pairing table decorative — the whole gate is "which agent is
// this", so establishing that first is what makes the rest mean anything.
func (h *AgentNotifyHandler) assertAgentIdentity(w http.ResponseWriter, r *http.Request, workspaceID, crewID, agentID string) bool {
	var (
		actualWorkspace string
		actualCrew      string
	)
	err := h.db.QueryRowContext(r.Context(),
		`SELECT c.workspace_id, a.crew_id
		   FROM agents a JOIN crews c ON c.id = a.crew_id
		  WHERE a.id = ? AND a.deleted_at IS NULL`, agentID).
		Scan(&actualWorkspace, &actualCrew)
	if err == sql.ErrNoRows {
		replyError(w, http.StatusForbidden, "unknown agent")
		return false
	}
	if err != nil {
		h.logger.Error("notify: resolve agent for send", "err", err, "agent_id", agentID)
		replyError(w, http.StatusInternalServerError, "internal")
		return false
	}
	if actualWorkspace != workspaceID {
		replyError(w, http.StatusForbidden, "agent does not belong to the specified workspace")
		return false
	}
	// A crew-bound token may only act as an agent of its own crew — the same
	// rule the crew-messaging path enforces, for the same reason: a
	// compromised sidecar must not be able to speak as a sibling crew.
	claimed := actualCrew
	if !assertBoundCrewWorkspaceDB(w, r, h.db, h.logger, &claimed) {
		return false
	}
	if crewID != "" && crewID != actualCrew {
		replyError(w, http.StatusForbidden, "agent does not belong to the specified crew")
		return false
	}
	return true
}

// emitJournal records the send on the Activity timeline, with the agent as
// the actor so "who sent this to Slack?" has an answer.
func (h *AgentNotifyHandler) emitJournal(ctx context.Context, req agentNotifySendRequest, ch notify.Channel, t journal.EntryType, sev journal.Severity, detail string) {
	if h.journal == nil {
		return
	}
	target := string(ch.Type)
	if ch.Provider != "" {
		target = ch.Provider
	}
	payload := map[string]any{
		"channel_id":   ch.ID,
		"channel_type": string(ch.Type),
		"target":       target,
		"category":     notify.CategoryAgentsMessage,
		"title":        req.Title,
		"agent_id":     req.AgentID,
	}
	if detail != "" {
		payload["detail"] = detail
	}
	summary := "Agent sent a notification to " + target
	if t == journal.EntryNotificationFailed {
		summary = "Agent notification to " + target + " failed: " + detail
	}
	if _, err := h.journal.Emit(ctx, journal.Entry{
		WorkspaceID: req.WorkspaceID,
		CrewID:      req.CrewID,
		AgentID:     req.AgentID,
		Type:        t,
		Severity:    sev,
		ActorType:   journal.ActorAgent,
		ActorID:     req.AgentID,
		Summary:     summary,
		Payload:     payload,
	}); err != nil {
		h.logger.Warn("notify: emit agent send journal entry", "error", err)
	}
}
