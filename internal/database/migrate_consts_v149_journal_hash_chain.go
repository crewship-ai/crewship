package database

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"

	"github.com/crewship-ai/crewship/internal/journal"
)

// v149: make the audit journal tamper-evident with a per-workspace hash-chain
// (issue #1369).
//
// Adds three columns to journal_entries:
//
//   - seq        — per-workspace monotonic sequence (1-based). Gives a
//     deterministic chain order independent of the random `id` PK
//     and the wall-clock `ts` (which can collide). A deleted
//     middle row shows up as a gap.
//   - prev_hash  — the entry_hash of the immediately-preceding entry in the
//     same workspace (” for the genesis entry).
//   - entry_hash — SHA-256 over this entry's canonical, length-framed content
//     plus prev_hash. Tampering any committed field, or the
//     linkage, is detectable by journal.VerifyChain.
//
// The migration is a Go fn (not raw SQL) because it must BACKFILL existing
// rows into a valid chain before the UNIQUE(workspace_id, seq) index can be
// created — every pre-migration row currently has no seq. We assign seq in
// (ts, id) order per workspace and compute the chain with the SAME
// journal.ChainHash the emit path uses, so a freshly-migrated instance
// verifies clean without a nuke+reseed.
//
// NOTE for dev: while backfill preserves verifiability, the safest path on a
// shared dev slot remains nuke+reseed, since any out-of-band row edits made
// before this migration are frozen into the chain as "genuine".
//
// Deferred (see issue #1369): signed compaction checkpoints (anchor the tip so
// tail-truncation is caught) and append-only keeper_requests.
func migrationJournalHashChain(ctx context.Context, tx *sql.Tx, logger *slog.Logger) error {
	for _, ddl := range []string{
		`ALTER TABLE journal_entries ADD COLUMN seq INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE journal_entries ADD COLUMN prev_hash TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE journal_entries ADD COLUMN entry_hash TEXT NOT NULL DEFAULT ''`,
	} {
		if _, err := tx.ExecContext(ctx, ddl); err != nil {
			return fmt.Errorf("journal hash-chain: add column: %w", err)
		}
	}

	if err := backfillJournalChain(ctx, tx, logger); err != nil {
		return err
	}

	// Enforce chain-order integrity going forward: two CHAINED rows can never
	// share a seq within a workspace. Partial (WHERE seq > 0) so a row that has
	// not been chained yet (seq 0 — a legacy row a future codepath forgot to
	// chain, never a row from the emit Writer) is exempt rather than colliding;
	// VerifyChain still flags any seq-0 row as a gap. Safe to create only after
	// the backfill has given every existing row a distinct seq.
	if _, err := tx.ExecContext(ctx,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_journal_ws_seq ON journal_entries(workspace_id, seq) WHERE seq > 0`); err != nil {
		return fmt.Errorf("journal hash-chain: unique index: %w", err)
	}
	return nil
}

// backfillJournalChain walks existing rows per workspace in (ts, id) order and
// writes seq/prev_hash/entry_hash so historical data forms a valid chain.
func backfillJournalChain(ctx context.Context, tx *sql.Tx, logger *slog.Logger) error {
	rows, err := tx.QueryContext(ctx, `
SELECT id, workspace_id,
       COALESCE(crew_id,''), COALESCE(agent_id,''), COALESCE(mission_id,''),
       ts, entry_type, severity, COALESCE(priority,'normal'), actor_type,
       COALESCE(actor_id,''), payload, refs,
       COALESCE(trace_id,''), COALESCE(span_id,''), COALESCE(expires_at,''),
       summary
FROM journal_entries
ORDER BY workspace_id ASC, ts ASC, id ASC`)
	if err != nil {
		return fmt.Errorf("journal hash-chain: scan existing: %w", err)
	}

	type update struct {
		id        string
		seq       int64
		prevHash  string
		entryHash string
	}
	var updates []update

	var curWS string
	var seq int64
	prevHash := journal.GenesisPrevHash

	for rows.Next() {
		var f journal.ChainFields
		if err := rows.Scan(
			&f.ID, &f.Workspace,
			&f.CrewID, &f.AgentID, &f.MissionID,
			&f.TS, &f.EntryType, &f.Severity, &f.Priority, &f.ActorType,
			&f.ActorID, &f.Payload, &f.Refs,
			&f.TraceID, &f.SpanID, &f.ExpiresAt,
			&f.Summary,
		); err != nil {
			rows.Close()
			return fmt.Errorf("journal hash-chain: scan row: %w", err)
		}
		if f.Workspace != curWS {
			curWS = f.Workspace
			seq = 0
			prevHash = journal.GenesisPrevHash
		}
		seq++
		f.Seq = seq
		entryHash := journal.ChainHash(prevHash, f)
		updates = append(updates, update{id: f.ID, seq: seq, prevHash: prevHash, entryHash: entryHash})
		prevHash = entryHash
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("journal hash-chain: iterate: %w", err)
	}
	rows.Close()

	stmt, err := tx.PrepareContext(ctx,
		`UPDATE journal_entries SET seq = ?, prev_hash = ?, entry_hash = ? WHERE id = ?`)
	if err != nil {
		return fmt.Errorf("journal hash-chain: prepare update: %w", err)
	}
	defer stmt.Close()

	for _, u := range updates {
		if _, err := stmt.ExecContext(ctx, u.seq, u.prevHash, u.entryHash, u.id); err != nil {
			return fmt.Errorf("journal hash-chain: backfill %s: %w", u.id, err)
		}
	}
	if logger != nil {
		logger.Info("journal hash-chain backfilled", "rows", len(updates))
	}
	return nil
}
