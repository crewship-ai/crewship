package api

import (
	"context"
	"strings"
	"testing"
)

// B3b (#2350, PRD §18 scenario 4): a correction — a comment that arrives
// while the session already has a run in flight — is reflected in the next
// step on the SAME session, ahead of ordinary follow-ups and labelled as a
// correction. These tests drive the real delivery + fold path, not the
// digest formatter in isolation.

// seedPendingDeliveryWithPriority inserts one pending delivery for f.target
// with a chosen priority and comment body.
func seedPendingDeliveryWithPriority(t *testing.T, f *mentionFixture, deliveryID, body, priority string, position int) {
	t.Helper()
	commentID := "cmt_" + deliveryID
	execOrFatal(t, f.db, `
		INSERT INTO mission_comments (id, mission_id, author_type, author_id, body)
		VALUES (?, ?, 'user', ?, ?)`, commentID, f.missionID, f.userID, body)
	execOrFatal(t, f.db, `
		INSERT INTO mission_comment_mentions
		    (id, workspace_id, mission_id, comment_id, agent_id, position, state, dispatch_state, priority, created_at)
		VALUES (?, ?, ?, ?, ?, ?, 'pending', 'queued', ?, datetime('now'))`,
		deliveryID, f.wsID, f.missionID, commentID, f.target, position, priority)
}

// A correction leads the folded brief even when it arrived AFTER a normal
// follow-up, and it is labelled CORRECTION; the normal one keeps the plain
// Comment label. This is the ordering §9.3 requires and the label scenario
// 4 needs so the resumed step reads the correction as steering.
func TestDispatchQueuedFollowUps_CorrectionLeadsAndIsLabelled(t *testing.T) {
	f := setupMentionFixture(t)
	ctx := context.Background()
	if err := ensureMissionChat(ctx, f.db, f.missionID, f.wsID, f.target, "Test issue"); err != nil {
		t.Fatalf("ensureMissionChat: %v", err)
	}
	execOrFatal(t, f.db, `
		INSERT INTO issue_agent_sessions (id, workspace_id, mission_id, agent_id, state, last_consumed_seq, created_at, updated_at)
		VALUES ('sess_corr', ?, ?, ?, 'pending', 0, datetime('now'), datetime('now'))`, f.wsID, f.missionID, f.target)
	execOrFatal(t, f.db, `
		INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status, depth, created_at, mission_id, session_id)
		VALUES ('asg_corr_winner', ?, ?, ?, ?, 'winning run', 'COMPLETED', 1, datetime('now'), ?, 'sess_corr')`,
		f.wsID, f.missionID, f.target, f.target, f.missionID)

	// A normal comment arrived at position 0, a correction at position 1 —
	// the correction is later in arrival order but must lead the brief.
	seedPendingDeliveryWithPriority(t, f, "mcm_normal", "please also update the changelog", deliveryPriorityNormal, 0)
	seedPendingDeliveryWithPriority(t, f, "mcm_corr", "STOP using the prod bucket, use staging", deliveryPriorityCorrection, 1)

	f.assign.dispatchQueuedFollowUpsForSession(ctx, "asg_corr_winner", f.wsID)
	f.assign.WaitDispatches()

	newRunID := onlyNewRunForSession(t, f, "sess_corr", "asg_corr_winner")
	var task string
	if err := f.db.QueryRow(`SELECT task FROM assignments WHERE id = ?`, newRunID).Scan(&task); err != nil {
		t.Fatalf("read task: %v", err)
	}
	ci := strings.Index(task, "use staging")
	ni := strings.Index(task, "update the changelog")
	if ci < 0 || ni < 0 {
		t.Fatalf("brief missing one of the comments:\n%s", task)
	}
	if ci > ni {
		t.Errorf("correction did not lead the brief (correction at %d, normal at %d):\n%s", ci, ni, task)
	}
	if !strings.Contains(task, "CORRECTION") {
		t.Errorf("brief does not label the correction:\n%s", task)
	}
	if !strings.Contains(task, "read and apply them first") {
		t.Errorf("brief header does not flag corrections:\n%s", task)
	}
}

// A plain follow-up (no correction in the fold) keeps the original,
// un-alarmed header and the Comment label — B3b must not relabel ordinary
// queued comments.
func TestDispatchQueuedFollowUps_NoCorrectionKeepsPlainHeader(t *testing.T) {
	f := setupMentionFixture(t)
	ctx := context.Background()
	if err := ensureMissionChat(ctx, f.db, f.missionID, f.wsID, f.target, "Test issue"); err != nil {
		t.Fatalf("ensureMissionChat: %v", err)
	}
	seedFollowUpFixture(t, f, "sess_plain", "asg_plain_winner", "COMPLETED", 1)

	f.assign.dispatchQueuedFollowUpsForSession(ctx, "asg_plain_winner", f.wsID)
	f.assign.WaitDispatches()

	newRunID := onlyNewRunForSession(t, f, "sess_plain", "asg_plain_winner")
	var task string
	if err := f.db.QueryRow(`SELECT task FROM assignments WHERE id = ?`, newRunID).Scan(&task); err != nil {
		t.Fatalf("read task: %v", err)
	}
	if strings.Contains(task, "CORRECTION") || strings.Contains(task, "read and apply them first") {
		t.Errorf("a plain follow-up was labelled as a correction:\n%s", task)
	}
}

// The delivery path itself classifies a comment that arrives during an
// active run as a correction: deliverAndDispatch releases the claim to
// pending AND raises the priority, so the row waiting to be folded is a
// correction, not normal.
func TestDeliverAndDispatch_SessionBusyMarksCorrection(t *testing.T) {
	f := setupMentionFixture(t)
	ctx := context.Background()
	if err := ensureMissionChat(ctx, f.db, f.missionID, f.wsID, f.target, "Test issue"); err != nil {
		t.Fatalf("ensureMissionChat: %v", err)
	}
	// A run already in flight on the session: DispatchMention returns a
	// sessionBusyError, so the delivery is released to pending.
	execOrFatal(t, f.db, `
		INSERT INTO issue_agent_sessions (id, workspace_id, mission_id, agent_id, state, last_consumed_seq, created_at, updated_at)
		VALUES ('sess_busy_corr', ?, ?, ?, 'active', 0, datetime('now'), datetime('now'))`, f.wsID, f.missionID, f.target)
	execOrFatal(t, f.db, `
		INSERT INTO assignments (id, workspace_id, chat_id, assigned_by_id, assigned_to_id, task, status, depth, created_at, mission_id, session_id)
		VALUES ('asg_busy_corr', ?, ?, ?, ?, 'in flight', 'RUNNING', 1, datetime('now'), ?, 'sess_busy_corr')`,
		f.wsID, f.missionID, f.target, f.target, f.missionID)
	execOrFatal(t, f.db, `
		INSERT INTO mission_comments (id, mission_id, author_type, author_id, body)
		VALUES ('cmt_busy_corr', ?, 'user', ?, 'actually, target the staging bucket')`, f.missionID, f.userID)

	rec := f.issues.mentionRecorder()
	mc := mentionContext{
		WorkspaceID: f.wsID, MissionID: f.missionID, Identifier: f.ident, IssueTitle: "Test issue",
		IssueCrewID: f.crewID, CommentID: "cmt_busy_corr", CommentBody: "actually, target the staging bucket",
		AuthorType: "user", AuthorID: f.userID,
	}
	mention := resolvedMention{AgentID: f.target, AgentSlug: "worker", Position: 0}
	// event id for the delivery
	eventID, written := rec.events.logEvent(ctx, issueEvent{
		MissionID: f.missionID, ActorType: "user", ActorID: f.userID, Action: actionMentioned, Details: f.target,
	})
	state, _, _ := rec.deliverAndDispatch(ctx, mc, mention, eventID, written.Seq, true)
	f.assign.WaitDispatches()
	if state != mentionDispatchQueued {
		t.Fatalf("dispatch state = %q, want queued (session busy)", state)
	}
	var pri, delState string
	if err := f.db.QueryRow(
		`SELECT priority, state FROM mission_comment_mentions WHERE event_id = ? AND agent_id = ?`,
		eventID, f.target).Scan(&pri, &delState); err != nil {
		t.Fatalf("read delivery: %v", err)
	}
	if delState != "pending" {
		t.Fatalf("delivery state = %q, want pending", delState)
	}
	if pri != deliveryPriorityCorrection {
		t.Errorf("delivery priority = %q, want correction", pri)
	}
}
