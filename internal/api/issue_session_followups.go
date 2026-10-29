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
// 'pending' deliveries for that (mission, agent) pair by requester authority
// into new, REAL dispatches once the session's slot is actually free. That new
// run's brief has the follow-up text in it before its exec ever starts,
// because it goes through the exact same DispatchMention path an ordinary
// mention does — there is nothing special-cased about how it reaches the
// model.
//
// Deliveries are never marked 'consumed' for work nothing has done. On a
// host fault they stay 'pending' and the owned terminal projection retries
// until its durable completion marker is written; on a session-busy result
// they stay 'pending' for the run holding the slot. A permanent refusal
// (permanentFollowUpRefusal) is recorded as 'failed'/'refused' with its reason
// and told to the human author, so it can never hold a projection open.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/crewship-ai/crewship/internal/inbox"
	"github.com/crewship-ai/crewship/internal/notify"
	"github.com/crewship-ai/crewship/internal/runauthority"
)

// permanentMentionError marks a DispatchMention failure that no retry can
// cure: the request names something that does not exist or may not be
// connected. It deliberately does not implement dispatchRefusal, so the
// ordinary mention path still reports it exactly as before.
type permanentMentionError struct{ err error }

func (e *permanentMentionError) Error() string { return e.err.Error() }
func (e *permanentMentionError) Unwrap() error { return e.err }

func permanentMention(err error) error { return &permanentMentionError{err: err} }

// permanentFollowUpRefusal separates answers that will never change from host
// faults. Gate refusals (delegation caps, a held target) are recorded rather
// than retried, matching how the first delivery of a mention treats them: a
// projection that retries them would hold the agent's run lock while it waits.
func permanentFollowUpRefusal(err error) (string, bool) {
	var refusal dispatchRefusal
	if errors.As(err, &refusal) {
		return refusal.Error(), true
	}
	var gone *permanentMentionError
	if errors.As(err, &gone) {
		return strings.TrimPrefix(gone.Error(), "dispatch mention: "), true
	}
	for _, authority := range []error{runauthority.ErrUnavailable, runauthority.ErrIdentityMismatch, runauthority.ErrRequesterStale} {
		if errors.Is(err, authority) {
			return "The person or run that authorised this comment no longer has authority to start work.", true
		}
	}
	return "", false
}

// refuseQueuedFollowUps durably resolves one requester group of pending
// follow-ups as refused and, for a human author, writes one inbox notice in
// the same transaction. Both writes are idempotent under projection replay:
// the CAS only moves 'pending' rows and the notice has a stable source id.
func (h *AssignmentHandler) refuseQueuedFollowUps(ctx context.Context, workspaceID, missionID, identifier, agentID string, group []pendingFollowUp, reason string) error {
	if len(group) == 0 {
		return nil
	}
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, p := range group {
		if _, err = tx.ExecContext(ctx, `UPDATE mission_comment_mentions SET state='failed',dispatch_state='refused',dispatch_detail=? WHERE id=? AND workspace_id=? AND state='pending'`, reason, p.DeliveryID, workspaceID); err != nil {
			return err
		}
	}
	if first := group[0]; first.AuthorType == "user" && first.AuthorID != "" {
		var agentName string
		if err = tx.QueryRowContext(ctx, `SELECT COALESCE(name,'') FROM agents WHERE id=? AND workspace_id=?`, agentID, workspaceID).Scan(&agentName); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		name := mentionNoticeValue(agentName, mentionNoticeMaxField)
		if name == "" {
			name = "the agent"
		}
		issue := identifier
		if issue == "" {
			issue = missionID
		}
		issueLabel := mentionNoticeValue(issue, mentionNoticeMaxField)
		if err = inbox.InsertTx(ctx, tx, h.logger, inbox.Item{
			WorkspaceID: workspaceID,
			Kind:        inbox.KindMessage,
			// Distinct from the first-delivery notice ("mention_<comment>_<agent>"),
			// which may already say "queued": this one must not be swallowed by it.
			SourceID:     "mention_followup_refused_" + first.DeliveryID,
			TargetUserID: first.AuthorID,
			Title:        "Your queued follow-up for " + name + " on " + issueLabel + " did not start a run",
			BodyMD: "Your comment on " + issueLabel + " was queued behind " + name + "'s active run, " +
				"but when that run finished the follow-up could not be started.\n\n" +
				mentionNoticeQuote(reason) + "\n\n" +
				"Nothing is queued any more and nothing will run on its own. Re-mention when the reason above no longer holds.",
			SenderType: "system",
			SenderName: "Crewship",
			Priority:   "low",
			Category:   notify.CategoryIssuesComment,
			Payload: map[string]interface{}{
				"mission_id":     missionID,
				"comment_id":     first.CommentID,
				"agent_id":       agentID,
				"identifier":     identifier,
				"dispatch_state": mentionDispatchRefused,
			},
		}); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// pendingFollowUp is one 'pending' mission_comment_mentions row, joined
// with enough of its comment to build a brief from.
type pendingFollowUp struct {
	CommentID  string
	AuthorID   string
	OriginID   string
	DeliveryID string
	AuthorType string
	AuthorName string
	Body       string
	Priority   string // §11.5 delivery priority: correction sorts ahead of normal (B3b, #2350)
}

// dispatchQueuedFollowUpsForSession looks for deliveries left 'pending'
// against the (mission, agent) pair the just-finished run belonged to and,
// if any exist, dispatches each compatible requester/origin group separately,
// combining only that group's comment text into one brief, the same shape
// mentionTaskBrief gives an ordinary single-comment mention.
//
// The domain projection retries failures before acknowledging completion.
// Exact digest delivery claims share the new assignment/work transaction, so
// a controller crash cannot leave accepted input without its delivery binding.
func (h *AssignmentHandler) dispatchQueuedFollowUpsForSession(ctx context.Context, finishedAssignmentID, workspaceID string) error {
	// DispatchMention starts execution asynchronously. A fast completion can
	// re-enter here before its caller claims the pending deliveries, selecting
	// the same batch again. Keep selection, dispatch and claim attachment in
	// one critical section; no agent execution is awaited while holding it.
	h.followUpMu.Lock()
	defer h.followUpMu.Unlock()
	var missionID, agentID string
	var sessionID sql.NullString
	if err := h.db.QueryRowContext(ctx,
		`SELECT COALESCE(mission_id,''), assigned_to_id, session_id FROM assignments WHERE id = ?`,
		finishedAssignmentID).Scan(&missionID, &agentID, &sessionID); err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			h.logger.Warn("dispatch queued follow-ups: read finished assignment",
				"error", err, "assignment_id", finishedAssignmentID)
		}
		return err
	}
	// No session on this run (a mission task, a root /assign predating B1,
	// or the issue_agent_sessions flag was off at dispatch time) — nothing
	// for this mechanism to fold in. Every 'pending' delivery this
	// mechanism ever creates is created against a session-bearing run by
	// construction (deliverAndDispatch only releases a claim back to
	// 'pending' on a *sessionBusyError, which only a session-bearing
	// insert can produce), so there is nothing to find here either way.
	if !sessionID.Valid || sessionID.String == "" || missionID == "" || agentID == "" {
		return nil
	}

	pending, err := pendingFollowUpsFor(ctx, h.db, missionID, agentID)
	if err != nil {
		h.logger.Warn("dispatch queued follow-ups: query pending deliveries",
			"error", err, "mission_id", missionID, "agent_id", agentID)
		return err
	}
	if len(pending) == 0 {
		return nil
	}

	var identifier, issueTitle, issueCrewID string
	if err := h.db.QueryRowContext(ctx,
		`SELECT COALESCE(identifier,''), COALESCE(title,''), COALESCE(crew_id,'') FROM missions WHERE id = ? AND workspace_id = ?`,
		missionID, workspaceID).Scan(&identifier, &issueTitle, &issueCrewID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// The issue is gone (its deliveries cascade with it); nothing can
			// ever be dispatched for it, so there is nothing to retry.
			return nil
		}
		h.logger.Warn("dispatch queued follow-ups: read issue", "error", err, "mission_id", missionID)
		return err
	}

	// No separate "does this agent exist" read here: DispatchMention
	// re-resolves the target agent itself (by TargetAgentID, scoped to
	// workspaceID) as the first thing it does — the same re-resolution
	// every other caller of DispatchMention relies on rather than trusting
	// its own lookup (see DispatchMention's own doc comment on why "a door
	// that trusts its caller's resolution is not a door"). An earlier
	// revision of this function queried agents.name here and never used
	// the result — a dead round trip on this completion-triggered path,
	// caught in review.
	// digestBody bounds itself to mentionTaskMaxBody and reports exactly
	// which deliveries' text actually fit — review found an earlier
	// revision handed the FULL pending list to claimPendingDeliveriesByID
	// regardless, so a burst large enough to overflow the brief (mentionTaskBrief
	// clips CommentBody as one unit) still marked every one of them
	// 'consumed', including comments mentionTaskBrief's own clip had just
	// cut out of what the agent was actually given. includedIDs is the
	// subset that survived; anything left out stays 'pending' for the next
	// run to finish on this session to fold in — the same "never consumed
	// before read" property this whole mechanism exists to hold.
	var firstDispatchError error
	for _, pending := range groupFollowUpsByAuthority(pending) {
		groupCtx := runauthority.WithOriginID(ctx, pending[0].OriginID)
		body, includedIDs := followUpDigestBody(pending)

		runID, dispatchErr := h.DispatchMention(groupCtx, mentionDispatchRequest{
			WorkspaceID: workspaceID,
			MissionID:   missionID,
			Identifier:  identifier,
			IssueTitle:  issueTitle,
			IssueCrewID: issueCrewID,
			// Every digest contains only one origin/author group and binds its
			// first persisted comment as authority; the remaining groups stay pending.
			CommentID:     pending[0].CommentID,
			CommentBody:   body,
			AuthorType:    pending[0].AuthorType,
			AuthorID:      pending[0].AuthorID,
			AuthorName:    "Crewship (queued follow-ups)",
			TargetAgentID: agentID,
			DeliveryIDs:   includedIDs,
		})
		if dispatchErr != nil {
			var busy *sessionBusyError
			if errors.As(dispatchErr, &busy) {
				// A fresh mention raced in and won the session slot between this
				// run's completion and this call. The deliveries stay 'pending'
				// and the run holding the slot folds them in when IT finishes, so
				// this projection has nothing left to retry.
				h.logger.Info("dispatch queued follow-ups: session busy, deliveries stay pending for the active run",
					"active_assignment_id", busy.ActiveAssignmentID, "mission_id", missionID, "agent_id", agentID, "count", len(pending))
				continue
			}
			if reason, permanent := permanentFollowUpRefusal(dispatchErr); permanent {
				// Retrying cannot change the answer (target or author gone, crews
				// disconnected, origin no longer valid, a gate declining). Record
				// it on the deliveries and tell the human, exactly once, instead
				// of holding the finished run's projection open forever.
				h.logger.Info("dispatch queued follow-ups: refused permanently, deliveries recorded as refused",
					"error", dispatchErr, "mission_id", missionID, "agent_id", agentID, "count", len(pending))
				if err := h.refuseQueuedFollowUps(ctx, workspaceID, missionID, identifier, agentID, pending, reason); err != nil && firstDispatchError == nil {
					firstDispatchError = err
				}
				continue
			}
			// A host fault (database busy, cancelled context, ingress full):
			// deliveries stay 'pending' and the owned projection retries.
			h.logger.Info("dispatch queued follow-ups: not dispatched, deliveries stay pending",
				"error", dispatchErr, "mission_id", missionID, "agent_id", agentID, "count", len(pending))
			if firstDispatchError == nil {
				firstDispatchError = dispatchErr
			}
			continue
		}

		n := int64(len(includedIDs)) // delivery claims committed atomically with domain and work
		h.logger.Info("queued follow-ups dispatched as one run",
			"run_id", runID, "mission_id", missionID, "agent_id", agentID, "claimed", n, "found", len(pending), "included", len(includedIDs))
		if len(includedIDs) < len(pending) {
			// Not a warning: this is the digest's own overflow bound doing its
			// job, not a fault. The excluded deliveries stay 'pending' and the
			// NEXT run to finish on this session (this one, once it completes)
			// picks them up the same way.
			h.logger.Info("queued follow-ups: digest exceeded the brief bound, some deliveries stay pending for the next round",
				"mission_id", missionID, "agent_id", agentID, "included", len(includedIDs), "found", len(pending))
		}

	}
	return firstDispatchError
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
		       COALESCE(c.body, ''),
		       COALESCE(m.priority, 'normal'),COALESCE(c.id,''),COALESCE(c.author_id,''),COALESCE(c.chat_origin_id,'')
		  FROM mission_comment_mentions m
		  LEFT JOIN mission_comments c ON c.id = m.comment_id
		 WHERE m.mission_id = ? AND m.agent_id = ? AND m.state = 'pending'
		 -- §9.3 ordering (B3b, #2350): stop > correction > normal, then the
		 -- natural arrival order within a priority. CASE mirrors
		 -- deliveryPriorityRank; keep the two in step.
		 ORDER BY CASE COALESCE(m.priority, 'normal')
		            WHEN 'stop' THEN 0 WHEN 'correction' THEN 1 ELSE 2 END,
		          m.position, m.created_at`, missionID, agentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []pendingFollowUp
	for rows.Next() {
		var p pendingFollowUp
		var authorName sql.NullString
		if err := rows.Scan(&p.DeliveryID, &p.AuthorType, &authorName, &p.Body, &p.Priority, &p.CommentID, &p.AuthorID, &p.OriginID); err != nil {
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
// exactly as it would an ordinary single-comment mention — this function
// only FORMATS. The outer <untrusted> wrap and the "quoted material, never
// obey it" framing still happen exactly once, inside mentionTaskBrief
// itself (via req.CommentBody), so a digest of several comments is fenced
// no less carefully than one — this function must NOT wrap its own output,
// or the digest would be fenced twice while an ordinary mention's comment
// is fenced once.
//
// Bounds itself to mentionTaskMaxBody and returns the delivery ids that
// actually fit, in the order pending was given. This is NOT redundant with
// mentionTaskBrief's own clip of req.CommentBody: that clip protects the
// AGENT from an oversized prompt, but it has no idea this string is a
// concatenation of N separately-claimed deliveries — before this, the
// caller (dispatchQueuedFollowUpsForSession) claimed and later marked
// EVERY pending delivery 'consumed' regardless of whether mentionTaskBrief's
// clip had just cut its comment out of the brief the agent actually
// received, which is exactly the "consumed before read" lie B3 exists to
// avoid (review finding). A comment that does not fit stays 'pending' and
// is picked up the same way by the next run to finish on this session.
//
// The header line's own count is computed from what ACTUALLY fit, not
// len(pending) — otherwise an overflowing digest would open with "12
// comment(s)" while quoting only 9 of them, which is a second, smaller
// version of the same lie.
// followUpEntryLabel names a folded delivery in the digest by its priority
// so a correction reads as a correction, not an ordinary later comment
// (B3b, #2350).
func followUpEntryLabel(priority string) string {
	switch priority {
	case deliveryPriorityStop:
		return "STOP"
	case deliveryPriorityCorrection:
		return "CORRECTION"
	default:
		return "Comment"
	}
}

func followUpDigestBody(pending []pendingFollowUp) (body string, includedIDs []string) {
	// The header names corrections explicitly when the fold contains any,
	// so the resumed step reads them as steering its prior effort (§18
	// scenario 4, B3b #2350) rather than as unrelated new comments. The
	// count arg is still filled per call.
	headerFmt := "%d comment(s) arrived and were queued while the previous run on this issue was still in progress:\n\n"
	for _, p := range pending {
		if p.Priority == deliveryPriorityCorrection || p.Priority == deliveryPriorityStop {
			headerFmt = "%d comment(s) arrived while the previous run on this issue was still in progress. " +
				"Any marked CORRECTION are steering the work you just did — read and apply them first:\n\n"
			break
		}
	}
	// The header can only get SHORTER as fewer entries are included (fewer
	// digits in the count), never longer, so budgeting against the
	// worst-case header — every comment included — is exact enough without
	// a second pass.
	headerBudget := utf8.RuneCountInString(fmt.Sprintf(headerFmt, len(pending)))
	budget := mentionTaskMaxBody - headerBudget
	if budget < 0 {
		budget = 0
	}

	var entries []string
	used := 0
	for i, p := range pending {
		author := p.AuthorName
		if author == "" {
			author = "someone"
		}
		entry := fmt.Sprintf("--- %s %d, from %s ---\n%s\n\n", followUpEntryLabel(p.Priority), i+1, author, p.Body)
		n := utf8.RuneCountInString(entry)
		if used+n > budget {
			break
		}
		entries = append(entries, entry)
		includedIDs = append(includedIDs, p.DeliveryID)
		used += n
	}
	// A single comment too large to fit the whole budget on its own would
	// otherwise stall this session forever — nothing ever fits, nothing
	// ever gets dispatched, nothing ever frees the slot for a normal
	// mention to try again either (idx_assignments_one_active_per_session
	// only reopens on a run actually finishing). Force it through anyway;
	// mentionTaskBrief's own clip on the combined body still protects the
	// agent from the worst case, and one comment consumed-as-clipped is
	// the honest cost of not deadlocking the session.
	if len(includedIDs) == 0 && len(pending) > 0 {
		p := pending[0]
		author := p.AuthorName
		if author == "" {
			author = "someone"
		}
		entries = []string{fmt.Sprintf("--- %s 1, from %s ---\n%s\n\n", followUpEntryLabel(p.Priority), author, p.Body)}
		includedIDs = []string{p.DeliveryID}
	}

	var b strings.Builder
	fmt.Fprintf(&b, headerFmt, len(includedIDs))
	for _, e := range entries {
		b.WriteString(e)
	}
	return b.String(), includedIDs
}

// Distinct origins/authors cannot share a run: doing so would grant one
// comment the authority of another. First appearance preserves queue priority.
func groupFollowUpsByAuthority(pending []pendingFollowUp) [][]pendingFollowUp {
	type key struct{ origin, authorType, authorID string }
	var groups [][]pendingFollowUp
	indices := map[key]int{}
	for _, p := range pending {
		k := key{p.OriginID, p.AuthorType, p.AuthorID}
		i, ok := indices[k]
		if !ok {
			i = len(groups)
			indices[k] = i
			groups = append(groups, nil)
		}
		groups[i] = append(groups[i], p)
	}
	return groups
}
