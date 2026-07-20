package api

// Atomicity tests for the ephemeral-hire decision (issue #1247).
//
// PR #1243 made the approvals_queue row the single CAS decision point,
// but the decision and its side effects were still three separate
// autocommit statements: flip the queue row, UPDATE the agent, resolve
// the inbox waitpoint. Any failure after the first commit stranded a
// terminal approval against a still-PENDING_REVIEW agent plus an
// unresolved blocking waitpoint.
//
// The three tests below pin the transactional contract:
//
//	inbox failure       → decision AND agent transition roll back
//	agent not decidable → approvals_queue row stays pending, 409
//	concurrent approve  → exactly one decider wins and the agent +
//	  vs deny             inbox state always matches that winner
//	                      (also the SQLITE_BUSY canary for the wider
//	                      write window the transaction introduces)

import (
	"database/sql"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"
)

// breakInboxTable makes every inbox_items write fail with "no such
// table" for the rest of the test. This is the injected inbox failure:
// no production seam, just a DB that stops cooperating mid-request —
// exactly the shape of the bug (a side effect failing after the
// decision already committed).
func breakInboxTable(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`DROP TABLE inbox_items`); err != nil {
		t.Fatalf("drop inbox_items: %v", err)
	}
}

func approvalStatus(t *testing.T, db *sql.DB, approvalID string) string {
	t.Helper()
	var status string
	if err := db.QueryRow(`SELECT status FROM approvals_queue WHERE id = ?`, approvalID).Scan(&status); err != nil {
		t.Fatalf("read approval status: %v", err)
	}
	return status
}

func agentStatusExpiry(t *testing.T, db *sql.DB, agentID string) (string, sql.NullString) {
	t.Helper()
	var (
		status  string
		expired sql.NullString
	)
	if err := db.QueryRow(`SELECT status, expired_at FROM agents WHERE id = ?`, agentID).Scan(&status, &expired); err != nil {
		t.Fatalf("read agent state: %v", err)
	}
	return status, expired
}

// TestApproveHire_InboxFailure_RollsBackDecisionAndAgent covers 1(a):
// the inbox resolve is part of the decision, not a best-effort tail.
func TestApproveHire_InboxFailure_RollsBackDecisionAndAgent(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewID := seedHireCrew(t, db, "guided", 5)
	h := newApproveHireHandler(t, db)

	seedPendingReviewAgent(t, db, wsID, crewID, "a-inbox-fail")
	approvalID := enqueueHireApproval(t, db, wsID, crewID, "a-inbox-fail", userID)

	breakInboxTable(t, db)

	rr := postApproveHire(t, h, userID, wsID, "MANAGER", "a-inbox-fail")
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (inbox write failed); body: %s", rr.Code, rr.Body.String())
	}

	if got := approvalStatus(t, db, approvalID); got != "pending" {
		t.Errorf("approvals_queue status = %q, want pending (decision must roll back)", got)
	}
	status, expired := agentStatusExpiry(t, db, "a-inbox-fail")
	if status != "PENDING_REVIEW" {
		t.Errorf("agents.status = %q, want PENDING_REVIEW (transition must roll back)", status)
	}
	if expired.Valid {
		t.Errorf("agents.expired_at = %q, want NULL", expired.String)
	}
}

// TestApprovalsDecide_AgentNotDecidable_LeavesApprovalPending covers
// 1(b): if the agent-side transition can't be applied, the
// approvals_queue row must not be left terminal. The TTL sweeper
// ghosting the staged agent (expired_at set, status still
// PENDING_REVIEW) is the realistic trigger.
func TestApprovalsDecide_AgentNotDecidable_LeavesApprovalPending(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewID := seedHireCrew(t, db, "guided", 5)
	ah := newHireApprovalsHandler(t, db)

	seedPendingReviewAgent(t, db, wsID, crewID, "a-ghosted")
	approvalID := enqueueHireApproval(t, db, wsID, crewID, "a-ghosted", userID)

	// TTL sweeper ghosts the staged agent between enqueue and decide.
	if _, err := db.Exec(`UPDATE agents SET expired_at = ? WHERE id = ?`,
		time.Now().UTC().Format(time.RFC3339), "a-ghosted"); err != nil {
		t.Fatalf("ghost agent: %v", err)
	}

	rr := postApprovalsDecide(t, ah, userID, wsID, "OWNER", approvalID, "approved")
	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (agent no longer decidable); body: %s", rr.Code, rr.Body.String())
	}
	if got := approvalStatus(t, db, approvalID); got != "pending" {
		t.Errorf("approvals_queue status = %q, want pending — a decision whose side "+
			"effect never applied must not leave a terminal row", got)
	}
}

// TestHireDecision_ConcurrentApproveVsDeny covers 1(c): approve-hire
// and approvals-decide(denied) fired simultaneously 50x. Exactly one
// wins, and the agent + inbox state always matches the winner. It also
// doubles as the SQLITE_BUSY canary for the widened write window —
// a 500 from either endpoint fails the test.
func TestHireDecision_ConcurrentApproveVsDeny(t *testing.T) {
	db := setupTestDB(t)
	userID, wsID, crewID := seedHireCrew(t, db, "guided", 5)
	agentH := newApproveHireHandler(t, db)
	apprH := newHireApprovalsHandler(t, db)

	for i := 0; i < 50; i++ {
		agentID := fmt.Sprintf("a-race-%d", i)
		seedPendingReviewAgent(t, db, wsID, crewID, agentID)
		approvalID := enqueueHireApproval(t, db, wsID, crewID, agentID, userID)

		var (
			wg         sync.WaitGroup
			startGate  = make(chan struct{})
			collectMu  sync.Mutex
			approveRes int
			denyRes    int
		)
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-startGate
			rr := postApproveHire(t, agentH, userID, wsID, "MANAGER", agentID)
			collectMu.Lock()
			approveRes = rr.Code
			collectMu.Unlock()
		}()
		go func() {
			defer wg.Done()
			<-startGate
			rr := postApprovalsDecide(t, apprH, userID, wsID, "OWNER", approvalID, "denied")
			collectMu.Lock()
			denyRes = rr.Code
			collectMu.Unlock()
		}()
		close(startGate)
		wg.Wait()

		if approveRes >= 500 || denyRes >= 500 {
			t.Fatalf("iteration %d: server error (approve=%d deny=%d) — likely SQLITE_BUSY "+
				"from the widened write window", i, approveRes, denyRes)
		}
		winners := 0
		if approveRes == http.StatusOK {
			winners++
		}
		if denyRes == http.StatusOK {
			winners++
		}
		if winners != 1 {
			t.Fatalf("iteration %d: %d winners (approve=%d deny=%d), want exactly 1",
				i, winners, approveRes, denyRes)
		}

		gotStatus, gotExpired := agentStatusExpiry(t, db, agentID)
		var inboxState, inboxAction sql.NullString
		if err := db.QueryRow(`SELECT state, resolved_action FROM inbox_items WHERE source_id = ?`,
			agentID).Scan(&inboxState, &inboxAction); err != nil {
			t.Fatalf("iteration %d: read inbox: %v", i, err)
		}
		queueStatus := approvalStatus(t, db, approvalID)

		if approveRes == http.StatusOK {
			if queueStatus != "approved" {
				t.Fatalf("iteration %d: approve won but queue status = %q", i, queueStatus)
			}
			if gotStatus != "IDLE" || gotExpired.Valid {
				t.Fatalf("iteration %d: approve won but agent = %q expired=%v", i, gotStatus, gotExpired)
			}
			if inboxAction.String != "approved" {
				t.Fatalf("iteration %d: approve won but inbox action = %q", i, inboxAction.String)
			}
		} else {
			if queueStatus != "denied" {
				t.Fatalf("iteration %d: deny won but queue status = %q", i, queueStatus)
			}
			if gotStatus != "PENDING_REVIEW" || !gotExpired.Valid {
				t.Fatalf("iteration %d: deny won but agent = %q expired=%v", i, gotStatus, gotExpired)
			}
			if inboxAction.String != "denied" {
				t.Fatalf("iteration %d: deny won but inbox action = %q", i, inboxAction.String)
			}
		}
		if inboxState.String != "resolved" {
			t.Fatalf("iteration %d: inbox state = %q, want resolved", i, inboxState.String)
		}
	}
}
