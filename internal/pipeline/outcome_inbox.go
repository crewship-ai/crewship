package pipeline

// outcome_inbox.go — the NEEDS_HUMAN half of the §9.6/§12 outcome contract
// for pipeline_runs (PRD-ISSUES-AND-ROUTINES-2026, work package B6, #2349).
// The assignments twin lives in internal/api/issue_outcome_inbox.go; the two
// share the vocabulary and routing decision (internal/orchestrator/outcome.go)
// but not this write, because a pipeline_runs row and an assignments row
// carry different context (a routine's pipeline_id/triggered_via vs. an
// issue session's mission_id/agent) and this package has no dependency on
// internal/api.
//
// §16.1 checklist: same as issue_outcome_inbox.go — inbox_items is already
// backup-classified, this is a new value in the existing kind CHECK plus
// the migration widening it; no data_subject_id (the body is a scrubbed,
// agent/routine-authored status line, not content about a specific
// person); no new feature flag (an unreported outcome still gets a routed
// default, so gating item creation separately would silently swallow it).

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"

	"github.com/crewship-ai/crewship/internal/inbox"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/scrubber"
)

// outcomeInboxScrubber redacts secrets from the run's own output before it
// is persisted onto the inbox card's body — §16.1's "Scrub before persist"
// rule, same instance shape as internal/api's checkpointScrubber /
// outcomeInboxScrubber: the pattern set is fixed, so one instance is shared
// process-wide.
var outcomeInboxScrubber = scrubber.New()

// createOutcomeInboxItem writes the §12 action contract for a pipeline run
// whose outcome resolved to NEEDS_HUMAN. Exactly one item per run:
// inbox.Insert's (kind, source_id) unique index is keyed on runID, so a
// retried or duplicate call is silently absorbed. Best-effort — logged, not
// returned — matching MarkTerminal's own persistWarn-style contract for
// everything past the terminal UPDATE itself.
func (s *RunStore) createOutcomeInboxItem(ctx context.Context, runID, output string) {
	var (
		workspaceID   string
		pipelineID    string
		pipelineSlug  string
		triggeredVia  string
		triggeredByID sql.NullString
		invokingAgent sql.NullString
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT workspace_id, pipeline_id, pipeline_slug, triggered_via, triggered_by_id, invoking_agent_id
		  FROM pipeline_runs WHERE id = ?`, runID,
	).Scan(&workspaceID, &pipelineID, &pipelineSlug, &triggeredVia, &triggeredByID, &invokingAgent)
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			slog.Default().Warn("create outcome inbox item: load run", "error", err, "run_id", runID)
		}
		return
	}

	// §11.3's checkpoint (blockers, then next_step) or HANDOFF's summary is
	// the concise signal, when the run's own agent_run step reported one —
	// mirrors issue_outcome_inbox.go's createOutcomeInboxItem rather than
	// dumping the run's ENTIRE (potentially large, potentially secret-
	// bearing) output verbatim onto the card.
	reason := ""
	if cp := orchestrator.ParseCheckpoint(output); cp.Parsed {
		switch {
		case cp.Blockers != "":
			reason = cp.Blockers
		case cp.NextStep != "":
			reason = cp.NextStep
		}
	}
	if reason == "" {
		if hd := orchestrator.ParseHandoff(output); hd.Parsed && hd.Summary != "" {
			reason = hd.Summary
		}
	}
	if reason == "" {
		reason = "This run stopped and needs a decision, missing input, or a credential to continue."
	}
	reason = outcomeInboxScrubber.Scrub(reason)
	reason = truncateReasonRuneSafe(reason, 500)

	contractContext := map[string]any{"run": runID, "routine": pipelineSlug}
	// TriggeredViaIssue carries the issue identifier in triggered_by_id
	// (RunRecord.TriggeredByID's own doc comment, runs.go) — surface it so
	// the card can be read alongside the issue it was fired from.
	if triggeredVia == string(TriggeredViaIssue) && triggeredByID.Valid {
		contractContext["issue"] = triggeredByID.String
	}

	// B10 (#2364): keyed per ROUTINE, not per run — the same "cross-run
	// deduping by subject" fix issue_outcome_inbox.go's assignments twin
	// got. A routine that needs a human on run 1, gets a decision, then
	// needs one again on run 3 is the same recurring condition ("this
	// routine keeps needing a human"); WriteThreaded only merges into a
	// still-OPEN row, so a resolved earlier card does not swallow a later,
	// genuinely new occurrence. Falls back to the pipeline_slug when
	// pipeline_id is somehow empty (should not happen — pipeline_runs.
	// pipeline_id is NOT NULL — but a thread_key is still better than none).
	routineKey := pipelineID
	if routineKey == "" {
		routineKey = pipelineSlug
	}
	threadKey := "routine:" + workspaceID + ":" + routineKey

	// attention_class/thread_key/actions stay off payload now that they are
	// real columns (B10, #2364, caught in review — see the matching note in
	// internal/api/issue_outcome_inbox.go). Only who_can_act and context
	// have no dedicated column.
	payload := map[string]any{
		"who_can_act": []string{"role:MANAGER"},
		"context":     contractContext,
	}

	if err := inbox.WriteThreaded(ctx, s.db, nil, inbox.Item{
		WorkspaceID:    workspaceID,
		Kind:           inbox.KindRunNeedsHuman,
		SourceID:       runID,
		TargetRole:     "MANAGER",
		Title:          fmt.Sprintf("Routine %s needs your input", pipelineSlug),
		BodyMD:         reason,
		SenderType:     "pipeline",
		SenderID:       invokingAgent.String,
		SenderName:     pipelineSlug,
		Priority:       "high",
		Blocking:       true,
		Payload:        payload,
		ThreadKey:      threadKey,
		AttentionClass: inbox.AttentionInput,
		Actions: []inbox.Action{
			{ID: "review_run", Label: "Review run", Effect: "Opens the run detail", Irreversible: false},
		},
	}); err != nil {
		slog.Default().Warn("create outcome inbox item", "error", err, "run_id", runID)
	}
}

// truncateReasonRuneSafe caps s at max bytes without splitting a multi-byte
// UTF-8 rune — the same shape truncateForGraderLog (outcomes.go) and
// truncateForPreview (journal.go) already use in this package, applied here
// too: a NEEDS_HUMAN reason over the cap with a Czech, CJK or emoji
// character sitting at the cut point would otherwise land invalid UTF-8 in
// the inbox card body, rendering as U+FFFD.
func truncateReasonRuneSafe(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && cut > max-4 && (s[cut]&0xc0) == 0x80 {
		cut--
	}
	return s[:cut] + "...(truncated)"
}
