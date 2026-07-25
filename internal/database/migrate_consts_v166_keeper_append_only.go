package database

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
)

// migrationKeeperAppendOnlyAudit (v166) closes the last two mutable holes in the
// tamper-evident audit story (#1369). PR #1401 gave journal_entries a keyed
// hash-chain and signed compaction checkpoints; two paths still MUTATE audit
// state in place, which either destroys prior state or breaks the chain.
//
// 1) keeper_request_events — the append-only keeper decision ledger.
//
// keeper_requests is written PENDING and then UPDATEd in place to its decision
// (keeper_request.go, keeper_execute.go ×2). The prior state is gone: there is no
// record that a request was ever pending, when it was decided relative to when it
// was raised, or that a decision was rewritten. This table records every state
// TRANSITION as a new row, so keeper_requests stays the current-state projection
// every existing reader already expects while the ledger holds the history.
//
// A BEFORE UPDATE trigger enforces append-only in the DATABASE, not merely by
// convention in the handlers — an audit ledger whose immutability depends on
// every future caller remembering to insert instead of update is not immutable.
// DELETE is deliberately NOT blocked: workspace_id carries ON DELETE CASCADE so
// tearing down a workspace (nuke, backup --replace) still works, and deletion is
// covered by the other half of the model — every transition is also mirrored into
// the hash-chained journal, where a removal shows up as a seq gap that only a
// signed checkpoint can legitimately bridge.
//
// 2) journal_entries.priority_at_emit + journal_entry_priorities.
//
// `priority` is inside the hashed projection (ChainFields.Priority) AND is
// UPDATEd in place by the operator-facing pin/permanent control
// (journal_handler.go). Every priority edit therefore permanently breaks
// VerifyChain for that row — a guaranteed false "tampered" verdict from a
// legitimate, authorised action, which is worse than no verification at all
// because it trains operators to ignore the result.
//
// The fix keeps the chain committing to an IMMUTABLE value (priority_at_emit,
// written once at emit) while `priority` stays where every reader already looks.
// The mutable column is not left unguarded: each edit appends to
// journal_entry_priorities (also append-only by trigger), and verification
// reconciles the live `priority` against priority_at_emit plus that ledger — so a
// silent DB-level flip, which leaves no ledger row, is still detected.
//
// Backfill is deliberate about what it can and cannot know: priority_at_emit is
// seeded from the CURRENT priority, because for a pre-migration row the emit-time
// value is unrecoverable. That makes existing chains verify (they were hashed
// with the current value), makes reconciliation hold with zero recorded changes,
// and starts the guarantee from this migration forward. The edit LEDGER is
// deliberately NOT backfilled — see the note at the bottom of this migration for
// why seeding it would break the very check it looks like it would help.
const migrationKeeperAppendOnlyAudit = `
-- ── 1. Append-only keeper decision ledger ────────────────────────────────────
CREATE TABLE IF NOT EXISTS keeper_request_events (
    id TEXT PRIMARY KEY,
    -- No FK to keeper_requests: the ledger is the durable record and must not be
    -- taken down by a cascade from the operational projection.
    request_id TEXT NOT NULL,
    -- Nullable because keeper_requests has no workspace_id column of its own; it
    -- is resolved from the requesting agent, which may be absent on legacy or
    -- Phase-2 rows. The FK gives workspace teardown/restore a clean cascade.
    workspace_id TEXT REFERENCES workspaces(id) ON DELETE CASCADE,
    -- 1-based, monotonic per request_id. A gap or a duplicate is itself a signal.
    seq INTEGER NOT NULL,
    -- The state ENTERED by this transition: PENDING, ALLOW, DENY, ESCALATE,
    -- DUPLICATE_SUPPRESSED, or a Phase-2 verdict. Deliberately schema-free (like
    -- keeper_requests.decision) so a new decision class needs no migration.
    state TEXT NOT NULL,
    request_type TEXT,
    requesting_agent_id TEXT,
    requesting_crew_id TEXT,
    credential_id TEXT,
    intent TEXT,
    command TEXT,
    reason TEXT,
    risk_score INTEGER,
    exit_code INTEGER,
    -- Who caused the transition: 'keeper' (the gatekeeper), 'user' (an operator
    -- resolving an escalation), 'system' (dedup suppression, timeouts).
    actor_type TEXT NOT NULL DEFAULT 'keeper',
    actor_id TEXT,
    recorded_at TEXT NOT NULL,
    UNIQUE(request_id, seq)
);
CREATE INDEX IF NOT EXISTS idx_keeper_req_events_req ON keeper_request_events(request_id, seq);
CREATE INDEX IF NOT EXISTS idx_keeper_req_events_ws ON keeper_request_events(workspace_id, recorded_at DESC);

CREATE TRIGGER IF NOT EXISTS keeper_request_events_append_only
BEFORE UPDATE ON keeper_request_events
BEGIN
    SELECT RAISE(ABORT, 'keeper_request_events is append-only: append a new transition instead of updating one');
END;

-- Backfill the ledger from what keeper_requests currently holds. Two rows per
-- decided request: the PENDING it must have passed through (at created_at) and
-- the decision it landed on (at decided_at). Intermediate rewrites, if any ever
-- happened, are unrecoverable — that is precisely the loss this table stops.
-- Deterministic ids keep the backfill idempotent against a restore replay.
INSERT OR IGNORE INTO keeper_request_events
    (id, request_id, workspace_id, seq, state, request_type, requesting_agent_id,
     requesting_crew_id, credential_id, intent, command, reason, risk_score,
     exit_code, actor_type, actor_id, recorded_at)
SELECT 'kre_' || kr.id || '_1', kr.id, a.workspace_id, 1, 'PENDING',
       kr.request_type, kr.requesting_agent_id, kr.requesting_crew_id,
       kr.credential_id, kr.intent, kr.command, NULL, NULL, NULL,
       'keeper', NULL, kr.created_at
FROM keeper_requests kr
LEFT JOIN agents a ON a.id = kr.requesting_agent_id;

INSERT OR IGNORE INTO keeper_request_events
    (id, request_id, workspace_id, seq, state, request_type, requesting_agent_id,
     requesting_crew_id, credential_id, intent, command, reason, risk_score,
     exit_code, actor_type, actor_id, recorded_at)
SELECT 'kre_' || kr.id || '_2', kr.id, a.workspace_id, 2, kr.decision,
       kr.request_type, kr.requesting_agent_id, kr.requesting_crew_id,
       kr.credential_id, kr.intent, kr.command, kr.reason, kr.risk_score,
       kr.exit_code, 'keeper', NULL, COALESCE(kr.decided_at, kr.created_at)
FROM keeper_requests kr
LEFT JOIN agents a ON a.id = kr.requesting_agent_id
WHERE kr.decision IS NOT NULL AND kr.decision <> 'PENDING';

-- ── 2. Immutable priority for the hash-chain + its append-only edit ledger ───
-- priority_at_emit is what the chain commits to from now on: written once at
-- emit and never updated, so an authorised pin/permanent edit can no longer
-- break verification.
ALTER TABLE journal_entries ADD COLUMN priority_at_emit TEXT;

-- Seed from the current priority: for a pre-migration row the emit-time value is
-- unrecoverable, and the current value is what its stored entry_hash was
-- computed over — so seeding it this way is exactly what keeps existing chains
-- verifying. The guarantee starts here and runs forward.
UPDATE journal_entries SET priority_at_emit = COALESCE(priority, 'normal')
WHERE priority_at_emit IS NULL;

CREATE TABLE IF NOT EXISTS journal_entry_priorities (
    id TEXT PRIMARY KEY,
    entry_id TEXT NOT NULL REFERENCES journal_entries(id) ON DELETE CASCADE,
    workspace_id TEXT NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    -- 1-based, monotonic per entry_id.
    seq INTEGER NOT NULL,
    previous_priority TEXT NOT NULL,
    priority TEXT NOT NULL,
    reason TEXT,
    -- The operator who made the change (journal priority editing is OWNER/ADMIN).
    set_by TEXT,
    set_at TEXT NOT NULL,
    UNIQUE(entry_id, seq)
);
CREATE INDEX IF NOT EXISTS idx_journal_entry_priorities_entry ON journal_entry_priorities(entry_id, seq);

CREATE TRIGGER IF NOT EXISTS journal_entry_priorities_append_only
BEFORE UPDATE ON journal_entry_priorities
BEGIN
    SELECT RAISE(ABORT, 'journal_entry_priorities is append-only: append a new change instead of updating one');
END;

-- NO ledger backfill, deliberately. A pre-migration row that an operator had
-- already pinned gets priority_at_emit = its CURRENT priority (above), so
-- reconciliation — "the live value must be reachable from priority_at_emit through
-- the recorded changes" — already holds with ZERO recorded changes.
--
-- Seeding a synthetic 'normal' -> 'pin' row would actively BREAK it: the first
-- edit's previous_priority ('normal') would not match priority_at_emit ('pin'),
-- so every legacy pinned entry would verify as tampered. The honest position is
-- that the pre-migration edit history is unrecoverable; the guarantee starts here
-- and runs forward, and inventing a change we cannot attribute would be a
-- fabricated audit record on top of a false positive.
`

// restoreBackfillPriorityAtEmit seeds journal_entries.priority_at_emit on rows
// restored from a bundle whose source schema predates v166 (#1369).
//
// Why a hook is required even though the migration already seeds the column: the
// migration runs once, against the rows present at upgrade time. A LATER restore
// re-inserts journal_entries rows straight from an older bundle, which has no
// priority_at_emit column — so those rows land NULL, and the migration will never
// run again to fix them.
//
// A NULL there is not harmless. VerifyChain reads
// COALESCE(priority_at_emit, priority, 'normal'), so with the column NULL the
// chain's anchor becomes the LIVE priority — a value that moves. The moment an
// operator edits the priority of such a row, the ledger records
// previous_priority = the old value while the anchor has already become the new
// one, the chain of changes no longer starts where it must, and verification
// reports tampering on a legitimate edit. Materializing the column pins the
// anchor so it cannot drift.
//
// Idempotent by construction (the required contract): the WHERE clause only
// touches rows that are still NULL, so a re-run after a failed restore is a
// no-op rather than an overwrite of an already-correct value.
func restoreBackfillPriorityAtEmit(ctx context.Context, tx *sql.Tx, logger *slog.Logger) error {
	res, err := tx.ExecContext(ctx, `
		UPDATE journal_entries
		   SET priority_at_emit = COALESCE(priority, 'normal')
		 WHERE priority_at_emit IS NULL`)
	if err != nil {
		return fmt.Errorf("v166 restore backfill: seed priority_at_emit: %w", err)
	}
	if n, aerr := res.RowsAffected(); aerr == nil && n > 0 && logger != nil {
		logger.Info("v166 restore backfill: seeded priority_at_emit on restored rows", "rows", n)
	}
	return nil
}
