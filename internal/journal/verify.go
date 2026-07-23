package journal

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"fmt"
)

// The journal hash-chain makes the audit trail tamper-evident.
//
// Every entry commits to (a) its own immutable content and (b) the
// entry_hash of the entry immediately preceding it in the SAME workspace.
// That linkage means any after-the-fact edit, deletion of a middle row, or
// in-place reordering breaks the chain and is detected by VerifyChain.
//
// Ordering is by a per-workspace monotonic `seq` (assigned at emit), NOT by
// the random `id` PK or the wall-clock `ts` (which can collide). seq gives a
// deterministic order and makes a deleted middle row show up as a gap.
//
// Not covered by the chain alone (deferred — needs signed checkpoints):
// truncation of the TAIL (deleting the newest N entries) leaves a shorter but
// still internally-consistent chain. A signed high-water checkpoint anchors
// the tip so truncation is caught; that is a follow-up (see issue #1369).

// GenesisPrevHash is the prev_hash of the first entry in a workspace chain.
// The empty string is deliberate: it is length-framed like any other field,
// so genesis is unambiguous and needs no sentinel value.
const GenesisPrevHash = ""

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

// ChainHash computes the deterministic content hash for an entry given the
// prev_hash it chains onto. Serialization is length-framed (an 8-byte
// big-endian length prefix before each field) so no field value can be
// confused with a delimiter or spill into its neighbour — "ab"+"c" and
// "a"+"bc" hash differently. It never depends on map iteration order: payload
// and refs are pre-serialized to their stored JSON strings (encoding/json
// already emits map keys sorted) and hashed as opaque bytes.
func ChainHash(prevHash string, f ChainFields) string {
	h := sha256.New()
	var seqb [8]byte
	binary.BigEndian.PutUint64(seqb[:], uint64(f.Seq))
	h.Write(seqb[:])
	writeFramed := func(s string) {
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], uint64(len(s)))
		h.Write(n[:])
		h.Write([]byte(s))
	}
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
		writeFramed(field)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// VerifyResult reports the outcome of walking one workspace's chain.
type VerifyResult struct {
	WorkspaceID string `json:"workspace_id"`
	OK          bool   `json:"ok"`
	Count       int    `json:"count"`                // entries walked
	BrokenSeq   int64  `json:"broken_seq,omitempty"` // seq of first bad entry (0 when OK)
	BrokenID    string `json:"broken_id,omitempty"`  // id of first bad entry
	Reason      string `json:"reason,omitempty"`     // human-readable failure cause
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

// VerifyChain walks the hash-chain for one workspace and reports the first
// broken link, if any. It detects: content mutation (recomputed hash ≠
// stored entry_hash), a broken prev_hash pointer (in-place reorder), and a
// sequence gap (a deleted or missing middle row). An empty workspace and a
// well-formed chain both return OK.
func VerifyChain(ctx context.Context, db *sql.DB, workspaceID string) (*VerifyResult, error) {
	res := &VerifyResult{WorkspaceID: workspaceID, OK: true}

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
		res.Count++

		// Sequence continuity: a gap means a row was deleted (or seq was
		// rewritten). This is what catches mid-chain deletion.
		if f.Seq != expectedSeq {
			res.OK = false
			res.BrokenSeq = f.Seq
			res.BrokenID = f.ID
			res.Reason = fmt.Sprintf("sequence gap: expected seq %d, found %d", expectedSeq, f.Seq)
			return res, nil
		}

		// Chain linkage: this entry must point at the prior entry's hash.
		if prevHash != expectedPrev {
			res.OK = false
			res.BrokenSeq = f.Seq
			res.BrokenID = f.ID
			res.Reason = fmt.Sprintf("broken chain link at seq %d: prev_hash does not match preceding entry", f.Seq)
			return res, nil
		}

		// Content integrity: recompute and compare.
		want := ChainHash(prevHash, f)
		if want != entryHash {
			res.OK = false
			res.BrokenSeq = f.Seq
			res.BrokenID = f.ID
			res.Reason = fmt.Sprintf("content hash mismatch at seq %d: entry was modified after write", f.Seq)
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
