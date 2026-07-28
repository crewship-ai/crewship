package api

import (
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/crewship-ai/crewship/internal/notify"
)

// NotifyTemplateHandler serves the per-category message templates: what the
// notifications Crewship generates itself actually SAY.
//
// Workspace-scoped, not self-scoped like the preference matrix. A preference
// is "do I want to hear about this"; a template is "what does everyone see",
// so it is an ADMIN concern — one person's rewording reaches every recipient
// in the workspace. The routes are registered ADMIN+ for that reason.
type NotifyTemplateHandler struct {
	templates *notify.TemplateStore
	logger    *slog.Logger
}

func NewNotifyTemplateHandler(db *sql.DB, logger *slog.Logger) *NotifyTemplateHandler {
	return &NotifyTemplateHandler{templates: notify.NewTemplateStore(db), logger: logger}
}

// notifyTemplateResponse is one template on the wire.
//
// Snake_case, and spelled out field by field rather than marshalling
// notify.MessageTemplate directly — widening that struct later must not
// silently add a field to a public payload.
type notifyTemplateResponse struct {
	Category  string `json:"category"`
	ChannelID string `json:"channel_id,omitempty"`
	Title     string `json:"title"`
	Body      string `json:"body"`
}

// List serves GET /api/v1/notification-templates.
func (h *NotifyTemplateHandler) List(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	if workspaceID == "" {
		replyError(w, http.StatusUnauthorized, "workspace required")
		return
	}
	rows, err := h.templates.List(r.Context(), workspaceID)
	if err != nil {
		h.logger.Error("notify: list templates", "err", err)
		replyError(w, http.StatusInternalServerError, "internal")
		return
	}
	out := make([]notifyTemplateResponse, 0, len(rows))
	for _, t := range rows {
		out = append(out, notifyTemplateResponse{
			Category: t.Category, ChannelID: t.ChannelID, Title: t.Title, Body: t.Body,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"templates": out})
}

// putNotifyTemplateRequest is the PUT body. Clearing both fields removes the
// override — an operator emptying the form means "go back to the default",
// which is a state, not a separate verb.
type putNotifyTemplateRequest struct {
	Category  string `json:"category"`
	ChannelID string `json:"channel_id"`
	Title     string `json:"title"`
	Body      string `json:"body"`
}

// Put serves PUT /api/v1/notification-templates.
func (h *NotifyTemplateHandler) Put(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	if workspaceID == "" {
		replyError(w, http.StatusUnauthorized, "workspace required")
		return
	}
	var body putNotifyTemplateRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		replyError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	t := notify.MessageTemplate{
		Category: body.Category, ChannelID: body.ChannelID,
		Title: body.Title, Body: body.Body,
	}
	// Validation errors are the operator's to fix and name the exact problem
	// (an unknown category, a reference to a namespace that does not exist),
	// so they are returned rather than logged as an internal fault.
	if err := notify.ValidateTemplate(t); err != nil {
		replyError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := h.templates.Upsert(r.Context(), workspaceID, t); err != nil {
		h.logger.Error("notify: save template", "err", err, "category", body.Category)
		replyError(w, http.StatusInternalServerError, "internal")
		return
	}
	writeJSON(w, http.StatusOK, notifyTemplateResponse{
		Category: t.Category, ChannelID: t.ChannelID, Title: t.Title, Body: t.Body,
	})
}

// Delete serves DELETE /api/v1/notification-templates?category=…&channel_id=…
//
// Deleting one that is not there succeeds: the caller asked for a state and
// that state holds, and a 404 here would make a CLI that tidies up noisier
// without being more correct.
func (h *NotifyTemplateHandler) Delete(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	if workspaceID == "" {
		replyError(w, http.StatusUnauthorized, "workspace required")
		return
	}
	category := r.URL.Query().Get("category")
	if category == "" {
		replyError(w, http.StatusBadRequest, "category is required")
		return
	}
	if err := h.templates.Delete(r.Context(), workspaceID, category, r.URL.Query().Get("channel_id")); err != nil {
		h.logger.Error("notify: delete template", "err", err, "category", category)
		replyError(w, http.StatusInternalServerError, "internal")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
