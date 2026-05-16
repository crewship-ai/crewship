package consolidate

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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

// Sentinel errors so the HTTP layer can map proposal-lookup failures to
// the right status code without parsing strings. The DB row identity is
// the canonical name (memory_proposals.id); the inbox row is a sibling
// projection — losing it doesn't change the proposal lifecycle, the
// audit trail just narrows.
var (
	// ErrProposalNotFound is returned when no memory_proposals row
	// matches the given id. HTTP handlers map to 404.
	ErrProposalNotFound = errors.New("memory proposal not found")
	// ErrProposalNotPending is returned when a row exists but its
	// status is not 'pending' — i.e. someone already approved or
	// rejected. Idempotent retries from a flaky client must NOT
	// re-merge the body; HTTP handlers map to 409.
	ErrProposalNotPending = errors.New("memory proposal not pending")
)

// ApprovalResult reports what an approve call did so the HTTP layer can
// echo it back to the caller without re-querying the DB.
type ApprovalResult struct {
	ProposalID    string
	CanonicalPath string
	RulesMerged   int
	WorkspaceID   string
	CrewID        string
}

// proposalRow is the in-memory representation of a memory_proposals row
// for the approve/reject path. evidence_json is left as a raw string so
// the helper does not have to take a dependency on LearnedRule's shape
// (the canonical merge re-reads the rendered markdown body from disk).
type proposalRow struct {
	ID           string
	WorkspaceID  string
	CrewID       string
	ProposalPath string
	Status       string
	EvidenceJSON string
	RulesCount   int
}

// ApproveProposal merges a pending proposal's rendered body into the
// canonical learned-YYYY-MM-DD.md file for *today* (UTC), flips the
// memory_proposals row to status='approved' with decided_by_user_id +
// decided_at populated, resolves the matching inbox row with
// action='approved', and emits journal.EntryMemoryConsolidated so
// downstream readers (paymaster, dashboard) treat the rules as live.
//
// Atomicity: the canonical file append happens *before* the DB update.
// If the append fails (lock contention, disk error, missing proposal
// file), the function returns the error and the DB row stays 'pending'
// so a retry is safe. If the DB update fails after a successful
// append, the canonical file keeps the merged content but the
// proposal row stays 'pending' — the operator can re-approve idempotently
// and the second approve will discover its body already merged. (The
// merge is content-additive on the canonical file; the duplication is
// caught by the dedupAgainstPrior pass on the next consolidator tick
// because the pattern hash is already present.)
//
// Errors map: ErrProposalNotFound (no row) → 404, ErrProposalNotPending
// (status != 'pending') → 409, anything else → 500.
//
// The caller is expected to have checked auth (OWNER/ADMIN) before
// invoking — this helper is unauthenticated by design so the same
// function can serve a future CLI subcommand without a fake HTTP context.
func ApproveProposal(
	ctx context.Context,
	db *sql.DB,
	j journal.Emitter,
	logger *slog.Logger,
	proposalID, userID string,
) (*ApprovalResult, error) {
	if logger == nil {
		logger = slog.Default()
	}

	row, err := loadProposalForDecision(ctx, db, proposalID)
	if err != nil {
		return nil, err
	}

	// Read the proposal body from disk and extract just the rules
	// section (everything below the "---" separator). The header
	// portion is proposal-specific scaffolding; the canonical file
	// has its own header and divider scheme already.
	body, err := os.ReadFile(row.ProposalPath)
	if err != nil {
		return nil, fmt.Errorf("read proposal body: %w", err)
	}
	rulesBlock := extractRulesBody(string(body))

	now := time.Now().UTC()
	canonicalDir := filepath.Dir(row.ProposalPath)
	// proposal file lives in {outputDir}/.proposed/proposal-*.md, so
	// the canonical file lives one directory up.
	if filepath.Base(canonicalDir) == ".proposed" {
		canonicalDir = filepath.Dir(canonicalDir)
	}
	canonicalPath := filepath.Join(canonicalDir, "learned-"+now.Format("2006-01-02")+".md")

	if err := appendToCanonical(canonicalPath, now, rulesBlock); err != nil {
		return nil, fmt.Errorf("append to canonical: %w", err)
	}

	if err := markProposalDecided(ctx, db, row.ID, "approved", userID, now); err != nil {
		// Canonical merge already landed — see func doc for the
		// idempotent-retry rationale.
		return nil, fmt.Errorf("mark approved: %w", err)
	}

	inbox.ResolveBySource(ctx, db, logger, inbox.KindMemoryConsolidation, row.ID, "approved", userID)

	if _, emitErr := j.Emit(ctx, journal.Entry{
		WorkspaceID: row.WorkspaceID,
		CrewID:      row.CrewID,
		Type:        journal.EntryMemoryConsolidated,
		ActorType:   journal.ActorUser,
		ActorID:     userID,
		Severity:    journal.SeverityNotice,
		Summary:     fmt.Sprintf("approved %d rule(s) into %s", row.RulesCount, canonicalPath),
		Payload: map[string]any{
			"proposal_id": row.ID,
			"rules_count": row.RulesCount,
			"output_path": canonicalPath,
			"approved_by": userID,
			"decided_at":  now.Format(time.RFC3339Nano),
		},
	}); emitErr != nil {
		// Journal emit is best-effort — the proposal IS approved
		// regardless. Log so operators can trace.
		logger.Warn("approve emit failed", "err", emitErr, "proposal_id", row.ID)
	}

	return &ApprovalResult{
		ProposalID:    row.ID,
		CanonicalPath: canonicalPath,
		RulesMerged:   row.RulesCount,
		WorkspaceID:   row.WorkspaceID,
		CrewID:        row.CrewID,
	}, nil
}

// RejectProposal flips the memory_proposals row to status='rejected'
// and resolves the inbox row with action='rejected'. The `.proposed/`
// file stays on disk for audit until a retention sweep removes it
// (separate concern). No journal entry is emitted on reject — the
// inbox audit trail and the existing EntryMemoryConsolidationProposed
// from the seed run carry the lineage.
//
// `reason` is optional; when non-empty it lands in the inbox
// resolved_action audit payload via a future payload upgrade (current
// inbox writer signature only takes action; reason rides on the
// memory_proposals row via an extension column once schema settles).
func RejectProposal(
	ctx context.Context,
	db *sql.DB,
	j journal.Emitter,
	logger *slog.Logger,
	proposalID, userID, reason string,
) error {
	if logger == nil {
		logger = slog.Default()
	}
	row, err := loadProposalForDecision(ctx, db, proposalID)
	if err != nil {
		return err
	}

	now := time.Now().UTC()
	if err := markProposalDecided(ctx, db, row.ID, "rejected", userID, now); err != nil {
		return fmt.Errorf("mark rejected: %w", err)
	}
	inbox.ResolveBySource(ctx, db, logger, inbox.KindMemoryConsolidation, row.ID, "rejected", userID)

	// Log the rejection reason at notice level so audit reviews
	// don't have to JOIN through memory_proposals for the "why".
	logger.Info("memory proposal rejected",
		"proposal_id", row.ID, "workspace_id", row.WorkspaceID,
		"crew_id", row.CrewID, "user_id", userID, "reason", reason)
	return nil
}

// ExplainProposal returns the raw proposal evidence + metadata for the
// HITL review UI. In PR #2 this is the un-scored body; PR #4 upgrades
// the same endpoint with per-signal scoring once writeProposal starts
// populating score_json.
type ProposalExplanation struct {
	ProposalID     string          `json:"proposal_id"`
	WorkspaceID    string          `json:"workspace_id"`
	CrewID         string          `json:"crew_id"`
	Status         string          `json:"status"`
	ProposalPath   string          `json:"proposal_path"`
	RulesCount     int             `json:"rules_count"`
	EntriesScanned int             `json:"entries_scanned"`
	CreatedAt      string          `json:"created_at"`
	DecidedAt      *string         `json:"decided_at,omitempty"`
	DecidedBy      *string         `json:"decided_by_user_id,omitempty"`
	Evidence       json.RawMessage `json:"evidence"`
}

// ExplainProposal reads the row + evidence_json without side effects.
// Returns ErrProposalNotFound on miss.
func ExplainProposal(ctx context.Context, db *sql.DB, proposalID string) (*ProposalExplanation, error) {
	out := &ProposalExplanation{}
	var decidedAt, decidedBy sql.NullString
	var evidenceJSON string
	var entriesScanned int
	err := db.QueryRowContext(ctx, `
		SELECT id, workspace_id, crew_id, status, proposal_path,
		       rules_count, entries_scanned, created_at,
		       decided_at, decided_by_user_id, evidence_json
		FROM memory_proposals WHERE id = ?`, proposalID).Scan(
		&out.ProposalID, &out.WorkspaceID, &out.CrewID, &out.Status,
		&out.ProposalPath, &out.RulesCount, &entriesScanned, &out.CreatedAt,
		&decidedAt, &decidedBy, &evidenceJSON,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrProposalNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("query proposal: %w", err)
	}
	out.EntriesScanned = entriesScanned
	if decidedAt.Valid {
		s := decidedAt.String
		out.DecidedAt = &s
	}
	if decidedBy.Valid {
		s := decidedBy.String
		out.DecidedBy = &s
	}
	out.Evidence = json.RawMessage(evidenceJSON)
	return out, nil
}

// --- internal helpers -----------------------------------------------------

// loadProposalForDecision fetches the proposal row and enforces the
// status=='pending' precondition. Single helper because approve and
// reject share the same lookup + guard semantics; the only difference
// is what they write afterward.
func loadProposalForDecision(ctx context.Context, db *sql.DB, proposalID string) (*proposalRow, error) {
	row := &proposalRow{}
	err := db.QueryRowContext(ctx, `
		SELECT id, workspace_id, crew_id, proposal_path, status, evidence_json, rules_count
		FROM memory_proposals WHERE id = ?`, proposalID).Scan(
		&row.ID, &row.WorkspaceID, &row.CrewID, &row.ProposalPath,
		&row.Status, &row.EvidenceJSON, &row.RulesCount,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrProposalNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("query proposal: %w", err)
	}
	if row.Status != "pending" {
		return nil, ErrProposalNotPending
	}
	return row, nil
}

// markProposalDecided is the single UPDATE path for both approve and
// reject. We also re-assert status='pending' in the WHERE so a racing
// second caller (e.g. two operators clicking Approve at the same
// instant) gets RowsAffected=0 and the caller maps to 409 by re-reading
// the row. Defensive — the loadProposalForDecision check already
// guards the common case.
func markProposalDecided(ctx context.Context, db *sql.DB, proposalID, status, userID string, decidedAt time.Time) error {
	res, err := db.ExecContext(ctx, `
		UPDATE memory_proposals
		SET status = ?, decided_at = ?, decided_by_user_id = ?
		WHERE id = ? AND status = 'pending'`,
		status, decidedAt.Format(time.RFC3339Nano), userID, proposalID,
	)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		// Race lost — re-read returns NotPending and the caller's
		// errors.Is check fires the 409.
		return ErrProposalNotPending
	}
	return nil
}

// appendToCanonical merges the proposal's rules block into the
// canonical learned-YYYY-MM-DD.md under a memory.FileLock. Reuses the
// same atomic-append shape as the consolidator's appendRules — header
// on first write, divider between runs, trailing newline. The lock
// path mirrors the canonical path with .lock suffix so concurrent
// approvers serialise.
func appendToCanonical(canonicalPath string, now time.Time, body string) error {
	if err := os.MkdirAll(filepath.Dir(canonicalPath), 0o755); err != nil {
		return fmt.Errorf("mkdir canonical: %w", err)
	}
	lk := memory.NewFileLock(canonicalPath + ".lock")
	if err := lk.Lock(); err != nil {
		return fmt.Errorf("lock canonical: %w", err)
	}
	defer func() { _ = lk.Unlock() }()

	var block strings.Builder
	if _, statErr := os.Stat(canonicalPath); os.IsNotExist(statErr) {
		block.WriteString("# Learned rules — ")
		block.WriteString(now.Format("2006-01-02"))
		block.WriteString("\n\n")
		block.WriteString("Auto-generated by the consolidation worker; entries below were\n")
		block.WriteString("approved via the HITL review flow. Each rule lists the source\n")
		block.WriteString("journal entries under \"evidence\" so you can audit the reasoning.\n\n")
	} else {
		block.WriteString("\n---\n\n")
	}
	block.WriteString("## Approved at ")
	block.WriteString(now.Format("15:04:05 MST"))
	block.WriteString("\n\n")
	block.WriteString(body)
	if !strings.HasSuffix(body, "\n") {
		block.WriteByte('\n')
	}

	f, err := os.OpenFile(canonicalPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open canonical: %w", err)
	}
	defer f.Close()
	if _, err := f.WriteString(block.String()); err != nil {
		return fmt.Errorf("write canonical: %w", err)
	}
	return nil
}

// extractRulesBody peels off the proposal markdown's header scaffolding
// and returns just the rule list. The proposal layout (see
// renderProposalMarkdown in proposed.go) puts the rules after a "---"
// divider; we keep everything after the first "---" line. If the
// proposal lacks a divider for any reason we return the full body so
// the worst case is "approver sees the header too" rather than "rules
// silently dropped".
func extractRulesBody(body string) string {
	idx := strings.Index(body, "\n---\n")
	if idx < 0 {
		return strings.TrimSpace(body)
	}
	return strings.TrimSpace(body[idx+len("\n---\n"):])
}
