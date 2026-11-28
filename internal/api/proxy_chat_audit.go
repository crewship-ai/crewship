package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/crewship-ai/crewship/internal/chataudience"
	"github.com/crewship-ai/crewship/internal/costvis"
)

// ChatRunAudit returns the private audit capture for one run of a readable chat.
func (h *ProxyHandler) ChatRunAudit(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := UserFromContext(ctx)
	workspace := WorkspaceIDFromContext(ctx)
	chatID, runID := r.PathValue("chatId"), r.PathValue("runId")
	if user == nil {
		replyError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}
	if !canRole(RoleFromContext(ctx), "read") {
		replyError(w, http.StatusForbidden, "Forbidden")
		return
	}
	allowed, err := chataudience.CanReadInWorkspace(ctx, h.db, chatID, user.ID, workspace)
	if err != nil {
		replyInternalError(w, h.logger, "check chat audit audience", err)
		return
	}
	if !allowed {
		replyError(w, http.StatusNotFound, "Chat not found")
		return
	}
	var status string
	var wire []byte
	err = h.db.QueryRowContext(ctx, `SELECT status, wire
		FROM chat_run_audits WHERE chat_id = ? AND run_id = ?`, chatID, runID).Scan(&status, &wire)
	if errors.Is(err, sql.ErrNoRows) {
		replyError(w, http.StatusNotFound, "Run audit not found")
		return
	}
	if err != nil {
		replyInternalError(w, h.logger, "read chat run audit", err)
		return
	}
	allowed, err = chataudience.CanReadArtifactConversation(ctx, h.db, chatID, user.ID, workspace, true)
	if err != nil {
		replyInternalError(w, h.logger, "authorize chat run audit", err)
		return
	}
	if !allowed {
		replyError(w, http.StatusNotFound, "Chat not found")
		return
	}
	if len(wire) > 0 && !canReadCosts(ctx) {
		wire, err = costvis.RedactJSON(wire)
		if err != nil {
			replyInternalError(w, h.logger, "filter chat run audit costs", err)
			return
		}
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, struct {
		Status string          `json:"status"`
		Events json.RawMessage `json:"events,omitempty"`
	}{status, wire})
}
