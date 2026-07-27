package database

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"

	"github.com/crewship-ai/crewship/internal/journal"
)

// migrationJournalAppendOnlyFKs (v167) takes the ability to rewrite and delete
// the audit journal away from the database itself (#1482).
//
// # What was wrong
//
// journal_entries is a tamper-evident hash chain (v152, #1369): every row
// commits, under a keyed HMAC, to its own content and to the row before it.
// The table it lives in, however, was declared back in v52 like an ordinary
// operational table:
//
//	crew_id    TEXT REFERENCES crews(id)    ON DELETE CASCADE   -- deletes audit rows
//	agent_id   TEXT REFERENCES agents(id)   ON DELETE CASCADE   -- deletes audit rows
//	mission_id TEXT REFERENCES missions(id) ON DELETE SET NULL  -- rewrites audit rows
//
// mission_id is one of the fields the chain hash commits to. Deleting a mission
// therefore makes SQLite UPDATE every journal row that referenced it, setting
// mission_id to NULL — and the stored entry_hash, computed over the real id,
// stops matching. VerifyChain reports "entry was modified after write", which
// is indistinguishable, to the verifier, from an attacker. That is why grepping
// for "a code path that UPDATEs a journal row" found nothing: there is no such
// code path. `seed --nuke` deletes that generation's missions and damages
// another block of rows, so on a reseeded slot the break count GROWS — the
// reseed is the cause, not the cure.
//
// The CASCADE half is the more serious one even though it never breaks a hash:
// deleting a crew or an agent DELETES its audit history outright, leaving a seq
// gap that reads as tampering and destroying the record of what that crew did.
//
// # What this does
//
// Rebuilds journal_entries with those three columns as plain TEXT. The audit
// log now outlives the operational rows it refers to, which is the whole point
// of an audit log.
//
// workspace_id KEEPS ON DELETE CASCADE, deliberately. The chain is
// per-workspace, so a workspace's journal going away with the workspace is
// coherent (and it is what makes `nuke` and `backup --replace` work). Every
// other reference is now a dangling-tolerant id.
//
// # Why it needs the fnNoTx escape hatch
//
// SQLite cannot drop a column constraint in place; the column list has to be
// rebuilt. That recipe cannot run inside the migration runner's wrapper
// transaction on a populated database — see the fnNoTx contract in migrate.go
// and v89's comment, which hit the same wall before the escape hatch existed.
//
// # Damage repair
//
// Once the FK is gone the nulled ids are recoverable: the entry's `refs` JSON
// still carries mission_id. The repair does NOT trust that blindly — writing
// back a value that was not the original one would break the very hash it is
// trying to fix. Each candidate is re-hashed and only written when it
// reproduces the STORED entry_hash, which (the hash being an HMAC under a key
// that is not in the database) proves it is the value the row was written with.
// See repairJournalMissionIDs.
func migrationJournalAppendOnlyFKs(ctx context.Context, db *sql.DB, logger *slog.Logger) error {
	// ONE pinned connection for the whole migration: `PRAGMA foreign_keys` is
	// per-connection state, so toggling it on a pooled *sql.DB would leave the
	// rebuild running on some other connection that still enforces FKs.
	conn, err := db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("v167: pin connection: %w", err)
	}
	defer func() { _ = conn.Close() }()

	ddl, err := journalEntriesDDL(ctx, conn)
	if err != nil {
		return err
	}

	// Idempotency probe (required of every fnNoTx migration — the _migrations
	// row lands only after this returns). A rebuilt table no longer carries the
	// destructive clauses, so a re-run skips straight to the repair, which is
	// itself idempotent.
	if journalDDLNeedsRebuild(ddl) {
		if err := rebuildJournalEntries(ctx, conn, ddl, logger); err != nil {
			return err
		}
	} else if logger != nil {
		logger.Info("v167: journal_entries already carries no destructive FK actions, skipping rebuild")
	}

	restored, unrecoverable, err := repairJournalMissionIDs(ctx, conn, logger)
	if err != nil {
		return err
	}
	if logger != nil {
		logger.Info("v167: journal audit rows repaired",
			"mission_id_restored", restored, "unrecoverable", unrecoverable)
		if unrecoverable > 0 {
			// Deliberately NOT rehashed. Recomputing entry_hash for a row whose
			// original content cannot be proven would launder real tampering
			// into a clean chain — the one thing the chain exists to prevent.
			// They stay visible as known breaks in `crewship journal verify`.
			logger.Warn("v167: some journal rows lost mission_id before this migration and cannot be proven",
				"rows", unrecoverable,
				"detail", "their refs carry no mission_id (or no candidate reproduces the stored hash); "+
					"they remain permanent known breaks rather than being silently rehashed")
		}
	}
	return nil
}

// journalDestructiveFKs are the exact column-level clauses v52 shipped. They
// are matched (and required) literally rather than parsed: a rebuild that
// guessed at the schema is how a column gets silently dropped, and a literal
// match that stops finding its target fails loudly instead.
var journalDestructiveFKs = []string{
	" REFERENCES crews(id) ON DELETE CASCADE",
	" REFERENCES agents(id) ON DELETE CASCADE",
	" REFERENCES missions(id) ON DELETE SET NULL",
}

// journalRebuildTable is the staging name for the rebuilt table. It exists
// only between CREATE and RENAME inside one transaction.
const journalRebuildTable = "journal_entries_v167_rebuild"

// journalDDLNeedsRebuild reports whether the live definition still lets the
// database rewrite or delete audit rows.
func journalDDLNeedsRebuild(ddl string) bool {
	for _, clause := range journalDestructiveFKs {
		if strings.Contains(ddl, clause) {
			return true
		}
	}
	return false
}

// journalEntriesDDL reads the live CREATE TABLE text from sqlite_master.
//
// The live text is the source of truth, NOT the v52 constant: v120/v121
// (run_id), v152 (seq, prev_hash, entry_hash), v166 (priority_at_emit) and
// migrate.go's priority column all added columns after v52, and SQLite folds
// each ADD COLUMN into this stored text. Hand-copying an older definition is
// how a rebuild silently drops four columns of a populated audit log.
func journalEntriesDDL(ctx context.Context, conn *sql.Conn) (string, error) {
	var ddl string
	err := conn.QueryRowContext(ctx,
		`SELECT sql FROM sqlite_master WHERE type='table' AND name='journal_entries'`).Scan(&ddl)
	if err != nil {
		return "", fmt.Errorf("v167: read journal_entries definition: %w", err)
	}
	return ddl, nil
}

// rewriteJournalDDL turns the live definition into the staging table's
// definition: same columns, same defaults, same CHECKs, minus the three
// destructive FK actions.
func rewriteJournalDDL(ddl string) (string, error) {
	out := ddl
	for _, clause := range journalDestructiveFKs {
		if !strings.Contains(out, clause) {
			continue // already stripped (partial re-run)
		}
		if n := strings.Count(out, clause); n != 1 {
			return "", fmt.Errorf("v167: expected exactly one %q in journal_entries definition, found %d", clause, n)
		}
		out = strings.Replace(out, clause, "", 1)
	}
	// Retarget the statement at the staging table. Everything before the first
	// "(" is `CREATE TABLE [IF NOT EXISTS] <name>`, so the table name is the
	// only occurrence there — a column default or CHECK cannot appear before
	// the column list opens.
	open := strings.Index(out, "(")
	if open < 0 {
		return "", fmt.Errorf("v167: journal_entries definition has no column list: %.80q", ddl)
	}
	header, body := out[:open], out[open:]
	if n := strings.Count(header, "journal_entries"); n != 1 {
		return "", fmt.Errorf("v167: unexpected CREATE TABLE header %q", header)
	}
	header = strings.Replace(header, "journal_entries", journalRebuildTable, 1)
	// IF NOT EXISTS would turn a leftover staging table from a crashed run into
	// a silent no-op that then copies into the WRONG shape. Let it collide.
	header = strings.Replace(header, "IF NOT EXISTS ", "", 1)
	return header + body, nil
}

// schemaObject is an index or trigger that belongs to journal_entries and is
// therefore destroyed by DROP TABLE.
type schemaObject struct {
	kind string
	name string
	sql  string
}

// journalDependentObjects captures every index and trigger attached to
// journal_entries so the rebuild can put them back verbatim.
//
// Captured rather than hard-coded: the table carries ~12 indexes spread across
// migrate.go, v42/v45, v120/121, v146, v152 and v156, and any list written out
// here would be one merge away from being wrong. Rows with a NULL sql are
// SQLite's own auto-indexes for PRIMARY KEY/UNIQUE — they come back with the
// CREATE TABLE and must not be replayed.
//
// Objects that merely REFERENCE journal_entries from elsewhere (the FTS5 table,
// the child tables' foreign keys, any view) are not captured and need no
// action: DROP TABLE does not touch them, and they resolve by name again the
// moment the staging table is renamed into place.
func journalDependentObjects(ctx context.Context, conn *sql.Conn) ([]schemaObject, error) {
	rows, err := conn.QueryContext(ctx, `
		SELECT type, name, sql FROM sqlite_master
		 WHERE tbl_name = 'journal_entries' AND sql IS NOT NULL AND type IN ('index','trigger')
		 ORDER BY type, name`)
	if err != nil {
		return nil, fmt.Errorf("v167: read journal_entries dependents: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []schemaObject
	for rows.Next() {
		var o schemaObject
		if err := rows.Scan(&o.kind, &o.name, &o.sql); err != nil {
			return nil, fmt.Errorf("v167: scan dependent: %w", err)
		}
		out = append(out, o)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("v167: iterate dependents: %w", err)
	}
	return out, nil
}

// journalFKCheckTables is the blast radius of the rebuild: the table itself
// (its own workspace_id FK) plus every table whose foreign key points AT it.
// A full-database PRAGMA foreign_key_check would scan every table in the
// instance for a change that cannot possibly affect the others.
var journalFKCheckTables = []string{
	"journal_entries",
	"journal_embeddings",       // entry_id -> journal_entries(id)   (v42/v45)
	"memory_relations",         // entry_id / related_entry_id       (v55)
	"journal_entry_priorities", // entry_id -> journal_entries(id)   (v166)
}

// countFKViolations totals PRAGMA foreign_key_check rows over the tables the
// rebuild can affect. Tables that do not exist yet are skipped.
func countFKViolations(ctx context.Context, conn *sql.Conn) (int, error) {
	total := 0
	for _, tbl := range journalFKCheckTables {
		var present int
		if err := conn.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, tbl).Scan(&present); err != nil {
			return 0, fmt.Errorf("v167: probe %s: %w", tbl, err)
		}
		if present == 0 {
			continue
		}
		// #nosec G202 -- tbl comes from the fixed list above, never from input.
		rows, err := conn.QueryContext(ctx, `PRAGMA foreign_key_check(`+tbl+`)`)
		if err != nil {
			return 0, fmt.Errorf("v167: foreign_key_check(%s): %w", tbl, err)
		}
		for rows.Next() {
			total++
		}
		err = rows.Err()
		_ = rows.Close()
		if err != nil {
			return 0, fmt.Errorf("v167: iterate foreign_key_check(%s): %w", tbl, err)
		}
	}
	return total, nil
}

// rebuildJournalEntries performs SQLite's documented table-rebuild procedure
// with foreign-key enforcement suspended, then verifies it introduced no new
// referential violation before committing.
func rebuildJournalEntries(ctx context.Context, conn *sql.Conn, ddl string, logger *slog.Logger) error {
	newDDL, err := rewriteJournalDDL(ddl)
	if err != nil {
		return err
	}
	dependents, err := journalDependentObjects(ctx, conn)
	if err != nil {
		return err
	}
	cols, err := journalColumns(ctx, conn, "journal_entries")
	if err != nil {
		return err
	}

	// Baseline taken BEFORE the rebuild. Comparing counts (rather than
	// demanding zero) is what keeps a pre-existing orphan — a row some earlier
	// restore left behind — from turning a schema fix into a boot failure,
	// while still catching a violation this migration actually introduced.
	before, err := countFKViolations(ctx, conn)
	if err != nil {
		return err
	}

	// Both pragmas are connection state and both must be set in autocommit
	// mode; `foreign_keys` is silently ignored inside a transaction, which is
	// exactly the trap v89 documented.
	var fkWas int
	if err := conn.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&fkWas); err != nil {
		return fmt.Errorf("v167: read foreign_keys pragma: %w", err)
	}
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		return fmt.Errorf("v167: disable foreign_keys: %w", err)
	}
	defer func() {
		if fkWas != 0 {
			if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys = ON`); err != nil && logger != nil {
				logger.Error("v167: failed to re-enable foreign_keys on the migration connection", "error", err)
			}
		}
	}()
	// legacy_alter_table keeps ALTER TABLE RENAME to renaming the table and
	// nothing else. The modern behaviour also rewrites references to the old
	// name inside other objects and reparses the whole schema while doing it —
	// neither is wanted here, where every dependent is being recreated by hand
	// from its captured DDL.
	if _, err := conn.ExecContext(ctx, `PRAGMA legacy_alter_table = ON`); err != nil {
		return fmt.Errorf("v167: enable legacy_alter_table: %w", err)
	}
	defer func() {
		if _, err := conn.ExecContext(ctx, `PRAGMA legacy_alter_table = OFF`); err != nil && logger != nil {
			logger.Warn("v167: failed to restore legacy_alter_table", "error", err)
		}
	}()

	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("v167: begin rebuild: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	if _, err := tx.ExecContext(ctx, `DROP TABLE IF EXISTS `+journalRebuildTable); err != nil {
		return fmt.Errorf("v167: clear staging table: %w", err)
	}
	if _, err := tx.ExecContext(ctx, newDDL); err != nil {
		return fmt.Errorf("v167: create rebuilt table: %w", err)
	}

	// Column parity is the guard against the failure the brief calls out: a
	// rebuild that quietly loses a column. The staging table is derived from
	// the live text, so this should be impossible — which is precisely why a
	// mismatch means the rewrite mangled something and must not proceed.
	newCols, err := journalColumnsTx(ctx, tx, journalRebuildTable)
	if err != nil {
		return err
	}
	if strings.Join(cols, ",") != strings.Join(newCols, ",") {
		return fmt.Errorf("v167: rebuilt column list %v does not match the live one %v", newCols, cols)
	}

	// rowid is copied explicitly. journal_entries_fts is an EXTERNAL-CONTENT
	// FTS5 index keyed on rowid; letting SQLite reassign rowids would leave
	// every indexed document pointing at a different row and full-text search
	// silently returning the wrong entries. (The index is rebuilt below as
	// well — belt and braces on a table nobody gets to re-verify by eye.)
	list := strings.Join(cols, ", ")
	copySQL := fmt.Sprintf(`INSERT INTO %s (rowid, %s) SELECT rowid, %s FROM journal_entries`,
		journalRebuildTable, list, list)
	if _, err := tx.ExecContext(ctx, copySQL); err != nil {
		return fmt.Errorf("v167: copy rows: %w", err)
	}

	var oldCount, newCount int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM journal_entries`).Scan(&oldCount); err != nil {
		return fmt.Errorf("v167: count source rows: %w", err)
	}
	// #nosec G202 -- constant table name.
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+journalRebuildTable).Scan(&newCount); err != nil {
		return fmt.Errorf("v167: count copied rows: %w", err)
	}
	if oldCount != newCount {
		return fmt.Errorf("v167: copied %d of %d journal rows", newCount, oldCount)
	}

	// DROP TABLE takes this table's indexes and triggers with it. It does NOT
	// fire the FTS delete trigger (SQLite fires no row triggers on DROP), so
	// the FTS index survives the swap untouched.
	if _, err := tx.ExecContext(ctx, `DROP TABLE journal_entries`); err != nil {
		return fmt.Errorf("v167: drop old table: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `ALTER TABLE `+journalRebuildTable+` RENAME TO journal_entries`); err != nil {
		return fmt.Errorf("v167: rename rebuilt table: %w", err)
	}

	for _, o := range dependents {
		if _, err := tx.ExecContext(ctx, o.sql); err != nil {
			return fmt.Errorf("v167: recreate %s %s: %w", o.kind, o.name, err)
		}
	}

	// Rebuild the external-content FTS index from the table it shadows. The
	// rowid copy above already keeps it consistent; this makes it certain, and
	// costs one pass over a table we have just rewritten anyway.
	var ftsPresent int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE name='journal_entries_fts'`).Scan(&ftsPresent); err != nil {
		return fmt.Errorf("v167: probe fts table: %w", err)
	}
	if ftsPresent > 0 {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO journal_entries_fts(journal_entries_fts) VALUES('rebuild')`); err != nil {
			return fmt.Errorf("v167: rebuild fts index: %w", err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("v167: commit rebuild: %w", err)
	}
	committed = true

	after, err := countFKViolations(ctx, conn)
	if err != nil {
		return err
	}
	if after > before {
		return fmt.Errorf("v167: rebuild introduced %d new referential violation(s) (%d before, %d after)",
			after-before, before, after)
	}
	if logger != nil {
		logger.Info("v167: journal_entries rebuilt without destructive FK actions",
			"rows", newCount, "indexes_and_triggers", len(dependents))
		if before > 0 {
			logger.Warn("v167: pre-existing referential violations remain (not introduced by this migration)",
				"count", before)
		}
	}
	return nil
}

// journalColumns lists a table's columns in declaration order.
func journalColumns(ctx context.Context, conn *sql.Conn, table string) ([]string, error) {
	rows, err := conn.QueryContext(ctx, `SELECT name FROM pragma_table_info(?) ORDER BY cid`, table)
	if err != nil {
		return nil, fmt.Errorf("v167: read %s columns: %w", table, err)
	}
	defer func() { _ = rows.Close() }()
	return scanNames(rows, table)
}

// journalColumnsTx is journalColumns against an open transaction.
func journalColumnsTx(ctx context.Context, tx *sql.Tx, table string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `SELECT name FROM pragma_table_info(?) ORDER BY cid`, table)
	if err != nil {
		return nil, fmt.Errorf("v167: read %s columns: %w", table, err)
	}
	defer func() { _ = rows.Close() }()
	return scanNames(rows, table)
}

func scanNames(rows *sql.Rows, table string) ([]string, error) {
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("v167: scan %s column: %w", table, err)
		}
		out = append(out, name)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("v167: iterate %s columns: %w", table, err)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("v167: table %s has no columns", table)
	}
	return out, nil
}

// journalRepairQuerier is the subset of database/sql shared by *sql.Conn,
// *sql.Tx and *sql.DB. The repair runs from the migration (on a pinned
// connection) and from the restore backfill hook (inside the backup runner's
// transaction), and must behave identically in both.
type journalRepairQuerier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// repairMissionCandidatesSQL selects every row the FK action could have
// damaged: mission_id gone from the column while `refs` still carries it.
// That asymmetry is the fingerprint of ON DELETE SET NULL — refs is opaque
// JSON the constraint could not reach.
//
// The projection is the hashed one, byte-for-byte as verify.go builds it
// (COALESCEs included), because the recomputed hash has to match a hash
// produced by that exact framing.
const repairMissionCandidatesSQL = `
SELECT seq, id, workspace_id,
       COALESCE(crew_id,''), COALESCE(agent_id,''),
       ts, entry_type, severity,
       COALESCE(priority_at_emit, priority, 'normal'), actor_type,
       COALESCE(actor_id,''), summary, payload, refs,
       COALESCE(trace_id,''), COALESCE(span_id,''), COALESCE(expires_at,''),
       COALESCE(prev_hash,''), COALESCE(entry_hash,''),
       json_extract(refs, '$.mission_id')
FROM journal_entries
WHERE mission_id IS NULL
  AND json_extract(refs, '$.mission_id') IS NOT NULL`

// repairJournalMissionIDs restores mission_id on audit rows the FK nulled, and
// reports how many could not be proven.
//
// # Why this is sound, and why the obvious one-line UPDATE is not
//
// The obvious repair is `SET mission_id = json_extract(refs,'$.mission_id')`.
// It is wrong: nothing guarantees the value in refs is the value the row was
// WRITTEN with. An entry emitted with an empty mission_id but a refs blob
// mentioning one — the shape several emit sites produce — would be "repaired"
// into a row whose stored hash no longer matches, converting a healthy row into
// a permanent break. The repair would manufacture the corruption it exists to
// undo.
//
// So each candidate is checked instead of trusted: recompute the keyed chain
// hash with the candidate in place and write it back ONLY if the result equals
// the entry_hash already on disk. Because that hash is an HMAC under a key held
// outside the database, a candidate that reproduces it is the original value —
// the same argument recoverEmitPriority relies on in verify.go. A row that
// fails is left exactly as it is.
//
// Idempotent: it only reads rows whose mission_id is already NULL, so a second
// run over a repaired table finds nothing (a hard requirement both for fnNoTx
// and for the restore-backfill contract).
func repairJournalMissionIDs(ctx context.Context, q journalRepairQuerier, logger *slog.Logger) (restored, unrecoverable int, err error) {
	key := journal.ChainKeyFromEnv()

	rows, err := q.QueryContext(ctx, repairMissionCandidatesSQL)
	if err != nil {
		return 0, 0, fmt.Errorf("v167: scan damaged rows: %w", err)
	}

	type fix struct {
		id        string
		missionID string
	}
	var fixes []fix

	for rows.Next() {
		var f journal.ChainFields
		var prevHash, entryHash string
		var candidate sql.NullString
		if err := rows.Scan(
			&f.Seq, &f.ID, &f.Workspace,
			&f.CrewID, &f.AgentID,
			&f.TS, &f.EntryType, &f.Severity, &f.Priority, &f.ActorType,
			&f.ActorID, &f.Summary, &f.Payload, &f.Refs,
			&f.TraceID, &f.SpanID, &f.ExpiresAt,
			&prevHash, &entryHash, &candidate,
		); err != nil {
			_ = rows.Close()
			return 0, 0, fmt.Errorf("v167: scan damaged row: %w", err)
		}
		// An unchained legacy row (no entry_hash) offers no proof either way,
		// and a non-string refs value is not an id. Neither is written.
		if entryHash == "" || !candidate.Valid || candidate.String == "" {
			unrecoverable++
			continue
		}
		f.MissionID = candidate.String
		if journal.ChainHashKeyed(key, prevHash, f) != entryHash {
			unrecoverable++
			continue
		}
		fixes = append(fixes, fix{id: f.ID, missionID: candidate.String})
	}
	if cerr := rows.Err(); cerr != nil {
		_ = rows.Close()
		return 0, 0, fmt.Errorf("v167: iterate damaged rows: %w", cerr)
	}
	_ = rows.Close()

	// Writes happen after the read is fully drained: on SQLite a single pinned
	// connection cannot serve an UPDATE while its own SELECT is still open.
	for _, f := range fixes {
		if _, err := q.ExecContext(ctx,
			`UPDATE journal_entries SET mission_id = ? WHERE id = ? AND mission_id IS NULL`,
			f.missionID, f.id); err != nil {
			return restored, unrecoverable, fmt.Errorf("v167: restore mission_id on %s: %w", f.id, err)
		}
		restored++
	}
	if logger != nil && (restored > 0 || unrecoverable > 0) {
		logger.Debug("v167: mission_id repair pass complete",
			"restored", restored, "unrecoverable", unrecoverable)
	}
	return restored, unrecoverable, nil
}

// restoreBackfillRepairJournalMissionIDs re-runs the #1482 repair over rows a
// restore just re-inserted from a bundle taken BEFORE v167.
//
// Why the migration alone is not enough: it runs once, over the rows present at
// upgrade time. A bundle captured from a damaged pre-v167 instance carries the
// nulled mission_ids inside it, and restoring it drops them straight back into a
// migrated database — where the migration will never run again. Without this
// hook a restore silently reintroduces the exact breakage this version removes,
// and `journal verify` goes red on an instance that was green a minute earlier.
//
// Idempotent by construction; see repairJournalMissionIDs.
func restoreBackfillRepairJournalMissionIDs(ctx context.Context, tx *sql.Tx, logger *slog.Logger) error {
	restored, unrecoverable, err := repairJournalMissionIDs(ctx, tx, logger)
	if err != nil {
		return err
	}
	if logger != nil && (restored > 0 || unrecoverable > 0) {
		logger.Info("v167 restore backfill: repaired restored journal rows",
			"mission_id_restored", restored, "unrecoverable", unrecoverable)
	}
	return nil
}
