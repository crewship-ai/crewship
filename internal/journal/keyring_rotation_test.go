package journal_test

import (
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/testutil"
)

// Only synthetic local fixture keys. The file is the proposed supported boot
// configuration; this regression invokes existing actual writers/verifiers.
func TestAuditKeyringWorkspaceRotationAndForeignRestore(t *testing.T) {
	for _, mode := range []string{"retained_rotation", "foreign_restore_missing_key"} {
		t.Run(mode, func(t *testing.T) {
			const oldSeed = "synthetic-audit-legacy-seed"
			t.Setenv("ENCRYPTION_KEY", oldSeed)
			t.Setenv("CREWSHIP_AUDIT_KEYRING_PATH", "")
			db := testutil.MigratedDB(t).DB
			if _, err := db.Exec(`INSERT INTO workspaces(id,name,slug) VALUES('f6-ws','F6 fixture','f6-fixture')`); err != nil {
				t.Fatal(err)
			}
			logger := slog.New(slog.NewTextHandler(io.Discard, nil))
			old := journal.NewWriter(db, logger, journal.WriterOptions{FlushInterval: time.Hour, ChainKey: journal.DeriveChainKey(oldSeed)})
			id, err := old.EmitSync(t.Context(), journal.Entry{WorkspaceID: "f6-ws", Type: journal.EntryInvitationCreated, ActorType: journal.ActorSystem, Summary: "Before rotation"})
			if err != nil {
				t.Fatal(err)
			}
			old.Close()
			baseline, err := journal.VerifyChain(t.Context(), db, "f6-ws")
			if err != nil || !baseline.OK {
				t.Fatalf("legacy calibration failed: %+v %v", baseline, err)
			}
			var before string
			if err := db.QueryRow(`SELECT entry_hash FROM journal_entries WHERE id=?`, id).Scan(&before); err != nil {
				t.Fatal(err)
			}
			dump, err := backup.DumpWorkspace(t.Context(), db, "f6-ws")
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv("ENCRYPTION_KEY", "synthetic-foreign-host-seed")
			if mode == "foreign_restore_missing_key" {
				target := testutil.MigratedDB(t).DB
				if err := backup.RestoreDump(t.Context(), target, dump); err != nil {
					t.Fatalf("native restore calibration: %v", err)
				}
				db = target
			} else {
				next := sha256.Sum256([]byte("synthetic-active-audit-key-two"))
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
			current := journal.NewWriter(db, logger, journal.WriterOptions{FlushInterval: time.Hour})
			defer current.Close()
			_, err = current.EmitSync(t.Context(), journal.Entry{WorkspaceID: "f6-ws", Type: journal.EntryInvitationCreated, ActorType: journal.ActorSystem, Summary: "After rotation"})
			if mode == "foreign_restore_missing_key" {
				if err == nil {
					t.Error("foreign restored chain was extended without old key or explicit OWNER checkpoint")
				}
				var count int
				if err := db.QueryRow(`SELECT COUNT(*) FROM journal_entries WHERE workspace_id='f6-ws'`).Scan(&count); err != nil {
					t.Fatal(err)
				}
				if count != 1 {
					t.Errorf("rejected foreign append changed history: count=%d", count)
				}
			} else {
				if err != nil {
					t.Fatalf("retained-key rotation refused append: %v", err)
				}
				result, err := journal.VerifyChain(t.Context(), db, "f6-ws")
				if err != nil || !result.OK {
					t.Errorf("retained keys did not verify mixed legacy/new chain: OK=%v err=%v", result != nil && result.OK, err)
				}
			}
			var after string
			if err := db.QueryRow(`SELECT entry_hash FROM journal_entries WHERE id=?`, id).Scan(&after); err != nil {
				t.Fatal(err)
			}
			if before != after {
				t.Error("rotation rewrote historical MAC")
			}
		})
	}
}

func TestAuditDefaultWriterUsesVersionedIdentity(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", "synthetic-default-audit-root")
	t.Setenv("CREWSHIP_AUDIT_KEYRING_PATH", "")
	db := testutil.MigratedDB(t).DB
	if _, err := db.Exec(`INSERT INTO workspaces(id,name,slug) VALUES('default-f6','Default F6','default-f6')`); err != nil {
		t.Fatal(err)
	}
	writer := journal.NewWriter(db, slog.New(slog.NewTextHandler(io.Discard, nil)), journal.WriterOptions{FlushInterval: time.Hour})
	defer writer.Close()
	id, err := writer.EmitSync(t.Context(), journal.Entry{WorkspaceID: "default-f6", Type: journal.EntryInvitationCreated, ActorType: journal.ActorSystem, Summary: "Default current writer"})
	if err != nil {
		t.Fatalf("healthy default write: %v", err)
	}
	var keyID sql.NullString
	var version sql.NullInt64
	if err := db.QueryRow(`SELECT chain_key_id,chain_mac_version FROM journal_entries WHERE id=?`, id).Scan(&keyID, &version); err != nil {
		t.Fatal(err)
	}
	if !keyID.Valid || keyID.String == "" || !version.Valid || version.Int64 != 2 {
		t.Errorf("new default row lacks authenticated signing identity: id_present=%v version=%v", keyID.Valid, version)
	}
}
