package api

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/journal"
)

func TestAuditOwnerCheckpointAtomicFaultsAndFreshAuthority(t *testing.T) {
	for _, scope := range []string{"instance", "workspace"} {
		for _, fault := range []string{"healthy", "nonowner", "stale", "abort", "ignore", "old_tail_undo", "checkpoint_delete", "actor_suspend", "deferred_commit", "after_successor"} {
			t.Run(scope+"/"+fault, func(t *testing.T) {
				t.Setenv("ENCRYPTION_KEY", strings.Repeat("6", 64))
				_, oldPath, keyErr := journal.EnsureSigningKeys(t.TempDir(), "")
				if keyErr != nil {
					t.Fatal(keyErr)
				}
				t.Setenv(journal.SigningKeysPathEnv, oldPath)
				f := newInstanceFixture(t)
				defer f.r.Shutdown()
				t.Setenv("CREWSHIP_OWNER_EMAIL", "boss@ex.com")
				table, sequence, kind := "instance_audit_logs", "chain_seq", "action"
				checkpointType := "audit.resealed"
				if scope == "instance" {
					if err := auditInstance(t.Context(), nil, f.db, "f6.history", "instance", "", "", nil); err != nil {
						t.Fatal(err)
					}
				} else {
					table, sequence, kind = "journal_entries", "seq", "entry_type"
					checkpointType = string(journal.EntryAuditEntityRestored)
					old := journal.NewWriter(f.db, newTestLogger(), journal.WriterOptions{FlushInterval: time.Hour})
					if _, err := old.EmitSync(t.Context(), journal.Entry{WorkspaceID: "ws-old", Type: journal.EntryInvitationCreated, ActorType: journal.ActorSystem, Summary: "Original history"}); err != nil {
						t.Fatal(err)
					}
					old.Close()
				}
				var oldID, oldHash string
				var seq int64
				if err := f.db.QueryRow(`SELECT id,`+sequence+`,entry_hash FROM `+table+` ORDER BY `+sequence+` DESC LIMIT 1`).Scan(&oldID, &seq, &oldHash); err != nil {
					t.Fatal(err)
				}
				var baselineCount int
				if err := f.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&baselineCount); err != nil {
					t.Fatal(err)
				}
				t.Setenv("ENCRYPTION_KEY", strings.Repeat("7", 64))
				_, newPath, keyErr := journal.EnsureSigningKeys(t.TempDir(), "")
				if keyErr != nil {
					t.Fatal(keyErr)
				}
				t.Setenv(journal.SigningKeysPathEnv, newPath)
				writer := journal.NewWriter(f.db, newTestLogger(), journal.WriterOptions{FlushInterval: time.Hour})
				defer writer.Close()
				f.r.journal = writer
				notify := writer.Notify()
				trigger := ""
				afterInsert := fmt.Sprintf("CREATE TRIGGER f6_fault AFTER INSERT ON %s WHEN NEW.%s='%s'", table, kind, checkpointType)
				switch fault {
				case "abort":
					trigger = fmt.Sprintf(`CREATE TRIGGER f6_fault BEFORE INSERT ON %s WHEN NEW.%s='%s' BEGIN SELECT RAISE(ABORT,'fixture'); END`, table, kind, checkpointType)
				case "ignore":
					trigger = fmt.Sprintf(`CREATE TRIGGER f6_fault BEFORE INSERT ON %s WHEN NEW.%s='%s' BEGIN SELECT RAISE(IGNORE); END`, table, kind, checkpointType)
				case "old_tail_undo":
					trigger = fmt.Sprintf(afterInsert+` BEGIN UPDATE %s SET entry_hash='changed' WHERE id='%s'; END`, table, oldID)
				case "checkpoint_delete":
					trigger = fmt.Sprintf(afterInsert+` BEGIN DELETE FROM %s WHERE %s>%d; END`, table, sequence, seq)
				case "actor_suspend":
					trigger = afterInsert + ` BEGIN UPDATE users SET suspended_at='2026-10-20' WHERE id='boss'; END`
				case "after_successor":
					if scope == "instance" {
						trigger = `CREATE TRIGGER f6_fault AFTER INSERT ON instance_audit_logs WHEN NEW.action='audit.resealed' BEGIN INSERT INTO instance_audit_logs(id,action,entity_type,created_at,chain_seq,prev_hash,entry_hash,chain_key_id,chain_mac_version) SELECT 'f6-successor','fixture.invalid','instance',created_at,chain_seq+1,entry_hash,'invalid',chain_key_id,chain_mac_version FROM instance_audit_logs ORDER BY chain_seq DESC LIMIT 1; END`
					} else {
						trigger = afterInsert + ` BEGIN INSERT INTO journal_entries(id,workspace_id,ts,entry_type,actor_type,summary,seq,prev_hash,entry_hash,chain_key_id,chain_mac_version) SELECT 'f6-successor',workspace_id,ts,'invitation.created','system','Invalid native successor',seq+1,entry_hash,'invalid',chain_key_id,chain_mac_version FROM journal_entries WHERE workspace_id='ws-old' ORDER BY seq DESC LIMIT 1; END`
					}

				case "deferred_commit":
					if _, err := f.db.Exec(`CREATE TABLE f6_deferred(user_id TEXT REFERENCES users(id) DEFERRABLE INITIALLY DEFERRED)`); err != nil {
						t.Fatal(err)
					}
					trigger = afterInsert + ` BEGIN INSERT INTO f6_deferred VALUES('absent-fixture-user'); END`
				}
				if trigger != "" {
					if _, err := f.db.Exec(trigger); err != nil {
						t.Fatal(err)
					}
				}
				body := map[string]any{"scope": scope, "expected_tail_seq": seq, "expected_tail_hash": oldHash, "reason": "Explicit synthetic recovery approval"}
				if scope == "workspace" {
					body["workspace_id"] = "ws-old"
				}
				if fault == "stale" {
					body["expected_tail_hash"] = "stale-fixture-tail"
				}
				raw, err := json.Marshal(body)
				if err != nil {
					t.Fatal(err)
				}
				actor := f.boss
				if fault == "nonowner" {
					actor = f.wsAdmin
				}
				response := f.do(actor, "POST", "/api/v1/admin/audit/reseal", string(raw))
				want := 500
				switch fault {
				case "healthy":
					want = 200
				case "nonowner":
					want = 403
				case "stale":
					want = 409
				case "actor_suspend":
					want = 403
				}
				if response.Code != want {
					t.Errorf("checkpoint %s: HTTP%d want%d", fault, response.Code, want)
				}
				var count int
				if err := f.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil {
					t.Fatal(err)
				}

				if fault != "healthy" {
					if count != baselineCount {
						t.Errorf("failed checkpoint committed rows: audit=%d", count)
					}
					select {
					case <-notify:
						t.Error("failed checkpoint notified readers")
					default:
					}
				} else {
					if count != baselineCount+1 {
						t.Errorf("healthy checkpoint count: audit=%d", count)
					}
					keys, err := writer.SigningKeySnapshot()
					if err != nil {
						t.Fatal(err)
					}
					var result *journal.VerifyResult
					if scope == "instance" {
						result, err = journal.VerifyInstanceAuditWithKeys(t.Context(), f.db, keys)
					} else {
						result, err = journal.VerifyChainWithKeys(t.Context(), f.db, "ws-old", keys)
					}
					if err != nil || result.OK || !result.ContinuationVerified || result.HistoricalStatus != "historical_unverified" {
						t.Errorf("checkpoint misrepresented historical validity: %+v %v", result, err)
					}
				}
				if fault != "healthy" {
					var suspended bool
					if err := f.db.QueryRow(`SELECT suspended_at IS NOT NULL FROM users WHERE id='boss'`).Scan(&suspended); err != nil || suspended {
						t.Error("failed checkpoint committed caller suspension")
					}
				}
				if fault == "healthy" && scope == "workspace" {
					select {
					case <-notify:
					default:
						t.Error("committed checkpoint did not notify readers")
					}
				}
				var retained string
				if err := f.db.QueryRow(`SELECT entry_hash FROM `+table+` WHERE id=?`, oldID).Scan(&retained); err != nil || retained != oldHash {
					t.Error("checkpoint altered historical tail")
				}
			})
		}
	}
}

func TestAuditOwnerCheckpointPreservesUnchainedLegacyHistory(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", "synthetic-legacy-checkpoint")
	t.Setenv(journal.SigningKeysPathEnv, "")
	f := newInstanceFixture(t)
	defer f.r.Shutdown()
	t.Setenv("CREWSHIP_OWNER_EMAIL", "boss@ex.com")
	if _, err := f.db.Exec(`DELETE FROM instance_audit_logs;INSERT INTO instance_audit_logs(id,action,entity_type,created_at) VALUES('unchained-legacy','fixture.legacy','instance','2020-01-01')`); err != nil {
		t.Fatal(err)
	}
	body := `{"scope":"instance","expected_tail_seq":0,"expected_tail_hash":"","reason":"Explicit preservation of unverifiable legacy history"}`
	response := f.do(f.boss, "POST", "/api/v1/admin/audit/reseal", body)
	if response.Code != 200 {
		t.Fatalf("explicit OWNER checkpoint HTTP%d", response.Code)
	}
	keys, err := journal.LoadSigningKeysFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	result, err := journal.VerifyInstanceAuditWithKeys(t.Context(), f.db, keys)
	if err != nil || result.OK || !result.ContinuationVerified || result.HistoricalStatus != "historical_unverified" {
		t.Errorf("OWNER checkpoint falsely authenticated unchained legacy history: %+v %v", result, err)
	}
	var unchanged bool
	if err := f.db.QueryRow(`SELECT EXISTS(SELECT 1 FROM instance_audit_logs WHERE id='unchained-legacy' AND action='fixture.legacy' AND created_at='2020-01-01' AND chain_seq IS NULL AND prev_hash IS NULL AND entry_hash IS NULL AND chain_key_id IS NULL AND chain_mac_version IS NULL)`).Scan(&unchanged); err != nil || !unchanged {
		t.Error("legacy row was rewritten")
	}
}

func TestAuditInstanceCheckpointCannotHideLaterUnchainedRow(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", "synthetic-empty-checkpoint")
	t.Setenv(journal.SigningKeysPathEnv, "")
	f := newInstanceFixture(t)
	defer f.r.Shutdown()
	t.Setenv("CREWSHIP_OWNER_EMAIL", "boss@ex.com")
	if _, err := f.db.Exec(`DELETE FROM instance_audit_logs`); err != nil {
		t.Fatal(err)
	}
	response := f.do(f.boss, "POST", "/api/v1/admin/audit/reseal", `{"scope":"instance","expected_tail_seq":0,"expected_tail_hash":"","reason":"Explicit empty history OWNER control"}`)
	if response.Code != 200 {
		t.Fatalf("healthy empty checkpoint HTTP%d", response.Code)
	}
	keys, err := journal.LoadSigningKeysFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	before, err := journal.VerifyInstanceAuditWithKeys(t.Context(), f.db, keys)
	if err != nil || !before.OK || !before.ContinuationVerified {
		t.Fatalf("healthy empty history: %+v %v", before, err)
	}
	if _, err := f.db.Exec(`INSERT INTO instance_audit_logs(id,action,entity_type,created_at) VALUES('later-unchained','fixture.legacy','instance','2000-01-01')`); err != nil {
		t.Fatal(err)
	}
	after, err := journal.VerifyInstanceAuditWithKeys(t.Context(), f.db, keys)
	if err != nil || after.OK || !after.ContinuationVerified || after.HistoricalStatus == "verified" {
		t.Errorf("valid checkpoint hid an unverified historical row: %+v %v", after, err)
	}
}

func TestAuditEmptyCheckpointRejectsLateLegacyRow(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", "synthetic-empty-transaction")
	t.Setenv(journal.SigningKeysPathEnv, "")
	f := newInstanceFixture(t)
	defer f.r.Shutdown()
	t.Setenv("CREWSHIP_OWNER_EMAIL", "boss@ex.com")
	if _, err := f.db.Exec(`DELETE FROM instance_audit_logs;CREATE TRIGGER f6_late_legacy AFTER INSERT ON instance_audit_logs WHEN NEW.action='audit.resealed' BEGIN INSERT INTO instance_audit_logs(id,action,entity_type,created_at) VALUES('trigger-legacy','fixture.legacy','instance','2000-01-01');END`); err != nil {
		t.Fatal(err)
	}
	response := f.do(f.boss, "POST", "/api/v1/admin/audit/reseal", `{"scope":"instance","expected_tail_seq":0,"expected_tail_hash":"","reason":"Explicit empty history transaction control"}`)
	if response.Code != 500 {
		t.Errorf("checkpoint attested empty history after native legacy insertion: HTTP%d want500", response.Code)
	}
	var audits int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM instance_audit_logs`).Scan(&audits); err != nil {
		t.Fatal(err)
	}

	if audits != 0 {
		t.Errorf("refused empty-history checkpoint committed state: audit=%d", audits)
	}
}

func TestAuditResealRequiresExplicitTailPresence(t *testing.T) {
	for _, tc := range []struct{ name, fields string }{
		{"missing_both", ""},
		{"missing_seq", `,"expected_tail_hash":""`},
		{"missing_hash", `,"expected_tail_seq":0`},
		{"null_seq", `,"expected_tail_seq":null,"expected_tail_hash":""`},
		{"null_hash", `,"expected_tail_seq":0,"expected_tail_hash":null`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ENCRYPTION_KEY", "synthetic-explicit-tail")
			t.Setenv(journal.SigningKeysPathEnv, "")
			f := newInstanceFixture(t)
			defer f.r.Shutdown()
			t.Setenv("CREWSHIP_OWNER_EMAIL", "boss@ex.com")
			if _, err := f.db.Exec(`DELETE FROM instance_audit_logs`); err != nil {
				t.Fatal(err)
			}
			// The same real OWNER and explicit empty CAS must remain usable.
			healthy := f.do(f.boss, "POST", "/api/v1/admin/audit/reseal", `{"scope":"instance","expected_tail_seq":0,"expected_tail_hash":"","reason":"Explicit empty tail healthy control"}`)
			if healthy.Code != 200 {
				t.Fatalf("healthy explicit CAS HTTP%d", healthy.Code)
			}
			if _, err := f.db.Exec(`DELETE FROM instance_audit_logs`); err != nil {
				t.Fatal(err)
			}
			response := f.do(f.boss, "POST", "/api/v1/admin/audit/reseal", `{"scope":"instance","reason":"Explicit old tail presence required"`+tc.fields+`}`)
			if response.Code != 400 {
				t.Errorf("omitted/null CAS HTTP%d want400", response.Code)
			}
			var count int
			if err := f.db.QueryRow(`SELECT COUNT(*) FROM instance_audit_logs`).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Errorf("invalid CAS committed %d rows", count)
			}
		})
	}
}
