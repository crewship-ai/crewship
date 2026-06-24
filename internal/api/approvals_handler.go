package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"io"
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
		replyError(w, http.StatusUnauthorized, "workspace required")
		return
	}
	status := harbormaster.Status(r.URL.Query().Get("status"))
	// The documented "?status=all" is shorthand for "no status filter".
	// harbormaster.List treats an empty Status as the all-statuses case,
	// so translate here — literal "all" would otherwise pin the filter
	// to a non-existent status value and return zero rows.
	if status == "all" {
		status = ""
	} else if status == "" {
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
		replyError(w, http.StatusInternalServerError, "list failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rows": rows, "status": status, "count": len(rows)})
}

// Get serves GET /api/v1/approvals/{id}. Returns full request detail
// including payload for the "review this approval" UI.
func (h *ApprovalsHandler) Get(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	if workspaceID == "" {
		replyError(w, http.StatusUnauthorized, "workspace required")
		return
	}
	id := r.PathValue("id")
	row, err := harbormaster.Get(r.Context(), h.db, workspaceID, id)
	if err == harbormaster.ErrNotFound {
		replyError(w, http.StatusNotFound, "not found")
		return
	}
	if err != nil {
		h.logger.Error("approvals get", "err", err)
		replyError(w, http.StatusInternalServerError, "get failed")
		return
	}
	if row == nil {
		// harbormaster.Get returns (nil, nil) for a missing row that
		// didn't map to ErrNotFound (e.g., an ID that's malformed but
		// the driver didn't complain). Surface as 404 so the UI
		// renders the right empty state instead of a successful null
		// response.
		replyError(w, http.StatusNotFound, "not found")
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
		replyError(w, http.StatusUnauthorized, "workspace required")
		return
	}
	user := UserFromContext(r.Context())
	if user == nil {
		replyError(w, http.StatusUnauthorized, "auth required")
		return
	}
	role := RoleFromContext(r.Context())
	if role != "OWNER" && role != "ADMIN" {
		replyError(w, http.StatusForbidden, "approval decisions require OWNER or ADMIN role")
		return
	}
	id := r.PathValue("id")
	var body struct {
		Status  string `json:"status"`
		Comment string `json:"comment"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		replyError(w, http.StatusBadRequest, "bad json")
		return
	}
	status := harbormaster.Status(body.Status)
	if status != harbormaster.StatusApproved && status != harbormaster.StatusDenied {
		replyError(w, http.StatusBadRequest, "status must be approved or denied")
		return
	}
	err := harbormaster.Decide(r.Context(), h.db, h.journal, workspaceID, id, status, user.ID, body.Comment)
	switch err {
	case nil:
		// ok
	case harbormaster.ErrNotFound:
		replyError(w, http.StatusNotFound, "not found")
		return
	case harbormaster.ErrNotPending:
		replyError(w, http.StatusConflict, "already decided")
		return
	default:
		h.logger.Error("approvals decide", "err", err)
		replyError(w, http.StatusInternalServerError, "decide failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": string(status), "decided_by": user.ID})
}

// Cancel serves POST /api/v1/approvals/{id}/cancel with an optional
// {"reason":"..."} body. It withdraws a still-pending approval, moving it
// to the 'cancelled' status (issue #617 — that status was previously
// unreachable from CLI/API; only an agent's internal self-abort hit it).
//
// Requires OWNER or ADMIN, mirroring Decide: withdrawing a gated action is
// an operator-level call, not something an unprivileged member should do.
//
// harbormaster.Cancel is invoked with a nil emitter so its built-in
// ActorAgent journal entry (which models an agent self-withdrawing) is
// suppressed; this path instead emits an operator-attributed (ActorUser)
// entry below, the same handler-emits-audit shape as ResetAutoTuning.
func (h *ApprovalsHandler) Cancel(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	if workspaceID == "" {
		replyError(w, http.StatusUnauthorized, "workspace required")
		return
	}
	user := UserFromContext(r.Context())
	if user == nil {
		replyError(w, http.StatusUnauthorized, "auth required")
		return
	}
	role := RoleFromContext(r.Context())
	if role != "OWNER" && role != "ADMIN" {
		replyError(w, http.StatusForbidden, "cancelling an approval requires OWNER or ADMIN role")
		return
	}
	id := r.PathValue("id")

	// Body is optional — a bare POST cancels with no reason. Only reject a
	// body that is present but malformed.
	var body struct {
		Reason string `json:"reason"`
	}
	if r.Body != nil {
		// An empty body (io.EOF) is fine — reason is optional; only a
		// present-but-malformed body is a 400.
		dec := json.NewDecoder(r.Body)
		if err := dec.Decode(&body); err != nil && !errors.Is(err, io.EOF) {
			replyError(w, http.StatusBadRequest, "bad json")
			return
		}
		// Decode consumes only the first JSON value, so a body like
		// `{"reason":"x"}{"evil":1}` would otherwise slip through. Require the
		// stream to end after the single (optional) payload.
		if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
			replyError(w, http.StatusBadRequest, "unexpected data after JSON body")
			return
		}
	}

	err := harbormaster.Cancel(r.Context(), h.db, nil, workspaceID, id, body.Reason)
	switch err {
	case nil:
		// ok
	case harbormaster.ErrNotFound:
		replyError(w, http.StatusNotFound, "not found")
		return
	case harbormaster.ErrNotPending:
		replyError(w, http.StatusConflict, "approval is not pending (already decided or cancelled)")
		return
	default:
		h.logger.Error("approvals cancel", "err", err)
		replyError(w, http.StatusInternalServerError, "cancel failed")
		return
	}

	// Operator-attributed audit. Pull the (now-cancelled) row for crew/agent/
	// mission refs; tolerate a nil row so a journal lookup miss can't turn a
	// successful cancel into a 500.
	entry := journal.Entry{
		WorkspaceID: workspaceID,
		Type:        journal.EntryApprovalCancelled,
		Severity:    journal.SeverityNotice,
		ActorType:   journal.ActorUser,
		ActorID:     user.ID,
		Summary:     "approval cancelled by operator: " + body.Reason,
		Payload:     map[string]any{"approval_id": id, "reason": body.Reason, "cancelled_by": user.ID},
		Refs:        map[string]any{"approval_id": id},
	}
	if row, _ := harbormaster.Get(r.Context(), h.db, workspaceID, id); row != nil {
		entry.CrewID = row.CrewID
		entry.AgentID = row.AgentID
		entry.MissionID = row.MissionID
	}
	if _, emitErr := h.journal.Emit(r.Context(), entry); emitErr != nil {
		h.logger.Warn("approvals cancel: audit emit failed", "err", emitErr)
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "cancelled", "cancelled_by": user.ID})
}

// ResetAutoTuning serves POST /api/v1/approvals/reset-auto-tuning.
// Body: {"tool": "shell.exec"}. Requires OWNER or ADMIN.
//
// Wipes the rolling reward window for (workspace, tool) so the next
// Gate() call falls back to the operator-requested mode instead of
// the auto-tuned one. Use this when a gate was mis-trained (e.g.
// automation approved on behalf of humans for a while, then humans
// took over — the history still biases approve, and you want it gone).
func (h *ApprovalsHandler) ResetAutoTuning(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	if workspaceID == "" {
		replyError(w, http.StatusUnauthorized, "workspace required")
		return
	}
	role := RoleFromContext(r.Context())
	if role != "OWNER" && role != "ADMIN" {
		replyError(w, http.StatusForbidden, "reset requires OWNER or ADMIN")
		return
	}
	var body struct {
		Tool string `json:"tool"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		replyError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if body.Tool == "" {
		replyError(w, http.StatusBadRequest, "tool required")
		return
	}
	n, err := harbormaster.ResetAutoTuning(r.Context(), h.db, workspaceID, body.Tool)
	if err != nil {
		h.logger.Error("approvals reset auto-tuning", "err", err, "tool", body.Tool)
		replyError(w, http.StatusInternalServerError, "reset failed")
		return
	}

	// Durable audit trail: who reset the rolling reward window, for which
	// tool, and how many rows got wiped. Without this, an operator can
	// silently neutralise auto-tuning and nobody can tell it happened —
	// so the gating history becomes un-auditable for compliance.
	actorID := ""
	if u := UserFromContext(r.Context()); u != nil {
		actorID = u.ID
	}
	if _, emitErr := h.journal.Emit(r.Context(), journal.Entry{
		WorkspaceID: workspaceID,
		Type:        "approval.auto_tuning_reset",
		ActorType:   journal.ActorUser,
		ActorID:     actorID,
		Severity:    journal.SeverityNotice,
		Summary:     "reset auto-tuning for tool=" + body.Tool,
		Payload: map[string]any{
			"tool":         body.Tool,
			"rows_deleted": n,
		},
	}); emitErr != nil {
		// Log only — the reset already happened; a journal write failure
		// shouldn't bubble 500 back to the operator.
		h.logger.Warn("approvals reset auto-tuning: audit emit failed", "err", emitErr)
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"tool":         body.Tool,
		"rows_deleted": n,
		"workspace_id": workspaceID,
	})
}
