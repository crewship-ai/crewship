package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/mailer"
	"github.com/crewship-ai/crewship/internal/notify"
)

// NotifyChannelHandler serves the workspace-scoped notification-channel
// API (issue #850): CRUD over outbound e-mail / signed-webhook targets
// plus a test-send. Writes are MANAGER+ (enforced by the route table);
// the handler scopes every operation to the caller's workspace context.
type NotifyChannelHandler struct {
	db         *sql.DB
	store      *notify.ChannelStore
	dispatcher *notify.Dispatcher
	mail       mailer.Mailer
	logger     *slog.Logger
}

// NewNotifyChannelHandler wires the handler. mail is used both to reject
// e-mail channels when no transport is configured (fail-closed) and to
// deliver the test send.
func NewNotifyChannelHandler(db *sql.DB, mail mailer.Mailer, logger *slog.Logger) *NotifyChannelHandler {
	if mail == nil {
		mail = mailer.Disabled{}
	}
	store := notify.NewChannelStore(db)
	return &NotifyChannelHandler{
		db:         db,
		store:      store,
		dispatcher: notify.NewDispatcher(store, mail, logger, db),
		mail:       mail,
		logger:     logger,
	}
}

// createChannelRequest is the POST body.
type createChannelRequest struct {
	Type   string   `json:"type"`   // email | webhook | shoutrrr
	URL    string   `json:"url"`    // webhook
	To     string   `json:"to"`     // email
	Secret string   `json:"secret"` // webhook (optional; auto-generated when blank)
	Events []string `json:"events"` // completed | failed | all (default: failed) — legacy #850 path

	Provider    string   `json:"provider,omitempty"`     // slack | discord | telegram (type=shoutrrr)
	ShoutrrrURL string   `json:"shoutrrr_url,omitempty"` // Apprise-style service url (type=shoutrrr)
	Personal    bool     `json:"personal,omitempty"`     // true = a member's own channel (scope=user)
	Categories  []string `json:"categories,omitempty"`   // admin allowlist; empty = every category
	MinPriority string   `json:"min_priority,omitempty"` // low|medium|high|urgent; default low
}

// createChannelResponse returns the created channel and, for webhooks,
// the signing secret THIS ONCE so the caller can configure the receiver.
type createChannelResponse struct {
	notify.Channel
	Secret string `json:"secret,omitempty"`
}

// List serves GET /api/v1/notification-channels. Every workspace-scoped
// channel plus the CALLER's own personal channels — never another
// member's (see ChannelStore.List).
func (h *NotifyChannelHandler) List(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	if workspaceID == "" {
		replyError(w, http.StatusUnauthorized, "workspace required")
		return
	}
	userID := ""
	if u := UserFromContext(r.Context()); u != nil {
		userID = u.ID
	}
	channels, err := h.store.List(r.Context(), workspaceID, userID)
	if err != nil {
		h.logger.Error("notify: list channels", "err", err, "workspace_id", workspaceID)
		replyError(w, http.StatusInternalServerError, "list failed")
		return
	}
	if channels == nil {
		channels = []notify.Channel{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"channels": channels})
}

// Create serves POST /api/v1/notification-channels. Registered roleInline
// (#1412): a WORKSPACE-scoped channel requires MANAGER+ (tightened from
// the pre-#1412 roleCreate floor per the issue's explicit instruction — a
// MEMBER could previously stand up a workspace-wide delivery target); a
// PERSONAL channel (personal=true) is self-service — any authenticated
// member may add their OWN Slack/Telegram/webhook, gated only by
// ownership (owner_user_id is always set to the caller, never a
// client-supplied id).
func (h *NotifyChannelHandler) Create(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	if workspaceID == "" {
		replyError(w, http.StatusUnauthorized, "workspace required")
		return
	}
	var body createChannelRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		replyError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	// Fail closed: an e-mail channel is useless (and silently drops
	// notifications) if no mail transport is configured. Reject at create
	// so the operator learns immediately, not on the first failed run.
	if notify.ChannelType(body.Type) == notify.ChannelEmail && !h.mail.Configured() {
		replyError(w, http.StatusBadRequest, "email delivery is not configured on this instance; set RESEND_API_KEY/RESEND_FROM before adding an email channel")
		return
	}
	// Fail closed for a shoutrrr provider the admin disabled instance-wide
	// (#1412 providers registry) — same "reject at create, not first send"
	// posture as the email-transport check above.
	if notify.ChannelType(body.Type) == notify.ChannelShoutrrr {
		enabled, err := providerEnabled(r.Context(), h.db, body.Provider)
		if err != nil {
			h.logger.Error("notify: check provider enabled", "err", err, "provider", body.Provider)
			replyError(w, http.StatusInternalServerError, "internal")
			return
		}
		if !enabled {
			replyError(w, http.StatusBadRequest, "provider \""+body.Provider+"\" is disabled on this instance")
			return
		}
	}

	userID := ""
	if u := UserFromContext(r.Context()); u != nil {
		userID = u.ID
	}

	scope := notify.ScopeWorkspace
	ownerUserID := ""
	if body.Personal {
		scope = notify.ScopeUser
		ownerUserID = userID // ALWAYS the authenticated caller — never client-supplied
		if ownerUserID == "" {
			replyError(w, http.StatusUnauthorized, "unauthenticated")
			return
		}
	} else if !canRole(RoleFromContext(r.Context()), "manage") {
		// Workspace-scoped channel: MANAGER can no longer write here post-
		// #1412 (tightened to roleManage, OWNER/ADMIN only) — the route
		// itself is roleInline so this check is the enforcement point.
		writeProblem(w, r, http.StatusForbidden, "Forbidden")
		return
	}

	ch, err := h.store.Create(r.Context(), notify.ChannelInput{
		WorkspaceID: workspaceID,
		Type:        notify.ChannelType(body.Type),
		URL:         body.URL,
		To:          body.To,
		Secret:      body.Secret,
		Events:      body.Events,
		CreatedBy:   userID,
		Provider:    body.Provider,
		ShoutrrrURL: body.ShoutrrrURL,
		Scope:       scope,
		OwnerUserID: ownerUserID,
		Categories:  body.Categories,
		MinPriority: body.MinPriority,
	})
	if err != nil {
		// Validation failures (bad url, bad type, bad email) are 400.
		replyError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, createChannelResponse{Channel: ch, Secret: ch.Secret})
}

// authorizeChannelWrite loads the channel and enforces the same
// workspace-vs-personal split as Create: a workspace-scoped channel
// requires MANAGER+; a personal (scope=user) channel requires the caller
// to be its owner. Returns the channel and whether the caller is
// authorized; on failure it has already written the HTTP response.
func (h *NotifyChannelHandler) authorizeChannelWrite(w http.ResponseWriter, r *http.Request, workspaceID, id string) (notify.Channel, bool) {
	ch, err := h.store.Get(r.Context(), workspaceID, id)
	if err != nil {
		if errors.Is(err, notify.ErrNotFound) {
			replyError(w, http.StatusNotFound, "channel not found")
		} else {
			h.logger.Error("notify: resolve channel", "err", err, "id", id)
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

// Delete serves DELETE /api/v1/notification-channels/{id}. Registered
// roleInline for the same reason as Create — see authorizeChannelWrite.
func (h *NotifyChannelHandler) Delete(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	if workspaceID == "" {
		replyError(w, http.StatusUnauthorized, "workspace required")
		return
	}
	id := r.PathValue("id")
	if id == "" {
		replyError(w, http.StatusBadRequest, "channel id required")
		return
	}
	if _, ok := h.authorizeChannelWrite(w, r, workspaceID, id); !ok {
		return
	}
	ok, err := h.store.Delete(r.Context(), workspaceID, id)
	if err != nil {
		h.logger.Error("notify: delete channel", "err", err, "id", id)
		replyError(w, http.StatusInternalServerError, "delete failed")
		return
	}
	if !ok {
		replyError(w, http.StatusNotFound, "channel not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted": id})
}

// patchChannelRequest is the PATCH body — every field optional; only the
// fields present are applied.
type patchChannelRequest struct {
	Enabled     *bool     `json:"enabled,omitempty"`
	Categories  *[]string `json:"categories,omitempty"`
	MinPriority *string   `json:"min_priority,omitempty"`
	Events      *[]string `json:"events,omitempty"`
}

// Patch serves PATCH /api/v1/notification-channels/{id} — enable/disable,
// change the admin category allowlist, priority floor, or (legacy path)
// subscribed events. Registered roleInline; see authorizeChannelWrite.
func (h *NotifyChannelHandler) Patch(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	if workspaceID == "" {
		replyError(w, http.StatusUnauthorized, "workspace required")
		return
	}
	id := r.PathValue("id")
	if id == "" {
		replyError(w, http.StatusBadRequest, "channel id required")
		return
	}
	if _, ok := h.authorizeChannelWrite(w, r, workspaceID, id); !ok {
		return
	}
	var body patchChannelRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		replyError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	ok, err := h.store.Patch(r.Context(), workspaceID, id, notify.PatchInput{
		Enabled: body.Enabled, Categories: body.Categories, MinPriority: body.MinPriority, Events: body.Events,
	})
	if err != nil {
		replyError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !ok {
		replyError(w, http.StatusNotFound, "channel not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"updated": id})
}

// Test serves POST /api/v1/notification-channels/{id}/test — sends a
// synthetic run.completed event to the one channel so an operator can
// confirm the receiver/secret before relying on it.
func (h *NotifyChannelHandler) Test(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	if workspaceID == "" {
		replyError(w, http.StatusUnauthorized, "workspace required")
		return
	}
	id := r.PathValue("id")
	if id == "" {
		replyError(w, http.StatusBadRequest, "channel id required")
		return
	}
	if _, ok := h.authorizeChannelWrite(w, r, workspaceID, id); !ok {
		return
	}
	ch, err := h.store.GetForDispatch(r.Context(), workspaceID, id)
	if err != nil {
		if errors.Is(err, notify.ErrNotFound) {
			replyError(w, http.StatusNotFound, "channel not found")
			return
		}
		h.logger.Error("notify: resolve channel for test", "err", err, "id", id)
		replyError(w, http.StatusInternalServerError, "resolve failed")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	if err := h.dispatcher.DispatchOne(ctx, ch, notify.NotificationEvent{
		Type:          notify.EventRunCompleted,
		WorkspaceID:   workspaceID,
		RunID:         "test",
		RoutineSlug:   "test-notification",
		Status:        "completed",
		OutputPreview: "This is a Crewship test notification.",
	}); err != nil {
		replyError(w, http.StatusBadGateway, "test send failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "channel_id": id})
}
