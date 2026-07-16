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

	"github.com/crewship-ai/crewship/internal/harbormaster"
	"github.com/crewship-ai/crewship/internal/inbox"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/ws"
)

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
		expiredAt sql.NullString
	)
	err := h.db.QueryRowContext(r.Context(), `
		SELECT status, ephemeral, crew_id, name, expired_at
		FROM agents
		WHERE id = ? AND workspace_id = ? AND deleted_at IS NULL`,
		agentID, workspaceID).Scan(&curStatus, &ephemeral, &crewID, &name, &expiredAt)
	if errors.Is(err, sql.ErrNoRows) {
		replyError(w, http.StatusNotFound, "Agent not found")
		return
	}
	if err != nil {
		h.logger.Error("approve-hire: load agent", "agent_id", agentID, "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	if ephemeral != 1 {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error":  "approve-hire is only valid on ephemeral agents",
			"reason": "permanent agents do not require hire approval",
		})
		return
	}
	if curStatus != "PENDING_REVIEW" {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error":          "Agent is not pending review",
			"reason":         "expected status=PENDING_REVIEW, got " + curStatus,
			"current_status": curStatus,
		})
		return
	}
	if expiredAt.Valid {
		// Denied via the approvals surface or ghosted by the TTL
		// sweeper — status stays PENDING_REVIEW in both terminal
		// states, so the check above can't catch them. Approving here
		// would resurrect a dead hire.
		writeJSON(w, http.StatusConflict, map[string]string{
			"error":  "Hire is no longer decidable",
			"reason": "the staged agent was denied or expired at " + expiredAt.String,
		})
		return
	}

	user := UserFromContext(r.Context())
	userID := ""
	if user != nil {
		userID = user.ID
	}

	logger := h.logger
	if logger == nil {
		logger = slog.Default()
	}

	// 2. Win the approvals-queue row BEFORE touching the agent (issue
	// #1209 review). A guided hire also enqueued a kind=ephemeral_hire
	// approvals_queue row; that row is the single decision point shared
	// with POST /approvals/{id}/decide, which likewise flips the row
	// first and applies agent side effects only after winning the CAS.
	// Same lock order on both endpoints means a concurrent deny can no
	// longer commit after we activate (denied queue + live agent):
	// whoever flips the row owns the side effects, the loser gets an
	// honest 409. Pre-#1209 hires (or a failed enqueue) have no row —
	// for those the conditional agent UPDATE below stays the decision
	// point, and no second surface exists to race it.
	approvalRowID, lerr := findPendingHireApprovalRow(r.Context(), h.db, workspaceID, agentID)
	switch {
	case errors.Is(lerr, sql.ErrNoRows):
		// Legacy hire — nothing to win on the approvals surface.
	case lerr != nil:
		h.logger.Error("approve-hire: lookup approvals row", "agent_id", agentID, "error", lerr)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	default:
		derr := harbormaster.Decide(r.Context(), h.db, h.journal, workspaceID, approvalRowID,
			harbormaster.StatusApproved, userID, "approved via hire approve")
		if errors.Is(derr, harbormaster.ErrNotPending) {
			// Someone decided through the approvals surface (or the
			// row timed out) between our preflight and now. Their
			// decision owns the agent transition; report it instead
			// of activating on top of a concurrent deny.
			reason := "approval " + approvalRowID + " was decided concurrently"
			if row, gerr := harbormaster.Get(r.Context(), h.db, workspaceID, approvalRowID); gerr == nil && row != nil {
				reason = "approval " + approvalRowID + " is already " + string(row.Status)
			}
			writeJSON(w, http.StatusConflict, map[string]string{
				"error":  "Hire already decided via the approvals queue",
				"reason": reason,
			})
			return
		}
		if derr != nil {
			h.logger.Error("approve-hire: decide approvals row", "agent_id", agentID,
				"approval_id", approvalRowID, "error", derr)
			replyError(w, http.StatusInternalServerError, "Internal server error")
			return
		}
	}

	// 3. Conditional UPDATE — the expired_at guard covers the TTL
	// sweeper ghosting the agent between preflight and here; the
	// status guard covers a concurrent legacy approve on a pre-#1209
	// hire (no approvals row to serialize on).
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := h.db.ExecContext(r.Context(), `
		UPDATE agents
		SET status = 'IDLE', updated_at = ?
		WHERE id = ? AND workspace_id = ? AND status = 'PENDING_REVIEW'
		  AND expired_at IS NULL`,
		now, agentID, workspaceID)
	if err != nil {
		h.logger.Error("approve-hire: update status", "agent_id", agentID, "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	n, err := res.RowsAffected()
	if err != nil {
		h.logger.Error("approve-hire: rows affected", "agent_id", agentID, "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	if n == 0 {
		// Lost the race — a concurrent legacy approve (pre-#1209 hire,
		// no approvals row to serialize on) or the TTL sweeper got the
		// row between our preflight and UPDATE. If we had won an
		// approvals row above this leaves it approved with the agent
		// still pending — log loudly; the sweeper ghosts the agent at
		// TTL and the surfaces reconverge.
		if approvalRowID != "" {
			logger.Error("approve-hire: approvals row won but agent transition lost",
				"agent_id", agentID, "approval_id", approvalRowID)
		}
		writeJSON(w, http.StatusConflict, map[string]string{
			"error":  "Agent is not pending review",
			"reason": "the hire was decided or expired concurrently",
		})
		return
	}

	// 4. Resolve the inbox waitpoint addressed to this agent so the
	// blocking row drops from the operator's inbox without a separate
	// PATCH. The writeInboxItem call in Hire uses source_id=agent_id
	// for blocking hire waitpoints, so the resolver keys off that.
	// Failures here are logged but non-fatal — the agent is already
	// IDLE; a stale inbox row is fixable manually and shouldn't
	// 500 the approve.
	inbox.ResolveBySource(r.Context(), h.db, logger, "waitpoint", agentID, "approved", userID)

	crewIDStr := ""
	if crewID.Valid {
		crewIDStr = crewID.String
	}

	WriteAuditLog(r.Context(), h.db, h.journal, "agent.hire_approved", "AGENT", agentID, userID, workspaceID, map[string]interface{}{
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

	writeJSON(w, http.StatusOK, map[string]any{
		"id":      agentID,
		"status":  "IDLE",
		"crew_id": crewIDStr,
	})
}

// findPendingHireApprovalRow returns the id of the newest pending
// kind=ephemeral_hire approvals_queue row for agentID, or sql.ErrNoRows
// for pre-#1209 hires (and hires whose enqueue failed) that have no row
// on the approvals surface. ApproveHire must win this row via
// harbormaster.Decide BEFORE activating the agent — the row is the
// atomic decision point shared with POST /approvals/{id}/decide.
func findPendingHireApprovalRow(ctx context.Context, db *sql.DB, workspaceID, agentID string) (string, error) {
	var approvalID string
	err := db.QueryRowContext(ctx, `
		SELECT id FROM approvals_queue
		WHERE workspace_id = ? AND agent_id = ? AND kind = ? AND status = 'pending'
		ORDER BY created_at DESC LIMIT 1`,
		workspaceID, agentID, string(harbormaster.KindEphemeralHire)).Scan(&approvalID)
	return approvalID, err
}

// applyEphemeralHireDecision performs the agent-side transition for a
// kind=ephemeral_hire approval decided through the standard approvals
// surface (POST /api/v1/approvals/{id}/decide — issue #1209):
//
//	approved → PENDING_REVIEW flips to IDLE (same transition as the
//	           /approve-hire endpoint) and the blocking inbox waitpoint
//	           resolves with action=approved.
//	denied   → the staged agent ghosts immediately (expired_at = now,
//	           the same terminal state the TTL sweeper writes) and the
//	           waitpoint resolves with action=denied. Ghosting rather
//	           than deleting preserves the audit trail and frees the
//	           crew's ephemeral quota slot; a denied hire can still be
//	           resurrected later via `crewship rehire`.
//
// Both UPDATEs are conditional on status='PENDING_REVIEW' so a decision
// racing the legacy `hire approve` path degrades to a logged no-op
// instead of clobbering an already-live agent. The approvals_queue row
// was already flipped by the caller — that row is the atomic decision
// point; this function only applies its side effects.
func applyEphemeralHireDecision(ctx context.Context, db *sql.DB, logger *slog.Logger, j journal.Emitter, hub *ws.Hub, workspaceID, agentID string, approved bool, decidedBy string) {
	if logger == nil {
		logger = slog.Default()
	}

	var (
		curStatus string
		crewID    sql.NullString
		name      string
	)
	err := db.QueryRowContext(ctx, `
		SELECT status, crew_id, name
		FROM agents
		WHERE id = ? AND workspace_id = ? AND ephemeral = 1 AND deleted_at IS NULL`,
		agentID, workspaceID).Scan(&curStatus, &crewID, &name)
	if errors.Is(err, sql.ErrNoRows) {
		logger.Warn("hire decision: agent not found", "agent_id", agentID)
		return
	}
	if err != nil {
		logger.Error("hire decision: load agent", "agent_id", agentID, "error", err)
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)
	var (
		res       sql.Result
		action    string
		eventType string
	)
	if approved {
		action, eventType = "approved", "agent.hire_approved"
		// expired_at guard: an approve landing after the TTL sweeper
		// ghosted the agent must not resurrect it — the skip branch
		// below logs it instead.
		res, err = db.ExecContext(ctx, `
			UPDATE agents
			SET status = 'IDLE', updated_at = ?
			WHERE id = ? AND workspace_id = ? AND status = 'PENDING_REVIEW'
			  AND expired_at IS NULL`,
			now, agentID, workspaceID)
	} else {
		action, eventType = "denied", "agent.hire_denied"
		res, err = db.ExecContext(ctx, `
			UPDATE agents
			SET expired_at = ?, updated_at = ?
			WHERE id = ? AND workspace_id = ? AND status = 'PENDING_REVIEW'
			  AND expired_at IS NULL`,
			now, now, agentID, workspaceID)
	}
	if err != nil {
		logger.Error("hire decision: update agent", "agent_id", agentID, "action", action, "error", err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// Already decided through the legacy path (or already ghosted).
		// The approvals row this call rides on is terminal either way;
		// log for the audit trail and leave the agent alone.
		logger.Warn("hire decision: agent no longer pending review — side effect skipped",
			"agent_id", agentID, "action", action, "current_status", curStatus)
		return
	}

	inbox.ResolveBySource(ctx, db, logger, "waitpoint", agentID, action, decidedBy)

	crewIDStr := crewID.String
	WriteAuditLog(ctx, db, j, eventType, "AGENT", agentID, decidedBy, workspaceID, map[string]interface{}{
		"agent_id": agentID,
		"crew_id":  crewIDStr,
		"name":     name,
		"via":      "approvals_queue",
	})

	payload := map[string]string{
		"id":      agentID,
		"crew_id": crewIDStr,
		"name":    name,
	}
	if approved {
		payload["status"] = "IDLE"
	} else {
		// Denied hires ghost in place — status stays PENDING_REVIEW,
		// expired_at marks the row terminal (same shape agent.expired uses).
		payload["expired_at"] = now
	}
	broadcastWorkspaceEvent(hub, workspaceID, eventType, payload)
}
