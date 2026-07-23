package journal

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
)

// The journal hash-chain makes the audit trail tamper-evident.
//
// Every entry commits to (a) its own immutable content and (b) the
// entry_hash of the entry immediately preceding it in the SAME workspace.
// That linkage means any after-the-fact edit, deletion of a middle row, or
// in-place reordering breaks the chain and is detected by VerifyChain.
//
// The chain is KEYED: entry_hash is HMAC-SHA256, not a bare SHA-256, under a
// key that never lives in the database (it is derived from the persisted
// ENCRYPTION_KEY — see DeriveChainKey). This is the property that defends
// against the stated threat model: an attacker with DB write access. Without
// the key such an attacker can mutate a row and recompute a *plain* SHA-256
// over the public columns, but they cannot recompute the HMAC, so
// verification still fails. A bare hash would offer no protection here — the
// attacker would simply recompute it.
//
// Ordering is by a per-workspace monotonic `seq` (assigned at emit), NOT by
// the random `id` PK or the wall-clock `ts` (which can collide). seq gives a
// deterministic order and makes a deleted middle row show up as a gap.
//
// Legitimate compaction (see internal/consolidate) and the pipeline-resurrect
// purge DELETE mid-chain rows on purpose. To keep that from reading as
// tampering, each such delete writes a SIGNED checkpoint into
// journal_chain_checkpoints that commits (under the same HMAC key) to the
// exact (seq, entry_hash) of every row it removed. VerifyChain fills a seq gap
// from a matching valid checkpoint and continues; an UNcheckpointed gap (a
// malicious mid-chain delete) still fails. An attacker cannot forge a
// checkpoint because the MAC needs the key.
//
// Not covered by the chain (documented residual — see docs/security/audit.mdx):
// truncation of the TAIL (deleting the newest N entries) leaves a shorter but
// still internally-consistent chain, and in plaintext dev mode (no
// ENCRYPTION_KEY) the key is derivable so keying degrades to detecting only
// key-unaware edits.

// GenesisPrevHash is the prev_hash of the first entry in a workspace chain.
// The empty string is deliberate: it is length-framed like any other field,
// so genesis is unambiguous and needs no sentinel value.
const GenesisPrevHash = ""

// chainKeyDerivationLabel domain-separates the journal-chain HMAC key from any
// other subkey derived off ENCRYPTION_KEY. The trailing NUL keeps the label a
// fixed-length prefix so it can never collide with a future-appended context.
// Mirrors the internal-token master derivation in internal/config.
const chainKeyDerivationLabel = "crewship journal chain v1\x00"

// checkpointMACLabel domain-separates the compaction-checkpoint MAC from the
// per-entry chain hash so a value from one can never be replayed as the other.
const checkpointMACLabel = "crewship journal checkpoint v1\x00"

// DeriveChainKey produces the per-installation HMAC key for the journal chain
// from a persisted secret (the ENCRYPTION_KEY). HMAC-SHA256 gives a one-way
// 256-bit subkey: the same seed always yields the same key across restarts, so
// a freshly-migrated or restarted instance verifies clean, while the seed
// itself is never recoverable from the key. An empty seed (plaintext dev mode,
// no persisted secret) still yields a deterministic key — tamper-evidence then
// degrades to detecting only key-unaware edits, which is documented.
func DeriveChainKey(seed string) []byte {
	m := hmac.New(sha256.New, []byte(seed))
	m.Write([]byte(chainKeyDerivationLabel))
	return m.Sum(nil)
}

// ChainKeyFromEnv derives the chain key from the process ENCRYPTION_KEY, which
// the secrets bootstrap re-exports via os.Setenv before anything runs (so the
// emit path, the migration backfill, the compactor, and VerifyChain all see
// the same value). Callers that cannot be handed a key explicitly (migration,
// background compactor, HTTP handler) use this.
func ChainKeyFromEnv() []byte {
	return DeriveChainKey(os.Getenv("ENCRYPTION_KEY"))
}

// ChainFields is the canonical, ordered projection of a journal row that the
// hash commits to. The emit path builds it from the in-memory Entry just
// before INSERT; the verify path (and the migration backfill) build it by
// reading the stored columns back. Both MUST produce byte-identical framing
// or every recomputed hash mismatches — so all nullable columns are
// normalized to "" on both sides.
type ChainFields struct {
	Seq       int64
	ID        string
	Workspace string
	CrewID    string
	AgentID   string
	MissionID string
	TS        string
	EntryType string
	Severity  string
	Priority  string
	ActorType string
	ActorID   string
	Summary   string
	Payload   string
	Refs      string
	TraceID   string
	SpanID    string
	ExpiresAt string
}

// writeFramed appends a length-framed field to h: an 8-byte big-endian length
// prefix before the bytes, so no field value can be confused with a delimiter
// or spill into its neighbour — "ab"+"c" and "a"+"bc" frame (and thus hash)
// differently.
func writeFramed(h interface{ Write([]byte) (int, error) }, s string) {
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], uint64(len(s)))
	_, _ = h.Write(n[:])
	_, _ = h.Write([]byte(s))
}

// ChainHashKeyed computes the KEYED (HMAC-SHA256) content hash for an entry
// given the prev_hash it chains onto and the per-installation chain key.
// Serialization is length-framed (see writeFramed) and never depends on map
// iteration order: payload and refs are pre-serialized to their stored JSON
// strings (encoding/json already emits map keys sorted) and hashed as opaque
// bytes. The emit path, the verify path, and the migration backfill MUST all
// call this with the SAME key or every recomputed hash mismatches.
func ChainHashKeyed(key []byte, prevHash string, f ChainFields) string {
	h := hmac.New(sha256.New, key)
	var seqb [8]byte
	binary.BigEndian.PutUint64(seqb[:], uint64(f.Seq))
	h.Write(seqb[:])
	for _, field := range []string{
		prevHash,
		f.ID,
		f.Workspace,
		f.CrewID,
		f.AgentID,
		f.MissionID,
		f.TS,
		f.EntryType,
		f.Severity,
		f.Priority,
		f.ActorType,
		f.ActorID,
		f.Summary,
		f.Payload,
		f.Refs,
		f.TraceID,
		f.SpanID,
		f.ExpiresAt,
	} {
		writeFramed(h, field)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// RemovedEntry records that the row with this seq (and this stored entry_hash)
// was legitimately removed from the live chain by compaction/purge. A
// checkpoint commits to a set of these so VerifyChain can bridge the resulting
// seq gap while still linking prev_hash pointers across it.
type RemovedEntry struct {
	Seq  int64  `json:"seq"`
	Hash string `json:"hash"`
}

// CheckpointMAC computes the HMAC over the canonical framing of a removed set
// for one workspace. The set is sorted by seq so storage order is irrelevant.
// An attacker cannot produce a valid MAC without the key, so a forged
// checkpoint provides no cover for a malicious delete.
func CheckpointMAC(key []byte, workspaceID string, removed []RemovedEntry) string {
	sorted := make([]RemovedEntry, len(removed))
	copy(sorted, removed)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Seq < sorted[j].Seq })

	h := hmac.New(sha256.New, key)
	writeFramed(h, checkpointMACLabel)
	writeFramed(h, workspaceID)
	var nb [8]byte
	binary.BigEndian.PutUint64(nb[:], uint64(len(sorted)))
	h.Write(nb[:])
	for _, r := range sorted {
		var sb [8]byte
		binary.BigEndian.PutUint64(sb[:], uint64(r.Seq))
		h.Write(sb[:])
		writeFramed(h, r.Hash)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// checkpointInsertSQL persists a signed checkpoint. Called inside the SAME
// transaction as the delete it covers so a crash can never leave a
// checkpoint-less gap (which would read as tampering).
const checkpointInsertSQL = `INSERT INTO journal_chain_checkpoints
	(id, workspace_id, removed_json, mac) VALUES (?, ?, ?, ?)`

// WriteChainCheckpoint records, within tx, a signed checkpoint committing to
// the exact rows a compaction/purge is about to delete (or has deleted) from
// workspaceID's chain. Only entries with seq > 0 are recorded — unchained
// legacy rows (seq 0) are not part of the verified chain. A no-op when nothing
// chained is being removed.
func WriteChainCheckpoint(ctx context.Context, tx *sql.Tx, key []byte, workspaceID string, removed []RemovedEntry) error {
	chained := removed[:0:0]
	for _, r := range removed {
		if r.Seq > 0 {
			chained = append(chained, r)
		}
	}
	if len(chained) == 0 {
		return nil
	}
	blob, err := json.Marshal(chained)
	if err != nil {
		return fmt.Errorf("journal: marshal checkpoint: %w", err)
	}
	mac := CheckpointMAC(key, workspaceID, chained)
	if _, err := tx.ExecContext(ctx, checkpointInsertSQL, newID(), workspaceID, string(blob), mac); err != nil {
		return fmt.Errorf("journal: write checkpoint: %w", err)
	}
	return nil
}

// loadCheckpointedRemovals returns the union of (seq -> entry_hash) over every
// checkpoint for the workspace whose MAC VALIDATES under key. A checkpoint with
// a bad MAC (forged or corrupted) contributes NOTHING, so any gap it tried to
// cover is left uncovered and VerifyChain flags it — an attacker gains no cover
// by fabricating checkpoints. Returns the count of valid checkpoints applied.
func loadCheckpointedRemovals(ctx context.Context, db *sql.DB, key []byte, workspaceID string) (map[int64]string, int, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT removed_json, mac FROM journal_chain_checkpoints WHERE workspace_id = ?`, workspaceID)
	if err != nil {
		return nil, 0, fmt.Errorf("journal: load checkpoints: %w", err)
	}
	defer rows.Close()

	out := make(map[int64]string)
	applied := 0
	for rows.Next() {
		var blob, mac string
		if err := rows.Scan(&blob, &mac); err != nil {
			return nil, 0, fmt.Errorf("journal: scan checkpoint: %w", err)
		}
		var removed []RemovedEntry
		if err := json.Unmarshal([]byte(blob), &removed); err != nil {
			// Unparseable checkpoint body: treat as no cover (skip).
			continue
		}
		if !hmac.Equal([]byte(CheckpointMAC(key, workspaceID, removed)), []byte(mac)) {
			// Bad MAC → no cover.
			continue
		}
		for _, r := range removed {
			out[r.Seq] = r.Hash
		}
		applied++
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("journal: iterate checkpoints: %w", err)
	}
	return out, applied, nil
}

// VerifyResult reports the outcome of walking one workspace's chain.
type VerifyResult struct {
	WorkspaceID string `json:"workspace_id"`
	OK          bool   `json:"ok"`
	Count       int    `json:"count"`                 // live entries walked
	Checkpoints int    `json:"checkpoints,omitempty"` // valid compaction checkpoints applied
	BrokenSeq   int64  `json:"broken_seq,omitempty"`  // seq of first bad entry (0 when OK)
	BrokenID    string `json:"broken_id,omitempty"`   // id of first bad entry
	Reason      string `json:"reason,omitempty"`      // human-readable failure cause
}

// verifySelect pulls the columns the hash commits to, in seq order. Nullable
// columns are COALESCEd to ” to match how the emit path framed them.
const verifySelect = `
SELECT seq, id, workspace_id,
       COALESCE(crew_id,''), COALESCE(agent_id,''), COALESCE(mission_id,''),
       ts, entry_type, severity, COALESCE(priority,'normal'), actor_type,
       COALESCE(actor_id,''), summary, payload, refs,
       COALESCE(trace_id,''), COALESCE(span_id,''), COALESCE(expires_at,''),
       COALESCE(prev_hash,''), COALESCE(entry_hash,'')
FROM journal_entries
WHERE workspace_id = ?
ORDER BY seq ASC`

// VerifyChain walks the KEYED hash-chain for one workspace and reports the
// first broken link, if any. It detects: content mutation (recomputed HMAC ≠
// stored entry_hash), a broken prev_hash pointer (in-place reorder), and an
// UNcheckpointed sequence gap (a malicious mid-chain deletion). A gap that is
// covered by a valid signed compaction checkpoint is bridged, not flagged. An
// empty workspace and a well-formed chain both return OK.
//
// The key is derived from the process ENCRYPTION_KEY (ChainKeyFromEnv), the
// same value the emit path and migration used, so a legitimate chain verifies
// clean while a DB-write attacker who lacks the key cannot forge either an
// entry_hash or a checkpoint.
func VerifyChain(ctx context.Context, db *sql.DB, workspaceID string) (*VerifyResult, error) {
	key := ChainKeyFromEnv()
	res := &VerifyResult{WorkspaceID: workspaceID, OK: true}

	removed, applied, err := loadCheckpointedRemovals(ctx, db, key, workspaceID)
	if err != nil {
		return nil, err
	}
	res.Checkpoints = applied

	rows, err := db.QueryContext(ctx, verifySelect, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("journal: verify query: %w", err)
	}
	defer rows.Close()

	expectedPrev := GenesisPrevHash
	var expectedSeq int64 = 1

	for rows.Next() {
		var f ChainFields
		var prevHash, entryHash string
		if err := rows.Scan(
			&f.Seq, &f.ID, &f.Workspace,
			&f.CrewID, &f.AgentID, &f.MissionID,
			&f.TS, &f.EntryType, &f.Severity, &f.Priority, &f.ActorType,
			&f.ActorID, &f.Summary, &f.Payload, &f.Refs,
			&f.TraceID, &f.SpanID, &f.ExpiresAt,
			&prevHash, &entryHash,
		); err != nil {
			return nil, fmt.Errorf("journal: verify scan: %w", err)
		}

		// A row whose seq is BELOW what we expect (duplicate or reorder that
		// slipped past the unique index) is unambiguous tampering.
		if f.Seq < expectedSeq {
			res.OK = false
			res.BrokenSeq = f.Seq
			res.BrokenID = f.ID
			res.Reason = fmt.Sprintf("sequence disorder at seq %d: expected seq >= %d", f.Seq, expectedSeq)
			return res, nil
		}

		// Bridge any gap between expectedSeq and this row using signed
		// checkpoints. Each missing seq MUST be covered by a valid checkpoint;
		// walking them in order advances expectedPrev to the last removed
		// entry's hash, so the surviving row's prev_hash can still be checked.
		// An uncovered missing seq is a malicious mid-chain delete.
		for expectedSeq < f.Seq {
			h, ok := removed[expectedSeq]
			if !ok {
				res.OK = false
				res.BrokenSeq = f.Seq
				res.BrokenID = f.ID
				res.Reason = fmt.Sprintf("sequence gap: expected seq %d, found %d (no signed compaction checkpoint covers the missing row)", expectedSeq, f.Seq)
				return res, nil
			}
			expectedPrev = h
			expectedSeq++
		}

		res.Count++

		// Chain linkage: this entry must point at the prior (live or
		// checkpoint-bridged) entry's hash.
		if prevHash != expectedPrev {
			res.OK = false
			res.BrokenSeq = f.Seq
			res.BrokenID = f.ID
			res.Reason = fmt.Sprintf("broken chain link at seq %d: prev_hash does not match preceding entry", f.Seq)
			return res, nil
		}

		// Content integrity: recompute the KEYED hash and compare.
		want := ChainHashKeyed(key, prevHash, f)
		if want != entryHash {
			res.OK = false
			res.BrokenSeq = f.Seq
			res.BrokenID = f.ID
			res.Reason = fmt.Sprintf("content hash mismatch at seq %d: entry was modified after write (or the chain key differs)", f.Seq)
			return res, nil
		}

		expectedPrev = entryHash
		expectedSeq++
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("journal: verify iterate: %w", err)
	}
	return res, nil
}
