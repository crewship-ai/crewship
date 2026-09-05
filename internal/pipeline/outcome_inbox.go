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
		pipelineSlug  string
		triggeredVia  string
		triggeredByID sql.NullString
		invokingAgent sql.NullString
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT workspace_id, pipeline_slug, triggered_via, triggered_by_id, invoking_agent_id
		  FROM pipeline_runs WHERE id = ?`, runID,
	).Scan(&workspaceID, &pipelineSlug, &triggeredVia, &triggeredByID, &invokingAgent)
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
	const maxReason = 500
	if len(reason) > maxReason {
		reason = reason[:maxReason] + "...(truncated)"
	}

	contractContext := map[string]any{"run": runID, "routine": pipelineSlug}
	// TriggeredViaIssue carries the issue identifier in triggered_by_id
	// (RunRecord.TriggeredByID's own doc comment, runs.go) — surface it so
	// the card can be read alongside the issue it was fired from.
	if triggeredVia == string(TriggeredViaIssue) && triggeredByID.Valid {
		contractContext["issue"] = triggeredByID.String
	}

	payload := map[string]any{
		"attention_class": "input",
		"thread_key":      "run:" + runID,
		"who_can_act":     []string{"role:MANAGER"},
		"actions": []map[string]any{
			{"id": "review_run", "label": "Review run", "effect": "Opens the run detail", "irreversible": false},
		},
		"context": contractContext,
	}

	if err := inbox.Insert(ctx, s.db, nil, inbox.Item{
		WorkspaceID: workspaceID,
		Kind:        inbox.KindRunNeedsHuman,
		SourceID:    runID,
		TargetRole:  "MANAGER",
		Title:       fmt.Sprintf("Routine %s needs your input", pipelineSlug),
		BodyMD:      reason,
		SenderType:  "pipeline",
		SenderID:    invokingAgent.String,
		SenderName:  pipelineSlug,
		Priority:    "high",
		Blocking:    true,
		Payload:     payload,
	}); err != nil {
		slog.Default().Warn("create outcome inbox item", "error", err, "run_id", runID)
	}
}
