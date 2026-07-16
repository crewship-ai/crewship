package api

// approve-hire endpoint for the PR-D F5 guided-autonomy hire flow.
//
// When a crew's autonomy policy is "guided", POST /api/v1/agents/hire
// inserts the ephemeral agent row with status='PENDING_REVIEW' and
// drops a blocking inbox waitpoint addressed to the operator. The
// chatbridge refuses to start a PENDING_REVIEW agent — without this
// guard the 202 response would race the first WS message and the
// container would spin up before the operator clicked Approve.
//
// This handler is what makes that gate actually release:
//
//   1. Verify the agent exists in the caller's workspace, is ephemeral,
//      and is currently PENDING_REVIEW. Anything else (already-IDLE,
//      RUNNING, ERROR, or a permanent agent) → 409, so a duplicate
//      Approve click can't accidentally re-open a closed waitpoint.
//   2. Atomic UPDATE WHERE status='PENDING_REVIEW' so two concurrent
//      operator clicks can't both think they were the one who
//      approved.
//   3. ResolveBySource on the inbox waitpoint addressed to this
//      agent — the inbox row drops from the unread queue without
//      needing a separate PATCH.
//   4. Journal entry agent.hire_approved for the audit timeline.
//   5. WS broadcast so any open dashboard sees the agent flip to IDLE
//      without a poll.

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/crewship-ai/crewship/internal/inbox"
)

// errStagedHireNotFound is returned by approveStagedHire when the agent
// id does not exist in the caller's workspace (or is soft-deleted).
var errStagedHireNotFound = errors.New("staged hire: agent not found")

// stagedHireConflict carries the exact 409 JSON body approveStagedHire
// wants the caller to render — the state machine owns the conflict
// vocabulary so ApproveHire and the approvals Decide delegation (#1209)
// can't drift apart.
type stagedHireConflict struct {
	resp map[string]string
}

func (e *stagedHireConflict) Error() string { return e.resp["error"] }

// approveStagedHire is the approve-hire state machine, shared by the
// HTTP ApproveHire handler and the approvals Decide delegation (#1209).
// It flips a PENDING_REVIEW ephemeral agent to IDLE, resolves the
// blocking inbox waitpoint, writes the audit entry, and broadcasts the
// WS event. RBAC is the caller's job — both call sites gate before
// invoking this.
//
// Returns the agent's crew id and name on success. Error contract:
//
//	errStagedHireNotFound — agent missing in this workspace (→ 404)
//	*stagedHireConflict   — not ephemeral / not pending / lost the
//	                        concurrent-approve race (→ 409, body in resp)
//	anything else         — DB failure (→ 500)
func (h *AgentHandler) approveStagedHire(ctx context.Context, workspaceID, agentID, userID string) (string, string, error) {
	// 1. Pre-flight: confirm the agent exists in this workspace and
	// capture its current state for the 409 message. We do this read
	// + write split (rather than a bare UPDATE … WHERE status=…) so
	// the 404 vs 409 distinction is honest — a bare UPDATE with 0
	// rows-affected can't tell those apart.
	var (
		curStatus string
		ephemeral int
		crewID    sql.NullString
		name      string
	)
	err := h.db.QueryRowContext(ctx, `
		SELECT status, ephemeral, crew_id, name
		FROM agents
		WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL`,
		agentID, workspaceID).Scan(&curStatus, &ephemeral, &crewID, &name)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", errStagedHireNotFound
	}
	if err != nil {
		h.logger.Error("approve-hire: load agent", "agent_id", agentID, "error", err)
		return "", "", err
	}
	if ephemeral != 1 {
		return "", "", &stagedHireConflict{resp: map[string]string{
			"error":  "approve-hire is only valid on ephemeral agents",
			"reason": "permanent agents do not require hire approval",
		}}
	}
	if curStatus != "PENDING_REVIEW" {
		return "", "", &stagedHireConflict{resp: map[string]string{
			"error":          "Agent is not pending review",
			"reason":         "expected status=PENDING_REVIEW, got " + curStatus,
			"current_status": curStatus,
		}}
	}

	// 2. Conditional UPDATE — if another operator just clicked
	// Approve in parallel, the RowsAffected = 0 branch handles the
	// race without a transaction. We're not touching any other
	// columns so the second operator's no-op is harmless.
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := h.db.ExecContext(ctx, `
		UPDATE agents
		SET status = 'IDLE', updated_at = ?
		WHERE id = ? AND workspace_id = ? AND status = 'PENDING_REVIEW'`,
		now, agentID, workspaceID)
	if err != nil {
		h.logger.Error("approve-hire: update status", "agent_id", agentID, "error", err)
		return "", "", err
	}
	n, err := res.RowsAffected()
	if err != nil {
		h.logger.Error("approve-hire: rows affected", "agent_id", agentID, "error", err)
		return "", "", err
	}
	if n == 0 {
		// Lost the race — another operator approved this row
		// between our SELECT and UPDATE. Honest 409 instead of
		// silent success so the caller's UI doesn't double-log.
		return "", "", &stagedHireConflict{resp: map[string]string{
			"error":  "Agent is not pending review",
			"reason": "another operator approved this hire concurrently",
		}}
	}

	// 3. Resolve the inbox waitpoint addressed to this agent so the
	// blocking row drops from the operator's inbox without a separate
	// PATCH. The writeInboxItem call in Hire uses source_id=agent_id
	// for blocking hire waitpoints, so the resolver keys off that.
	// Failures here are logged but non-fatal — the agent is already
	// IDLE; a stale inbox row is fixable manually and shouldn't
	// fail the approve.
	logger := h.logger
	if logger == nil {
		logger = slog.Default()
	}
	inbox.ResolveBySource(ctx, h.db, logger, "waitpoint", agentID, "approved", userID)

	crewIDStr := ""
	if crewID.Valid {
		crewIDStr = crewID.String
	}

	WriteAuditLog(ctx, h.db, h.journal, "agent.hire_approved", "AGENT", agentID, userID, workspaceID, map[string]interface{}{
		"agent_id": agentID,
		"crew_id":  crewIDStr,
		"name":     name,
	})

	h.broadcastAgentEvent("agent.hire_approved", workspaceID, map[string]string{
		"id":      agentID,
		"crew_id": crewIDStr,
		"name":    name,
		"status":  "IDLE",
	})

	return crewIDStr, name, nil
}

// ApproveHire flips a PENDING_REVIEW ephemeral agent to IDLE,
// resolves the blocking inbox waitpoint, and writes the audit entry.
//
// POST /api/v1/agents/{agentId}/approve-hire
//
// Returns:
//
//	200 OK     — agent flipped to IDLE; chatbridge will now serve
//	             messages.
//	403       — caller lacks MANAGER+ (same gate as Hire/Rehire).
//	404       — agent not found in this workspace.
//	409       — agent is not in PENDING_REVIEW state (already approved
//	             by a concurrent operator, was hired under non-guided
//	             autonomy, or is a permanent agent).
//	500       — DB error.
func (h *AgentHandler) ApproveHire(w http.ResponseWriter, r *http.Request) {
	workspaceID := WorkspaceIDFromContext(r.Context())
	role := RoleFromContext(r.Context())
	agentID := r.PathValue("agentId")

	if !canRole(role, "create") {
		replyError(w, http.StatusForbidden, "Forbidden")
		return
	}
	if agentID == "" {
		replyError(w, http.StatusBadRequest, "agentId is required")
		return
	}

	user := UserFromContext(r.Context())
	userID := ""
	if user != nil {
		userID = user.ID
	}

	crewID, _, err := h.approveStagedHire(r.Context(), workspaceID, agentID, userID)
	var conflict *stagedHireConflict
	switch {
	case errors.Is(err, errStagedHireNotFound):
		replyError(w, http.StatusNotFound, "Agent not found")
		return
	case errors.As(err, &conflict):
		writeJSON(w, http.StatusConflict, conflict.resp)
		return
	case err != nil:
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"id":      agentID,
		"status":  "IDLE",
		"crew_id": crewID,
	})
}
