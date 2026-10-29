package journal

import (
	"database/sql"
	"errors"
	"testing"
)

func emitAuthentic(t *testing.T, writer *Writer, workspace string, n int) {
	t.Helper()
	for range n {
		if _, err := writer.EmitSync(t.Context(), Entry{WorkspaceID: workspace, Type: EntryInvitationCreated, ActorType: ActorSystem, Summary: "Authentic history"}); err != nil {
			t.Fatal(err)
		}
	}
}

func countEntries(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM journal_entries`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// A defect behind an authentic tail does not block appends; full verification
// still reports it. Rows from before the keyring upgrade (legacy v1 MACs) are
// inherited history (historical_*); a defect in a keyring-signed row is damage
// since the upgrade (post_upgrade_invalid). Before tail-only admission each
// case refused every later audited write in the workspace (HTTP 500).
func TestAuditHistoricalDefectDoesNotBlockAppend(t *testing.T) {
	cases := []struct {
		name, change, want string
		legacy             bool
	}{
		{"legacy content tampered", "UPDATE journal_entries SET summary='changed' WHERE seq=2", "historical_invalid", true},
		{"legacy priority mismatch", "UPDATE journal_entries SET priority='pin' WHERE seq=2", "historical_unverified", true},
		{"signed content tampered", "UPDATE journal_entries SET summary='changed' WHERE seq=2", "post_upgrade_invalid", false},
		{"signed retired key", "UPDATE journal_entries SET chain_key_id='missing-old-key' WHERE seq=2", "post_upgrade_invalid", false},
		{"signed retired key at genesis", "UPDATE journal_entries SET chain_key_id='missing-old-key' WHERE seq=1", "post_upgrade_invalid", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := openTestDB(t)
			defer db.Close()
			if tc.legacy {
				old := NewWriter(db, quietLogger(), WriterOptions{ChainKey: ChainKeyFromEnv()})
				emitAuthentic(t, old, "ws_test", 3)
				old.Close()
			}
			writer := NewWriter(db, quietLogger(), WriterOptions{})
			defer writer.Close()
			signed := 4
			if tc.legacy {
				signed = 1 // the upgraded writer's first row is the authentic tail
			}
			emitAuthentic(t, writer, "ws_test", signed)
			if _, err := db.Exec(tc.change); err != nil {
				t.Fatal(err)
			}
			before := countEntries(t, db)
			if _, err := writer.EmitSync(t.Context(), Entry{WorkspaceID: "ws_test", Type: EntryInvitationCreated, ActorType: ActorSystem, Summary: "After historical defect"}); err != nil {
				t.Fatalf("historical defect blocked append: %v", err)
			}
			if n := countEntries(t, db); n != before+1 {
				t.Fatalf("append not committed: %d rows", n)
			}
			result, err := VerifyChain(t.Context(), db, "ws_test")
			if err != nil {
				t.Fatal(err)
			}
			if result.OK || result.BreakCount == 0 || result.HistoricalStatus != tc.want || !result.ContinuationVerified {
				t.Fatalf("defect not reported as %s: %+v", tc.want, result)
			}
		})
	}
}

// A workspace whose every row predates the chain (seq 0) starts a chain on
// its first append and keeps reporting the unsigned prefix.
func TestAuditPreChainWorkspaceAppends(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	for _, id := range []string{"legacy-a", "legacy-b"} {
		if _, err := db.Exec(`INSERT INTO journal_entries(id,workspace_id,ts,entry_type,actor_type,summary,priority) VALUES(?,'ws_test','2000-01-01','invitation.created','system','Unsigned legacy','pin')`, id); err != nil {
			t.Fatal(err)
		}
	}
	writer := NewWriter(db, quietLogger(), WriterOptions{})
	defer writer.Close()
	emitAuthentic(t, writer, "ws_test", 2)
	result, err := VerifyChain(t.Context(), db, "ws_test")
	if err != nil || result.OK || !result.ContinuationVerified || result.HistoricalStatus != "legacy_unverified" {
		t.Fatalf("pre-chain prefix lost or blessed: %+v %v", result, err)
	}
}

// An inconsistent tail still fails closed: a row chained onto it would make
// the tampered row look endorsed. Nothing is written, other chains continue.
func TestAuditTamperedTailRefusesAppend(t *testing.T) {
	cases := []struct {
		name, change string
		want         error
	}{
		{"tail content", "UPDATE journal_entries SET summary='changed' WHERE seq=3", ErrChainIntegrity},
		{"tail key retired", "UPDATE journal_entries SET chain_key_id='missing-tail-key' WHERE seq=3", ErrSigningKeyUnavailable},
		{"tail key stripped", "UPDATE journal_entries SET chain_key_id=NULL,chain_mac_version=NULL WHERE seq=3", ErrChainIntegrity},
		{"tail relinked", "UPDATE journal_entries SET entry_hash='forged' WHERE seq=2", ErrChainIntegrity},
		{"tail resequenced", "UPDATE journal_entries SET seq=7 WHERE seq=3", ErrChainIntegrity},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := openTestDB(t)
			defer db.Close()
			writer := NewWriter(db, quietLogger(), WriterOptions{})
			defer writer.Close()
			emitAuthentic(t, writer, "ws_test", 3)
			if _, err := db.Exec(tc.change); err != nil {
				t.Fatal(err)
			}
			_, err := writer.EmitSync(t.Context(), Entry{WorkspaceID: "ws_test", Type: EntryInvitationCreated, ActorType: ActorSystem, Summary: "Must refuse"})
			if !errors.Is(err, tc.want) {
				t.Fatalf("append onto broken tail: %v want %v", err, tc.want)
			}
			if n := countEntries(t, db); n != 3 {
				t.Fatalf("refused append changed history: %d rows", n)
			}
			if _, err := writer.EmitSync(t.Context(), Entry{WorkspaceID: "unrelated", Type: EntryInvitationCreated, ActorType: ActorSystem, Summary: "Unaffected chain"}); err != nil {
				t.Fatalf("unrelated chain blocked: %v", err)
			}
		})
	}
}

// Rows a batch inserts are re-read and authenticated after native triggers.
func TestAuditAppendDetectsTriggerRewrite(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	writer := NewWriter(db, quietLogger(), WriterOptions{})
	defer writer.Close()
	emitAuthentic(t, writer, "ws_test", 2)
	if _, err := db.Exec(`CREATE TRIGGER rewrite AFTER INSERT ON journal_entries BEGIN UPDATE journal_entries SET summary='rewritten' WHERE id=NEW.id; END`); err != nil {
		t.Fatal(err)
	}
	if err := writer.persistBatch(t.Context(), []Entry{mustPrepare(t, Entry{WorkspaceID: "ws_test", Type: EntryInvitationCreated, ActorType: ActorSystem, Summary: "Batch row"})}); !errors.Is(err, ErrChainIntegrity) {
		t.Fatalf("rewritten batch row admitted: %v", err)
	}
	if n := countEntries(t, db); n != 2 {
		t.Fatalf("rejected batch committed: %d rows", n)
	}
}

func mustPrepare(t *testing.T, e Entry) Entry {
	t.Helper()
	prepared, err := prepareEntry(t.Context(), e)
	if err != nil {
		t.Fatal(err)
	}
	return prepared
}

// A tail whose predecessor vanished without a signed compaction checkpoint is
// an unexplained mid-chain delete next to the tail: refuse. A checkpoint that
// covers the removed predecessor admits the append.
func TestAuditTailPredecessorGap(t *testing.T) {
	for _, checkpointed := range []bool{false, true} {
		t.Run(map[bool]string{false: "uncovered", true: "checkpointed"}[checkpointed], func(t *testing.T) {
			db := openTestDB(t)
			defer db.Close()
			writer := NewWriter(db, quietLogger(), WriterOptions{})
			defer writer.Close()
			emitAuthentic(t, writer, "ws_test", 3)
			var hash string
			if err := db.QueryRow(`SELECT entry_hash FROM journal_entries WHERE seq=2`).Scan(&hash); err != nil {
				t.Fatal(err)
			}
			tx, err := db.Begin()
			if err != nil {
				t.Fatal(err)
			}
			if checkpointed {
				if err := WriteChainCheckpoint(t.Context(), tx, ChainKeyFromEnv(), "ws_test", []RemovedEntry{{Seq: 2, Hash: hash}}); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := tx.Exec(`DELETE FROM journal_entries WHERE seq=2`); err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatal(err)
			}
			_, err = writer.EmitSync(t.Context(), Entry{WorkspaceID: "ws_test", Type: EntryInvitationCreated, ActorType: ActorSystem, Summary: "After gap"})
			if checkpointed && err != nil {
				t.Fatalf("checkpointed predecessor refused: %v", err)
			}
			if !checkpointed && !errors.Is(err, ErrChainIntegrity) {
				t.Fatalf("uncovered predecessor gap admitted: %v", err)
			}
		})
	}
}

// A legacy tail damaged by the v166 backfill (priority_at_emit overwritten by
// a pre-v166 pin) is refused, exactly as before tail-only admission: the old
// path checked the tail's MAC strictly too. Its content stays reported as
// repairable by full verification.
func TestAuditBackfillDamagedLegacyTailRefused(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	old := NewWriter(db, quietLogger(), WriterOptions{ChainKey: ChainKeyFromEnv()})
	emitAuthentic(t, old, "ws_test", 2)
	old.Close()
	if _, err := db.Exec(`UPDATE journal_entries SET priority='pin', priority_at_emit='pin' WHERE seq=2`); err != nil {
		t.Fatal(err)
	}
	writer := NewWriter(db, quietLogger(), WriterOptions{})
	defer writer.Close()
	if _, err := writer.EmitSync(t.Context(), Entry{WorkspaceID: "ws_test", Type: EntryInvitationCreated, ActorType: ActorSystem, Summary: "After backfill"}); !errors.Is(err, ErrChainIntegrity) {
		t.Fatalf("backfill-damaged tail: %v", err)
	}
	result, err := VerifyChain(t.Context(), db, "ws_test")
	if err != nil || result.RepairableCount != 1 {
		t.Fatalf("backfill damage not reported repairable: %+v %v", result, err)
	}
}
