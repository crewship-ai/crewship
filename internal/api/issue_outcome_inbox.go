package api

// issue_outcome_inbox.go — the NEEDS_HUMAN half of the §9.6/§12 outcome
// contract (PRD-ISSUES-AND-ROUTINES-2026, work package B6, #2349): the ONE
// call site that turns a run's outcome into an inbox item, reached only
// through orchestrator.RouteForOutcome(outcome).CreatesInboxItem —
// currently true for NEEDS_HUMAN alone (§12: "NO_CHANGE and SUCCEEDED
// never create an item").
//
// §16.1 checklist for this new inbox kind (run_needs_human):
//   - backup classification: inbox_items is already BackupTableIntent-
//     classified; this is a new VALUE in an existing column, not a new
//     table, so nothing in internal/backup/intent.go changes.
//   - data_subject_id: left NULL, matching every other kind this package's
//     shared writer inserts (escalation, failed_run, ...) — the body is a
//     short agent-authored status line (a checkpoint's blockers/next_step,
//     or a HANDOFF summary), not content ABOUT a specific person, so there
//     is no data subject to tag. Scrubbed before persist regardless
//     (outcomeInboxScrubber), matching the checkpoint/mission_activity rule.
//   - DELETE/SELECT GDPR blocks: inbox_items' existing by-data_subject_id
//     block already covers every kind uniformly; nothing kind-specific to
//     add for a kind that sets no data_subject_id.
//   - feature flag: none — outcome itself is not flag-gated (unlike B1/B2's
//     issue_agent_sessions / issue_deliveries flags, a run that never
//     reports an outcome still gets one, by default), so gating item
//     creation separately would let outcome=NEEDS_HUMAN silently produce
//     no card, the exact failure §9.6 says an absent-outcome default must
//     not be.

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/crewship-ai/crewship/internal/inbox"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/scrubber"
)

// outcomeInboxScrubber redacts secrets from the agent-reported text before
// it is persisted onto the inbox card's body — §16.1's "Scrub before
// persist" rule, same instance shape as checkpointScrubber
// (issue_checkpoints.go): the pattern set is fixed, so one instance is
// shared process-wide.
var outcomeInboxScrubber = scrubber.New()

// createOutcomeInboxItem writes the §12 action contract for a NEEDS_HUMAN
// run. Exactly one item per run (§18 scenario 15's "exactly one inbox
// item"): inbox.Insert's underlying (kind, source_id) unique index is keyed
// on assignmentID here, via id = "ibx_run_needs_human_<assignmentID>", so a
// second call for the SAME run — a duplicate terminal write that lost
// finishAssignment's own terminal CAS and could never reach this far, or
// any future retried caller — is silently absorbed rather than doubled.
//
// Best-effort: called from finishAssignment's post-completion side
// effects, which must never fail (or block) the completion this is a side
// effect of — same contract as recordSessionCheckpoint and
// consumeDeliveriesForRun right next to its call site.
func (h *AssignmentHandler) createOutcomeInboxItem(ctx context.Context, assignmentID, workspaceID, targetSlug, result string) {
	var (
		missionID   sql.NullString
		identifier  sql.NullString
		ownerUserID sql.NullString
		agentName   sql.NullString
	)
	err := h.db.QueryRowContext(ctx, `
		SELECT COALESCE(a.group_id, a.chat_id), m.identifier, m.owner_user_id, ag.name
		  FROM assignments a
		  LEFT JOIN missions m ON m.id = COALESCE(a.group_id, a.chat_id)
		  LEFT JOIN agents ag ON ag.id = a.assigned_to_id
		 WHERE a.id = ?`, assignmentID).Scan(&missionID, &identifier, &ownerUserID, &agentName)
	if err != nil {
		h.logger.Warn("create outcome inbox item: load assignment", "error", err, "assignment_id", assignmentID)
		return
	}

	issueLabel := targetSlug
	if identifier.Valid && identifier.String != "" {
		issueLabel = identifier.String
	}
	agentLabel := agentName.String
	if agentLabel == "" {
		agentLabel = targetSlug
	}

	// §11.3's checkpoint (blockers, then next_step) is the richer signal
	// when a session-bearing run reported it; HANDOFF's summary is the
	// fallback for a mission-task run. Neither is required to report
	// NEEDS_HUMAN at all — a run can say only "outcome: NEEDS_HUMAN" and
	// nothing else, and the card must still say something useful rather
	// than render an empty body.
	reason := ""
	if cp := orchestrator.ParseCheckpoint(result); cp.Parsed {
		switch {
		case cp.Blockers != "":
			reason = cp.Blockers
		case cp.NextStep != "":
			reason = cp.NextStep
		}
	}
	if reason == "" {
		if hd := orchestrator.ParseHandoff(result); hd.Parsed && hd.Summary != "" {
			reason = hd.Summary
		}
	}
	if reason == "" {
		reason = "The agent stopped and needs a decision, missing input, or a credential to continue."
	}
	reason = outcomeInboxScrubber.Scrub(reason)

	// §12's action contract. who_can_act prefers the issue's own human
	// owner (missions.owner_user_id, Track A10, I5 — "the human owner stays
	// the owner") over a blanket role, so the person actually responsible
	// for the issue is the one addressed; MANAGER is the fallback for an
	// issue with no recorded owner, matching every other escalation kind's
	// default audience.
	whoCanAct := []string{"role:MANAGER"}
	targetUserID := ""
	targetRole := "MANAGER"
	if ownerUserID.Valid && ownerUserID.String != "" {
		targetUserID = ownerUserID.String
		targetRole = ""
		whoCanAct = []string{"user:" + ownerUserID.String}
	}

	contractContext := map[string]any{"issue": issueLabel, "run": assignmentID}
	if missionID.Valid && missionID.String != "" {
		contractContext["mission_id"] = missionID.String
	}

	payload := map[string]any{
		"attention_class": "input",
		// One card per run, not per (issue, day): a run is a one-time
		// event, so its thread_key is its own id — unlike a recurring
		// routine condition (§12's "routine:daily-triage:2026-09-01"
		// example), there is no daily recurrence to collapse onto one
		// card. B10 owns cross-run deduping by SUBJECT (thread_key shared
		// across several runs of the same routine); this is what a single
		// run's own contract looks like before that layer exists.
		"thread_key":  "run:" + assignmentID,
		"who_can_act": whoCanAct,
		"actions": []map[string]any{
			{"id": "take_over", "label": "Take over", "effect": "Opens the issue for you to continue", "irreversible": false},
		},
		"context": contractContext,
	}

	if err := inbox.Insert(ctx, h.db, h.logger, inbox.Item{
		WorkspaceID:  workspaceID,
		Kind:         inbox.KindRunNeedsHuman,
		SourceID:     assignmentID,
		TargetUserID: targetUserID,
		TargetRole:   targetRole,
		Title:        fmt.Sprintf("%s needs your input on %s", agentLabel, issueLabel),
		BodyMD:       reason,
		SenderType:   "agent",
		SenderID:     targetSlug,
		SenderName:   agentLabel,
		Priority:     "high",
		Blocking:     true,
		Payload:      payload,
	}); err != nil {
		h.logger.Warn("create outcome inbox item", "error", err, "assignment_id", assignmentID)
	}
}
