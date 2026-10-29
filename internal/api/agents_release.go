package api

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// agentReleasedResponse is the body of POST /api/v1/agents/{agentId}/release.
type agentReleasedResponse struct {
	ID        string `json:"id"`
	ExpiredAt string `json:"expired_at"`
	Released  bool   `json:"released"`
}

// Release ends a temporary hire now instead of at its expiry. The agent
// becomes an expired hire exactly as the expiry sweeper would make it: the row,
// its runs and notes stay and Rehire brings it back. Releasing an expired
// hire changes nothing and succeeds. A hire that is running is refused (stop
// the run first; the sweeper gives a running hire the same grace) and so is
// one that still waits for approval (that is Deny, not Release).
// POST /api/v1/agents/{agentId}/release
func (h *AgentHandler) Release(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	agentID := r.PathValue("agentId")
	if !canRole(RoleFromContext(r.Context()), "create") {
		replyError(w, http.StatusForbidden, "Forbidden")
		return
	}

	var (
		crewID, name, status string
		isEphemeral          int
		expiredAt            sql.NullString
		hireReason           sql.NullString
	)
	err := h.db.QueryRowContext(r.Context(), `
		SELECT COALESCE(crew_id, ''), name, status, ephemeral, expired_at, hire_reason
		FROM agents
		WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL`,
		agentID, workspaceID).Scan(&crewID, &name, &status, &isEphemeral, &expiredAt, &hireReason)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && isEphemeral != 1) {
		replyError(w, http.StatusNotFound, "Agent not found or not a temporary hire")
		return
	}
	if err != nil {
		h.logger.Error("release: load agent", "error", err, "agent_id", agentID)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	if expiredAt.Valid && expiredAt.String != "" {
		writeJSON(w, http.StatusOK, agentReleasedResponse{ID: agentID, ExpiredAt: expiredAt.String})
		return
	}
	switch status {
	case "RUNNING":
		replyError(w, http.StatusConflict, "The hire is running; stop its run first")
		return
	case "PENDING_REVIEW":
		replyError(w, http.StatusConflict, "The hire still waits for approval; deny it instead")
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)
	line := fmt.Sprintf("[%s] release: ended early", now)
	history := strings.TrimRight(hireReason.String, "\n")
	if history != "" {
		line = history + "\n" + line
	}
	userID := ""
	if u := UserFromContext(r.Context()); u != nil {
		userID = u.ID
	}
	// Releasing ends the hire's crew membership, so the write and its chained
	// audit commit together; an audit failure leaves the hire live.
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		replyInternalError(w, h.logger, "release: begin tx", err)
		return
	}
	defer func() { _ = tx.Rollback() }()
	// The status guard repeats the check above so a run that starts between
	// the read and the write is not ghosted.
	res, err := tx.ExecContext(r.Context(), `
		UPDATE agents
		SET expires_at = ?, expired_at = ?, hire_reason = ?, updated_at = ?
		WHERE id = ? AND workspace_id = ? AND ephemeral = 1 AND expired_at IS NULL
		  AND deleted_at IS NULL AND status NOT IN ('RUNNING', 'PENDING_REVIEW')`,
		now, now, line, now, agentID, workspaceID)
	if err != nil {
		h.logger.Error("release: update agent", "error", err, "agent_id", agentID)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	if n, _ := res.RowsAffected(); n != 1 {
		replyError(w, http.StatusConflict, "The hire changed state; reload and try again")
		return
	}
	notify, err := appendAgentAudit(r.Context(), tx, h.journal, workspaceID, userID, "agent.released", agentID, map[string]any{
		"crew_id": crewID, "released_at": now,
	})
	if err != nil {
		replyInternalError(w, h.logger, "release: audit", err)
		return
	}
	if err := tx.Commit(); err != nil {
		replyInternalError(w, h.logger, "release: commit", err)
		return
	}
	if notify != nil {
		notify()
	}
	h.broadcastAgentEvent("agent.expired", workspaceID, map[string]string{
		"id": agentID, "crew_id": crewID, "name": name, "expired_at": now,
	})
	writeJSON(w, http.StatusOK, agentReleasedResponse{ID: agentID, ExpiredAt: now, Released: true})
}
