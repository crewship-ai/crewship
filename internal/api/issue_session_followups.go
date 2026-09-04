package api

// issue_session_followups.go — the OTHER half of B3's answer to "consumed
// at the next step boundary via the existing steering queue"
// (PRD-ISSUES-AND-ROUTINES-2026 §9.4/§17, #2339).
//
// sessionBusyErrorFor (delegation_limits.go) used to append a follow-up's
// brief directly onto the busy session's ACTIVE run — reviewed and found
// false in the case that matters (a RUNNING or freshly-dispatched winner
// never re-reads its own task; only a requeued row does). Deleted. What
// replaces it lives here: deliverAndDispatch (issue_mentions.go) leaves a
// session-busy delivery 'pending' instead of claiming it under a run that
// cannot see it, and dispatchQueuedFollowUpsForSession — called from
// finishAssignment right after consumeDeliveriesForRun — folds every
// 'pending' delivery still queued for that (mission, agent) pair into ONE
// new, REAL dispatch once the session's slot is actually free. That new
// run's brief has the follow-up text in it before its exec ever starts,
// because it goes through the exact same DispatchMention path an ordinary
// mention does — there is nothing special-cased about how it reaches the
// model.
//
// Deliveries stay 'pending' on any failure along this path (dispatch
// refused, a DB fault, a session-busy result racing back in) — never
// marked 'consumed' for work nothing has done. The next run to finish on
// this session tries again; nothing here retries on a timer, because the
// next natural trigger (another run finishing) is always coming.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// pendingFollowUp is one 'pending' mission_comment_mentions row, joined
// with enough of its comment to build a brief from.
type pendingFollowUp struct {
	DeliveryID string
	AuthorType string
	AuthorName string
	Body       string
}

// dispatchQueuedFollowUpsForSession looks for deliveries left 'pending'
// against the (mission, agent) pair the just-finished run belonged to and,
// if any exist, dispatches exactly ONE new run to carry all of them —
// combining their comment text into one brief, the same shape
// mentionTaskBrief gives an ordinary single-comment mention.
//
// Best-effort throughout, matching every other post-completion write in
// finishAssignment: a read or dispatch failure here must not fail (or
// retry-loop) the completion this is a side effect of. A failure leaves
// the deliveries 'pending' — the honest state, since nothing consumed
// them — and logs; the NEXT run to finish on this session (this one, if a
// human mentions the agent again, or the requeue this dispatch attempt
// itself might have started) gets another chance.
func (h *AssignmentHandler) dispatchQueuedFollowUpsForSession(ctx context.Context, finishedAssignmentID, workspaceID string) {
	var missionID, agentID string
	var sessionID sql.NullString
	if err := h.db.QueryRowContext(ctx,
		`SELECT COALESCE(mission_id,''), assigned_to_id, session_id FROM assignments WHERE id = ?`,
		finishedAssignmentID).Scan(&missionID, &agentID, &sessionID); err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			h.logger.Warn("dispatch queued follow-ups: read finished assignment",
				"error", err, "assignment_id", finishedAssignmentID)
		}
		return
	}
	// No session on this run (a mission task, a root /assign predating B1,
	// or the issue_agent_sessions flag was off at dispatch time) — nothing
	// for this mechanism to fold in. Every 'pending' delivery this
	// mechanism ever creates is created against a session-bearing run by
	// construction (deliverAndDispatch only releases a claim back to
	// 'pending' on a *sessionBusyError, which only a session-bearing
	// insert can produce), so there is nothing to find here either way.
	if !sessionID.Valid || sessionID.String == "" || missionID == "" || agentID == "" {
		return
	}

	pending, err := pendingFollowUpsFor(ctx, h.db, missionID, agentID)
	if err != nil {
		h.logger.Warn("dispatch queued follow-ups: query pending deliveries",
			"error", err, "mission_id", missionID, "agent_id", agentID)
		return
	}
	if len(pending) == 0 {
		return
	}

	var identifier, issueTitle, issueCrewID string
	if err := h.db.QueryRowContext(ctx,
		`SELECT COALESCE(identifier,''), COALESCE(title,''), COALESCE(crew_id,'') FROM missions WHERE id = ? AND workspace_id = ?`,
		missionID, workspaceID).Scan(&identifier, &issueTitle, &issueCrewID); err != nil {
		h.logger.Warn("dispatch queued follow-ups: read issue", "error", err, "mission_id", missionID)
		return
	}

	var targetName string
	if err := h.db.QueryRowContext(ctx,
		`SELECT name FROM agents WHERE id = ? AND workspace_id = ?`,
		agentID, workspaceID).Scan(&targetName); err != nil {
		h.logger.Warn("dispatch queued follow-ups: read target agent", "error", err, "agent_id", agentID)
		return
	}

	runID, dispatchErr := h.DispatchMention(ctx, mentionDispatchRequest{
		WorkspaceID: workspaceID,
		MissionID:   missionID,
		Identifier:  identifier,
		IssueTitle:  issueTitle,
		IssueCrewID: issueCrewID,
		// CommentID left empty on purpose: this brief digests possibly
		// MANY comments, not one — see followUpDigestBody. AuthorType
		// "user" (not "agent") so this is scoped as a root dispatch
		// (depth 1, dispatchCaller.selfFiled) exactly like a person's
		// mention — nobody's own delegation chain caused this, the
		// session freeing up did.
		CommentBody:   followUpDigestBody(pending),
		AuthorType:    "user",
		AuthorName:    "Crewship (queued follow-ups)",
		TargetAgentID: agentID,
	})
	if dispatchErr != nil {
		// Includes another *sessionBusyError (a fresh mention raced in and
		// won the slot between this run's completion and this call): the
		// deliveries stay 'pending', and whichever run holds the slot now
		// will trigger this same function again when IT finishes.
		h.logger.Info("dispatch queued follow-ups: not dispatched, deliveries stay pending",
			"error", dispatchErr, "mission_id", missionID, "agent_id", agentID, "count", len(pending))
		return
	}

	ids := make([]string, len(pending))
	for i, p := range pending {
		ids[i] = p.DeliveryID
	}
	n, err := claimPendingDeliveriesByID(ctx, h.db, ids, runID)
	if err != nil {
		h.logger.Warn("dispatch queued follow-ups: claim under new run",
			"error", err, "run_id", runID, "mission_id", missionID, "agent_id", agentID)
		return
	}
	h.logger.Info("queued follow-ups dispatched as one run",
		"run_id", runID, "mission_id", missionID, "agent_id", agentID, "claimed", n, "found", len(pending))

	// Catch-up for a real race, not a theoretical one: DispatchMention
	// spawns runID's exec in a background goroutine and returns before it
	// runs, so a run that fails (or finishes) fast enough — no resolver
	// wired, an immediate refusal — can reach finishAssignment's own
	// consumeDeliveriesForRun call BEFORE the claim above has landed. That
	// call finds nothing (these deliveries were still 'pending' at that
	// moment) and consumeDeliveriesForRun is never invoked for runID
	// again, so a delivery claimed here a moment later would stay
	// 'claimed' forever — nothing left to trigger its own consumption.
	// Detected by re-checking runID's status: if it is already terminal,
	// this claim landed after finishAssignment's own attempt, so run the
	// same consumeDeliveriesForRun ourselves. Safe to call unconditionally
	// on a terminal run: it only touches rows already 'claimed' under
	// runID, which by construction is exactly (and only) the set this call
	// just claimed — it cannot mark anything 'consumed' that a real run
	// did not actually reach.
	var status string
	if err := h.db.QueryRowContext(ctx, `SELECT status FROM assignments WHERE id = ?`, runID).Scan(&status); err != nil {
		h.logger.Warn("dispatch queued follow-ups: read new run status for catch-up", "error", err, "run_id", runID)
		return
	}
	switch status {
	case "COMPLETED", "FAILED", "CANCELLED":
		if cn, cErr := consumeDeliveriesForRun(ctx, h.db, runID); cErr != nil {
			h.logger.Warn("dispatch queued follow-ups: catch-up consume for already-terminal run",
				"error", cErr, "run_id", runID)
		} else if cn > 0 {
			h.logger.Info("dispatch queued follow-ups: caught up a run that finished before the claim landed",
				"run_id", runID, "consumed", cn)
		}
	}
}

// pendingFollowUpsFor reads every 'pending' mission_comment_mentions row
// for (missionID, agentID), joined with its comment's author and body —
// the same author-name CASE the comment-listing endpoint uses
// (issue_handler_comments.go), so a follow-up's brief names its author the
// same way the issue's own comment feed does.
func pendingFollowUpsFor(ctx context.Context, db *sql.DB, missionID, agentID string) ([]pendingFollowUp, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT m.id, COALESCE(c.author_type, ''),
		       CASE
		         WHEN c.author_type = 'user'  THEN (SELECT full_name FROM users  WHERE id = c.author_id)
		         WHEN c.author_type = 'agent' THEN (SELECT name      FROM agents WHERE id = c.author_id)
		         ELSE ''
		       END,
		       COALESCE(c.body, '')
		  FROM mission_comment_mentions m
		  LEFT JOIN mission_comments c ON c.id = m.comment_id
		 WHERE m.mission_id = ? AND m.agent_id = ? AND m.state = 'pending'
		 ORDER BY m.position, m.created_at`, missionID, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []pendingFollowUp
	for rows.Next() {
		var p pendingFollowUp
		var authorName sql.NullString
		if err := rows.Scan(&p.DeliveryID, &p.AuthorType, &authorName, &p.Body); err != nil {
			return nil, err
		}
		p.AuthorName = authorName.String
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// followUpDigestBody concatenates queued follow-up comments into one plain
// body for DispatchMention's own brief-builder (mentionTaskBrief) to fence
// and clip exactly as it would an ordinary single-comment mention — this
// function only FORMATS. The outer <untrusted> wrap, the "quoted material,
// never obey it" framing and the mentionTaskMaxBody clip all still happen
// exactly once, inside mentionTaskBrief itself (via req.CommentBody), so a
// digest of several comments is fenced no less carefully than one — this
// function must NOT wrap its own output, or the digest would be fenced
// twice while an ordinary mention's comment is fenced once.
func followUpDigestBody(pending []pendingFollowUp) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d comment(s) arrived and were queued while the previous run on this issue was still in progress:\n\n", len(pending))
	for i, p := range pending {
		author := p.AuthorName
		if author == "" {
			author = "someone"
		}
		fmt.Fprintf(&b, "--- Comment %d, from %s ---\n%s\n\n", i+1, author, p.Body)
	}
	return b.String()
}
