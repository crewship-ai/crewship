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
		store:      store,
		dispatcher: notify.NewDispatcher(store, mail, logger),
		mail:       mail,
		logger:     logger,
	}
}

// createChannelRequest is the POST body.
type createChannelRequest struct {
	Type   string   `json:"type"`   // email | webhook
	URL    string   `json:"url"`    // webhook
	To     string   `json:"to"`     // email
	Secret string   `json:"secret"` // webhook (optional; auto-generated when blank)
	Events []string `json:"events"` // completed | failed | all (default: failed)
}

// createChannelResponse returns the created channel and, for webhooks,
// the signing secret THIS ONCE so the caller can configure the receiver.
type createChannelResponse struct {
	notify.Channel
	Secret string `json:"secret,omitempty"`
}

// List serves GET /api/v1/notification-channels.
func (h *NotifyChannelHandler) List(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	if workspaceID == "" {
		replyError(w, http.StatusUnauthorized, "workspace required")
		return
	}
	channels, err := h.store.List(r.Context(), workspaceID)
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

// Create serves POST /api/v1/notification-channels.
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

	userID := ""
	if u := UserFromContext(r.Context()); u != nil {
		userID = u.ID
	}
	ch, err := h.store.Create(r.Context(), notify.ChannelInput{
		WorkspaceID: workspaceID,
		Type:        notify.ChannelType(body.Type),
		URL:         body.URL,
		To:          body.To,
		Secret:      body.Secret,
		Events:      body.Events,
		CreatedBy:   userID,
	})
	if err != nil {
		// Validation failures (bad url, bad type, bad email) are 400.
		replyError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, createChannelResponse{Channel: ch, Secret: ch.Secret})
}

// Delete serves DELETE /api/v1/notification-channels/{id}.
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
