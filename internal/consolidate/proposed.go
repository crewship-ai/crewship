package consolidate

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/inbox"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/memory"
)

// writeProposal is the HITL-mode write path. Instead of appending the
// extracted rules to learned-YYYY-MM-DD.md, it:
//
//  1. Writes the rendered rules to {outputDir}/.proposed/proposal-{id}.md
//     under a memory.FileLock so concurrent ticks cannot interleave
//     bytes on the staging file.
//  2. Inserts a memory_proposals row with status='pending' and the
//     evidence JSON so the explain endpoint can rebuild the per-rule
//     trace without re-parsing the markdown body.
//  3. Inserts an inbox_items row (kind=memory_consolidation) so the
//     proposal surfaces on the workspace inbox bell at the next read.
//     Insertion is INSERT-OR-IGNORE on (kind, source_id) per v85; the
//     proposal id is the source_id so the inbox dedupes naturally.
//  4. Emits the EntryMemoryConsolidationProposed journal entry —
//     deliberately distinct from EntryMemoryConsolidated so downstream
//     readers (paymaster cost rollup, dashboard "live rules" widget)
//     do not interpret a not-yet-approved proposal as live.
//
// The function is best-effort about side effects: a DB failure to
// insert the inbox row or the journal row is logged but does not
// roll back the on-disk proposal file or the memory_proposals row,
// because the operator can still find the proposal by listing the
// table and the markdown is intact. Inverse failures (proposal file
// fails, DB rows ride) are worse, so the function returns early on
// any filesystem error before any DB write.
func (c *Consolidator) writeProposal(
	ctx context.Context,
	cfg Config,
	now time.Time,
	rules []LearnedRule,
	entriesScanned int,
) (ConsolidationResult, error) {
	logger := c.Logger
	if logger == nil {
		logger = slog.Default()
	}

	runID := newProposalID(now)
	proposedDir := filepath.Join(cfg.OutputDir, ".proposed")
	if err := os.MkdirAll(proposedDir, 0o755); err != nil {
		return ConsolidationResult{EntriesScanned: entriesScanned}, fmt.Errorf("mkdir proposed: %w", err)
	}
	proposalPath := filepath.Join(proposedDir, "proposal-"+runID+".md")

	// Render the rules into the same markdown shape appendRules uses
	// so an operator approving the proposal can move the body verbatim.
	body := renderProposalMarkdown(now, runID, rules)

	// flock on the proposal path; concurrent ticks must serialise on
	// the same staging file because their proposal-{runID}.md will be
	// distinct, but a future "rewrite the proposal" flow would need
	// this lock to be honoured.
	lk := memory.NewFileLock(proposalPath + ".lock")
	if err := lk.Lock(); err != nil {
		return ConsolidationResult{EntriesScanned: entriesScanned}, fmt.Errorf("lock proposal: %w", err)
	}
	defer func() { _ = lk.Unlock() }()

	if err := os.WriteFile(proposalPath, []byte(body), 0o644); err != nil {
		return ConsolidationResult{EntriesScanned: entriesScanned}, fmt.Errorf("write proposal: %w", err)
	}

	evidenceJSON, err := json.Marshal(rules)
	if err != nil {
		// Silently dropping the error leaves evidence_json blank for
		// downstream explain readers — better to fail the whole
		// proposal write so the operator sees the bug before it
		// becomes "why are all my proposals empty in HITL UI".
		_ = os.Remove(proposalPath)
		return ConsolidationResult{EntriesScanned: entriesScanned}, fmt.Errorf("marshal proposal evidence: %w", err)
	}
	// Score every candidate rule with the OpenClaw six-signal model
	// at proposal-creation time. The blob is keyed by rule index so
	// the explain endpoint can render per-rule signal breakdowns
	// without re-running the scorer. CandidateMetrics fields are
	// best-effort: today we pass RawRelevance from LearnedRule
	// .Confidence and the rest at conservative defaults — future
	// iterations populate RecallCount/UniqueQueries from journal
	// queries against the rule's evidence ids.
	// Populate RecallCount + UniqueQueries from journal-backed
	// memory.searched events. Without this the Skill-promotion gate
	// would never fire in steady state — every rule would have
	// recall=0 at score time, blocking promotion regardless of LLM
	// confidence. db nil-safe: tests that pass nil get the legacy
	// zero-counter behaviour.
	scores := computeProposalScoresWithRecall(ctx, c.DB, cfg.WorkspaceID, rules, recallLookbackWindow, now)
	scoreJSON := marshalScoresJSON(scores)
	proposalID := "mp_" + runID
	if c.DB != nil {
		if _, err := c.DB.ExecContext(ctx, `
			INSERT INTO memory_proposals (
				id, workspace_id, crew_id, proposal_path,
				status, evidence_json, rules_count, entries_scanned,
				created_at, score_json
			) VALUES (?, ?, ?, ?, 'pending', ?, ?, ?, ?, ?)`,
			proposalID, cfg.WorkspaceID, cfg.CrewID, proposalPath,
			string(evidenceJSON), len(rules), entriesScanned,
			now.UTC().Format(time.RFC3339Nano), scoreJSON,
		); err != nil {
			// Roll back the on-disk file so an operator does not see
			// a proposal markdown that has no DB row to actually
			// approve. The lockfile stays — flock is per-fd, harmless.
			_ = os.Remove(proposalPath)
			return ConsolidationResult{EntriesScanned: entriesScanned}, fmt.Errorf("insert memory_proposal: %w", err)
		}

		inbox.Insert(ctx, c.DB, logger, inbox.Item{
			WorkspaceID: cfg.WorkspaceID,
			Kind:        inbox.KindMemoryConsolidation,
			SourceID:    proposalID,
			TargetRole:  "MANAGER",
			Title:       fmt.Sprintf("Memory consolidation: %d rules pending review", len(rules)),
			BodyMD:      proposalInboxBody(len(rules), entriesScanned),
			SenderType:  "system",
			SenderName:  "consolidator",
			Priority:    "medium",
			Blocking:    false,
			Payload: map[string]any{
				"proposal_id":     proposalID,
				"proposal_path":   proposalPath,
				"rules_count":     len(rules),
				"entries_scanned": entriesScanned,
			},
		})
	}

	// memory→Skills bridge: any rule that has accumulated sustained
	// operator-validated traction (recall ≥ 10 + composite ≥ 0.85 in
	// the score map) graduates to a staged Anthropic SKILL.md under
	// .proposed/skill-{slug}.md. The bridge is non-fatal: on the very
	// first proposal for a pattern, recall is by definition 0 and no
	// skill is written; only repeated, recalled-against rules cross
	// the gate on later runs. See skill_promote.go for the thresholds.
	if skillPaths := c.promoteProposalSkills(rules, scores, cfg.OutputDir, now); len(skillPaths) > 0 {
		logger.Info("memory→skills promotion staged",
			"proposal_id", proposalID,
			"skill_count", len(skillPaths),
			"skill_paths", skillPaths)
	}

	id, emitErr := c.Journal.Emit(ctx, journal.Entry{
		WorkspaceID: cfg.WorkspaceID,
		CrewID:      cfg.CrewID,
		Type:        journal.EntryMemoryConsolidationProposed,
		ActorType:   journal.ActorSystem,
		ActorID:     "consolidator",
		Severity:    journal.SeverityNotice,
		Summary: fmt.Sprintf("consolidated %d entries into %d proposed rules — awaiting approval",
			entriesScanned, len(rules)),
		Payload: map[string]any{
			"proposal_id":     proposalID,
			"proposal_path":   proposalPath,
			"rules_count":     len(rules),
			"entries_scanned": entriesScanned,
			"model":           cfg.LLMModel,
		},
		Refs: map[string]any{
			"source_entry_ids": collectEvidence(rules),
		},
	})
	if emitErr != nil {
		// Journal is logged-not-fatal; the proposal file + DB row
		// already exist, and the operator can still approve. The
		// dashboard won't show the proposal until the next refresh,
		// but that's an availability concern, not correctness.
		logger.Warn("consolidate proposal emit", "err", emitErr, "proposal_id", proposalID)
	}

	return ConsolidationResult{
		EntriesScanned: entriesScanned,
		RulesAppended:  len(rules),
		OutputPath:     proposalPath,
		JournalEntryID: id,
	}, nil
}

// renderProposalMarkdown renders a proposal-{id}.md body. Same per-rule
// shape as appendRules for clean approve-side merging; adds a proposal
// header so an operator opening the file directly knows what they're
// looking at and what to do next.
func renderProposalMarkdown(now time.Time, runID string, rules []LearnedRule) string {
	var b strings.Builder
	b.WriteString("# Proposed learned rules\n\n")
	fmt.Fprintf(&b, "Proposal ID: `%s`  \n", runID)
	fmt.Fprintf(&b, "Generated at: %s  \n", now.UTC().Format(time.RFC3339))
	b.WriteString("Status: **pending** — approve via POST /api/v1/consolidate/proposed/{id}/approve\n\n")
	b.WriteString("---\n\n")
	for i, r := range rules {
		fmt.Fprintf(&b, "- **Pattern:** %s  \n", r.Pattern)
		fmt.Fprintf(&b, "  **Action:** %s  \n", r.Action)
		fmt.Fprintf(&b, "  **Confidence:** %.2f  \n", r.Confidence)
		if len(r.Evidence) > 0 {
			fmt.Fprintf(&b, "  **Evidence:** %s\n", strings.Join(r.Evidence, ", "))
		}
		if i < len(rules)-1 {
			b.WriteByte('\n')
		}
	}
	b.WriteByte('\n')
	return b.String()
}

// proposalInboxBody is the inbox row's body_md — a short summary the
// list endpoint can render. Full proposal markdown lives at
// payload.proposal_path; this is just the bell pop-up.
func proposalInboxBody(rulesCount, entriesScanned int) string {
	return fmt.Sprintf(
		"The memory consolidator extracted %d rule(s) from %d journal entries and parked them for review. Approve to merge into the canonical learned-*.md, or reject to discard.",
		rulesCount, entriesScanned,
	)
}

// marshalScoresJSON serialises a per-pattern scores map for the
// score_json column. Returns "{}" on error so the NOT-NULL DEFAULT
// contract on the column holds even under pathological inputs.
func marshalScoresJSON(scores map[string]ScoreResult) string {
	b, err := json.Marshal(scores)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// recallLookbackWindow is the period over which we count distinct
// `memory.searched` events when populating CandidateMetrics. 7 days
// matches the consolidator's own dedup window — a rule that has been
// recalled in the last week is the operational definition of
// "actively useful" for the Skill-promotion gate. Shorter windows
// produce noisy false-negatives; longer windows let stale rules
// accumulate phantom recall credit.
const recallLookbackWindow = 7 * 24 * time.Hour

// computeProposalScoresWithRecall is the database-aware variant of
// computeProposalScores. When db is non-nil, every rule's RecallCount
// and UniqueQueries get populated from the journal's
// EntryMemorySearched entries within the lookback window — closing
// the PRD §8.1 gap where the Skill-promotion gate never fired in
// steady state because both counters were always zero.
//
// db == nil short-circuits to the previous behaviour (RecallCount=0,
// UniqueQueries=0) so the function is safe to call from tests or
// callers that haven't yet wired the journal-backed DB.
//
// lookback == 0 defaults to recallLookbackWindow (7 days).
func computeProposalScoresWithRecall(
	ctx context.Context,
	db *sql.DB,
	workspaceID string,
	rules []LearnedRule,
	lookback time.Duration,
	now time.Time,
) map[string]ScoreResult {
	if lookback <= 0 {
		lookback = recallLookbackWindow
	}
	scores := make(map[string]ScoreResult, len(rules))
	for _, r := range rules {
		recallCount, uniqueQueries := 0, 0
		if db != nil && workspaceID != "" && len(r.Evidence) > 0 {
			recallCount, uniqueQueries = loadRecallMetrics(ctx, db, workspaceID, r.Evidence, now.Add(-lookback))
		}
		scores[r.Pattern] = ComputeScore(
			CandidateMetrics{
				RawRelevance:       r.Confidence,
				RecallCount:        recallCount,
				UniqueQueries:      uniqueQueries,
				EvidenceCount:      len(r.Evidence),
				DistinctEntryTypes: 1,
			},
			DefaultSignalWeights(),
			DefaultThresholds(),
			now,
		)
	}
	return scores
}

// loadRecallMetrics queries journal_entries for memory.searched events
// whose payload hit_chunk_ids list intersects with the rule's
// evidence ids, since the supplied cutoff. Returns (recallCount,
// uniqueQueries). SQLite-specific JSON predicate — Postgres port
// would substitute jsonb @> array containment.
//
// Failure modes: every error is logged and treated as zero counts.
// A scoring miss is strictly preferable to crashing the consolidator;
// the next tick re-queries.
func loadRecallMetrics(ctx context.Context, db *sql.DB, workspaceID string, evidenceIDs []string, since time.Time) (int, int) {
	if len(evidenceIDs) == 0 {
		return 0, 0
	}
	// Build a single SELECT that pulls (entry_id, query) for every
	// memory.searched row within the window whose hit_chunk_ids JSON
	// array contains ANY of the evidence ids. json_each unrolls the
	// array; the WHERE filters to rows whose unrolled element equals
	// one of our ids. DISTINCT (id, query) prevents one search hitting
	// multiple evidence ids from inflating either counter.
	placeholders := make([]string, len(evidenceIDs))
	args := make([]any, 0, len(evidenceIDs)+2)
	args = append(args, workspaceID, since.UTC().Format(time.RFC3339Nano))
	for i, eid := range evidenceIDs {
		placeholders[i] = "?"
		args = append(args, eid)
	}
	q := `
		SELECT DISTINCT je.id, COALESCE(json_extract(je.payload, '$.query'), '') AS q
		FROM journal_entries je, json_each(json_extract(je.payload, '$.hit_chunk_ids')) AS hits
		WHERE je.workspace_id = ?
		  AND je.entry_type = 'memory.searched'
		  AND je.ts >= ?
		  AND hits.value IN (` + strings.Join(placeholders, ",") + `)`
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		// Most likely cause: journal payload is non-JSON on a legacy
		// row. Don't pollute the consolidator log with this on every
		// scoring pass — the consolidator's caller logs ScoreResult,
		// which surfaces the zero counts.
		return 0, 0
	}
	defer rows.Close()
	queries := make(map[string]struct{})
	recallCount := 0
	for rows.Next() {
		var id, query string
		if err := rows.Scan(&id, &query); err != nil {
			continue
		}
		recallCount++
		q := strings.ToLower(strings.TrimSpace(query))
		if q != "" {
			queries[q] = struct{}{}
		}
	}
	if err := rows.Err(); err != nil {
		// Iterator-level failure — fall back to whatever we already
		// counted rather than zeroing it.
		return recallCount, len(queries)
	}
	return recallCount, len(queries)
}

// promoteProposalSkills is the post-DB-insert hook that runs the
// memory→Skills bridge against any rules whose ScoreResult meets the
// Skill-promotion gate (recall ≥ 10 + composite ≥ 0.85 by default).
// Failures are logged-not-fatal: a Skill that fails to stage does not
// invalidate the underlying proposal — operators still see the
// learned-rule proposal under .proposed/proposal-*.md and can approve
// it normally; the missing Skill simply means no SKILL.md was staged
// for that rule on this run, and the next consolidator pass will try
// again.
//
// Returns the absolute paths of any Skills that landed on disk so the
// caller can include them in observability logs.
func (c *Consolidator) promoteProposalSkills(
	rules []LearnedRule,
	scores map[string]ScoreResult,
	outputDir string,
	now time.Time,
) []string {
	logger := c.Logger
	if logger == nil {
		logger = slog.Default()
	}
	paths, err := PromoteEligibleRules(rules, scores, SkillPromoteOptions{
		OutputDir: outputDir,
		Now:       now,
	})
	if err != nil {
		logger.Warn("memory→skills promotion partial failure",
			"err", err, "promoted_count", len(paths))
	}
	return paths
}

// newProposalID returns a short, sortable run identifier. Format:
// YYYYMMDDHHMMSS-XXXXXX (timestamp + 6 hex chars of random) so a list
// of proposals on disk sorts chronologically. Random suffix avoids
// collision when two ticks land in the same second.
func newProposalID(now time.Time) string {
	// 8 bytes of random for 16 hex chars of suffix (vs 6 chars before) —
	// negligible bandwidth, much tighter collision floor for the
	// pathological "many proposals in one second" case. If crypto/rand
	// fails (extremely rare; OS RNG drained), fall back to UnixNano so
	// the suffix is still unique within the process — better than a
	// silently-empty suffix which the prior `_, _ = rand.Read` masked.
	var r [8]byte
	if _, err := rand.Read(r[:]); err != nil {
		return now.UTC().Format("20060102150405") + "-" + fmt.Sprintf("%016x", now.UTC().UnixNano())
	}
	return now.UTC().Format("20060102150405") + "-" + hex.EncodeToString(r[:])
}

// ensureDB is a small helper that lets writeProposal pass an explicit
// nil DB for tests without panicking; kept here so the Consolidator
// struct definition stays single-purpose.
func ensureDB(db *sql.DB) *sql.DB { return db }

var _ = ensureDB // reserved for use when the approve endpoint lands
