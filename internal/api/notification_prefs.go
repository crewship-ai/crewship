package api

import (
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/crewship-ai/crewship/internal/notifyroute"
)

// NotifyPrefsHandler serves GET/PUT /api/v1/me/notification-prefs — the
// authenticated caller's OWN category x channel preference matrix
// (issue #1412). Self-scoped by construction (PrefStore is always called
// with the caller's own user id from context, never a path/body id), so
// this is registered roleSelf.
type NotifyPrefsHandler struct {
	prefs  *notifyroute.PrefStore
	logger *slog.Logger
}

func NewNotifyPrefsHandler(db *sql.DB, logger *slog.Logger) *NotifyPrefsHandler {
	return &NotifyPrefsHandler{prefs: notifyroute.NewPrefStore(db), logger: logger}
}

// Get serves GET /api/v1/me/notification-prefs.
func (h *NotifyPrefsHandler) Get(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	u := UserFromContext(r.Context())
	if workspaceID == "" || u == nil {
		replyError(w, http.StatusUnauthorized, "workspace + auth required")
		return
	}
	cells, err := h.prefs.Get(r.Context(), workspaceID, u.ID)
	if err != nil {
		h.logger.Error("notify: get prefs", "err", err)
		replyError(w, http.StatusInternalServerError, "internal")
		return
	}
	if cells == nil {
		cells = []notifyroute.PrefCell{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"cells": cells})
}

// putPrefsRequest is the PUT body: a full or partial batch of cells to
// upsert. Cells not included are left as whatever they already were
// (PUT here means "upsert these cells," not "replace the whole matrix" —
// a partial UI edit, e.g. one cell toggled, shouldn't require resending
// every other cell to avoid clobbering them).
type putPrefsRequest struct {
	Cells []notifyroute.PrefCell `json:"cells"`
}

// Put serves PUT /api/v1/me/notification-prefs.
func (h *NotifyPrefsHandler) Put(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	u := UserFromContext(r.Context())
	if workspaceID == "" || u == nil {
		replyError(w, http.StatusUnauthorized, "workspace + auth required")
		return
	}
	var body putPrefsRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		replyError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if len(body.Cells) == 0 {
		replyError(w, http.StatusBadRequest, "cells required")
		return
	}
	if err := h.prefs.Set(r.Context(), workspaceID, u.ID, body.Cells); err != nil {
		replyError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
