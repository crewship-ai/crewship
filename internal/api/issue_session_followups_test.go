package api

// Tests for dispatchQueuedFollowUpsForSession (issue_session_followups.go),
// the other half of B3's answer to "consumed at the next step boundary via
// the existing steering queue" (PRD-ISSUES-AND-ROUTINES-2026 §9.4/§17,
// #2339) — the half that replaced sessionBusyErrorFor's task-append after
// review on #2342 found it could never reach a RUNNING or freshly-PENDING
// winner.
//
// Driven directly against the AssignmentHandler method rather than through
// a real finishAssignment lifecycle: setupMentionFixture wires no resolver,
// so a real dispatched run fails near-instantly, which makes racing "still
// RUNNING when the second comment lands" against a background goroutine
// exactly the kind of flaky timing dependency these tests exist to avoid.
// Seeding the precondition (a terminal "winner" run, 'pending' deliveries
// behind it) and calling the method directly is deterministic and tests
// the same code finishAssignment calls.

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// seedFollowUpFixture seeds one session with a TERMINAL "winner" run (the
// run whose completion is what frees the session's slot and triggers
// dispatchQueuedFollowUpsForSession in production) and n 'pending'
// deliveries behind it, each with its own real mission_comments row so the
// author-name join has something to find.
func seedFollowUpFixture(t *testing.T, f *mentionFixture, sessionID, winnerID, winnerStatus string, n int) []string {
	t.Helper()
	execOrFatal(t, f.db, `
		INSERT INTO issue_agent_sessions (id, workspace_id, mission_id, agent_id, state, last_consumed_seq, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'pending', 0, datetime('now'), datetime('now'))`,
		sessionID, f.wsID, f.missionID, f.target)
	execOrFatal(t, f.db, `
		INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status, depth, created_at, mission_id, session_id)
		VALUES (?, ?, ?, ?, ?, 'winning run', ?, 1, datetime('now'), ?, ?)`,
		winnerID, f.wsID, f.missionID, f.target, f.target, winnerStatus, f.missionID, sessionID)

	var deliveryIDs []string
	for i := 0; i < n; i++ {
		commentID := fmt.Sprintf("cmt_fu_%s_%d", sessionID, i)
		deliveryID := fmt.Sprintf("mcm_fu_%s_%d", sessionID, i)
		body := fmt.Sprintf("follow-up comment number %d", i)
		execOrFatal(t, f.db, `
			INSERT INTO mission_comments (id, mission_id, author_type, author_id, body)
			VALUES (?, ?, 'user', ?, ?)`,
			commentID, f.missionID, f.userID, body)
		execOrFatal(t, f.db, `
			INSERT INTO mission_comment_mentions
			    (id, workspace_id, mission_id, comment_id, agent_id, position, state, dispatch_state, created_at)
			VALUES (?, ?, ?, ?, ?, ?, 'pending', 'queued', datetime('now'))`,
			deliveryID, f.wsID, f.missionID, commentID, f.target, i)
		deliveryIDs = append(deliveryIDs, deliveryID)
	}
	return deliveryIDs
}

// TestDispatchQueuedFollowUps_OneRunCarriesTheFollowUpText is test (a) from
// review: winner terminal (simulating "just finished"), one pending
// delivery behind it → exactly one new run for the session, the delivery
// claimed by it, and — the user-observable half — the new run's task
// actually contains the queued comment's text.
func TestDispatchQueuedFollowUps_OneRunCarriesTheFollowUpText(t *testing.T) {
	f := setupMentionFixture(t)
	ctx := context.Background()
	if err := ensureMissionChat(ctx, f.db, f.missionID, f.wsID, f.target, "Test issue"); err != nil {
		t.Fatalf("ensureMissionChat: %v", err)
	}

	deliveryIDs := seedFollowUpFixture(t, f, "sess_fu_a", "asg_fu_a_winner", "COMPLETED", 1)

	f.assign.dispatchQueuedFollowUpsForSession(ctx, "asg_fu_a_winner", f.wsID)
	f.assign.WaitDispatches()

	newRunID := onlyNewRunForSession(t, f, "sess_fu_a", "asg_fu_a_winner")

	var task string
	if err := f.db.QueryRow(`SELECT task FROM assignments WHERE id = ?`, newRunID).Scan(&task); err != nil {
		t.Fatalf("read follow-up run's task: %v", err)
	}
	if !strings.Contains(task, "follow-up comment number 0") {
		t.Errorf("follow-up run's task does not contain the queued comment's text: %q", task)
	}

	// By the time WaitDispatches returns, the follow-up run has already
	// gone through claim -> consumed — this fixture wires no resolver, so
	// the run fails (and finishAssignment runs) near-instantly, exercising
	// dispatchQueuedFollowUpsForSession's own catch-up path for a run that
	// finishes before its claim lands (see that function's doc comment).
	// Checking the FINAL state (consumed, claimed_by_run_id = the
	// follow-up run) rather than the transient 'claimed' state in between
	// is what makes this assertion race-free instead of timing-dependent.
	var state, claimedBy string
	if err := f.db.QueryRow(`SELECT state, COALESCE(claimed_by_run_id,'') FROM mission_comment_mentions WHERE id = ?`,
		deliveryIDs[0]).Scan(&state, &claimedBy); err != nil {
		t.Fatalf("read delivery: %v", err)
	}
	if state != "consumed" {
		t.Errorf("delivery state = %q, want consumed — B3's accept line (\"two consumed deliveries\") "+
			"holds transitively for a folded-in follow-up too, not just the direct-attach shape", state)
	}
	if claimedBy != newRunID {
		t.Errorf("delivery claimed_by_run_id = %q, want the follow-up run %q", claimedBy, newRunID)
	}
}

// TestDispatchQueuedFollowUps_TenFollowUpsProduceOneRun is test (b) from
// review: ten comments queued behind one winner → one follow-up run
// claiming all ten, not ten follow-up runs.
func TestDispatchQueuedFollowUps_TenFollowUpsProduceOneRun(t *testing.T) {
	f := setupMentionFixture(t)
	ctx := context.Background()
	if err := ensureMissionChat(ctx, f.db, f.missionID, f.wsID, f.target, "Test issue"); err != nil {
		t.Fatalf("ensureMissionChat: %v", err)
	}

	const n = 10
	deliveryIDs := seedFollowUpFixture(t, f, "sess_fu_b", "asg_fu_b_winner", "COMPLETED", n)

	f.assign.dispatchQueuedFollowUpsForSession(ctx, "asg_fu_b_winner", f.wsID)
	f.assign.WaitDispatches()

	newRunID := onlyNewRunForSession(t, f, "sess_fu_b", "asg_fu_b_winner")

	var task string
	if err := f.db.QueryRow(`SELECT task FROM assignments WHERE id = ?`, newRunID).Scan(&task); err != nil {
		t.Fatalf("read follow-up run's task: %v", err)
	}
	for i := 0; i < n; i++ {
		want := fmt.Sprintf("follow-up comment number %d", i)
		if !strings.Contains(task, want) {
			t.Errorf("follow-up run's task is missing comment %d's text", i)
		}
	}

	// Final state, not transient — see the sibling test's comment on why:
	// this fixture's no-resolver run fails fast enough that
	// dispatchQueuedFollowUpsForSession's own catch-up path (or the
	// ordinary finishAssignment one) has already consumed every delivery
	// this one follow-up run claimed by the time WaitDispatches returns.
	var consumed int
	if err := f.db.QueryRow(
		`SELECT COUNT(*) FROM mission_comment_mentions WHERE state = 'consumed' AND claimed_by_run_id = ?`,
		newRunID).Scan(&consumed); err != nil {
		t.Fatalf("count consumed deliveries: %v", err)
	}
	if consumed != n {
		t.Fatalf("deliveries consumed via the one follow-up run = %d, want %d", consumed, n)
	}
	_ = deliveryIDs
}

// TestDispatchQueuedFollowUps_FailedDispatchLeavesDeliveriesPending is test
// (c) from review: the follow-up dispatch itself is refused (here: the
// "winner" is left RUNNING, so idx_assignments_one_active_per_session
// refuses the follow-up dispatch too — a real shape, not a synthetic one:
// another mention can race in and take the slot between a run's completion
// UPDATE and this call). No new run, and the deliveries stay 'pending' —
// never marked consumed for a comment nothing has read.
func TestDispatchQueuedFollowUps_FailedDispatchLeavesDeliveriesPending(t *testing.T) {
	f := setupMentionFixture(t)
	ctx := context.Background()
	if err := ensureMissionChat(ctx, f.db, f.missionID, f.wsID, f.target, "Test issue"); err != nil {
		t.Fatalf("ensureMissionChat: %v", err)
	}

	// RUNNING, not COMPLETED: the slot is NOT actually free, so the
	// follow-up dispatch this call attempts collides with the exclusivity
	// index exactly like a fresh mention would.
	deliveryIDs := seedFollowUpFixture(t, f, "sess_fu_c", "asg_fu_c_winner", "RUNNING", 2)

	f.assign.dispatchQueuedFollowUpsForSession(ctx, "asg_fu_c_winner", f.wsID)
	f.assign.WaitDispatches()

	var total int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM assignments WHERE session_id = 'sess_fu_c'`).Scan(&total); err != nil {
		t.Fatalf("count assignments: %v", err)
	}
	if total != 1 {
		t.Fatalf("assignments for the session = %d, want 1 (the still-running winner only — no follow-up run)", total)
	}

	for _, id := range deliveryIDs {
		var state, claimedBy string
		if err := f.db.QueryRow(`SELECT state, COALESCE(claimed_by_run_id,'') FROM mission_comment_mentions WHERE id = ?`,
			id).Scan(&state, &claimedBy); err != nil {
			t.Fatalf("read delivery %s: %v", id, err)
		}
		if state != "pending" {
			t.Errorf("delivery %s state = %q, want pending (nothing consumed it)", id, state)
		}
		if claimedBy != "" {
			t.Errorf("delivery %s claimed_by_run_id = %q, want empty", id, claimedBy)
		}
	}
}

// TestDispatchQueuedFollowUps_OversizedDigestLeavesTheOverflowPending is a
// regression test for a review finding on #2342: an earlier revision
// claimed and later marked 'consumed' EVERY pending delivery regardless of
// whether its comment text actually fit in the follow-up run's brief.
// mentionTaskBrief clips CommentBody to mentionTaskMaxBody (4000 runes) as
// ONE unit, so a big enough burst of queued comments silently lost its
// tail to that clip while dispatchQueuedFollowUpsForSession still recorded
// every one of them as read.
//
// Three comments at ~1800 runes each (~5400 total, comfortably over the
// 4000 budget once the header is subtracted) force an overflow: the first
// two fit, the third does not — on the FIRST fold. This fixture's
// no-resolver run fails near-instantly, so the follow-up run this test
// dispatches itself finishes fast enough to trigger its OWN
// dispatchQueuedFollowUpsForSession call before this test's single
// WaitDispatches returns — the overflow comment left behind by the first
// fold is picked up automatically by that second, chained fold, not by a
// second call this test has to make itself. That self-draining chain is
// exactly the point: nothing here manufactures a "next round" by hand.
func TestDispatchQueuedFollowUps_OversizedDigestLeavesTheOverflowPending(t *testing.T) {
	f := setupMentionFixture(t)
	ctx := context.Background()
	if err := ensureMissionChat(ctx, f.db, f.missionID, f.wsID, f.target, "Test issue"); err != nil {
		t.Fatalf("ensureMissionChat: %v", err)
	}

	const sessionID = "sess_fu_overflow"
	const winnerID = "asg_fu_overflow_winner"
	execOrFatal(t, f.db, `
		INSERT INTO issue_agent_sessions (id, workspace_id, mission_id, agent_id, state, last_consumed_seq, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'pending', 0, datetime('now'), datetime('now'))`,
		sessionID, f.wsID, f.missionID, f.target)
	execOrFatal(t, f.db, `
		INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status, depth, created_at, mission_id, session_id)
		VALUES (?, ?, ?, ?, ?, 'winning run', 'COMPLETED', 1, datetime('now'), ?, ?)`,
		winnerID, f.wsID, f.missionID, f.target, f.target, f.missionID, sessionID)

	const n = 3
	bigBody := strings.Repeat("x", 1800)
	deliveryIDs := make([]string, n)
	for i := 0; i < n; i++ {
		commentID := fmt.Sprintf("cmt_fu_overflow_%d", i)
		deliveryID := fmt.Sprintf("mcm_fu_overflow_%d", i)
		execOrFatal(t, f.db, `
			INSERT INTO mission_comments (id, mission_id, author_type, author_id, body)
			VALUES (?, ?, 'user', ?, ?)`,
			commentID, f.missionID, f.userID, fmt.Sprintf("%s (comment %d)", bigBody, i))
		execOrFatal(t, f.db, `
			INSERT INTO mission_comment_mentions
			    (id, workspace_id, mission_id, comment_id, agent_id, position, state, dispatch_state, created_at)
			VALUES (?, ?, ?, ?, ?, ?, 'pending', 'queued', datetime('now'))`,
			deliveryID, f.wsID, f.missionID, commentID, f.target, i)
		deliveryIDs[i] = deliveryID
	}

	f.assign.dispatchQueuedFollowUpsForSession(ctx, winnerID, f.wsID)
	f.assign.WaitDispatches()

	// The overflow forced at least a SECOND follow-up run to exist for the
	// session — one digest could not carry all three comments — proving
	// the bound actually bit rather than silently widening to fit
	// everything.
	rows, err := f.db.Query(`SELECT id, task FROM assignments WHERE session_id = ? AND id != ?`, sessionID, winnerID)
	if err != nil {
		t.Fatalf("query follow-up runs: %v", err)
	}
	defer rows.Close()
	var tasks []string
	for rows.Next() {
		var id, task string
		if err := rows.Scan(&id, &task); err != nil {
			t.Fatalf("scan: %v", err)
		}
		tasks = append(tasks, task)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	if len(tasks) < 2 {
		t.Fatalf("follow-up runs for the session = %d, want at least 2 — one digest cannot fit all three "+
			"~1800-rune comments under mentionTaskMaxBody, so the overflow must have forced a second run", len(tasks))
	}
	allTasks := strings.Join(tasks, "\n---\n")
	if !strings.Contains(allTasks, "(comment 0)") {
		t.Error("no follow-up run's task contains the first queued comment")
	}
	if !strings.Contains(allTasks, "(comment 2)") {
		t.Error("no follow-up run's task contains the last queued comment — the self-draining chain should " +
			"have picked it up in a later fold")
	}

	// Every delivery ends up 'consumed' — the self-draining chain (each
	// follow-up run's own completion re-triggers dispatchQueuedFollowUpsForSession)
	// keeps folding the backlog in until nothing is left pending, all within
	// the ONE WaitDispatches call above.
	for _, id := range deliveryIDs {
		var state string
		if err := f.db.QueryRow(`SELECT state FROM mission_comment_mentions WHERE id = ?`, id).Scan(&state); err != nil {
			t.Fatalf("read delivery %s: %v", id, err)
		}
		if state != "consumed" {
			t.Errorf("delivery %s state = %q, want consumed (the self-draining chain should have folded it "+
				"into one of the follow-up runs above)", id, state)
		}
	}
}

// onlyNewRunForSession asserts exactly one assignment exists for sessionID
// besides excludeID (the seeded "winner") and returns its id.
func onlyNewRunForSession(t *testing.T, f *mentionFixture, sessionID, excludeID string) string {
	t.Helper()
	rows, err := f.db.Query(`SELECT id FROM assignments WHERE session_id = ? AND id != ?`, sessionID, excludeID)
	if err != nil {
		t.Fatalf("query new runs for session: %v", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan: %v", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	if len(ids) != 1 {
		t.Fatalf("new runs for session %s = %d (%v), want exactly 1", sessionID, len(ids), ids)
	}
	return ids[0]
}
