package api

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/crewship-ai/crewship/internal/journal"
)

func TestAuditKeyringInstanceRotationAndForeignKey(t *testing.T) {
	for _, mode := range []string{"retained_rotation", "foreign_missing_key"} {
		t.Run(mode, func(t *testing.T) {
			const oldSeed = "synthetic-instance-legacy-seed"
			t.Setenv("ENCRYPTION_KEY", oldSeed)
			t.Setenv("CREWSHIP_AUDIT_KEYRING_PATH", "")
			db := setupTestDB(t)
			if err := auditInstance(journal.WithSigningKeys(t.Context(), journal.LegacySigningKeys(journal.DeriveChainKey(oldSeed))), nil, db, "f6.before", "instance", "", "", nil); err != nil {
				t.Fatalf("healthy current writer: %v", err)
			}
			var oldHash string
			if err := db.QueryRow(`SELECT entry_hash FROM instance_audit_logs WHERE action='f6.before'`).Scan(&oldHash); err != nil {
				t.Fatal(err)
			}
			t.Setenv("ENCRYPTION_KEY", "synthetic-instance-foreign-seed")
			if mode == "retained_rotation" {
				next := sha256.Sum256([]byte("synthetic-instance-active-key-two"))
				data, err := json.Marshal(map[string]any{"active_key_id": "audit-two", "keys": []map[string]string{{"id": "legacy-v1", "key_b64": base64.StdEncoding.EncodeToString(journal.DeriveChainKey(oldSeed))}, {"id": "audit-two", "key_b64": base64.StdEncoding.EncodeToString(next[:])}}})
				if err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(t.TempDir(), "audit-keys.json")
				if err := os.WriteFile(path, data, 0600); err != nil {
					t.Fatal(err)
				}
				t.Setenv("CREWSHIP_AUDIT_KEYRING_PATH", path)
			}
			err := auditInstance(t.Context(), nil, db, "f6.after", "instance", "", "", nil)
			if mode == "retained_rotation" && err != nil {
				t.Errorf("retained verification key did not allow active-key append: %v", err)
			}
			if mode == "foreign_missing_key" && err == nil {
				t.Error("foreign instance chain accepted append without explicit reseal")
			}
			var count int
			if err := db.QueryRow(`SELECT COUNT(*) FROM instance_audit_logs WHERE action='f6.after'`).Scan(&count); err != nil {
				t.Fatal(err)
			}
			want := 0
			if mode == "retained_rotation" {
				want = 1
			}
			if count != want {
				t.Errorf("append count=%d want=%d", count, want)
			}
			var after string
			if err := db.QueryRow(`SELECT entry_hash FROM instance_audit_logs WHERE action='f6.before'`).Scan(&after); err != nil {
				t.Fatal(err)
			}
			if after != oldHash {
				t.Error("rotation changed old MAC")
			}
		})
	}
}

func TestAuditInstanceOwnerExplicitReseal(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", "synthetic-old-owner-reseal-root")
	t.Setenv("CREWSHIP_AUDIT_KEYRING_PATH", "")
	f := newInstanceFixture(t)
	defer f.r.Shutdown()
	t.Setenv("CREWSHIP_OWNER_EMAIL", "boss@ex.com")
	if err := auditInstance(t.Context(), nil, f.db, "f6.history", "instance", "", "", nil); err != nil {
		t.Fatal(err)
	}
	var seq int64
	var hash, id string
	if err := f.db.QueryRow(`SELECT id,chain_seq,entry_hash FROM instance_audit_logs ORDER BY chain_seq DESC LIMIT 1`).Scan(&id, &seq, &hash); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ENCRYPTION_KEY", "synthetic-new-owner-reseal-root")
	if err := auditInstance(t.Context(), nil, f.db, "f6.must_fail", "instance", "", "", nil); err == nil {
		t.Fatal("foreign-key positive denial control failed")
	}
	body, err := json.Marshal(map[string]any{"scope": "instance", "expected_tail_seq": seq, "expected_tail_hash": hash, "reason": "Approved synthetic foreign-host recovery"})
	if err != nil {
		t.Fatal(err)
	}
	response := f.do(f.boss, "POST", "/api/v1/admin/audit/reseal", string(body))
	if response.Code != 200 {
		t.Fatalf("explicit current OWNER checkpoint unavailable: HTTP%d", response.Code)
	}
	if err := auditInstance(t.Context(), nil, f.db, "f6.after_reseal", "instance", "", "", nil); err != nil {
		t.Errorf("explicit reseal did not restore continuation: %v", err)
	}
	var retained string
	if err := f.db.QueryRow(`SELECT entry_hash FROM instance_audit_logs WHERE id=?`, id).Scan(&retained); err != nil {
		t.Fatal(err)
	}
	if retained != hash {
		t.Error("explicit reseal rewrote historical MAC")
	}
}

func TestAuditInstanceMiddleFailureRefusesAppend(t *testing.T) {
	for _, change := range []string{"metadata='changed'", "chain_key_id='missing-old-key'", "chain_key_id='missing-tail-key'"} {
		t.Run(change, func(t *testing.T) {
			db := setupTestDB(t)
			for range 3 {
				if err := auditInstance(t.Context(), nil, db, "fixture.authentic", "instance", "", "", nil); err != nil {
					t.Fatal(err)
				}
			}
			sequence := 1
			if change == "chain_key_id='missing-tail-key'" {
				sequence = 3
			}
			if _, err := db.Exec("UPDATE instance_audit_logs SET "+change+" WHERE chain_seq=?", sequence); err != nil {
				t.Fatal(err)
			}
			err := auditInstance(t.Context(), nil, db, "fixture.must_refuse", "instance", "", "", nil)
			wanted := journal.ErrChainIntegrity
			if change != "metadata='changed'" {
				wanted = journal.ErrSigningKeyUnavailable
			}
			if !errors.Is(err, wanted) {
				t.Errorf("append after invalid middle: %v want %v", err, wanted)
			}
			var count int
			if err := db.QueryRow("SELECT COUNT(*) FROM instance_audit_logs").Scan(&count); err != nil || count != 3 {
				t.Errorf("refused append changed history: %d %v", count, err)
			}
		})
	}
}
