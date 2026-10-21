package backup_test

import (
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"

	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/journal"
)

func TestRecoveryKitCarriesIndependentRetainedAuditKeyring(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", strings.Repeat("ab", 32))
	t.Setenv("CREWSHIP_ENCRYPTION_KEY_VERSION", "")
	keys, path, err := journal.EnsureSigningKeys(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(journal.SigningKeysPathEnv, path)
	kit, missing, err := backup.BuildRecoveryKit(backup.VaultScan{}, time.Now())
	if err != nil || len(missing) != 0 {
		t.Fatalf("healthy kit: %v", err)
	}
	raw, err := json.Marshal(kit)
	if err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		AuditKeyring json.RawMessage `json:"audit_keyring"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.AuditKeyring) == 0 {
		t.Fatal("instance RecoveryKit omitted the independent retained audit keyring")
	}
	recovered, err := journal.ParseSigningKeys(envelope.AuditKeyring)
	if err != nil || recovered.ActiveID() != keys.ActiveID() {
		t.Fatalf("recovered active audit identity differs: %v", err)
	}
	want, err := journal.ExportSigningKeys(keys)
	if err != nil {
		t.Fatal(err)
	}
	got, err := journal.ExportSigningKeys(recovered)
	if err != nil || string(got) != string(want) {
		t.Fatal("RecoveryKit changed retained audit identities")
	}
}

func TestAuditRecoveryKitRestoresAndDrillsBothChainsOnForeignHost(t *testing.T) {
	f := newInstanceFixture(t)
	keys, path, err := journal.EnsureSigningKeys(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(journal.SigningKeysPathEnv, path)
	writer := journal.NewWriter(f.db, slog.New(slog.NewTextHandler(io.Discard, nil)), journal.WriterOptions{SigningKeys: keys})
	if _, err := writer.EmitSync(t.Context(), journal.Entry{WorkspaceID: "ws_e2e_1", Type: journal.EntryInvitationCreated, ActorType: journal.ActorSystem, Summary: "Independent current audit key"}); err != nil {
		t.Fatal(err)
	}
	writer.Close()
	fields := journal.InstanceAuditFields{ID: "f6-native-instance", Action: "fixture.audit", EntityType: "instance", CreatedAt: "2026-10-20T12:00:00.000000000Z"}
	id, version := journal.ActiveSigningIdentity(keys)
	hash, err := journal.InstanceAuditMACForKeys(keys, id, version, 1, "", fields)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Exec(`INSERT INTO instance_audit_logs(id,action,entity_type,created_at,chain_seq,prev_hash,entry_hash,chain_key_id,chain_mac_version) VALUES(?,?,?,?,1,'',?,?,?)`, fields.ID, fields.Action, fields.EntityType, fields.CreatedAt, hash, id, version); err != nil {
		t.Fatal(err)
	}
	created := f.create(t, true)
	t.Setenv("ENCRYPTION_KEY", strings.Repeat("cd", 32))
	t.Setenv(journal.SigningKeysPathEnv, "")
	root := filepath.Join(t.TempDir(), "recovered")
	restored, err := backup.RecoverInstance(t.Context(), backup.RecoverOptions{BundlePath: created.Path, Identities: []age.Identity{f.identity}, DataDir: root})
	if err != nil {
		t.Fatal(err)
	}
	recoveredKeys, err := journal.LoadSigningKeys(filepath.Join(root, journal.SigningKeysFile))
	if err != nil || recoveredKeys.ActiveID() != keys.ActiveID() {
		t.Fatalf("recovery did not publish the original audit snapshot: %v", err)
	}
	db, err := database.Open("file:" + restored.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	workspace, err := journal.VerifyChainWithKeys(t.Context(), db.DB, "ws_e2e_1", recoveredKeys)
	if err != nil || !workspace.OK {
		t.Fatalf("recovered workspace audit: %+v %v", workspace, err)
	}
	instance, err := journal.VerifyInstanceAuditWithKeys(t.Context(), db.DB, recoveredKeys)
	if err != nil || !instance.OK || instance.Count != 1 {
		t.Fatalf("recovered instance audit: %+v %v", instance, err)
	}
	var stored sql.NullString
	if err := db.QueryRow(`SELECT entry_hash FROM instance_audit_logs WHERE id=?`, fields.ID).Scan(&stored); err != nil || stored.String != hash {
		t.Fatal("normal instance recovery changed historical MAC")
	}
	drill, err := backup.DrillInstance(t.Context(), backup.DrillOptions{BundlePath: created.Path, Identities: []age.Identity{f.identity}, TempRoot: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	var audit *backup.DrillCheck
	for i := range drill.Checks {
		if drill.Checks[i].Name == backup.DrillCheckJournal {
			audit = &drill.Checks[i]
		}
	}
	if audit == nil || audit.Status != backup.DrillStatusOK || audit.Checked != 3 {
		t.Fatalf("drill did not verify both workspace chains and instance: %+v", audit)
	}
}

func TestAuditVersionedForkRestoreKeepsExplicitNewGenesis(t *testing.T) {
	testAuditVersionedFork(t, false)
}

func TestAuditVersionedRetentionCheckpointFork(t *testing.T) {
	testAuditVersionedFork(t, true)
}

func testAuditVersionedFork(t *testing.T, compact bool) {
	t.Helper()
	t.Setenv("ENCRYPTION_KEY", strings.Repeat("ab", 32))
	keys, path, err := journal.EnsureSigningKeys(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(journal.SigningKeysPathEnv, path)
	db := openMigratedDB(t)
	ws := seedWorkspace(t, db)
	if _, err := db.Exec(`DELETE FROM journal_entries WHERE workspace_id=?`, ws); err != nil {
		t.Fatal(err)
	}
	writer := journal.NewWriter(db, slog.New(slog.NewTextHandler(io.Discard, nil)), journal.WriterOptions{SigningKeys: keys})
	for i := 0; i < 3; i++ {
		if _, err := writer.EmitSync(t.Context(), journal.Entry{WorkspaceID: ws, Type: journal.EntryInvitationCreated, ActorType: journal.ActorSystem, Summary: "Synthetic versioned history"}); err != nil {
			t.Fatal(err)
		}
	}
	writer.Close()
	if compact {
		var removed journal.RemovedEntry
		if err := db.QueryRow(`SELECT seq,entry_hash FROM journal_entries WHERE workspace_id=? AND seq=2`, ws).Scan(&removed.Seq, &removed.Hash); err != nil {
			t.Fatal(err)
		}
		tx, err := db.BeginTx(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		if err := journal.WriteChainCheckpointWithKeys(t.Context(), tx, keys, ws, []journal.RemovedEntry{removed}); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(`DELETE FROM journal_entries WHERE workspace_id=? AND seq=2`, ws); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	healthy, err := journal.VerifyChainWithKeys(t.Context(), db, ws, keys)
	if err != nil || !healthy.OK {
		t.Fatalf("healthy source: %+v %v", healthy, err)
	}
	actor := backup.Actor{UserID: "u_admin", Email: "admin@e2e.test", Role: "ADMIN"}
	created, err := backup.CreateBackup(t.Context(), db, backup.CreateOptions{Scope: backup.ScopeWorkspace, WorkspaceID: ws, OutputDir: t.TempDir(), Actor: actor, Passphrase: "synthetic-audit-fork-passphrase"})
	if err != nil {
		t.Fatal(err)
	}
	restored, err := backup.RestoreBackup(t.Context(), db, backup.RestoreOptions{Path: created.Path, Passphrase: "synthetic-audit-fork-passphrase", Actor: actor, AsWorkspace: "f6-versioned-fork"})
	if err != nil {
		t.Fatal(err)
	}
	var fork string
	if err := db.QueryRow(`SELECT id FROM workspaces WHERE slug='f6-versioned-fork'`).Scan(&fork); err != nil {
		t.Fatal(err)
	}
	result, err := journal.VerifyChainWithKeys(t.Context(), db, fork, keys)
	if err != nil || !result.OK {
		t.Errorf("versioned fork failed verification: %+v %v", result, err)
	}
	var all, versioned, notices int
	if err := db.QueryRow(`SELECT COUNT(*),COUNT(CASE WHEN chain_key_id=? AND chain_mac_version=2 THEN 1 END),COUNT(CASE WHEN entry_type='backup.chain_resigned' THEN 1 END) FROM journal_entries WHERE workspace_id=?`, keys.ActiveID(), fork).Scan(&all, &versioned, &notices); err != nil {
		t.Fatal(err)
	}
	want := 4
	if compact {
		want--
		var checkpoints int
		if err := db.QueryRow(`SELECT COUNT(*) FROM journal_chain_checkpoints WHERE workspace_id=? AND chain_key_id=? AND chain_mac_version=2`, fork, keys.ActiveID()).Scan(&checkpoints); err != nil {
			t.Fatal(err)
		}
		if checkpoints != 1 || restored.JournalCheckpointsResigned != 1 {
			t.Error("versioned retention checkpoint was not authenticated and re-signed under fork identity")
		}
	}
	if all != want || versioned != want || notices != 1 {
		t.Errorf("new genesis missing versioned identity/provenance: total=%d versioned=%d notices=%d restored=%v", all, versioned, notices, restored != nil)
	}
}
