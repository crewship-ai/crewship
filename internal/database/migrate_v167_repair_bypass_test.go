package database

// Explicit attempt-to-bypass tests for the v167 journal repair path (#1486).
//
// #1486's addition, verbatim: "Every recovery / repair / acknowledge path needs
// its own bypass test. Any mechanism that undoes or forgives a failed check is a
// security surface in its own right."
//
// repairJournalMissionIDs is exactly such a mechanism. It takes rows whose audit
// chain is BROKEN and writes a column back so they verify again — a red turned
// into a green, by a migration that runs unattended at upgrade and again on
// every restore of a pre-v167 bundle. TestV167_UpgradeRebuildsAndRepairsDamagedJournal
// covers the honest case and one benign decoy. What is written here is the
// adversarial case: an attacker who wants the repair to endorse a value of THEIR
// choosing, or to run at all on a row whose authenticity cannot be proven.
//
// The one thing standing in the way is that the repair re-derives the keyed
// chain hash and writes only on an exact match (migrate_consts_v167…:608). These
// tests make sure that check is doing the work and is not, say, satisfied by the
// candidate merely being present in refs.

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
)

// TestV167_RepairRefusesAnAttackerChosenMissionID walks the plans an attacker
// with DB write access would try in order to get the repair to write a
// mission_id they picked — i.e. to relocate an audit record onto a different
// mission, with the migration's own signature on the change.
//
// All four must end the same way: the column stays NULL and the row stays a
// visible break. "Left broken" is the correct outcome — an unprovable row is
// supposed to stay red, and quietly repairing it would be the bypass.
func TestV167_RepairRefusesAnAttackerChosenMissionID(t *testing.T) {
	cases := []struct {
		name string
		// tamper mutates the row after it was honestly emitted. It runs against a
		// fully migrated DB with the entry already in place.
		tamper func(t *testing.T, db *sql.DB, entryID string)
	}{
		{
			// The direct attempt: null the column and point refs at a mission of
			// the attacker's choosing, betting the repair trusts refs.
			name: "refs rewritten to name a different mission",
			tamper: func(t *testing.T, db *sql.DB, entryID string) {
				execV167(t, db, `UPDATE journal_entries
					SET mission_id = NULL, refs = '{"mission_id":"mission_ATTACKER"}' WHERE id = ?`, entryID)
			},
		},
		{
			// Same, but the attacker also clears entry_hash so there is no proof
			// to fail against. A repair that treats "no hash" as "nothing to
			// check" would write the value.
			name: "entry_hash cleared so there is nothing to verify against",
			tamper: func(t *testing.T, db *sql.DB, entryID string) {
				execV167(t, db, `UPDATE journal_entries
					SET mission_id = NULL, refs = '{"mission_id":"mission_ATTACKER"}', entry_hash = ''
					WHERE id = ?`, entryID)
			},
		},
		{
			// The attacker recomputes the hash — with a bare SHA-256, the only
			// thing they can compute without the chain key. This is the same
			// forgery journal.TestVerifyChain_KeyedRejectsRecomputedHash rejects,
			// re-run against the repair, because the repair re-implements the
			// check on its own and could regress independently.
			name: "entry_hash recomputed with an unkeyed sha256",
			tamper: func(t *testing.T, db *sql.DB, entryID string) {
				var f journal.ChainFields
				var prevHash string
				if err := db.QueryRow(`
					SELECT seq, id, workspace_id, COALESCE(crew_id,''), COALESCE(agent_id,''),
					       ts, entry_type, severity, COALESCE(priority_at_emit, priority, 'normal'),
					       actor_type, COALESCE(actor_id,''), summary, payload, refs,
					       COALESCE(trace_id,''), COALESCE(span_id,''), COALESCE(expires_at,''),
					       COALESCE(prev_hash,'')
					  FROM journal_entries WHERE id = ?`, entryID).Scan(
					&f.Seq, &f.ID, &f.Workspace, &f.CrewID, &f.AgentID,
					&f.TS, &f.EntryType, &f.Severity, &f.Priority,
					&f.ActorType, &f.ActorID, &f.Summary, &f.Payload, &f.Refs,
					&f.TraceID, &f.SpanID, &f.ExpiresAt, &prevHash); err != nil {
					t.Fatalf("read row for forgery: %v", err)
				}
				f.MissionID = "mission_ATTACKER"
				f.Refs = `{"mission_id":"mission_ATTACKER"}`
				execV167(t, db, `UPDATE journal_entries
					SET mission_id = NULL, refs = ?, entry_hash = ? WHERE id = ?`,
					f.Refs, unkeyedChainHash(prevHash, f), entryID)
			},
		},
		{
			// The subtle one. The attacker leaves refs alone — it is inside the
			// hash, so touching it is fatal — and simply nulls the column, hoping
			// the repair restores a mission id they had already substituted in
			// refs at emit time. It cannot: the hash commits to the mission_id the
			// row was WRITTEN with, so only that value can be written back.
			// Asserting the restored value (not merely "something was written")
			// is what makes this a bypass test rather than a smoke test.
			name: "column nulled, refs untouched — only the authentic value may return",
			tamper: func(t *testing.T, db *sql.DB, entryID string) {
				execV167(t, db, `UPDATE journal_entries SET mission_id = NULL WHERE id = ?`, entryID)
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ENCRYPTION_KEY", strings.Repeat("a1b2c3d4", 8))
			ctx := context.Background()
			quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
			db := openV167TestDB(t)

			if err := Migrate(ctx, db, quiet); err != nil {
				t.Fatalf("migrate: %v", err)
			}
			const wsID, missionID = "ws_repair_bypass", "mission_authentic"
			execV167(t, db, `INSERT INTO workspaces (id, name, slug) VALUES (?,?,?)`, wsID, "rb", "rb")

			w := journal.NewWriter(db, quiet, journal.WriterOptions{FlushInterval: time.Hour})
			entryID, err := w.Emit(ctx, journal.Entry{
				WorkspaceID: wsID, MissionID: missionID,
				Type: journal.EntryMissionStatus, Severity: journal.SeverityInfo,
				ActorType: journal.ActorUser, ActorID: "user_1",
				Summary: "status_changed: TODO → DONE",
				Refs:    map[string]any{"mission_id": missionID},
			})
			if err != nil {
				t.Fatalf("emit: %v", err)
			}
			if err := w.Flush(ctx); err != nil {
				t.Fatalf("flush: %v", err)
			}
			_ = w.Close()

			if res := verifyV167(t, ctx, db, wsID); !res.OK {
				t.Fatalf("precondition: the honest chain must verify (%s)", res.Reason)
			}

			tc.tamper(t, db, entryID)

			// Drive the repair exactly as a restore does.
			hook := RestoreBackfillFor(167)
			if hook == nil {
				t.Fatal("v167 registers no restore backfill hook")
			}
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatalf("begin: %v", err)
			}
			if err := hook(ctx, tx, quiet); err != nil {
				_ = tx.Rollback()
				t.Fatalf("repair: %v", err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatalf("commit: %v", err)
			}

			got := scanString(t, db, `SELECT COALESCE(mission_id,'') FROM journal_entries WHERE id = ?`, entryID)
			if got == "mission_ATTACKER" {
				t.Fatalf("the repair wrote the ATTACKER's mission id — it endorsed a value it could not "+
					"prove, over case %q", tc.name)
			}

			if tc.name == "column nulled, refs untouched — only the authentic value may return" {
				// The honest half of the same mechanism, asserted in the same
				// place so a change that breaks either is caught here.
				if got != missionID {
					t.Fatalf("mission_id = %q, want the authentic %q restored", got, missionID)
				}
				if res := verifyV167(t, ctx, db, wsID); !res.OK {
					t.Errorf("the chain does not verify after an authentic repair (%s)", res.Reason)
				}
				return
			}

			// Every adversarial case must be left red, not silently mended.
			if got != "" {
				t.Errorf("mission_id = %q, want it left NULL — the repair wrote to a row whose content "+
					"it could not authenticate", got)
			}
			if res := verifyV167(t, ctx, db, wsID); res.OK {
				t.Errorf("the chain reports OK after a tampered row went through the repair — the " +
					"tampering has been laundered by a migration")
			}
		})
	}
}

// unkeyedChainHash is the bare SHA-256 an attacker without the chain key can
// compute, using the exact length-framing the real (HMAC) hash uses. It exists
// only to simulate that attacker; if it ever validated, the repair would be
// writing values on the strength of a hash anyone can forge.
func unkeyedChainHash(prevHash string, f journal.ChainFields) string {
	h := sha256.New()
	var seqb [8]byte
	binary.BigEndian.PutUint64(seqb[:], uint64(f.Seq))
	h.Write(seqb[:])
	for _, field := range []string{
		prevHash, f.ID, f.Workspace, f.CrewID, f.AgentID, f.MissionID,
		f.TS, f.EntryType, f.Severity, f.Priority, f.ActorType, f.ActorID,
		f.Summary, f.Payload, f.Refs, f.TraceID, f.SpanID, f.ExpiresAt,
	} {
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], uint64(len(field)))
		h.Write(n[:])
		h.Write([]byte(field))
	}
	return hex.EncodeToString(h.Sum(nil))
}
