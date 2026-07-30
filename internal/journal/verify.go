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
	"strings"
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

	// Breaks lists EVERY per-row integrity failure found, not just the first.
	//
	// Halting at the first one was actively harmful: on stage a single legacy
	// row (pre-v166, pinned before the migration backfilled priority_at_emit,
	// so its emit-time value is gone and its hash can never be recomputed)
	// stopped the walk at seq 86657 — leaving the ~86k entries written after it
	// completely unchecked. One unrepairable row must not blind the operator to
	// real tampering that follows it.
	//
	// BrokenSeq/BrokenID/Reason are retained and still describe the FIRST
	// break, so existing callers and the CLI's exit code are unchanged.
	//
	// CAPPED at maxReportedBreaks. If the chain key differs — a rotated or
	// unset ENCRYPTION_KEY — then EVERY row mismatches, and an unbounded list
	// would answer an admin request with one entry per journal row (tens of
	// megabytes on stage's 86k-entry journal). BreakCount stays exact so the
	// true scale is never hidden by the trim.
	Breaks []ChainBreak `json:"breaks,omitempty"`

	// Repairable lists rows the v166 backfill corrupted but whose content is
	// PROVABLY authentic — see recoverEmitPriority. They are not breaks; they
	// are a wrong value in a column, and EmitPriority is what to write back.
	// Capped exactly like Breaks, and for exactly the same reason: a workspace
	// where the migration touched many rows would otherwise return one item per
	// recovered row on an admin request. The count survives the trim.
	Repairable          []RepairableEntry `json:"repairable,omitempty"`
	RepairableCount     int               `json:"repairable_count,omitempty"`
	RepairableTruncated bool              `json:"repairable_truncated,omitempty"`
	BreakCount          int               `json:"break_count,omitempty"`      // total breaks found (>= len(Breaks))
	BreaksTruncated     bool              `json:"breaks_truncated,omitempty"` // list trimmed; see BreakCount
}

// maxReportedBreaks bounds the per-row breaks carried in a VerifyResult. A
// hundred is far more than an operator will read and enough to tell "one
// legacy row" from "the whole chain is unverifiable" — which is the only
// distinction that changes what they do next.
const maxReportedBreaks = 100

// RepairableEntry is a row whose stored priority_at_emit is wrong but whose
// emit-time value was recovered, proving the entry itself is untouched.
type RepairableEntry struct {
	Seq            int64  `json:"seq"`
	ID             string `json:"id"`
	StoredPriority string `json:"stored_priority"` // what the backfill wrote
	EmitPriority   string `json:"emit_priority"`   // what the hash proves it was
}

// priorityDomain is the complete set of values the column can hold
// (journal_handler.go validates exactly these). Small enough to search.
var priorityDomain = []string{"normal", "high", "pin", "permanent"}

// recoverEmitPriority finds the emit-time priority that reproduces storedHash,
// or "" when none does.
//
// WHY THIS IS SOUND, and not a loosening of the oracle: the hash is an
// HMAC-SHA256 under a per-installation secret chain key. Producing content that
// hashes correctly under ANY candidate priority requires the key. An attacker
// who could do that for one of four values could already do it for the real
// one, so the search removes a false positive without removing a real
// detection. What it buys is the difference between "this row is unverifiable
// forever" and "this row is authentic and one column needs fixing".
func recoverEmitPriority(key []byte, prevHash string, f ChainFields, storedHash string) string {
	for _, candidate := range priorityDomain {
		if candidate == f.Priority {
			continue // already tried by the caller
		}
		probe := f
		probe.Priority = candidate
		if ChainHashKeyed(key, prevHash, probe) == storedHash {
			return candidate
		}
	}
	return ""
}

// ChainBreak is one row that failed its integrity check.
type ChainBreak struct {
	Seq    int64  `json:"seq"`
	ID     string `json:"id"`
	Kind   string `json:"kind"` // content | priority
	Reason string `json:"reason"`
}

// note records a per-row break and keeps the legacy first-break fields
// pointing at the earliest one.
func (r *VerifyResult) note(seq int64, id, kind, reason string) {
	r.OK = false
	r.BreakCount++
	if len(r.Breaks) < maxReportedBreaks {
		r.Breaks = append(r.Breaks, ChainBreak{Seq: seq, ID: id, Kind: kind, Reason: reason})
	} else {
		r.BreaksTruncated = true
	}
	if r.BrokenSeq == 0 {
		r.BrokenSeq = seq
		r.BrokenID = id
		r.Reason = reason
	}
}

// verifyColumns is the projection the hash commits to, plus the two columns the
// checks need alongside it (the stored hashes and the LIVE priority). Nullable
// columns are COALESCEd to ” to match how the emit path framed them.
//
// #1369: the hashed priority comes from priority_at_emit, NOT from the mutable
// `priority` column. `priority` is legitimately edited in place by the
// operator-facing pin/permanent control, and hashing it made every such edit a
// permanent false "tampered" verdict. Both are selected: the immutable one feeds
// the hash, the live one feeds the reconciliation check below.
//
// The `%s` placeholder is the expression yielding the HASHED priority; it is
// filled in by hashedPriorityExpr, which picks the right one for the schema on
// disk so a row written by an older binary still verifies against the value its
// stored hash was actually computed over.
//
// Shared by the chain walk and the per-row deletion fence so the two can never
// drift onto different projections — a fence that hashed a different set of
// columns than the verifier would answer a different question than the one it
// is asked.
const verifyColumns = `
SELECT seq, id, workspace_id,
       COALESCE(crew_id,''), COALESCE(agent_id,''), COALESCE(mission_id,''),
       ts, entry_type, severity,
       %s, actor_type,
       COALESCE(actor_id,''), summary, payload, refs,
       COALESCE(trace_id,''), COALESCE(span_id,''), COALESCE(expires_at,''),
       COALESCE(prev_hash,''), COALESCE(entry_hash,''),
       COALESCE(priority,'normal')
FROM journal_entries
`

// verifySelect walks one workspace's whole chain, in seq order.
const verifySelect = verifyColumns + `
WHERE workspace_id = ?
ORDER BY seq ASC`

// verifySelectByID checks a named set of rows. The second %s is the id
// placeholder list (see UnresolvedIntegrity), so Sprintf takes the priority
// expression first and the placeholders second.
const verifySelectByID = verifyColumns + `
WHERE workspace_id = ? AND id IN (%s)`

// hashedPriorityExpr picks the hashed-priority expression for the schema on disk.
//
// On v166+ the chain commits to the immutable priority_at_emit. On an older
// schema that column does not exist and every stored hash was computed over the
// live `priority`, so that is what must be fed back in — otherwise verification
// would report a mid-upgrade DB as universally tampered. The probe is one cheap
// pragma read per verify, which is negligible next to walking the chain.
// The bool reports whether the schema has the IMMUTABLE priority_at_emit
// column. It gates the backfill recovery: only when the hash is taken over a
// column the operator-facing pin cannot touch is a content mismatch guaranteed
// not to be a live priority flip.
func hashedPriorityExpr(ctx context.Context, db *sql.DB) (string, bool, error) {
	var present int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pragma_table_info('journal_entries') WHERE name = 'priority_at_emit'`,
	).Scan(&present); err != nil {
		return "", false, fmt.Errorf("journal: probe priority_at_emit: %w", err)
	}
	if present == 0 {
		return `COALESCE(priority,'normal')`, false, nil
	}
	return `COALESCE(priority_at_emit, priority, 'normal')`, true, nil
}

// verifySelectFor returns the full-chain query for the schema on disk.
func verifySelectFor(ctx context.Context, db *sql.DB) (string, bool, error) {
	expr, hasEmitColumn, err := hashedPriorityExpr(ctx, db)
	if err != nil {
		return "", false, err
	}
	return fmt.Sprintf(verifySelect, expr), hasEmitColumn, nil
}

// scanChainRow reads one row of the verifyColumns projection. Both the chain
// walk and the deletion fence go through it, so the column order lives in
// exactly one place.
func scanChainRow(rows *sql.Rows) (f ChainFields, prevHash, entryHash, livePriority string, err error) {
	err = rows.Scan(
		&f.Seq, &f.ID, &f.Workspace,
		&f.CrewID, &f.AgentID, &f.MissionID,
		&f.TS, &f.EntryType, &f.Severity, &f.Priority, &f.ActorType,
		&f.ActorID, &f.Summary, &f.Payload, &f.Refs,
		&f.TraceID, &f.SpanID, &f.ExpiresAt,
		&prevHash, &entryHash, &livePriority,
	)
	return f, prevHash, entryHash, livePriority, err
}

// priorityLedgerSQL loads the append-only chain of operator priority edits for a
// workspace, in (entry, seq) order. Used to reconcile each row's LIVE priority
// against the immutable priority_at_emit: the live value must be reachable from
// the emit-time value by following the recorded changes.
const priorityLedgerSQL = `
SELECT entry_id, seq, previous_priority, priority
FROM journal_entry_priorities
WHERE workspace_id = ?
ORDER BY entry_id ASC, seq ASC`

// priorityEdit is one recorded change in the ledger.
type priorityEdit struct {
	Seq      int64
	Previous string
	Next     string
}

// loadPriorityLedger groups the recorded edits by entry id.
//
// A DB that predates migration v166 has no such table. That is not a verification
// failure: on such a schema priority_at_emit is also absent, verifySelect's
// COALESCE falls back to the live `priority` (the value those rows' hashes were
// actually computed over), and reconciliation trivially holds. Verification must
// keep working across an upgrade boundary rather than erroring out — a verifier
// that refuses to run is a verifier nobody trusts.
func loadPriorityLedger(ctx context.Context, db *sql.DB, workspaceID string) (map[string][]priorityEdit, error) {
	var present int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='journal_entry_priorities'`,
	).Scan(&present); err != nil {
		return nil, fmt.Errorf("journal: probe priority ledger: %w", err)
	}
	if present == 0 {
		return map[string][]priorityEdit{}, nil
	}

	rows, err := db.QueryContext(ctx, priorityLedgerSQL, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("journal: load priority ledger: %w", err)
	}
	defer rows.Close()
	out := map[string][]priorityEdit{}
	for rows.Next() {
		var id string
		var e priorityEdit
		if err := rows.Scan(&id, &e.Seq, &e.Previous, &e.Next); err != nil {
			return nil, fmt.Errorf("journal: scan priority ledger: %w", err)
		}
		out[id] = append(out[id], e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("journal: iterate priority ledger: %w", err)
	}
	return out, nil
}

// reconcilePriority reports whether livePriority is explained by atEmit plus the
// recorded chain of edits.
//
// With no edits the live value must still equal the emit-time value. With edits,
// each one must start where the previous left off (beginning at atEmit) and the
// last one must land on the live value. That makes two distinct attacks visible:
//
//   - a silent column flip, which leaves no ledger row at all; and
//   - a fabricated ledger row that does not chain back to the emit-time value.
//
// It deliberately does NOT try to authenticate the ledger row itself with a MAC.
// An attacker with DB write can append a fully consistent chain of fake edits, so
// this check bounds the forgery to "must look like a plausible sequence of
// operator actions" rather than making it impossible. The reason it is still worth
// having: the honest path is now verifiable (no false positives), and every real
// edit also emits a `memory.priority_changed` entry INTO the keyed chain, so a
// forged ledger with no corresponding chained entry is detectable by comparing the
// two — see docs/security/audit.mdx for what is and is not guaranteed.
func reconcilePriority(atEmit, livePriority string, edits []priorityEdit) bool {
	if len(edits) == 0 {
		return livePriority == atEmit
	}
	cur := atEmit
	for _, e := range edits {
		if e.Previous != cur {
			return false
		}
		cur = e.Next
	}
	return cur == livePriority
}

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

	// #1369: the recorded operator priority edits, so a row whose live priority
	// differs from the hashed emit-time value can be told apart from a silent
	// DB-level flip.
	priorityEdits, err := loadPriorityLedger(ctx, db, workspaceID)
	if err != nil {
		return nil, err
	}

	selectSQL, hasEmitColumn, err := verifySelectFor(ctx, db)
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, selectSQL, workspaceID)
	if err != nil {
		return nil, fmt.Errorf("journal: verify query: %w", err)
	}
	defer rows.Close()

	expectedPrev := GenesisPrevHash
	var expectedSeq int64 = 1

	for rows.Next() {
		f, prevHash, entryHash, livePriority, err := scanChainRow(rows)
		if err != nil {
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
		// A content mismatch is a fact about THIS row. Rows after it chain onto
		// its STORED hash, which is unaffected, so the walk continues and later
		// tampering stays reachable.
		want := ChainHashKeyed(key, prevHash, f)
		hashedPriority := f.Priority
		recovered := ""
		if want != entryHash {
			// Before calling it tampering, try the one benign explanation we
			// know of: the v166 backfill overwrote priority_at_emit with an
			// already-edited value. If some candidate priority reproduces the
			// stored hash, the entry's CONTENT is authentic and the column is
			// wrong. That is all the search establishes — see below for what it
			// deliberately does NOT establish.
			//
			// TWO gates, both still load-bearing:
			//
			//  1. The schema must hash the IMMUTABLE priority_at_emit. Before
			//     v166 the hash covered the LIVE priority, so a silent in-place
			//     flip changes it, and searching the domain there would launder
			//     exactly the attack reconcilePriority exists to catch. (Caught
			//     by TestVerifyChain_SilentPriorityFlipDetected.)
			//
			//  2. The stored emit value must EQUAL the live one — the v166
			//     backfill ran `priority_at_emit = COALESCE(priority,'normal')`,
			//     so a row it damaged necessarily has the two columns identical.
			//     An attacker who edits priority_at_emit ALONE leaves them
			//     different, and that stays an unexplained content break rather
			//     than a recovered row.
			//
			// What #1572 changed is not these gates but what passing them BUYS.
			// Matching the fingerprint is not evidence of a migration — an
			// attacker who writes both columns reproduces it byte for byte — so
			// it can no longer suppress the live-priority check below. It buys
			// one thing: the recovered emit-time value.
			if hasEmitColumn && f.Priority == livePriority {
				recovered = recoverEmitPriority(key, prevHash, f, entryHash)
			}
			if recovered != "" {
				res.RepairableCount++
				if len(res.Repairable) < maxReportedBreaks {
					res.Repairable = append(res.Repairable, RepairableEntry{
						Seq: f.Seq, ID: f.ID, StoredPriority: f.Priority, EmitPriority: recovered,
					})
				} else {
					res.RepairableTruncated = true
				}
				hashedPriority = recovered
			} else {
				res.note(f.Seq, f.ID, "content",
					fmt.Sprintf("content hash mismatch at seq %d: entry was modified after write (or the chain key differs)", f.Seq))
			}
		}

		// Priority reconciliation (#1369). `priority` is outside the hash because
		// it is legitimately mutable, so it gets its own check: the live value must
		// be reachable from the hashed emit-time value through the append-only
		// ledger of operator edits. A raw column flip leaves no ledger row and is
		// caught here.
		//
		// #1572: this check used to be SKIPPED for a recovered row with no ledger
		// rows, on the theory that such a row was pinned before v166 created the
		// ledger and therefore had nothing to reconcile against. The theory was
		// sound and the conclusion was still wrong, because the premise is not
		// observable: a pre-v166 pin and an attacker's two-column write leave
		// byte-identical state, so "this row predates the ledger" was an
		// assumption the attacker got to make on our behalf — and it switched off
		// the one check standing between a `permanent` entry and compaction.
		//
		// The honest answer is that the provenance is UNRESOLVED, and unresolved
		// reads as a break. Recovery keeps its real value (it proves the content
		// authentic and names the emit-time priority, which is the material any
		// repair would need) but it no longer excuses the live column. The cost
		// is that a genuine pre-v166 pin now reports red — a row which, before
		// v166 moved the hash off the mutable column, was a hard content break
		// anyway. Reporting a real ambiguity is not a regression; silently
		// resolving it in the attacker's favour was.
		if !reconcilePriority(hashedPriority, livePriority, priorityEdits[f.ID]) {
			if recovered != "" {
				res.note(f.Seq, f.ID, "priority", fmt.Sprintf(
					"priority provenance unresolved at seq %d: the keyed hash proves the entry was emitted %q, "+
						"the stored priority_at_emit says %q, and no recorded operator edit explains the live %q — "+
						"a v166 backfill artefact and a deliberate downgrade are indistinguishable here",
					f.Seq, recovered, f.Priority, livePriority))
			} else {
				res.note(f.Seq, f.ID, "priority", fmt.Sprintf(
					"priority mismatch at seq %d: live priority %q is not reachable from the emit-time %q through the recorded priority changes",
					f.Seq, livePriority, hashedPriority))
			}
		}

		expectedPrev = entryHash
		expectedSeq++
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("journal: verify iterate: %w", err)
	}
	return res, nil
}

// UnresolvedIntegrity reports which of the given entries must NOT be destroyed,
// mapped to the reason. Ids absent from the returned map are cleared for
// deletion.
//
// This is the deletion-side half of #1572, and it deliberately answers a
// NARROWER question than VerifyChain: not "is this chain intact" but "may this
// specific row be destroyed". The distinction matters because the two are read
// by different actors. VerifyChain is read by an operator, occasionally, on
// purpose. Compaction runs on a timer and reads nothing — so a verdict that
// only ever reaches a human is no defence against a deletion that is about to
// happen automatically, and to be signed by a valid checkpoint on its way out,
// leaving a chain that verifies clean forever after.
//
// A row is refused when:
//
//   - its stored keyed hash does not reproduce (tampered — or repairable, whose
//     CONTENT is proven but whose priority column is precisely what is in
//     doubt, and priority is what compaction reads);
//   - it is in the chain (seq > 0) but carries no stored hash, so there is no
//     evidence to check rather than evidence that checks out;
//   - its live priority is not reachable from the emit-time value through the
//     recorded operator edits; or
//   - it was PERMANENT AT EMIT. That last one holds even when the chain
//     verifies clean, and it is the rule meant to survive the next refactor:
//     `permanent` at emit is a keyed, unforgeable fact, while the un-pin that
//     moves the live column is a row in journal_entry_priorities that anyone
//     with DB write can append (see reconcilePriority — the ledger is
//     deliberately not MAC-authenticated). An unauthenticated claim may move a
//     label; it may not authorise destroying the record the label protects. An
//     operator who genuinely wants such an entry gone can delete it explicitly.
//     What must not happen is a timer doing it for them on forgeable evidence.
//
// Rows with seq <= 0 predate the hash-chain (v152) and make no tamper-evidence
// claim at all — WriteChainCheckpoint already refuses to attest them. They are
// cleared, because turning "never chained" into "undeletable forever" would
// make every pre-v152 journal grow without bound.
func UnresolvedIntegrity(ctx context.Context, db *sql.DB, workspaceID string, ids []string) (map[string]string, error) {
	blocked := make(map[string]string)
	if len(ids) == 0 {
		return blocked, nil
	}

	key := ChainKeyFromEnv()
	priorityEdits, err := loadPriorityLedger(ctx, db, workspaceID)
	if err != nil {
		return nil, err
	}
	expr, hasEmitColumn, err := hashedPriorityExpr(ctx, db)
	if err != nil {
		return nil, err
	}

	// Chunked well under SQLite's 999-parameter ceiling (one slot goes to the
	// workspace id), matching how the compactor chunks its own statements.
	const chunk = 400
	for start := 0; start < len(ids); start += chunk {
		end := start + chunk
		if end > len(ids) {
			end = len(ids)
		}
		batch := ids[start:end]
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(batch)), ",")
		args := make([]any, 0, len(batch)+1)
		args = append(args, workspaceID)
		for _, id := range batch {
			args = append(args, id)
		}

		rows, err := db.QueryContext(ctx, fmt.Sprintf(verifySelectByID, expr, placeholders), args...)
		if err != nil {
			return nil, fmt.Errorf("journal: integrity fence query: %w", err)
		}
		err = func() error {
			defer rows.Close()
			for rows.Next() {
				f, prevHash, entryHash, livePriority, err := scanChainRow(rows)
				if err != nil {
					return fmt.Errorf("journal: integrity fence scan: %w", err)
				}
				if reason := unresolvedReason(
					key, hasEmitColumn, f, prevHash, entryHash, livePriority, priorityEdits[f.ID],
				); reason != "" {
					blocked[f.ID] = reason
				}
			}
			return rows.Err()
		}()
		if err != nil {
			return nil, err
		}
	}
	return blocked, nil
}

// unresolvedReason is the per-row half of UnresolvedIntegrity: why this row may
// not be destroyed, or "" when it may.
func unresolvedReason(
	key []byte, hasEmitColumn bool,
	f ChainFields, prevHash, entryHash, livePriority string,
	edits []priorityEdit,
) string {
	if f.Seq <= 0 {
		// Not part of the verified chain: nothing is claimed about it, so
		// nothing is contradicted. Same rule WriteChainCheckpoint applies.
		return ""
	}
	if entryHash == "" {
		// Belt to the hash comparison's braces: an empty stored hash can never
		// equal an HMAC, so the next check would refuse this row anyway. Stated
		// separately because the REASON matters to whoever reads it, and because
		// "no hash stored" is exactly the shape a future "nothing to check, skip
		// it" shortcut would be written to skip.
		return "chained row (seq > 0) carries no entry_hash: there is no evidence to check"
	}

	if ChainHashKeyed(key, prevHash, f) != entryHash {
		recovered := ""
		if hasEmitColumn && f.Priority == livePriority {
			recovered = recoverEmitPriority(key, prevHash, f, entryHash)
		}
		if recovered == "" {
			return "content hash mismatch: the row was modified after write (or the chain key differs)"
		}
		return fmt.Sprintf(
			"priority provenance unresolved: the keyed hash proves it was emitted %q, stored priority_at_emit is %q, live is %q",
			recovered, f.Priority, livePriority)
	}

	// From here the hash reproduces, so f.Priority IS the emit-time value.
	emitPriority := f.Priority
	if !reconcilePriority(emitPriority, livePriority, edits) {
		return fmt.Sprintf(
			"live priority %q is not reachable from the emit-time %q through the recorded operator edits",
			livePriority, emitPriority)
	}
	if emitPriority == string(PriorityPermanent) {
		return "entry was permanent at emit: the keyed hash pins that, and an un-pin recorded in the " +
			"(unauthenticated) priority ledger does not authorise deleting it"
	}
	return ""
}
