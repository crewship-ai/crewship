package api

import (
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"

	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/encryption"
	"github.com/crewship-ai/crewship/internal/journal"
	"github.com/crewship-ai/crewship/internal/quiesce"
)

type journalAuditTail struct {
	fields journal.InstanceAuditFields
	proof  instanceAuditProof
}

func (tail *journalAuditTail) read(t *testing.T, db *sql.DB) {
	t.Helper()
	if err := db.QueryRow(`SELECT ` + instanceAuditColumns + `,` + instanceAuditChainColumns + ` FROM instance_audit_logs a` + instanceAuditPredecessor + ` ORDER BY a.chain_seq DESC LIMIT 1`).Scan(tail.proof.scanArgs(&tail.fields)...); err != nil {
		t.Fatal(err)
	}
}

func rotationCreateToken(t *testing.T, db *sql.DB, user string) int {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/v1/auth/cli-token", strings.NewReader(`{}`))
	req = req.WithContext(withUser(req.Context(), &AuthUser{ID: user}))
	rr := httptest.NewRecorder()
	NewCLITokenHandler(db, newTestLogger()).Create(rr, req)
	return rr.Code // never log the issued token body
}

func TestInstanceAuditSupportedVersionedRotation(t *testing.T) {
	reencSetV1(t)
	f := newTokenAuditFixture(t, "create")
	if rr := f.invoke(); rr.Code != 200 {
		t.Fatalf("healthy v1 create: HTTP%d", rr.Code)
	}
	reencRotateToV2(t) // the documented configuration retains the original v1
	if rr := f.invoke(); rr.Code != 200 {
		t.Fatalf("supported v2 rotation with retained v1: HTTP%d", rr.Code)
	}
	var fields journalAuditTail
	fields.read(t, f.db)
	if fields.proof.integrity(fields.fields) != "entry_verified" {
		t.Fatal("retained source key did not verify the tail")
	}
	// Loss of the v1 identity is not permission to trust or reseal history.
	t.Setenv("ENCRYPTION_KEY", reencKeyV2)
	if rr := f.invoke(); rr.Code != 500 {
		t.Fatalf("unknown audit key must refuse append: HTTP%d", rr.Code)
	}
	var tokens int
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM cli_tokens`).Scan(&tokens); err != nil || tokens != 2 {
		t.Fatalf("wrong-key append did not roll back: count=%d err=%v", tokens, err)
	}
	t.Setenv("ENCRYPTION_KEY", reencKeyV1)
	execOrFatal(t, f.db, `UPDATE instance_audit_logs SET metadata='tampered' WHERE chain_seq=(SELECT MAX(chain_seq) FROM instance_audit_logs)`)
	if rr := f.invoke(); rr.Code != 500 {
		t.Fatalf("tampered tail must refuse append: HTTP%d", rr.Code)
	}
	if err := f.db.QueryRow(`SELECT COUNT(*) FROM cli_tokens`).Scan(&tokens); err != nil || tokens != 2 {
		t.Fatalf("tampered-tail append did not roll back: count=%d err=%v", tokens, err)
	}
}

func TestInstanceAuditWorkspaceNormalAndForkScope(t *testing.T) {
	for _, fork := range []bool{false, true} {
		t.Run(map[bool]string{false: "normal", true: "fork"}[fork], func(t *testing.T) {
			reencSetV1(t)
			source := newTokenAuditFixture(t, "create")
			workspace := seedTestWorkspace(t, source.db, source.owner)
			if rr := source.invoke(); rr.Code != 200 {
				t.Fatalf("healthy source audit: HTTP%d", rr.Code)
			}
			dump, err := backup.DumpWorkspace(t.Context(), source.db, workspace)
			if err != nil {
				t.Fatal(err)
			}
			if _, present := dump.Tables["instance_audit_logs"]; present {
				t.Fatal("workspace dump included instance-global audit")
			}
			if fork {
				if err := backup.RemapIDs(t.Context(), source.db, dump); err != nil {
					t.Fatal(err)
				}
			}
			// A different instance has its own root key and existing trail.
			t.Setenv("ENCRYPTION_KEY", reencKeyV2)
			target := newTokenAuditFixture(t, "create")
			if rr := target.invoke(); rr.Code != 200 {
				t.Fatalf("healthy destination audit: HTTP%d", rr.Code)
			}
			var before journalAuditTail
			before.read(t, target.db)
			if err := backup.RestoreDump(t.Context(), target.db, dump); err != nil {
				t.Fatal(err)
			}
			var after journalAuditTail
			after.read(t, target.db)
			if before != after || after.proof.integrity(after.fields) != "entry_verified" {
				t.Fatal("workspace restore changed the destination instance trail")
			}
			if rr := target.invoke(); rr.Code != 200 {
				t.Fatalf("workspace restore blocked destination append: HTTP%d", rr.Code)
			}
		})
	}
}

func TestInstanceAuditRecoveryAfterCompleteReencrypt(t *testing.T) {
	reencSetV1(t)
	f := newTokenAuditFixture(t, "create")
	workspace := seedTestWorkspace(t, f.db, f.owner)
	if rr := f.invoke(); rr.Code != 200 {
		t.Fatalf("healthy source audit: HTTP%d", rr.Code)
	}
	sealed := reencMustEncrypt(t, "synthetic-rotation-value")
	execOrFatal(t, f.db, `INSERT INTO credentials(id,workspace_id,name,encrypted_value,created_by) VALUES('rotation-c',?,'Rotation',?,?)`, workspace, sealed, f.owner)
	reencRotateToV2(t)
	req := withWorkspaceUser(httptest.NewRequest("POST", "/api/v1/admin/reencrypt", nil), f.owner, workspace, "OWNER")
	rr := httptest.NewRecorder()
	NewReencryptHandler(f.db, newTestLogger()).Reencrypt(rr, req)
	var result reencryptResponse
	if rr.Code != 200 || json.Unmarshal(rr.Body.Bytes(), &result) != nil || result.Failed != 0 || result.Reencrypted < 1 {
		t.Fatalf("actual complete reencrypt did not succeed: HTTP%d failed=%d changed=%d", rr.Code, result.Failed, result.Reencrypted)
	}
	scan, err := backup.ScanVaultEnvelopes(t.Context(), f.db)
	if err != nil || scan.Versions["v1"] != 0 || scan.Versions["v2"] < 1 {
		t.Fatalf("expected only v2 envelopes after actual reencrypt: %v %v", scan.Versions, err)
	}
	if rr := f.invoke(); rr.Code != 200 {
		t.Fatalf("supported retained-v1 source cannot append: HTTP%d", rr.Code)
	}
	var original journalAuditTail
	original.read(t, f.db)
	if original.proof.integrity(original.fields) != "entry_verified" {
		t.Fatal("source audit was not verified before capture")
	}
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	// These are disposable real backup/recovery fixtures, not retained
	// evidence archives. Every path is owned by this test's TempDir.
	root := t.TempDir()
	for _, dir := range []string{"output", "memory", "chats", "skills"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	created, err := backup.CreateInstanceBackup(t.Context(), f.db, backup.InstanceOptions{
		OutputDir: filepath.Join(root, "backups"), Actor: backup.Actor{UserID: f.owner, Role: "instance_admin"},
		Recipients: []age.Recipient{identity.Recipient()}, RecoveryKit: true, Quiesce: quiesce.New(), Poll: time.Millisecond,
		Paths: backup.InstancePaths{Output: filepath.Join(root, "output"), Memory: filepath.Join(root, "memory"), Chats: filepath.Join(root, "chats"), Skills: filepath.Join(root, "skills")},
	})
	if err != nil {
		t.Fatalf("real instance capture: %v", err)
	}
	recovered, err := backup.RecoverInstance(t.Context(), backup.RecoverOptions{BundlePath: created.Path, Identities: []age.Identity{identity}, DataDir: filepath.Join(root, "restored"), Actor: f.owner})
	if err != nil || recovered.Result != backup.RestoreResultOK {
		t.Fatalf("real instance recovery: %v", err)
	}
	keys, err := backup.ReadEnvFile(recovered.KeysFile)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate the recovered server using ONLY its legitimate returned
	// environment, without accidentally borrowing the source process's v1.
	t.Setenv("ENCRYPTION_KEY", keys["ENCRYPTION_KEY"])
	t.Setenv("ENCRYPTION_KEY_V2", keys["ENCRYPTION_KEY_V2"])
	t.Setenv(encryption.KeyVersionEnvVar, keys[encryption.KeyVersionEnvVar])
	db, err := database.Open("file:" + recovered.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var restored journalAuditTail
	restored.read(t, db.DB)
	if restored != original {
		t.Fatal("recovery changed stored audit bytes/proof")
	}
	status := rotationCreateToken(t, db.DB, f.owner)
	if keys["ENCRYPTION_KEY"] != reencKeyV1 || restored.proof.integrity(restored.fields) != "entry_verified" || status != 200 {
		t.Fatalf("supported recovery lost retained audit key: v1_present=%v integrity=%s append_HTTP%d", keys["ENCRYPTION_KEY"] == reencKeyV1, restored.proof.integrity(restored.fields), status)
	}
}
