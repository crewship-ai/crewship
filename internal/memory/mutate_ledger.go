package memory

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

// The durable half of the §8 memory contract: the revision anchor, the mutation
// ledger, and the recovery protocol that reads them.
//
// Schema: internal/database/migrations/*_memory_mutation_ledger.sql.

// revisionAnchor is one row of memory_revisions — the CAS anchor. There is
// exactly one per (workspace_id, path), and `path` is the SCOPED audit path
// ("agent:alice/AGENT.md"), because two agents in one workspace both own an
// "AGENT.md" and only the scoped form separates them.
type revisionAnchor struct {
	revision   int64
	contentSHA string
	bytes      int64
}

func loadAnchor(ctx context.Context, db *sql.DB, workspaceID, auditPath string) (*revisionAnchor, error) {
	var a revisionAnchor
	err := db.QueryRowContext(ctx, `
		SELECT revision, content_sha256, bytes
		  FROM memory_revisions
		 WHERE workspace_id = ? AND path = ?`,
		workspaceID, auditPath).Scan(&a.revision, &a.contentSHA, &a.bytes)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load revision anchor: %w", err)
	}
	return &a, nil
}

// storedOperation is a memory_mutations row, as the idempotency check needs it.
type storedOperation struct {
	id            string
	requestSHA    string
	state         string
	baseSHA       string
	targetSHA     string
	targetBlobRef string
	canonicalPath string
	newRevision   int64
	baseRevision  int64
	targetBytes   int64
	result        MutateResult
}

func loadOperation(ctx context.Context, db *sql.DB, workspaceID, auditPath, operationID string) (*storedOperation, error) {
	var (
		op         storedOperation
		resultJSON string
	)
	err := db.QueryRowContext(ctx, `
		SELECT id, request_sha256, state, base_sha256, target_sha256, target_blob_ref,
		       canonical_path, new_revision, base_revision, target_bytes, result_json
		  FROM memory_mutations
		 WHERE workspace_id = ? AND path = ? AND operation_id = ?`,
		workspaceID, auditPath, operationID,
	).Scan(&op.id, &op.requestSHA, &op.state, &op.baseSHA, &op.targetSHA, &op.targetBlobRef,
		&op.canonicalPath, &op.newRevision, &op.baseRevision, &op.targetBytes, &resultJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load memory mutation: %w", err)
	}
	op.result = storedResult(op, resultJSON)
	return &op, nil
}

// storedResult reconstructs the result an identical retry must be handed back.
// It is rebuilt from the row's own columns rather than trusted from
// result_json, so a row written by an older build still answers correctly;
// result_json only carries what the columns do not.
func storedResult(op storedOperation, resultJSON string) MutateResult {
	out := MutateResult{
		Revision:       op.newRevision,
		ContentSHA256:  op.targetSHA,
		BytesWritten:   int(op.targetBytes),
		BaseRevision:   op.baseRevision,
		BaseSHA256:     op.baseSHA,
		MutationID:     op.id,
		LedgerRecorded: true,
	}
	var extra struct {
		AdoptedBase      bool `json:"adopted_base"`
		NormalizedBase   bool `json:"normalized_base"`
		RemovalsVerified bool `json:"removals_verified"`
	}
	if resultJSON != "" {
		_ = json.Unmarshal([]byte(resultJSON), &extra)
	}
	out.AdoptedBase = extra.AdoptedBase
	out.NormalizedBase = extra.NormalizedBase
	out.RemovalsVerified = extra.RemovalsVerified
	return out
}

type intentRow struct {
	requestSHA   string
	baseRevision int64
	baseSHA      string
	newRevision  int64
	targetSHA    string
	target       []byte
	adopted      bool
	normalized   bool
	verified     bool
	override     bool
}

// writeIntent parks the target content in the content-addressed blob store and
// commits one `intent` row. §8: "Nepotvrzený intent musí obsahovat durable data
// pro dokončení." The blob IS that durable data — recovery re-reads it and
// finishes the rename without needing the original caller.
//
// The transaction is opened after the blob is on disk and closed before the
// canonical write starts, so no SQLite write lock is ever held across an fsync
// of the memory file.
func writeIntent(ctx context.Context, db *sql.DB, req MutateRequest, row intentRow) (string, error) {
	blobPath := blobPathFor(req.BlobRoot, row.targetSHA)
	if _, err := writeBlobIfMissing(blobPath, row.target); err != nil {
		return "", fmt.Errorf("park intent blob: %w", err)
	}

	id, err := newMutationID()
	if err != nil {
		return "", err
	}
	removalsJSON, err := json.Marshal(canonicalRemovals(req.Removals))
	if err != nil {
		return "", fmt.Errorf("encode removals: %w", err)
	}
	resultJSON, err := json.Marshal(map[string]any{
		"adopted_base":      row.adopted,
		"normalized_base":   row.normalized,
		"removals_verified": row.verified,
	})
	if err != nil {
		return "", fmt.Errorf("encode result: %w", err)
	}

	_, err = db.ExecContext(ctx, `
		INSERT INTO memory_mutations (
			id, workspace_id, path, canonical_path, scope, tier,
			operation_id, request_sha256, op,
			actor_type, actor_id, run_id, generation, source,
			base_revision, base_sha256, new_revision, target_sha256, target_bytes,
			target_blob_ref, removals_json, removals_declared, adopted_base, normalized_base,
			override_drift, state, result_json, recorded_at
		) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		id, req.WorkspaceID, req.AuditPath, req.Path, req.Scope, string(req.Tier),
		req.OperationID, row.requestSHA, string(req.Op),
		req.ActorType, req.ActorID, req.RunID, req.Generation, req.Source,
		row.baseRevision, row.baseSHA, row.newRevision, row.targetSHA, len(row.target),
		blobPath, string(removalsJSON), boolInt(row.verified), boolInt(row.adopted), boolInt(row.normalized),
		boolInt(row.override), mutationStateIntent, string(resultJSON), nowStamp(),
	)
	if err != nil {
		// The partial unique index idx_memory_mutations_pending is what makes
		// "one pending intent per key" a database fact rather than a
		// convention. Hitting it means another writer crashed between its
		// intent and its confirmation and recovery could not settle it.
		if isPendingIntentCollision(err) {
			return "", fmt.Errorf("%w: %s already has an unconfirmed mutation intent that recovery could not settle",
				ErrMemoryConflict, req.AuditPath)
		}
		if isUniqueViolation(err) {
			return "", fmt.Errorf("%w: operation %q on %s is already recorded",
				ErrOperationConflict, req.OperationID, req.AuditPath)
		}
		return "", fmt.Errorf("insert memory mutation intent: %w", err)
	}
	return id, nil
}

// confirmMutation is the second transaction: the mutation becomes `confirmed`
// and the revision anchor moves, atomically with respect to each other. A
// reader that sees the new revision therefore always sees the mutation that
// produced it.
func confirmMutation(ctx context.Context, db *sql.DB, req MutateRequest, mutationID string, revision int64, targetSHA string, bytes int, res MutateResult) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin confirm: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	now := nowStamp()
	if _, err := tx.ExecContext(ctx, `
		UPDATE memory_mutations
		   SET state = ?, confirmed_at = ?
		 WHERE id = ?`,
		mutationStateConfirmed, now, mutationID); err != nil {
		return fmt.Errorf("confirm memory mutation: %w", err)
	}
	if err := upsertAnchorTx(ctx, tx, req.WorkspaceID, req.AuditPath, req.Scope, string(req.Tier),
		revision, targetSHA, bytes, mutationID, now); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit confirm: %w", err)
	}
	return nil
}

func upsertAnchorTx(ctx context.Context, tx *sql.Tx, workspaceID, auditPath, scope, tier string, revision int64, sha string, bytes int, mutationID, now string) error {
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO memory_revisions (
			workspace_id, path, scope, tier, revision, content_sha256, bytes,
			last_mutation_id, updated_at
		) VALUES (?,?,?,?,?,?,?,?,?)
		ON CONFLICT(workspace_id, path) DO UPDATE SET
			revision = excluded.revision,
			content_sha256 = excluded.content_sha256,
			bytes = excluded.bytes,
			scope = excluded.scope,
			tier = excluded.tier,
			last_mutation_id = excluded.last_mutation_id,
			updated_at = excluded.updated_at`,
		workspaceID, auditPath, scope, tier, revision, sha, bytes, mutationID, now); err != nil {
		return fmt.Errorf("upsert revision anchor: %w", err)
	}
	return nil
}

// RecoverPending completes or conflicts every unconfirmed mutation intent in
// the ledger. Call it at boot, before any writer is admitted.
//
// It returns how many intents it settled into a confirmed write and how many it
// declared drifted. A conflicted intent is NOT an error: it means a third party
// changed the file, and §8 forbids resolving that by overwriting.
func RecoverPending(ctx context.Context, db *sql.DB, blobRoot string) (recovered, conflicted int, err error) {
	if db == nil {
		return 0, 0, fmt.Errorf("%w: recovery needs the mutation ledger", ErrLedgerRequired)
	}
	rows, err := db.QueryContext(ctx, `
		SELECT DISTINCT workspace_id, path, canonical_path
		  FROM memory_mutations
		 WHERE state IN (?, ?)
		 ORDER BY workspace_id, path`,
		mutationStateIntent, mutationStateRenamed)
	if err != nil {
		return 0, 0, fmt.Errorf("list unconfirmed mutations: %w", err)
	}
	type key struct{ ws, auditPath, canonical string }
	var keys []key
	for rows.Next() {
		var k key
		if err := rows.Scan(&k.ws, &k.auditPath, &k.canonical); err != nil {
			rows.Close()
			return 0, 0, fmt.Errorf("scan unconfirmed mutation: %w", err)
		}
		keys = append(keys, k)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, 0, fmt.Errorf("iterate unconfirmed mutations: %w", err)
	}

	for _, k := range keys {
		lk := NewFileLock(k.canonical + ".lock")
		if lerr := lk.Lock(); lerr != nil {
			return recovered, conflicted, fmt.Errorf("recovery lock %s: %w", k.canonical, lerr)
		}
		n, rerr := recoverKeyLocked(ctx, db, k.ws, k.auditPath, k.canonical, blobRoot)
		_ = lk.Unlock()
		if rerr != nil {
			if errors.Is(rerr, ErrMemoryConflict) {
				conflicted++
				continue
			}
			return recovered, conflicted, rerr
		}
		recovered += n
	}
	return recovered, conflicted, nil
}

// recoverKeyLocked runs §8's recovery hash protocol for ONE key. The caller
// must already hold that key's file lock.
//
//	file matches the TARGET hash -> the rename happened, the confirmation did
//	                                not. Complete the DB confirmation.
//	file matches the BASE hash   -> the rename did not happen. Complete the
//	                                intent: redo the durable write from the
//	                                parked blob, then confirm.
//	any other hash               -> drift. Mark the intent conflicted and
//	                                surface it. NEVER overwrite: those bytes
//	                                belong to somebody, and the ledger cannot
//	                                say who.
//
// The anchor is deliberately left stale on the drift path. That is what makes
// the NEXT write of this key fail its drift check too, instead of the file
// quietly rejoining the contract at whatever a third party left behind.
func recoverKeyLocked(ctx context.Context, db *sql.DB, workspaceID, auditPath, canonicalPath, blobRoot string) (int, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT id, operation_id, state, base_sha256, target_sha256, target_blob_ref,
		       base_revision, new_revision, target_bytes, scope, tier
		  FROM memory_mutations
		 WHERE workspace_id = ? AND path = ? AND state IN (?, ?)
		 ORDER BY recorded_at, id`,
		workspaceID, auditPath, mutationStateIntent, mutationStateRenamed)
	if err != nil {
		return 0, fmt.Errorf("list unconfirmed mutations for %s: %w", auditPath, err)
	}
	type pending struct {
		id, operationID, state      string
		baseSHA, targetSHA, blobRef string
		baseRevision, newRevision   int64
		targetBytes                 int64
		scope, tier                 string
	}
	var todo []pending
	for rows.Next() {
		var p pending
		if err := rows.Scan(&p.id, &p.operationID, &p.state, &p.baseSHA, &p.targetSHA,
			&p.blobRef, &p.baseRevision, &p.newRevision, &p.targetBytes, &p.scope, &p.tier); err != nil {
			rows.Close()
			return 0, fmt.Errorf("scan unconfirmed mutation: %w", err)
		}
		todo = append(todo, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("iterate unconfirmed mutations: %w", err)
	}
	if len(todo) == 0 {
		return 0, nil
	}

	recovered := 0
	for _, p := range todo {
		onDisk, rerr := readRegularNoFollow(canonicalPath)
		if rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
			return recovered, fmt.Errorf("recovery read %s: %w", canonicalPath, rerr)
		}
		diskSHA := sha256Hex(onDisk)

		switch diskSHA {
		case p.targetSHA:
			// The rename landed. Only the confirmation is missing.
			if err := markRenamed(ctx, db, p.id); err != nil {
				return recovered, err
			}
			if err := finishConfirm(ctx, db, workspaceID, auditPath, p.scope, p.tier,
				p.id, p.newRevision, p.targetSHA, int(p.targetBytes)); err != nil {
				return recovered, err
			}
			recovered++

		case p.baseSHA:
			// The rename never happened. The parked blob is the durable data
			// §8 requires the intent to carry; redo the write from it.
			blob, berr := readIntentBlob(p.blobRef, blobRoot, p.targetSHA)
			if berr != nil {
				return recovered, fmt.Errorf("recovery blob for mutation %s: %w", p.id, berr)
			}
			if got := sha256Hex(blob); got != p.targetSHA {
				return recovered, fmt.Errorf("recovery blob for mutation %s hashes to %s, expected %s",
					p.id, short(got), short(p.targetSHA))
			}
			if err := writeFileDurable(canonicalPath, blob, 0o644); err != nil {
				return recovered, fmt.Errorf("recovery write %s: %w", canonicalPath, err)
			}
			if err := markRenamed(ctx, db, p.id); err != nil {
				return recovered, err
			}
			if err := finishConfirm(ctx, db, workspaceID, auditPath, p.scope, p.tier,
				p.id, p.newRevision, p.targetSHA, len(blob)); err != nil {
				return recovered, err
			}
			recovered++

		default:
			if err := markConflicted(ctx, db, p.id); err != nil {
				return recovered, err
			}
			return recovered, fmt.Errorf(
				"%w: %s has an unconfirmed mutation (operation %q) whose file now hashes to %s, matching neither the base %s nor the target %s — it was changed by something outside the contract and will not be overwritten",
				ErrMemoryConflict, auditPath, p.operationID, short(diskSHA), short(p.baseSHA), short(p.targetSHA))
		}
	}
	return recovered, nil
}

// readIntentBlob reads the parked target content. It prefers the absolute
// target_blob_ref the intent recorded, and falls back to recomputing the path
// under blobRoot — a data directory that moved between the crash and the
// restart would otherwise strand every recoverable intent.
func readIntentBlob(blobRef, blobRoot, sha string) ([]byte, error) {
	if blobRef != "" {
		if b, err := os.ReadFile(blobRef); err == nil {
			return b, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	if blobRoot == "" {
		return nil, fmt.Errorf("intent blob %s is missing and no blob root was supplied", short(sha))
	}
	return os.ReadFile(blobPathFor(blobRoot, sha))
}

func markRenamed(ctx context.Context, db *sql.DB, id string) error {
	if _, err := db.ExecContext(ctx,
		`UPDATE memory_mutations SET state = ? WHERE id = ? AND state = ?`,
		mutationStateRenamed, id, mutationStateIntent); err != nil {
		return fmt.Errorf("mark mutation renamed: %w", err)
	}
	return nil
}

func markConflicted(ctx context.Context, db *sql.DB, id string) error {
	if _, err := db.ExecContext(ctx,
		`UPDATE memory_mutations SET state = ?, confirmed_at = ? WHERE id = ?`,
		mutationStateConflicted, nowStamp(), id); err != nil {
		return fmt.Errorf("mark mutation conflicted: %w", err)
	}
	return nil
}

func finishConfirm(ctx context.Context, db *sql.DB, workspaceID, auditPath, scope, tier, mutationID string, revision int64, sha string, bytes int) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin recovery confirm: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	now := nowStamp()
	if _, err := tx.ExecContext(ctx,
		`UPDATE memory_mutations SET state = ?, confirmed_at = ? WHERE id = ?`,
		mutationStateConfirmed, now, mutationID); err != nil {
		return fmt.Errorf("recovery confirm mutation: %w", err)
	}
	if err := upsertAnchorTx(ctx, tx, workspaceID, auditPath, scope, tier, revision, sha, bytes, mutationID, now); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit recovery confirm: %w", err)
	}
	return nil
}

func blobPathFor(blobRoot, sha string) string {
	if blobRoot == "" || len(sha) < 2 {
		return ""
	}
	return blobRoot + string(os.PathSeparator) + sha[:2] + string(os.PathSeparator) + sha
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// isUniqueViolation / isPendingIntentCollision read modernc SQLite's error
// text. The driver does not export typed constraint errors, and matching the
// message is what the rest of the tree does; the index name in the second
// check is what makes it specific rather than a guess.
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "UNIQUE constraint failed") || strings.Contains(s, "constraint failed: UNIQUE")
}

// pendingIntentIndex is the partial unique index that enforces "one pending
// intent per key". SQLite names the INDEX in the violation message for a
// partial unique index, and names the COLUMNS for a table-level UNIQUE, which
// is what lets these two cases be told apart at all.
const pendingIntentIndex = "idx_memory_mutations_pending"

func isPendingIntentCollision(err error) bool {
	return isUniqueViolation(err) && strings.Contains(err.Error(), pendingIntentIndex)
}
