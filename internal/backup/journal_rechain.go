package backup

// journal_rechain.go — re-sign a FORKED restore's journal hash chain, and say
// so in the journal itself (#2226).
//
// The journal is tamper-evident: every row carries a keyed HMAC
// (journal.ChainHashKeyed) over its own content AND the entry_hash of the row
// before it in the same workspace. Identity columns are inside that HMAC, and
// RemapIDs rewrites them when the admin forks a bundle with --as-workspace /
// --as-crew: `id` in pass 1 unconditionally, and every FK column SQLite
// reports in pass 2 — which on today's schema means `workspace_id`. (crew_id,
// agent_id and mission_id are also hashed; schema v167 deliberately turned
// them into plain TEXT so a deleted crew cannot cascade away its own audit
// trail, so pass 2 no longer reaches them. Two rewritten columns is already
// enough to break every row, and this pass re-signs whatever the row carries,
// so it stays correct if that changes.)
//
// seq, prev_hash and entry_hash rode through the dump untouched, so the stored
// hash still attested to the PRE-remap values and the very first integrity
// check on the new workspace reported every restored row as tampered.
//
// The fix is not to exempt the journal from the remap (a same-instance fork
// would then collide on id and on UNIQUE(workspace_id, seq), and the fork's
// journal would point at the source workspace). It is to treat the fork for
// what it is: a NEW chain, re-signed under this installation's key, whose
// genesis is this restore. That is the same operation the v152 migration
// performs in backfillJournalChain — re-signing a chain we legitimately
// rewrote — and it is done here the same way.
//
// A silently re-signed chain would be worse than a broken one: the new chain
// attests to THIS instance, not to the source, and an operator who is not told
// would read it as unbroken provenance back to the original. So the re-sign
// appends one entry of its own (journal.EntryBackupChainResigned) recording
// what was re-signed, from which bundle, and by whom. A fork deserves a new
// genesis; it must not get one silently.
//
// Failure posture: there is no skip branch. Every error here aborts the
// restore, and it aborts BEFORE RestoreDumpTx opens its transaction, so
// nothing lands. Producing a fork whose chain cannot be verified is the one
// outcome this file exists to prevent, and returning success while quietly
// leaving the hashes stale is how that outcome happened.

import (
	"context"
	"crypto/hmac"
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
)

// journalEntriesTable / journalCheckpointsTable name the two tables this pass
// rewrites. Both are already in BackupTables; naming them here keeps the
// string literals in one place.
const (
	journalEntriesTable     = "journal_entries"
	journalCheckpointsTable = "journal_chain_checkpoints"
)

// journalEntryTSLayout mirrors the layout the emit path writes into
// journal_entries.ts (internal/journal/emit.go persistBatch). The appended
// re-sign entry must be indistinguishable from a row the Writer produced, and
// ts is inside the hash, so the two must agree.
const journalEntryTSLayout = "2006-01-02T15:04:05.000Z"

// resignProvenance is what the appended entry records about the fork: where
// its rows came from and who asked for them.
type resignProvenance struct {
	// SourceWorkspaceID is the workspace id the BUNDLE carried, before
	// RemapIDs replaced it. Empty for a crew-scope bundle whose manifest
	// records no workspace summary.
	SourceWorkspaceID string
	SourceWorkspace   string // source slug, for a human reading the entry
	BundlePath        string
	BundleSHA256      string
	Actor             Actor
	// CheckpointSourceWorkspaces is each journal_chain_checkpoints row's
	// workspace_id AS THE BUNDLE CARRIED IT, by row position, snapshotted
	// before RemapIDs rewrote the column in place — the same
	// pair-by-position trick bundleCrewSlugs uses in runner_restore.go.
	// The stored MAC commits to that id, so it is the only value the
	// checkpoint's signature can be checked against.
	CheckpointSourceWorkspaces []string
}

// journalRechainStats reports what the re-sign touched, so the caller can put
// real numbers in front of the operator instead of "done".
type journalRechainStats struct {
	Workspaces  int
	Entries     int
	Checkpoints int
	// CheckpointsUnverified counts compaction checkpoints carried over
	// WITHOUT being re-signed, because their stored MAC did not validate
	// under the workspace they came from. See resignCheckpoints.
	CheckpointsUnverified int
}

// rechainForkedJournal re-signs the journal rows of a remapped dump.
//
// Call it AFTER RemapIDs and BEFORE the dump is inserted: it works on the
// in-memory rows, so the re-signed hashes land in the same transaction as the
// data they attest to, and a failure here costs nothing because no write has
// happened yet.
//
// db is used only to probe the TARGET schema (see hashedPriorityForRow) — the
// re-chain reads and writes the dump, never the database.
func rechainForkedJournal(ctx context.Context, db *sql.DB, dump *DBDump, key []byte, prov resignProvenance) (journalRechainStats, error) {
	var stats journalRechainStats
	if dump == nil {
		return stats, nil
	}
	entries := dump.Tables[journalEntriesTable]
	checkpoints := dump.Tables[journalCheckpointsTable]
	if len(entries) == 0 && len(checkpoints) == 0 {
		return stats, nil
	}
	// The chain key is the whole point of the exercise: a chain re-signed
	// under no key at all is a chain anybody can forge. ChainKeyFromEnv
	// cannot return empty (an unset ENCRYPTION_KEY still derives the
	// documented plaintext-dev-mode key, which is the same key VerifyChain
	// will use in this process), so this guard exists for the caller that
	// passes a key explicitly — and it FAILS the restore rather than
	// falling back to an unkeyed hash or skipping the re-sign.
	if len(key) == 0 {
		return stats, fmt.Errorf("backup: cannot re-sign the forked journal chain: no chain key available; " +
			"refusing to restore a fork whose journal would report as tampered on the next integrity check")
	}

	hasEmitColumn, err := targetHasPriorityAtEmit(ctx, db)
	if err != nil {
		return stats, err
	}

	// 1. Re-MAC the compaction checkpoints under the workspace they now
	//    belong to. RemapIDs has already re-pointed workspace_id (see
	//    virtualForeignKeys), but the column is FRAMED INTO the MAC by
	//    journal.CheckpointMAC, so moving the row without re-signing it
	//    yields a checkpoint that validates for nobody and covers nothing.
	removedByWorkspace, checkpointsByWorkspace, unverified, err := resignCheckpoints(checkpoints, key, prov.CheckpointSourceWorkspaces)
	if err != nil {
		return stats, err
	}
	stats.Checkpoints = len(checkpoints) - unverified
	stats.CheckpointsUnverified = unverified

	// 2. Re-chain the entries, workspace by workspace, in seq order.
	touched, err := resignEntries(entries, key, removedByWorkspace, checkpointsByWorkspace, hasEmitColumn)
	if err != nil {
		return stats, err
	}

	// 3. Append the entry that says a re-sign happened. One per workspace
	//    whose chain this pass actually rewrote.
	order := make([]string, 0, len(touched))
	for ws := range touched {
		order = append(order, ws)
	}
	sort.Strings(order)
	now := time.Now().UTC()
	for _, ws := range order {
		tail := touched[ws]
		stats.Workspaces++
		stats.Entries += tail.entries
		row, err := resignNoticeRow(ws, tail, key, hasEmitColumn, now, prov)
		if err != nil {
			return stats, err
		}
		dump.Tables[journalEntriesTable] = append(dump.Tables[journalEntriesTable], row)
	}
	return stats, nil
}

// chainTail is the state of one workspace's chain after the walk: what the
// next entry must chain onto, and how much was re-signed.
type chainTail struct {
	prevHash string
	nextSeq  int64
	entries  int
	// checkpoints counts the compaction checkpoint ROWS re-signed into this
	// workspace, so the notice entry can report them.
	checkpoints int
}

// resignCheckpoints re-signs every compaction checkpoint in the dump under the
// workspace id it now carries, and returns the (workspace -> seq -> removed
// entry_hash) index the entry walk needs to bridge compacted gaps.
//
// The removed hashes themselves are NOT recomputed and must not be: the rows
// they describe were legitimately deleted, their content is gone, and there is
// nothing left to hash. VerifyChain treats a checkpointed hash as an opaque
// bridge value — the surviving row after the gap must carry it as its
// prev_hash — so carrying the source's value through verbatim, and re-signing
// only the MAC that binds the set to a workspace, is both sufficient and the
// only thing that can be done.
func resignCheckpoints(rows []map[string]any, key []byte, sourceWorkspaces []string) (map[string]map[int64]string, map[string]int, int, error) {
	out := map[string]map[int64]string{}
	counts := map[string]int{}
	unverified := 0
	for i, row := range rows {
		ws := rowString(row, "workspace_id")
		if ws == "" {
			return nil, nil, 0, fmt.Errorf("backup: journal chain checkpoint %q carries no workspace_id; refusing to re-sign a checkpoint that belongs to no chain",
				rowString(row, "id"))
		}
		blob := rowString(row, "removed_json")
		var removed []journal.RemovedEntry
		if err := json.Unmarshal([]byte(blob), &removed); err != nil {
			// A checkpoint whose body cannot be read covers nothing:
			// VerifyChain would skip it and report the gap it was
			// meant to bridge as a mid-chain delete. Fail here rather
			// than hand the operator a fork that reads as tampered.
			return nil, nil, 0, fmt.Errorf("backup: journal chain checkpoint %s: unreadable removed_json: %w",
				rowString(row, "id"), err)
		}
		// Only re-sign a checkpoint that was ALREADY valid where it came
		// from. Re-MACing unconditionally would launder a forged
		// checkpoint: an attacker with write access to the source could
		// plant one covering a mid-chain delete, watch VerifyChain reject
		// it there, and have a forked restore hand it back with a
		// signature this installation vouches for. A checkpoint whose
		// stored MAC does not validate under the workspace it came from
		// is carried across untouched — inert in the fork exactly as it
		// was inert in the source, and still visible as evidence.
		sourceWS := ""
		if i < len(sourceWorkspaces) {
			sourceWS = sourceWorkspaces[i]
		}
		if sourceWS == "" || !hmac.Equal(
			[]byte(journal.CheckpointMAC(key, sourceWS, removed)),
			[]byte(rowString(row, "mac")),
		) {
			unverified++
			continue
		}
		row["mac"] = journal.CheckpointMAC(key, ws, removed)
		counts[ws]++
		if out[ws] == nil {
			out[ws] = map[int64]string{}
		}
		for _, r := range removed {
			out[ws][r.Seq] = r.Hash
		}
	}
	return out, counts, unverified, nil
}

// resignEntries recomputes prev_hash/entry_hash for every chained journal row
// in the dump, per workspace, in seq order — the same walk backfillJournalChain
// performs for the v152 migration, on the dump rather than on the table.
//
// Rows with seq <= 0 are UNCHAINED legacy rows (pre-v152, never signed). They
// are left exactly as they are: inventing a hash for them would claim an
// attestation this instance never made, and VerifyChain already reports them
// for what they are. A source workspace that carried such rows forks into one
// that carries them too — no better, and no worse.
func resignEntries(rows []map[string]any, key []byte, removed map[string]map[int64]string, checkpointCounts map[string]int, hasEmitColumn bool) (map[string]*chainTail, error) {
	byWorkspace := map[string][]map[string]any{}
	for _, row := range rows {
		ws := rowString(row, "workspace_id")
		if ws == "" {
			return nil, fmt.Errorf("backup: journal entry %q carries no workspace_id; refusing to re-sign a row that belongs to no chain",
				rowString(row, "id"))
		}
		byWorkspace[ws] = append(byWorkspace[ws], row)
	}
	// A workspace can carry checkpoints with no surviving entries (every
	// chained row compacted away). Its checkpoints were still re-pointed
	// and re-signed, so it gets a notice too.
	for ws := range checkpointCounts {
		if _, ok := byWorkspace[ws]; !ok {
			byWorkspace[ws] = nil
		}
	}

	tails := map[string]*chainTail{}
	for ws, wsRows := range byWorkspace {
		seqs := make([]int64, len(wsRows))
		for i, row := range wsRows {
			seq, err := rowInt64(row, "seq")
			if err != nil {
				return nil, fmt.Errorf("backup: journal entry %s: unreadable seq: %w", rowString(row, "id"), err)
			}
			seqs[i] = seq
		}
		idx := make([]int, len(wsRows))
		for i := range idx {
			idx[i] = i
		}
		sort.SliceStable(idx, func(a, b int) bool { return seqs[idx[a]] < seqs[idx[b]] })

		tail := &chainTail{prevHash: journal.GenesisPrevHash, nextSeq: 1, checkpoints: checkpointCounts[ws]}
		expectedSeq := int64(1)
		for _, i := range idx {
			row, seq := wsRows[i], seqs[i]
			if seq <= 0 {
				continue // unchained legacy row — see the doc comment
			}
			// Walk any compacted gap the way VerifyChain does: each
			// missing seq covered by a checkpoint advances the link to
			// the removed row's recorded hash. An UNCOVERED gap leaves
			// the link where it is, which reproduces exactly the break
			// the source chain already had — the fork inherits the
			// source's integrity, it does not launder it.
			// Bounded by the number of checkpointed removals, not by
			// the size of the gap: a bundle claiming seq 2^62 must not
			// spin here. The first UNCOVERED missing seq stops the walk
			// and leaves the link where it is.
			for expectedSeq < seq {
				h, ok := removed[ws][expectedSeq]
				if !ok {
					break
				}
				tail.prevHash = h
				expectedSeq++
			}

			f := chainFieldsFromRow(row, ws, seq, hasEmitColumn)
			hash := journal.ChainHashKeyed(key, tail.prevHash, f)
			row["prev_hash"] = tail.prevHash
			row["entry_hash"] = hash
			tail.prevHash = hash
			tail.entries++
			expectedSeq = seq + 1
		}
		// A checkpoint can cover seqs BEYOND the last surviving row — the
		// TAIL of the chain was compacted. Walk those too: the notice
		// appended below sits after them, so it must chain onto the last
		// removed row's recorded hash, and it must not reuse a seq the
		// checkpoint already records as removed.
		for {
			h, ok := removed[ws][expectedSeq]
			if !ok {
				break
			}
			tail.prevHash = h
			expectedSeq++
		}
		tail.nextSeq = expectedSeq
		if tail.entries > 0 || tail.checkpoints > 0 {
			tails[ws] = tail
		}
	}
	return tails, nil
}

// chainFieldsFromRow projects a dump row onto the exact field set
// journal.ChainHashKeyed commits to. It MUST mirror verifyColumns in
// internal/journal/verify.go — nullable columns normalized to "", and the
// hashed priority taken from the same expression the verifier uses — or every
// row we re-sign here mismatches when the verifier recomputes it.
func chainFieldsFromRow(row map[string]any, workspaceID string, seq int64, hasEmitColumn bool) journal.ChainFields {
	return journal.ChainFields{
		Seq:       seq,
		ID:        rowString(row, "id"),
		Workspace: workspaceID,
		CrewID:    rowString(row, "crew_id"),
		AgentID:   rowString(row, "agent_id"),
		MissionID: rowString(row, "mission_id"),
		TS:        rowString(row, "ts"),
		EntryType: rowString(row, "entry_type"),
		Severity:  rowString(row, "severity"),
		Priority:  hashedPriorityForRow(row, hasEmitColumn),
		ActorType: rowString(row, "actor_type"),
		ActorID:   rowString(row, "actor_id"),
		Summary:   rowString(row, "summary"),
		Payload:   rowString(row, "payload"),
		Refs:      rowString(row, "refs"),
		TraceID:   rowString(row, "trace_id"),
		SpanID:    rowString(row, "span_id"),
		ExpiresAt: rowString(row, "expires_at"),
	}
}

// hashedPriorityForRow reproduces the verifier's
// COALESCE(priority_at_emit, priority, 'normal') — but only when the TARGET
// schema actually has priority_at_emit. On an older target that column is
// dropped at insert time and the verifier hashes the live `priority`, so
// hashing the bundle's emit-time value here would sign a value the row will
// not carry.
func hashedPriorityForRow(row map[string]any, hasEmitColumn bool) string {
	if hasEmitColumn {
		if v := rowString(row, "priority_at_emit"); v != "" {
			return v
		}
	}
	if v := rowString(row, "priority"); v != "" {
		return v
	}
	return "normal"
}

// targetHasPriorityAtEmit probes the restore target for the v166 column, the
// same probe journal.VerifyChain makes before it walks a chain.
func targetHasPriorityAtEmit(ctx context.Context, db *sql.DB) (bool, error) {
	if db == nil {
		return false, nil
	}
	var present int
	if err := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM pragma_table_info('journal_entries') WHERE name = 'priority_at_emit'`,
	).Scan(&present); err != nil {
		return false, fmt.Errorf("backup: probe journal_entries.priority_at_emit: %w", err)
	}
	return present > 0, nil
}

// resignNoticeRow builds the journal row that records the re-sign, already
// chained onto the tail of the workspace it describes.
//
// It is written as a dump row rather than through journal.Emitter on purpose:
// the notice must land in the SAME transaction as the chain it attests to, so
// a rolled-back restore cannot leave behind a claim that a chain was re-signed
// when its rows never landed — and an emit after commit would be a second
// write that a crash could lose, leaving a silently re-signed fork, which is
// the exact outcome this entry exists to prevent.
func resignNoticeRow(workspaceID string, tail *chainTail, key []byte, hasEmitColumn bool, now time.Time, prov resignProvenance) (map[string]any, error) {
	payload := map[string]any{
		"reason":                "forked restore (--as-workspace/--as-crew) rewrote the ids the journal hash chain commits to",
		"entries_resigned":      tail.entries,
		"checkpoints_resigned":  tail.checkpoints,
		"source_workspace_id":   prov.SourceWorkspaceID,
		"source_workspace_slug": prov.SourceWorkspace,
		"bundle_path":           prov.BundlePath,
		"bundle_sha256":         prov.BundleSHA256,
		"chain_genesis":         "this instance",
	}
	for k, v := range payload {
		if s, ok := v.(string); ok && s == "" {
			delete(payload, k)
		}
	}
	payloadJSON, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("backup: marshal chain re-sign payload: %w", err)
	}
	refs := map[string]any{"workspace_id": workspaceID}
	if prov.SourceWorkspaceID != "" {
		refs["source_workspace_id"] = prov.SourceWorkspaceID
	}
	refsJSON, err := json.Marshal(refs)
	if err != nil {
		return nil, fmt.Errorf("backup: marshal chain re-sign refs: %w", err)
	}

	actorType, actorID := string(journal.ActorSystem), ""
	if prov.Actor.UserID != "" {
		actorType, actorID = string(journal.ActorUser), prov.Actor.UserID
	}

	f := journal.ChainFields{
		Seq:       tail.nextSeq,
		ID:        newRemapCUID(),
		Workspace: workspaceID,
		TS:        now.Format(journalEntryTSLayout),
		EntryType: string(journal.EntryBackupChainResigned),
		Severity:  string(journal.SeverityNotice),
		Priority:  "normal",
		ActorType: actorType,
		ActorID:   actorID,
		Summary:   resignSummary(tail, prov),
		Payload:   string(payloadJSON),
		Refs:      string(refsJSON),
	}
	row := map[string]any{
		"id":           f.ID,
		"workspace_id": f.Workspace,
		"ts":           f.TS,
		"entry_type":   f.EntryType,
		"severity":     f.Severity,
		"priority":     f.Priority,
		"actor_type":   f.ActorType,
		"summary":      f.Summary,
		"payload":      f.Payload,
		"refs":         f.Refs,
		"seq":          f.Seq,
		"prev_hash":    tail.prevHash,
		"entry_hash":   journal.ChainHashKeyed(key, tail.prevHash, f),
	}
	if actorID != "" {
		row["actor_id"] = actorID
	}
	if hasEmitColumn {
		row["priority_at_emit"] = f.Priority
	}
	return row, nil
}

// resignSummary is the one line an operator sees in the timeline.
func resignSummary(tail *chainTail, prov resignProvenance) string {
	from := prov.SourceWorkspace
	if from == "" {
		from = prov.SourceWorkspaceID
	}
	if from == "" {
		from = "a backup bundle"
	}
	if tail.checkpoints > 0 {
		return fmt.Sprintf("journal chain re-signed as a new genesis: %d entries and %d compaction checkpoints forked from %s",
			tail.entries, tail.checkpoints, from)
	}
	return fmt.Sprintf("journal chain re-signed as a new genesis: %d entries forked from %s", tail.entries, from)
}

// rowInt64 reads a dump cell as an integer, the numeric sibling of rowString.
// json.Unmarshal turns every JSON number into a float64, so `seq` arrives as
// float64 on the restore path and as int64 when a caller hands us a dump it
// built in memory; both must work. A missing/NULL seq is 0 — an unchained
// legacy row, not an error.
func rowInt64(row map[string]any, key string) (int64, error) {
	switch n := row[key].(type) {
	case nil:
		return 0, nil
	case int64:
		return n, nil
	case int:
		return int64(n), nil
	case float64:
		return int64(n), nil
	case json.Number:
		return n.Int64()
	case string:
		if n == "" {
			return 0, nil
		}
		return strconv.ParseInt(n, 10, 64)
	default:
		return 0, fmt.Errorf("unsupported value type %T", n)
	}
}
