package api

import (
	"database/sql"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/crewship-ai/crewship/internal/notifyroute"
)

// NotifyDeliveriesHandler serves GET /api/v1/notification-deliveries — the
// outbox/delivery-log read backing "why didn't my notification arrive?"
// (issue #1412). Admin-only (ADMIN/OWNER): the log spans every recipient
// in the workspace, not just the caller, so it is gated in-handler with
// the same canRole("manage") check the roleInline notification-channel
// writes use — this is a GET route, which this codebase's route table
// doesn't declare a role for (authedMut is mutation-only), so the check
// has to live here.
type NotifyDeliveriesHandler struct {
	deliveries *notifyroute.DeliveryStore
	logger     *slog.Logger
}

func NewNotifyDeliveriesHandler(db *sql.DB, logger *slog.Logger) *NotifyDeliveriesHandler {
	return &NotifyDeliveriesHandler{deliveries: notifyroute.NewDeliveryStore(db), logger: logger}
}

// List serves GET /api/v1/notification-deliveries?status=&channel_id=&category=&limit=
func (h *NotifyDeliveriesHandler) List(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	if workspaceID == "" {
		replyError(w, http.StatusUnauthorized, "workspace required")
		return
	}
	if !canRole(RoleFromContext(r.Context()), "manage") {
		writeProblem(w, r, http.StatusForbidden, "Forbidden")
		return
	}
	q := r.URL.Query()
	limit := 0
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	rows, err := h.deliveries.List(r.Context(), workspaceID, notifyroute.ListFilter{
		Status: q.Get("status"), ChannelID: q.Get("channel_id"), Category: q.Get("category"), Limit: limit,
	})
	if err != nil {
		h.logger.Error("notify: list deliveries", "err", err)
		replyError(w, http.StatusInternalServerError, "internal")
		return
	}
	if rows == nil {
		rows = []notifyroute.Delivery{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"deliveries": rows})
}
