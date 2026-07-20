package api

// Regression tests for the two defects #1272 shipped alongside its
// (correct) transactional fix for #1247.
//
// Defect 1 — permanent bricking. #1272 replaced the `status='pending'`
// filter in the approve-hire approvals lookup with "newest row at ANY
// status, 409 if it isn't pending". A hire cycle's approvals row goes
// terminal on deny (`denied`) or on harbormaster.SweepTimeouts
// (`timeout`) while the agent row stays PENDING_REVIEW. The documented
// recovery — `crewship rehire` — resets the agent lifecycle
// (expires_at / expired_at) but does NOT enqueue a fresh approvals row,
// so the terminal row from the PREVIOUS cycle 409'd every subsequent
// approve forever. No API or CLI path led back to IDLE.
//
// Defect 2 — harbormaster.DecideTx swallowed its post-CAS reload error
// and returned (nil, nil). POST /approvals/{id}/decide gates the
// agent-side transition on `row != nil`, so a reload failure committed
// the queue CAS, skipped the agent transition, the waitpoint resolve,
// the journal, the audit row and the WS broadcast — and answered 200.
// That is precisely the drift #1247 exists to eliminate.

import (
	"context"
	"database/sql"
	"net/http"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/harbormaster"
)

// rehireOK drives POST /agents/{id}/rehire and fails the test unless it
// returns 200 — every test below uses rehire as the documented recovery
// step, so a non-200 there means the test never reached its assertion.
func rehireOK(t *testing.T, h *AgentHandler, userID, wsID, agentID, reason string) {
	t.Helper()
	rr := postRehire(t, h, userID, wsID, "MANAGER", agentID, map[string]any{
		"reason":      reason,
		"ttl_minutes": 60,
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("rehire status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
}

// TestApproveHire_AfterDenyThenRehire_ReachesIdle is defect 1's primary
// repro: deny the guided hire, rehire the agent, approve. The approve
// must succeed — the `denied` row describes the PREVIOUS hire cycle and
// must not veto a decision on the current one.
func TestApproveHire_AfterDenyThenRehire_ReachesIdle(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewID := seedHireCrew(t, db, "guided", 5)
	agentH := newApproveHireHandler(t, db)
	seedPendingReviewAgent(t, db, wsID, crewID, "a-deny-rehire")
	approvalID := enqueueHireApproval(t, db, wsID, crewID, "a-deny-rehire", userID)

	rr := postApprovalsDecide(t, newHireApprovalsHandler(t, db), userID, wsID, "OWNER", approvalID, "denied")
	if rr.Code != http.StatusOK {
		t.Fatalf("deny status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	if got := approvalStatus(t, db, approvalID); got != "denied" {
		t.Fatalf("approvals_queue status = %q, want denied", got)
	}

	// The documented recovery path. It un-ghosts the agent (expired_at
	// back to NULL) and pushes expires_at forward; it does not touch
	// agents.status and does not enqueue a new approvals row.
	rehireOK(t, agentH, userID, wsID, "a-deny-rehire", "second thoughts, we do need them")

	status, expired := agentStatusExpiry(t, db, "a-deny-rehire")
	if status != "PENDING_REVIEW" || expired.Valid {
		t.Fatalf("post-rehire agent = %q expired=%v, want PENDING_REVIEW / NULL", status, expired)
	}

	rr = postApproveHire(t, agentH, userID, wsID, "MANAGER", "a-deny-rehire")
	if rr.Code != http.StatusOK {
		t.Fatalf("approve-hire after rehire: status = %d, want 200 — a terminal row from the "+
			"previous hire cycle must not brick the agent; body: %s", rr.Code, rr.Body.String())
	}
	status, expired = agentStatusExpiry(t, db, "a-deny-rehire")
	if status != "IDLE" {
		t.Errorf("agents.status = %q, want IDLE", status)
	}
	if expired.Valid {
		t.Errorf("agents.expired_at = %q, want NULL", expired.String)
	}
}

// timeOutHireApproval enqueues a kind=ephemeral_hire approval whose
// timeout_at is already in the past and runs the real sweeper over it,
// i.e. the exact state a lapsed approval window leaves behind. Returns
// the approvals_queue id.
func timeOutHireApproval(t *testing.T, db *sql.DB, userID, wsID, crewID, agentID string) string {
	t.Helper()
	past := time.Now().UTC().Add(-time.Minute)
	lapseAgentTTL(t, db, agentID, past)
	approvalID, err := harbormaster.Enqueue(context.Background(), db, nil, harbormaster.Request{
		WorkspaceID: wsID,
		CrewID:      crewID,
		AgentID:     agentID,
		RequestedBy: userID,
		Kind:        harbormaster.KindEphemeralHire,
		Reason:      "hire ephemeral agent " + agentID + ": test",
		Payload:     map[string]any{"tool": "agent.hire", "agent_id": agentID},
		TimeoutAt:   &past,
	})
	if err != nil {
		t.Fatalf("enqueue hire approval: %v", err)
	}

	n, err := harbormaster.SweepTimeouts(context.Background(), db, nil)
	if err != nil {
		t.Fatalf("sweep timeouts: %v", err)
	}
	if n != 1 {
		t.Fatalf("swept %d rows, want 1", n)
	}
	if got := approvalStatus(t, db, approvalID); got != "timeout" {
		t.Fatalf("approvals_queue status = %q, want timeout", got)
	}
	return approvalID
}

// lapseAgentTTL rewinds agents.expires_at to match a lapsed approval
// window. Hire writes both deadlines from the same instant + TTL, so
// they only ever disagree once `crewship rehire` pushes the agent's
// forward — the seed helper's far-future default would be a state the
// hire path cannot produce.
func lapseAgentTTL(t *testing.T, db *sql.DB, agentID string, at time.Time) {
	t.Helper()
	if _, err := db.Exec(`UPDATE agents SET expires_at = ? WHERE id = ?`,
		at.Format(time.RFC3339), agentID); err != nil {
		t.Fatalf("lapse agent ttl: %v", err)
	}
}

// TestApproveHire_AfterApprovalTimeout_Returns409AndStaysGhosted is the
// timeout twin of TestApproveHire_AfterApprovalsDeny_… (#1304). A
// lapsed approval window is a decision on the hire, so approve-hire
// must lose exactly the way it loses to a deny: the sweeper ghosts the
// agent, the `expired_at IS NULL` guard rejects the approve, and the
// only way back is `crewship rehire` — the contract
// docs/guides/ephemeral-agents.mdx has always described.
//
// Pre-fix the sweep flipped only the queue row, approve-hire skipped
// the terminal row's CAS and won the agent CAS against a NULL
// expired_at, and a lapsed window silently activated the agent (200
// {"status":"IDLE"}).
func TestApproveHire_AfterApprovalTimeout_Returns409AndStaysGhosted(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewID := seedHireCrew(t, db, "guided", 5)
	seedPendingReviewAgent(t, db, wsID, crewID, "a-timeout-only")
	timeOutHireApproval(t, db, userID, wsID, crewID, "a-timeout-only")

	status, expired := agentStatusExpiry(t, db, "a-timeout-only")
	if status != "PENDING_REVIEW" || !expired.Valid {
		t.Fatalf("post-sweep agent = %q expired=%v, want PENDING_REVIEW / ghosted", status, expired)
	}

	rr := postApproveHire(t, newApproveHireHandler(t, db), userID, wsID, "MANAGER", "a-timeout-only")
	if rr.Code != http.StatusConflict {
		t.Fatalf("approve-hire after timeout: status = %d, want 409 — a lapsed approval "+
			"window must bar the approve until rehire; body: %s", rr.Code, rr.Body.String())
	}

	status, expired = agentStatusExpiry(t, db, "a-timeout-only")
	if status == "IDLE" {
		t.Errorf("approve-hire resurrected a timed-out hire (status = IDLE)")
	}
	if !expired.Valid {
		t.Errorf("expired_at cleared — a timed-out hire must stay ghosted")
	}
}

// TestApproveHire_AfterApprovalTimeoutThenRehire_ReachesIdle is the
// other direction, and the reason the ghosting above is safe:
// `crewship rehire` clears expired_at and reopens the hire cycle, so
// the very next approve-hire succeeds. It also still guards the #1272
// bricking regression — the terminal `timeout` row belongs to the
// PREVIOUS cycle and must not veto this one.
func TestApproveHire_AfterApprovalTimeoutThenRehire_ReachesIdle(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewID := seedHireCrew(t, db, "guided", 5)
	agentH := newApproveHireHandler(t, db)
	seedPendingReviewAgent(t, db, wsID, crewID, "a-timeout-rehire")

	timeOutHireApproval(t, db, userID, wsID, crewID, "a-timeout-rehire")

	// The sweep ghosts the staged agent the same way a deny does.
	status, expired := agentStatusExpiry(t, db, "a-timeout-rehire")
	if status != "PENDING_REVIEW" || !expired.Valid {
		t.Fatalf("post-sweep agent = %q expired=%v, want PENDING_REVIEW / ghosted", status, expired)
	}

	rehireOK(t, agentH, userID, wsID, "a-timeout-rehire", "approval window lapsed, extend it")

	if status, expired = agentStatusExpiry(t, db, "a-timeout-rehire"); status != "PENDING_REVIEW" || expired.Valid {
		t.Fatalf("post-rehire agent = %q expired=%v, want PENDING_REVIEW / NULL", status, expired)
	}

	rr := postApproveHire(t, agentH, userID, wsID, "MANAGER", "a-timeout-rehire")
	if rr.Code != http.StatusOK {
		t.Fatalf("approve-hire after timeout+rehire: status = %d, want 200; body: %s",
			rr.Code, rr.Body.String())
	}
	if status, _ = agentStatusExpiry(t, db, "a-timeout-rehire"); status != "IDLE" {
		t.Errorf("agents.status = %q, want IDLE", status)
	}
}

// TestSweep_AfterRehireBeforeTimeout_LeavesHireApprovable is the review
// finding on #1316: a rehire extends agents.expires_at but leaves the
// approvals_queue row's timeout_at where it was (rehire reopens a hire
// cycle without enqueuing a new approval). If the sweeper ghosted off
// that stale queue deadline, the next tick — within 30s — would undo
// the operator's extension and the approve that follows would 409, with
// nothing in the API surface explaining why.
//
// The order here is what makes it a regression and not a rediscovery of
// the timeout contract: the rehire lands BEFORE the sweep runs.
func TestSweep_AfterRehireBeforeTimeout_LeavesHireApprovable(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewID := seedHireCrew(t, db, "guided", 5)
	agentH := newApproveHireHandler(t, db)
	seedPendingReviewAgent(t, db, wsID, crewID, "a-rehire-then-sweep")

	past := time.Now().UTC().Add(-time.Minute)
	lapseAgentTTL(t, db, "a-rehire-then-sweep", past)
	approvalID, err := harbormaster.Enqueue(context.Background(), db, nil, harbormaster.Request{
		WorkspaceID: wsID,
		CrewID:      crewID,
		AgentID:     "a-rehire-then-sweep",
		RequestedBy: userID,
		Kind:        harbormaster.KindEphemeralHire,
		Reason:      "hire ephemeral agent a-rehire-then-sweep: test",
		Payload:     map[string]any{"tool": "agent.hire", "agent_id": "a-rehire-then-sweep"},
		TimeoutAt:   &past,
	})
	if err != nil {
		t.Fatalf("enqueue hire approval: %v", err)
	}

	// Operator notices the window is about to lapse and extends it.
	rehireOK(t, agentH, userID, wsID, "a-rehire-then-sweep", "still deciding, give me another hour")

	if _, err := harbormaster.SweepTimeouts(context.Background(), db, nil); err != nil {
		t.Fatalf("sweep timeouts: %v", err)
	}
	if got := approvalStatus(t, db, approvalID); got != "timeout" {
		t.Fatalf("approvals_queue status = %q, want timeout — the queue row's own window did lapse", got)
	}

	status, expired := agentStatusExpiry(t, db, "a-rehire-then-sweep")
	if status != "PENDING_REVIEW" || expired.Valid {
		t.Fatalf("post-sweep agent = %q expired=%v, want PENDING_REVIEW / NULL — the sweep ghosted "+
			"a hire whose TTL the operator had just extended", status, expired)
	}

	rr := postApproveHire(t, agentH, userID, wsID, "MANAGER", "a-rehire-then-sweep")
	if rr.Code != http.StatusOK {
		t.Fatalf("approve-hire after rehire-then-sweep: status = %d, want 200; body: %s",
			rr.Code, rr.Body.String())
	}
	if status, _ = agentStatusExpiry(t, db, "a-rehire-then-sweep"); status != "IDLE" {
		t.Errorf("agents.status = %q, want IDLE", status)
	}
}

// TestApprovalsDecide_ReloadFailure_RollsBackAndReturns500 is defect 2
// end to end.
//
// Injection: drop a column the post-CAS reload SELECT reads but the CAS
// UPDATE does not write, so the decision applies and the reload fails —
// same "the DB stops cooperating mid-request" shape as
// breakInboxTable, aimed one statement later. `reason` is NOT NULL with
// no index and no FK, so SQLite lets it go and only the reload breaks.
//
// Pre-fix: DecideTx returned (nil, nil), the handler skipped the
// ephemeral-hire side effect on its `row != nil` gate, committed, and
// answered 200 with a terminal approval against a PENDING_REVIEW agent.
func TestApprovalsDecide_ReloadFailure_RollsBackAndReturns500(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewID := seedHireCrew(t, db, "guided", 5)
	seedPendingReviewAgent(t, db, wsID, crewID, "a-reload-fail")
	approvalID := enqueueHireApproval(t, db, wsID, crewID, "a-reload-fail", userID)

	if _, err := db.Exec(`ALTER TABLE approvals_queue DROP COLUMN reason`); err != nil {
		t.Fatalf("break reload column: %v", err)
	}

	rr := postApprovalsDecide(t, newHireApprovalsHandler(t, db), userID, wsID, "OWNER", approvalID, "approved")
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 — a reload failure must fail the transaction, not "+
			"commit a decision whose side effects were skipped; body: %s", rr.Code, rr.Body.String())
	}

	var queueStatus string
	if err := db.QueryRow(`SELECT status FROM approvals_queue WHERE id = ?`, approvalID).Scan(&queueStatus); err != nil {
		t.Fatalf("read approval status: %v", err)
	}
	if queueStatus != "pending" {
		t.Errorf("approvals_queue status = %q, want pending (the CAS must roll back)", queueStatus)
	}

	var status string
	var expired sql.NullString
	if err := db.QueryRow(`SELECT status, expired_at FROM agents WHERE id = ?`, "a-reload-fail").
		Scan(&status, &expired); err != nil {
		t.Fatalf("read agent state: %v", err)
	}
	if status != "PENDING_REVIEW" || expired.Valid {
		t.Errorf("agent = %q expired=%v, want PENDING_REVIEW / NULL", status, expired)
	}
}
