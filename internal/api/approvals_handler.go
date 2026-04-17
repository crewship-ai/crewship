package api

import (
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/crewship-ai/crewship/internal/harbormaster"
	"github.com/crewship-ai/crewship/internal/journal"
)

// ApprovalsHandler serves the Harbor Master HITL inbox. The enqueue
// path fires from inside gate.go when an agent hits a gated tool; this
// handler is strictly reads + decide transitions for the human UI.
type ApprovalsHandler struct {
	db      *sql.DB
	logger  *slog.Logger
	journal journal.Emitter
}

func NewApprovalsHandler(db *sql.DB, logger *slog.Logger, j journal.Emitter) *ApprovalsHandler {
	if j == nil {
		j = noopEmitter{}
	}
	return &ApprovalsHandler{db: db, logger: logger, journal: j}
}

// List serves GET /api/v1/approvals. Filter by ?status=pending
// (default) to drive the inbox, or ?status=all for history. Limit
// defaults to 50, max 200.
func (h *ApprovalsHandler) List(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	if workspaceID == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "workspace required"})
		return
	}
	status := harbormaster.Status(r.URL.Query().Get("status"))
	if status == "" {
		status = harbormaster.StatusPending
	}
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}
	rows, err := harbormaster.List(r.Context(), h.db, workspaceID, status, limit)
	if err != nil {
		h.logger.Error("approvals list", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "list failed"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rows": rows, "status": status, "count": len(rows)})
}

// Get serves GET /api/v1/approvals/{id}. Returns full request detail
// including payload for the "review this approval" UI.
func (h *ApprovalsHandler) Get(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	if workspaceID == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "workspace required"})
		return
	}
	id := r.PathValue("id")
	row, err := harbormaster.Get(r.Context(), h.db, workspaceID, id)
	if err == harbormaster.ErrNotFound {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	if err != nil {
		h.logger.Error("approvals get", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "get failed"})
		return
	}
	writeJSON(w, http.StatusOK, row)
}

// Decide serves POST /api/v1/approvals/{id}/decide with body
// {"status":"approved|denied","comment":"..."}. Only OWNER or ADMIN
// workspace roles may decide — approval gates exist specifically to
// keep high-risk actions out of unprivileged members' hands, so the
// decide path enforces the same bar. The original Decide comment
// claimed role-based gating lived "at the middleware layer" but no
// such middleware wrapped this route; the check is inline here now.
func (h *ApprovalsHandler) Decide(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	if workspaceID == "" {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "workspace required"})
		return
	}
	user := UserFromContext(r.Context())
	if user == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "auth required"})
		return
	}
	role := RoleFromContext(r.Context())
	if role != "OWNER" && role != "ADMIN" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "approval decisions require OWNER or ADMIN role"})
		return
	}
	id := r.PathValue("id")
	var body struct {
		Status  string `json:"status"`
		Comment string `json:"comment"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad json"})
		return
	}
	status := harbormaster.Status(body.Status)
	if status != harbormaster.StatusApproved && status != harbormaster.StatusDenied {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "status must be approved or denied"})
		return
	}
	err := harbormaster.Decide(r.Context(), h.db, h.journal, workspaceID, id, status, user.ID, body.Comment)
	switch err {
	case nil:
		// ok
	case harbormaster.ErrNotFound:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	case harbormaster.ErrNotPending:
		writeJSON(w, http.StatusConflict, map[string]string{"error": "already decided"})
		return
	default:
		h.logger.Error("approvals decide", "err", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "decide failed"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": string(status), "decided_by": user.ID})
}
