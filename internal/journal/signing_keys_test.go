package journal

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
)

func TestAuditSigningIdentityCannotBeRelabeled(t *testing.T) {
	for _, column := range []string{"chain_key_id", "chain_mac_version"} {
		t.Run(column, func(t *testing.T) {
			db := openTestDB(t)
			defer db.Close()
			key := base64.StdEncoding.EncodeToString(DeriveChainKey("synthetic-same-key-two-labels"))
			raw, err := json.Marshal(signingKeyDocument{ActiveID: "current", Keys: []signingKeyRecord{{ID: "current", Key: key}, {ID: "alias", Key: key}}})
			if err != nil {
				t.Fatal(err)
			}
			keys, err := ParseSigningKeys(raw)
			if err != nil {
				t.Fatal(err)
			}
			writer := NewWriter(db, quietLogger(), WriterOptions{SigningKeys: keys})
			defer writer.Close()
			id, err := writer.EmitSync(t.Context(), Entry{WorkspaceID: "ws_test", Type: EntryInvitationCreated, ActorType: ActorSystem, Summary: "Identity binding"})
			if err != nil {
				t.Fatal(err)
			}
			before, err := VerifyChainWithKeys(t.Context(), db, "ws_test", keys)
			if err != nil || !before.OK {
				t.Fatalf("healthy signed row: %+v %v", before, err)
			}
			value := any("alias")
			if column == "chain_mac_version" {
				value = int64(1)
			}
			if _, err := db.Exec(`UPDATE journal_entries SET `+column+`=? WHERE id=?`, value, id); err != nil {
				t.Fatal(err)
			}
			after, err := VerifyChainWithKeys(t.Context(), db, "ws_test", keys)
			if err != nil || after.OK {
				t.Fatalf("changed signing identity accepted: %+v %v", after, err)
			}
		})
	}
}

func TestAuditAllDefaultAppendEntrypointsUseVersionedRows(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", "synthetic-default-entrypoints")
	t.Setenv(SigningKeysPathEnv, "")
	db := openTestDB(t)
	defer db.Close()
	writer := NewWriter(db, quietLogger(), WriterOptions{})
	defer writer.Close()
	for _, method := range []string{"emit_sync", "emit_batch", "configured_tx", "standalone_tx"} {
		entry := Entry{WorkspaceID: "ws_test", Type: EntryInvitationCreated, ActorType: ActorSystem, Summary: method}
		if method == "emit_batch" {
			if _, err := writer.Emit(t.Context(), entry); err != nil {
				t.Fatal(err)
			}
			if err := writer.Flush(t.Context()); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if method == "emit_sync" {
			if _, err := writer.EmitSync(t.Context(), entry); err != nil {
				t.Fatal(err)
			}
			continue
		}
		tx, err := db.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		var notify func()
		if method == "configured_tx" {
			notify, err = writer.AppendTx(t.Context(), tx, entry)
		} else {
			notify, err = AppendTx(t.Context(), tx, entry)
		}
		if err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		notify()
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE chain_key_id IS NOT NULL AND chain_mac_version=2`).Scan(&count); err != nil || count != 4 {
		t.Fatalf("versioned actual entrypoints count=%d err=%v", count, err)
	}
}

func TestAuditKeyringRejectsAmbiguousIdentityDocuments(t *testing.T) {
	key := base64.StdEncoding.EncodeToString(DeriveChainKey("synthetic-key-parser"))
	for _, document := range []signingKeyDocument{
		{ActiveID: LegacySigningKeyID, Keys: []signingKeyRecord{{ID: LegacySigningKeyID, Key: key}}},
		{ActiveID: "same", Keys: []signingKeyRecord{{ID: "same", Key: key}, {ID: "same", Key: key}}},
		{ActiveID: "missing", Keys: []signingKeyRecord{{ID: "present", Key: key}}},
		{ActiveID: "bad id", Keys: []signingKeyRecord{{ID: "bad id", Key: key}}},
		{ActiveID: "short", Keys: []signingKeyRecord{{ID: "short", Key: base64.StdEncoding.EncodeToString([]byte("short"))}}},
	} {
		raw, err := json.Marshal(document)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ParseSigningKeys(raw); err == nil {
			t.Error("ambiguous audit keyring accepted")
		}
	}
}

func TestAuditVerifierNeverAcceptsUnavailableMACAsEmpty(t *testing.T) {
	for _, mode := range []string{"unknown_id", "invalid_version", "half_null"} {
		t.Run(mode, func(t *testing.T) {
			db := openTestDB(t)
			defer db.Close()
			writer := NewWriter(db, quietLogger(), WriterOptions{})
			defer writer.Close()
			id, err := writer.EmitSync(t.Context(), Entry{WorkspaceID: "ws_test", Type: EntryInvitationCreated, ActorType: ActorSystem, Summary: "Healthy verification control"})
			if err != nil {
				t.Fatal(err)
			}
			keys, err := writer.SigningKeySnapshot()
			if err != nil {
				t.Fatal(err)
			}
			before, err := VerifyChainWithKeys(t.Context(), db, "ws_test", keys)
			if err != nil || !before.OK {
				t.Fatalf("healthy native control: %+v %v", before, err)
			}
			query := `UPDATE journal_entries SET entry_hash='',chain_key_id='unknown-fixture-id' WHERE id=?`
			if mode == "invalid_version" {
				query = `UPDATE journal_entries SET entry_hash='',chain_mac_version=999 WHERE id=?`
			}
			if mode == "half_null" {
				query = `UPDATE journal_entries SET entry_hash='',chain_mac_version=NULL WHERE id=?`
			}
			if _, err := db.Exec(query, id); err != nil {
				t.Fatal(err)
			}
			after, err := VerifyChainWithKeys(t.Context(), db, "ws_test", keys)
			if err != nil {
				t.Fatal(err)
			}
			if after.OK {
				t.Error("unavailable MAC was accepted as an empty valid hash")
			}

		})
	}
}

// Reduced schemas still exercise the current writer; they must provide its
// actual signing metadata columns, not silently request v1.
func addAuditSigningFixture(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`ALTER TABLE journal_entries ADD COLUMN chain_key_id TEXT;
 ALTER TABLE journal_entries ADD COLUMN chain_mac_version INTEGER;
 CREATE TABLE IF NOT EXISTS journal_chain_checkpoints (
  id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL, created_at TEXT NOT NULL,
  removed_json TEXT NOT NULL, mac TEXT NOT NULL,
  chain_key_id TEXT, chain_mac_version INTEGER
 );`); err != nil {
		t.Fatal(err)
	}
}

// An unavailable configured writer must not silently adopt a different env key.
func TestAuditSigningContextRefusesUnavailableSnapshot(t *testing.T) {
	t.Setenv(SigningKeysPathEnv, "")
	if keys, err := SigningKeysForContext(t.Context()); err != nil || keys == nil {
		t.Fatalf("healthy default control: %v", err)
	}
	if _, err := SigningKeysForContext(WithSigningKeys(t.Context(), nil)); !errors.Is(err, ErrSigningKeyUnavailable) {
		t.Fatalf("unavailable boot snapshot fell back to environment: %v", err)
	}
}

// Middle-of-history failures no longer refuse appends: see
// TestAuditHistoricalDefectDoesNotBlockAppend and TestAuditTamperedTailRefusesAppend.

func TestAuditUnsignedLegacyDoesNotAuthenticateOrHideChainedFailure(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	if _, err := db.Exec(`INSERT INTO journal_entries(id,workspace_id,ts,entry_type,actor_type,summary) VALUES('legacy','ws_test','2000-01-01','invitation.created','system','Unsigned legacy')`); err != nil {
		t.Fatal(err)
	}
	writer := NewWriter(db, quietLogger(), WriterOptions{})
	defer writer.Close()
	for range 3 {
		if _, err := writer.EmitSync(t.Context(), Entry{WorkspaceID: "ws_test", Type: EntryInvitationCreated, ActorType: ActorSystem, Summary: "Authentic segment"}); err != nil {
			t.Fatal(err)
		}
	}
	result, err := VerifyChain(t.Context(), db, "ws_test")
	if err != nil || result.OK || !result.ContinuationVerified || result.HistoricalStatus != "legacy_unverified" {
		t.Fatalf("legacy history was blessed or current segment lost: %+v %v", result, err)
	}
	if _, err := db.Exec(`UPDATE journal_entries SET summary='changed' WHERE seq=3`); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.EmitSync(t.Context(), Entry{WorkspaceID: "ws_test", Type: EntryInvitationCreated, ActorType: ActorSystem, Summary: "Must refuse"}); !errors.Is(err, ErrChainIntegrity) {
		t.Fatalf("unsigned legacy hid chained failure: %v", err)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM journal_entries`).Scan(&count); err != nil || count != 4 {
		t.Fatalf("invalid chain changed: %d %v", count, err)
	}
}
