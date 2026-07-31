package journal

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// Priority provenance (#1572).
//
// The reporter tried ONE shape: `UPDATE journal_entries SET priority='normal',
// priority_at_emit='normal'` on a permanent entry. What made that work was not
// the specific pair of columns, it was the CLASSIFICATION: a row whose stored
// hash could be reproduced under some other priority was filed as "repairable"
// and, on the strength of that, had its live-priority check switched off. Any
// value and any column combination that lands in the same classification buys
// the same suppression.
//
// So these tests attack the classification, not the fixture: every combination
// of emit-time value and written-back value, both orderings against a
// legitimate ledger-recorded edit, and the direct question — can a caller reach
// the repairable state on demand and harvest what it suppresses.

// emitAt emits one chained entry at an explicit priority and flushes.
func emitAt(t *testing.T, w *Writer, ws string, prio Priority, summary string) string {
	t.Helper()
	ctx := context.Background()
	id, err := w.Emit(ctx, Entry{
		WorkspaceID: ws,
		Type:        EntryRunStarted,
		ActorType:   ActorAgent,
		Summary:     summary,
		Priority:    prio,
	})
	if err != nil {
		t.Fatalf("emit %s: %v", summary, err)
	}
	if err := w.Flush(ctx); err != nil {
		t.Fatalf("flush: %v", err)
	}
	return id
}

// TestVerifyChain_PriorityProvenanceMatrix walks the column/value combinations
// an attacker with DB write can produce on a chained entry with NO ledger row.
//
// Every one of them is a live priority the record itself cannot account for, so
// every one of them must be reported. The two that were green before #1572 are
// marked; they are the ones the recovery search used to absolve.
func TestVerifyChain_PriorityProvenanceMatrix(t *testing.T) {
	cases := []struct {
		name string
		// emit is the priority the entry is honestly emitted with — the value
		// the keyed hash commits to and the only one an attacker cannot forge.
		emit Priority
		// live / atEmit are what the attacker writes back. An empty string
		// means "leave the column alone".
		live   string
		atEmit string
	}{
		{
			// The reported hole. Both columns agree, which is exactly the v166
			// backfill fingerprint, and the entry drops out of compaction's
			// `priority != 'permanent'` exemption.
			name: "permanent downgraded to normal, both columns (the reported case)",
			emit: PriorityPermanent, live: "normal", atEmit: "normal",
		},
		{
			// Same channel, a different value: 'high' is equally outside the
			// compaction exemption. Tightening the fix to the literal string
			// 'normal' would leave this open.
			name: "permanent downgraded to high, both columns",
			emit: PriorityPermanent, live: "high", atEmit: "high",
		},
		{
			// 'pin' is outside the exemption too — pinned is not permanent.
			name: "permanent downgraded to pin, both columns",
			emit: PriorityPermanent, live: "pin", atEmit: "pin",
		},
		{
			// A victim that was never permanent. The payload is smaller (pins
			// feed curated memory rather than the compaction exemption) but the
			// laundering channel is identical, and a fix that only guards
			// 'permanent' leaves the mechanism intact for the next attacker.
			name: "pin downgraded to normal, both columns",
			emit: PriorityPin, live: "normal", atEmit: "normal",
		},
		{
			// An UPGRADE through the same channel. Harmless in isolation, but it
			// is the shape a legitimate pre-v166 pin leaves behind, and the
			// verifier cannot tell the two apart — which is the whole point:
			// once it is indistinguishable it must be reported, not absolved.
			name: "normal raised to permanent, both columns (also the honest v166 shape)",
			emit: PriorityNormal, live: "permanent", atEmit: "permanent",
		},
		{
			// Already caught before #1572 (the columns diverge, so the backfill
			// fingerprint does not match). Kept so the fix cannot trade one
			// detection for another.
			name: "live column only",
			emit: PriorityPermanent, live: "normal",
		},
		{
			// Also already caught: no candidate priority reproduces the stored
			// hash once priority_at_emit is the one that moved, so it is a plain
			// content break.
			name: "priority_at_emit column only",
			emit: PriorityPermanent, atEmit: "normal",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ENCRYPTION_KEY", bypassTestKey)
			db := openTestDB(t)
			defer db.Close()
			w := NewWriter(db, quietLogger(), WriterOptions{FlushInterval: time.Hour})
			defer w.Close()
			ctx := context.Background()

			seedChain(t, w, "ws_test", 2)
			victim := emitAt(t, w, "ws_test", tc.emit, "the record under attack")
			seedChain(t, w, "ws_test", 2)

			if res, err := VerifyChain(ctx, db, "ws_test"); err != nil || !res.OK {
				t.Fatalf("baseline chain should verify; err=%v res=%+v", err, res)
			}

			if tc.live != "" {
				if _, err := db.Exec(`UPDATE journal_entries SET priority = ? WHERE id = ?`,
					tc.live, victim); err != nil {
					t.Fatalf("write live priority: %v", err)
				}
			}
			if tc.atEmit != "" {
				if _, err := db.Exec(`UPDATE journal_entries SET priority_at_emit = ? WHERE id = ?`,
					tc.atEmit, victim); err != nil {
					t.Fatalf("write priority_at_emit: %v", err)
				}
			}

			res, err := VerifyChain(ctx, db, "ws_test")
			if err != nil {
				t.Fatalf("VerifyChain: %v", err)
			}
			if res.OK {
				t.Fatalf("a priority write with no ledger row was reported as an intact chain (%+v)", res)
			}
			if res.BrokenID != victim {
				t.Errorf("BrokenID = %q, want the attacked entry %q", res.BrokenID, victim)
			}
			// The walk must not stop at the break: the two entries emitted after
			// the victim still have to be examined.
			if res.Count != 5 {
				t.Errorf("Count = %d, want 5 — one bad row must not blind the rest of the walk", res.Count)
			}
		})
	}
}

// TestVerifyChain_PriorityFlipAroundALegitimateEdit pins BOTH orderings against
// a real, ledger-recorded operator edit. The ledger is a chain of changes, so
// which side of it the attacker writes on decides which link fails — and both
// have to fail. An attacker who can pick the order picks the one that verifies.
func TestVerifyChain_PriorityFlipAroundALegitimateEdit(t *testing.T) {
	// flipFirst decides whether the raw write lands before or after the
	// authorised edit that leaves a ledger row.
	for _, flipFirst := range []bool{true, false} {
		name := "legitimate edit, then the raw flip"
		if flipFirst {
			name = "raw flip, then a legitimate edit on top"
		}
		t.Run(name, func(t *testing.T) {
			t.Setenv("ENCRYPTION_KEY", bypassTestKey)
			db := openTestDB(t)
			defer db.Close()
			w := NewWriter(db, quietLogger(), WriterOptions{FlushInterval: time.Hour})
			defer w.Close()
			ctx := context.Background()

			victim := emitAt(t, w, "ws_test", PriorityPermanent, "permanent record")
			seedChain(t, w, "ws_test", 2)

			// The authorised action, exactly as journal_handler.go records it:
			// the live column moves and the append-only ledger gets the change.
			legit := func(from, to string) {
				t.Helper()
				if _, err := db.Exec(
					`UPDATE journal_entries SET priority = ? WHERE id = ? AND workspace_id = ?`,
					to, victim, "ws_test"); err != nil {
					t.Fatalf("authorised edit: %v", err)
				}
				if _, err := db.Exec(`
					INSERT INTO journal_entry_priorities
						(id, entry_id, workspace_id, seq, previous_priority, priority, reason, set_by, set_at)
					VALUES (?, ?, 'ws_test',
						(SELECT COALESCE(MAX(seq),0)+1 FROM journal_entry_priorities WHERE entry_id = ?),
						?, ?, 'operator action', 'u_admin', ?)`,
					fmt.Sprintf("jep_%s_%s", from, to), victim, victim,
					from, to, time.Now().UTC().Format(time.RFC3339)); err != nil {
					t.Fatalf("ledger row: %v", err)
				}
			}
			// The raw write: both columns set to the same value, no ledger row.
			flip := func(to string) {
				t.Helper()
				if _, err := db.Exec(
					`UPDATE journal_entries SET priority = ?, priority_at_emit = ? WHERE id = ?`,
					to, to, victim); err != nil {
					t.Fatalf("raw flip: %v", err)
				}
			}

			if flipFirst {
				flip("normal")
				// The attacker then waits for (or provokes) a real operator
				// action, so the row ends up carrying a genuine ledger row that
				// starts from the value they planted.
				legit("normal", "high")
			} else {
				legit("permanent", "pin")
				flip("normal")
			}

			res, err := VerifyChain(ctx, db, "ws_test")
			if err != nil {
				t.Fatalf("VerifyChain: %v", err)
			}
			if res.OK {
				t.Fatalf("a raw priority write next to a legitimate edit verified clean (%+v)", res)
			}
			if res.BrokenID != victim {
				t.Errorf("BrokenID = %q, want the attacked entry %q", res.BrokenID, victim)
			}
		})
	}
}

// TestVerifyChain_RepairableIsNotReachableSuppression is the question #1572 is
// really about: not "is this one UPDATE caught" but "can a caller drive the
// system into the repairable state and collect what that state switches off".
//
// The repairable classification is still produced — it carries the recovered
// emit-time value, which is the only material a repair could ever use — but it
// must buy the attacker nothing: the chain reports broken, the row is named,
// and the row is refused by the deletion fence.
func TestVerifyChain_RepairableIsNotReachableSuppression(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", bypassTestKey)
	db := openTestDB(t)
	defer db.Close()
	w := NewWriter(db, quietLogger(), WriterOptions{FlushInterval: time.Hour})
	defer w.Close()
	ctx := context.Background()

	victim := emitAt(t, w, "ws_test", PriorityPermanent, "the record the attacker wants gone")
	seedChain(t, w, "ws_test", 2)

	if _, err := db.Exec(
		`UPDATE journal_entries SET priority = 'normal', priority_at_emit = 'normal' WHERE id = ?`,
		victim); err != nil {
		t.Fatalf("downgrade: %v", err)
	}

	res, err := VerifyChain(ctx, db, "ws_test")
	if err != nil {
		t.Fatalf("VerifyChain: %v", err)
	}
	if res.OK {
		t.Fatal("the repairable classification still suppresses the priority check")
	}

	// The diagnostic survives the reclassification: an operator staring at a
	// real v166 artefact still gets told what the emit-time value was.
	if res.RepairableCount != 1 || len(res.Repairable) != 1 {
		t.Fatalf("Repairable = %+v (count %d), want exactly the one row — the recovered value is the repair material",
			res.Repairable, res.RepairableCount)
	}
	if got := res.Repairable[0]; got.ID != victim || got.EmitPriority != "permanent" || got.StoredPriority != "normal" {
		t.Errorf("Repairable[0] = %+v, want the victim recovered as permanent over a stored 'normal'", got)
	}

	// And it is a break, on the row itself, named as a priority failure rather
	// than filed as an unexplained content mismatch.
	if len(res.Breaks) != 1 {
		t.Fatalf("Breaks = %+v, want exactly one", res.Breaks)
	}
	if res.Breaks[0].ID != victim || res.Breaks[0].Kind != "priority" {
		t.Errorf("Breaks[0] = %+v, want a priority break on %s", res.Breaks[0], victim)
	}

	// The other half of the harvest: even with verification reporting red, the
	// deletion fence must independently refuse the row, because compaction runs
	// on a timer and nobody reads the verifier first.
	blocked, err := UnresolvedIntegrity(ctx, db, "ws_test", []string{victim})
	if err != nil {
		t.Fatalf("UnresolvedIntegrity: %v", err)
	}
	if _, ok := blocked[victim]; !ok {
		t.Errorf("the downgraded row is not fenced off from deletion: %+v", blocked)
	}
}

// TestUnresolvedIntegrity_FencesWhatMustNotBeDeleted covers the fence directly,
// including the cases VerifyChain reports green.
//
// The fence answers a narrower question than VerifyChain: not "is this chain
// intact" but "may this specific row be destroyed". A row that was emitted
// permanent may not, whatever the live column says and whatever the (not
// MAC-authenticated, see verify.go) ledger claims — un-pinning is an operator
// convenience, and it cannot double as authorisation to delete the record.
func TestUnresolvedIntegrity_FencesWhatMustNotBeDeleted(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", bypassTestKey)
	db := openTestDB(t)
	defer db.Close()
	w := NewWriter(db, quietLogger(), WriterOptions{FlushInterval: time.Hour})
	defer w.Close()
	ctx := context.Background()

	ordinary := emitAt(t, w, "ws_test", PriorityNormal, "ordinary chatter")
	pinned := emitAt(t, w, "ws_test", PriorityPermanent, "pinned at emit")
	unpinned := emitAt(t, w, "ws_test", PriorityPermanent, "pinned at emit, unpinned later")
	tampered := emitAt(t, w, "ws_test", PriorityNormal, "content edited after the fact")

	// A ledger-recorded un-pin. Self-consistent, and NOT MAC-authenticated —
	// which is why it may move the live column but may not license a delete.
	if _, err := db.Exec(`UPDATE journal_entries SET priority = 'normal' WHERE id = ?`, unpinned); err != nil {
		t.Fatalf("unpin: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO journal_entry_priorities
			(id, entry_id, workspace_id, seq, previous_priority, priority, reason, set_by, set_at)
		VALUES ('jep_unpin', ?, 'ws_test', 1, 'permanent', 'normal', 'cleanup', 'u_admin', ?)`,
		unpinned, time.Now().UTC().Format(time.RFC3339)); err != nil {
		t.Fatalf("unpin ledger row: %v", err)
	}
	if _, err := db.Exec(`UPDATE journal_entries SET summary = 'EDITED' WHERE id = ?`, tampered); err != nil {
		t.Fatalf("tamper: %v", err)
	}

	blocked, err := UnresolvedIntegrity(ctx, db, "ws_test",
		[]string{ordinary, pinned, unpinned, tampered})
	if err != nil {
		t.Fatalf("UnresolvedIntegrity: %v", err)
	}

	if _, ok := blocked[ordinary]; ok {
		t.Errorf("an intact ordinary entry was fenced off: %q — the fence must not stop compaction working",
			blocked[ordinary])
	}
	for _, id := range []string{pinned, unpinned, tampered} {
		if _, ok := blocked[id]; !ok {
			t.Errorf("entry %s was not fenced off from deletion; blocked = %+v", id, blocked)
		}
	}
}

// A row that predates the hash-chain (seq 0, written before v152) carries no
// tamper-evidence claim at all — WriteChainCheckpoint already refuses to attest
// such rows. The fence must not turn "never chained" into "undeletable
// forever", or every pre-v152 journal would grow without bound.
func TestUnresolvedIntegrity_LeavesUnchainedLegacyRowsAlone(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", bypassTestKey)
	db := openTestDB(t)
	defer db.Close()
	ctx := context.Background()

	if _, err := db.Exec(`
		INSERT INTO journal_entries
			(id, workspace_id, ts, entry_type, severity, actor_type, actor_id,
			 summary, payload, refs, seq, prev_hash, entry_hash)
		VALUES ('j_legacy', 'ws_test', ?, 'exec.output_chunk', 'info', 'system', 'legacy',
			'pre-chain row', '{}', '{}', 0, '', '')`,
		time.Now().UTC().Format(time.RFC3339)); err != nil {
		t.Fatalf("insert legacy row: %v", err)
	}

	blocked, err := UnresolvedIntegrity(ctx, db, "ws_test", []string{"j_legacy"})
	if err != nil {
		t.Fatalf("UnresolvedIntegrity: %v", err)
	}
	if _, ok := blocked["j_legacy"]; ok {
		t.Errorf("an unchained legacy row was fenced off: %q", blocked["j_legacy"])
	}
}

// A CHAINED row whose entry_hash was blanked is the obvious way to make the
// fence's "no chain claim, no objection" rule do work for an attacker. seq > 0
// says the row is in the chain, so an empty hash is missing evidence, not
// absent evidence.
//
// Two guards cover this and either one suffices — an empty string can never
// equal an HMAC, so the hash comparison refuses the row even with the explicit
// empty-hash rule removed. The test is here for the shape, not for one line:
// "the row says it is chained and cannot show for it" must stay refused
// however the fence is later restructured.
func TestUnresolvedIntegrity_ChainedRowWithNoHashIsFenced(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", bypassTestKey)
	db := openTestDB(t)
	defer db.Close()
	w := NewWriter(db, quietLogger(), WriterOptions{FlushInterval: time.Hour})
	defer w.Close()
	ctx := context.Background()

	victim := emitAt(t, w, "ws_test", PriorityPermanent, "permanent record")
	if _, err := db.Exec(
		`UPDATE journal_entries SET entry_hash = '', priority = 'normal', priority_at_emit = 'normal' WHERE id = ?`,
		victim); err != nil {
		t.Fatalf("blank the hash: %v", err)
	}

	blocked, err := UnresolvedIntegrity(ctx, db, "ws_test", []string{victim})
	if err != nil {
		t.Fatalf("UnresolvedIntegrity: %v", err)
	}
	if _, ok := blocked[victim]; !ok {
		t.Error("a chained row with a blanked entry_hash was cleared for deletion")
	}
}
