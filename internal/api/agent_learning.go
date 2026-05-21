package api

import (
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// PR-G F4.1 UX — per-agent self-learning posture.
//
// Two endpoints:
//
//	GET   /api/v1/agents/{agentId}/learning   — read flag + audit trail
//	PATCH /api/v1/agents/{agentId}/learning   — flip flag (ADMIN+)
//
// The flag is per-agent and orthogonal to the crew's autonomy_level
// (v101). Strict/guided crews can still have a self-learning agent;
// the per-action policy gate (policy.DecideAction) is the upstream
// authority — this flag controls whether ALLOW decisions from the
// keeper evaluators auto-apply OR queue for operator approval.
//
// Audit triple is required on PATCH so an operator can later answer
// "who turned this on, when, and why" — same shape as v101 autonomy.

// LearningHandler owns the learning endpoints. Holds only the DB +
// logger; no policy resolver because this surface doesn't itself
// gate any action — it records the posture; consumers (keeper
// evaluators) read it on-demand.
type LearningHandler struct {
	db     *sql.DB
	logger *slog.Logger
}

// NewLearningHandler constructs the handler. Logger must be non-nil;
// the handler logs every flip for compliance audit.
func NewLearningHandler(db *sql.DB, logger *slog.Logger) *LearningHandler {
	return &LearningHandler{db: db, logger: logger}
}

// learningResponse is the wire shape for GET and PATCH.
type learningResponse struct {
	AgentID     string  `json:"agent_id"`
	Enabled     bool    `json:"enabled"`
	SetByUserID *string `json:"set_by_user_id,omitempty"`
	SetAt       *string `json:"set_at,omitempty"`
	Reason      *string `json:"reason,omitempty"`
}

// learningUpdateBody is the PATCH payload. Both fields are required:
// reason because every flip is audit-relevant (no row with "set by
// X at Y for ”"), and enabled because the client must be explicit
// about which direction it's flipping the flag.
//
// Enabled is a *bool, not a plain bool, so the handler can
// distinguish "field missing or null" (reject as 400) from
// "field explicitly false" (turn self-learning OFF). With a plain
// bool, an operator PATCH like {"reason":"trim audit noise"} would
// decode as Enabled=false and silently disable self-learning —
// CodeRabbit round-8 catch. Same pattern as v107 reason CHECK:
// audit-critical fields must be explicit.
type learningUpdateBody struct {
	Enabled *bool  `json:"enabled"`
	Reason  string `json:"reason"`
}

// Get returns the current flag + audit trail. Any authenticated
// member of the workspace can read; the value is non-secret
// diagnostic state.
func (h *LearningHandler) Get(w http.ResponseWriter, r *http.Request) {
	agentID := r.PathValue("agentId")
	if agentID == "" {
		replyError(w, http.StatusBadRequest, "agent id required")
		return
	}
	wsID := WorkspaceIDFromContext(r.Context())

	var (
		enabled int
		setBy   sql.NullString
		setAt   sql.NullString
		reason  sql.NullString
	)
	err := h.db.QueryRowContext(r.Context(), `
		SELECT self_learning_enabled,
		       self_learning_set_by_user_id,
		       self_learning_set_at,
		       self_learning_reason
		FROM agents
		WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL`,
		agentID, wsID,
	).Scan(&enabled, &setBy, &setAt, &reason)
	if errors.Is(err, sql.ErrNoRows) {
		replyError(w, http.StatusNotFound, "Agent not found")
		return
	}
	if err != nil {
		h.logger.Error("learning: load row", "agent_id", agentID, "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	resp := learningResponse{
		AgentID: agentID,
		Enabled: enabled == 1,
	}
	if setBy.Valid {
		resp.SetByUserID = &setBy.String
	}
	if setAt.Valid {
		resp.SetAt = &setAt.String
	}
	if reason.Valid {
		resp.Reason = &reason.String
	}
	writeJSON(w, http.StatusOK, resp)
}

// Patch flips the flag. Requires ADMIN+ (canRole "manage") because
// self-learning weakens the inbox approval pattern — the operator
// who turns it on must be senior enough to own the consequences.
// Reason field is non-optional; audit trail is the whole point.
func (h *LearningHandler) Patch(w http.ResponseWriter, r *http.Request) {
	role := RoleFromContext(r.Context())
	if !canRole(role, "manage") {
		replyError(w, http.StatusForbidden, "Forbidden")
		return
	}
	agentID := r.PathValue("agentId")
	if agentID == "" {
		replyError(w, http.StatusBadRequest, "agent id required")
		return
	}
	wsID := WorkspaceIDFromContext(r.Context())

	var body learningUpdateBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		replyError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	body.Reason = strings.TrimSpace(body.Reason)
	if body.Reason == "" {
		replyError(w, http.StatusBadRequest, "reason is required (audit trail)")
		return
	}
	if body.Enabled == nil {
		replyError(w, http.StatusBadRequest, "enabled is required (true or false; omitting it would silently disable self-learning on a payload that only meant to update reason)")
		return
	}

	user := UserFromContext(r.Context())
	if user == nil {
		replyError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	enabled := 0
	if *body.Enabled {
		enabled = 1
	}
	now := time.Now().UTC().Format(time.RFC3339)

	// Atomic UPDATE with WHERE guard so a concurrent soft-delete or
	// workspace-id mismatch returns 404 rather than silently writing
	// nothing. RowsAffected is the only safe race-window detector.
	res, err := h.db.ExecContext(r.Context(), `
		UPDATE agents
		SET self_learning_enabled = ?,
		    self_learning_set_by_user_id = ?,
		    self_learning_set_at = ?,
		    self_learning_reason = ?
		WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL`,
		enabled, user.ID, now, body.Reason,
		agentID, wsID,
	)
	if err != nil {
		h.logger.Error("learning: update row", "agent_id", agentID, "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	n, err := res.RowsAffected()
	if err != nil {
		h.logger.Error("learning: rows affected", "agent_id", agentID, "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	if n == 0 {
		replyError(w, http.StatusNotFound, "Agent not found")
		return
	}

	// Log fact-of-flip only; never the free-form reason text — it can
	// contain operator-entered PII / business context and centralized
	// logs are a different trust boundary than the DB audit row. The
	// reason is still persisted on the agents row above for audit.
	h.logger.Info("self_learning flipped",
		"agent_id", agentID,
		"workspace_id", wsID,
		"enabled", *body.Enabled,
		"by_user_id", user.ID,
		"reason_len", len(body.Reason),
	)

	setBy := user.ID
	setAt := now
	reason := body.Reason
	writeJSON(w, http.StatusOK, learningResponse{
		AgentID:     agentID,
		Enabled:     *body.Enabled,
		SetByUserID: &setBy,
		SetAt:       &setAt,
		Reason:      &reason,
	})
}
