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

	// 2–4. ONE transaction: win the approvals-queue CAS, flip the
	// agent, resolve the inbox waitpoint (issue #1247). Before this,
	// the three steps were separate autocommit statements, so a
	// failure after the first left a terminal approval against a
	// still-PENDING_REVIEW agent plus an unresolved blocking
	// waitpoint — the "approvals row won but agent transition lost"
	// branch this replaces was reachable without any crash.
	//
	// database.Open sets `_txlock=immediate`, so BeginTx issues BEGIN
	// IMMEDIATE: the write lock is taken up front rather than
	// upgraded mid-transaction, which is what makes a concurrent
	// decider block on busy_timeout (30s) instead of failing with
	// SQLITE_BUSY on a lock upgrade.
	tx, err := h.db.BeginTx(r.Context(), nil)
	if err != nil {
		h.logger.Error("approve-hire: begin tx", "agent_id", agentID, "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}
	// Rollback is a no-op after a successful Commit.
	defer func() { _ = tx.Rollback() }()

	// The approvals-queue row is the single decision point shared with
	// POST /approvals/{id}/decide, which flips the same row first.
	// Same lock order on both endpoints means the loser gets an honest
	// 409. Pre-#1209 hires (or a failed enqueue) have no row at all —
	// for those the conditional agent UPDATE below stays the decision
	// point, and no second surface exists to race it. A row that
	// exists but is already terminal is a concurrent decision, NOT a
	// legacy hire: activating on top of it is exactly the double-win
	// #1247 reproduced.
	approvalRowID, rowStatus, lerr := findHireApprovalRow(r.Context(), tx, workspaceID, agentID)
	switch {
	case errors.Is(lerr, sql.ErrNoRows):
		// Legacy hire — nothing to win on the approvals surface.
	case lerr != nil:
		h.logger.Error("approve-hire: lookup approvals row", "agent_id", agentID, "error", lerr)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	case rowStatus != string(harbormaster.StatusPending):
		writeJSON(w, http.StatusConflict, map[string]string{
			"error":  "Hire already decided via the approvals queue",
			"reason": "approval " + approvalRowID + " is already " + rowStatus,
		})
		return
	}

	var decidedRow *harbormaster.Request
	if approvalRowID != "" {
		var derr error
		decidedRow, derr = harbormaster.DecideTx(r.Context(), tx, workspaceID, approvalRowID,
			harbormaster.StatusApproved, userID, "approved via hire approve")
		if errors.Is(derr, harbormaster.ErrNotPending) || errors.Is(derr, harbormaster.ErrNotFound) {
			writeJSON(w, http.StatusConflict, map[string]string{
				"error":  "Hire already decided via the approvals queue",
				"reason": "approval " + approvalRowID + " was decided concurrently",
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

	// Conditional UPDATE — the expired_at guard covers the TTL sweeper
	// ghosting the agent between preflight and here; the status guard
	// covers a concurrent legacy approve on a pre-#1209 hire.
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := tx.ExecContext(r.Context(), `
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
		// Lost the race, or the TTL sweeper ghosted the row. The
		// deferred rollback undoes the approvals-queue CAS too, so the
		// row stays pending and decidable instead of going terminal
		// against an agent that never moved.
		writeJSON(w, http.StatusConflict, map[string]string{
			"error":  "Agent is not pending review",
			"reason": "the hire was decided or expired concurrently",
		})
		return
	}

	// Resolve the inbox waitpoint addressed to this agent so the
	// blocking row drops from the operator's inbox without a separate
	// PATCH. The writeInboxItem call in Hire uses source_id=agent_id
	// for blocking hire waitpoints, so the resolver keys off that.
	// This is part of the decision now: a failed projection rolls the
	// whole approval back rather than leaving a blocking waitpoint
	// nobody can clear.
	if err := inbox.ResolveBySourceTx(r.Context(), tx, "waitpoint", agentID, "approved", userID); err != nil {
		logger.Error("approve-hire: resolve inbox waitpoint", "agent_id", agentID, "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	if err := tx.Commit(); err != nil {
		h.logger.Error("approve-hire: commit", "agent_id", agentID, "error", err)
		replyError(w, http.StatusInternalServerError, "Internal server error")
		return
	}

	// 5. Post-commit tail. Journal, audit and broadcast only describe
	// state that is now durable — emitting them inside the tx would
	// announce a transition a rollback could still erase.
	harbormaster.AfterDecide(r.Context(), h.db, h.journal, decidedRow,
		harbormaster.StatusApproved, userID, "approved via hire approve")

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

// findHireApprovalRow returns the id AND status of the newest
// kind=ephemeral_hire approvals_queue row for agentID, or sql.ErrNoRows
// for pre-#1209 hires (and hires whose enqueue failed) that have no row
// on the approvals surface. ApproveHire must win this row via
// harbormaster.DecideTx BEFORE activating the agent — the row is the
// atomic decision point shared with POST /approvals/{id}/decide.
//
// The status matters: the earlier version filtered on status='pending'
// and mapped "row exists but is already denied" onto the same
// sql.ErrNoRows the legacy-hire case uses. A concurrent deny that
// committed first therefore looked like a hire with no approvals row,
// and ApproveHire happily activated the agent on top of it — two
// winners for one decision (#1247). Returning the status lets the
// caller tell "no such row" from "already decided".
func findHireApprovalRow(ctx context.Context, q harbormaster.DBTX, workspaceID, agentID string) (string, string, error) {
	var approvalID, status string
	err := q.QueryRowContext(ctx, `
		SELECT id, status FROM approvals_queue
		WHERE workspace_id = ? AND agent_id = ? AND kind = ?
		ORDER BY created_at DESC LIMIT 1`,
		workspaceID, agentID, string(harbormaster.KindEphemeralHire)).Scan(&approvalID, &status)
	return approvalID, status, err
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
// racing the legacy `hire approve` path cannot clobber an already-live
// agent. Unlike the pre-#1247 version, a skipped side effect is no
// longer a logged no-op on top of an already-committed decision: the
// function runs inside the CALLER's transaction alongside the
// approvals_queue CAS and returns errHireNotDecidable, so the caller
// rolls the decision back and the queue row stays pending. A terminal
// approval must never describe a transition that did not happen.
//
// The returned effect carries everything the post-commit tail (audit
// log + WS broadcast) needs; nothing is emitted from inside the tx.
func applyEphemeralHireDecisionTx(ctx context.Context, tx harbormaster.DBTX, workspaceID, agentID string, approved bool, decidedBy string) (*hireDecisionEffect, error) {
	var (
		curStatus string
		crewID    sql.NullString
		name      string
	)
	err := tx.QueryRowContext(ctx, `
		SELECT status, crew_id, name
		FROM agents
		WHERE id = ? AND workspace_id = ? AND ephemeral = 1 AND deleted_at IS NULL`,
		agentID, workspaceID).Scan(&curStatus, &crewID, &name)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errHireNotDecidable
	}
	if err != nil {
		return nil, err
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
		// ghosted the agent must not resurrect it.
		res, err = tx.ExecContext(ctx, `
			UPDATE agents
			SET status = 'IDLE', updated_at = ?
			WHERE id = ? AND workspace_id = ? AND status = 'PENDING_REVIEW'
			  AND expired_at IS NULL`,
			now, agentID, workspaceID)
	} else {
		action, eventType = "denied", "agent.hire_denied"
		res, err = tx.ExecContext(ctx, `
			UPDATE agents
			SET expired_at = ?, updated_at = ?
			WHERE id = ? AND workspace_id = ? AND status = 'PENDING_REVIEW'
			  AND expired_at IS NULL`,
			now, now, agentID, workspaceID)
	}
	if err != nil {
		return nil, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return nil, err
	}
	if n == 0 {
		// Already decided through the legacy path, or already ghosted
		// by the TTL sweeper. Fail the whole decision so the queue row
		// stays pending rather than going terminal on a no-op.
		return nil, errHireNotDecidable
	}

	if err := inbox.ResolveBySourceTx(ctx, tx, "waitpoint", agentID, action, decidedBy); err != nil {
		return nil, err
	}

	return &hireDecisionEffect{
		agentID:   agentID,
		crewID:    crewID.String,
		name:      name,
		action:    action,
		eventType: eventType,
		decidedAt: now,
		approved:  approved,
	}, nil
}

// errHireNotDecidable means the staged agent could not take the
// transition the decision implies (gone, already live, or ghosted by
// the TTL sweeper). The caller must roll back and answer 409 —
// committing the queue row would leave a terminal approval with no
// matching agent state, the exact drift #1247 is about.
var errHireNotDecidable = errors.New("staged hire is no longer decidable")

// hireDecisionEffect is the post-commit description of an applied
// ephemeral-hire decision: what to write to the audit log and what to
// broadcast, once the transaction is durable.
type hireDecisionEffect struct {
	agentID   string
	crewID    string
	name      string
	action    string
	eventType string
	decidedAt string
	approved  bool
}

// emit writes the audit row and broadcasts the agent lifecycle flip.
// Call ONLY after the enclosing transaction commits.
func (e *hireDecisionEffect) emit(ctx context.Context, db *sql.DB, j journal.Emitter, hub *ws.Hub, workspaceID, decidedBy string) {
	WriteAuditLog(ctx, db, j, e.eventType, "AGENT", e.agentID, decidedBy, workspaceID, map[string]interface{}{
		"agent_id": e.agentID,
		"crew_id":  e.crewID,
		"name":     e.name,
		"via":      "approvals_queue",
	})

	payload := map[string]string{
		"id":      e.agentID,
		"crew_id": e.crewID,
		"name":    e.name,
	}
	if e.approved {
		payload["status"] = "IDLE"
	} else {
		// Denied hires ghost in place — status stays PENDING_REVIEW,
		// expired_at marks the row terminal (same shape agent.expired uses).
		payload["expired_at"] = e.decidedAt
	}
	broadcastWorkspaceEvent(hub, workspaceID, e.eventType, payload)
}
