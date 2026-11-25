//go:build linux

package backup_test

import (
	"bytes"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"

	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/encryption"
	"github.com/crewship-ai/crewship/internal/journal"
)

// Each bundle has a valid outer checksum. Failures must be discovered by
// inspecting its actual contents and must leave the existing generation intact.
func TestForceValidationRejectsInvalidSnapshot(t *testing.T) {
	f := newInstanceFixture(t)
	for _, tc := range []struct {
		name, want string
		corrupt    func(*testing.T) *backup.InstanceResult
	}{
		{"store totals", "manifest", func(t *testing.T) *backup.InstanceResult {
			bundle := f.create(t, true)
			src, err := os.Open(bundle.Path)
			if err != nil {
				t.Fatal(err)
			}
			defer src.Close()
			m, sealed, closeBundle, err := backup.ReadBundleStream(src, t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer closeBundle()
			payload, err := io.ReadAll(sealed)
			if err != nil {
				t.Fatal(err)
			}
			store := m.Contents.Instance.Stores[backup.StoreMemory]
			store.Bytes++
			m.Contents.Instance.Stores[backup.StoreMemory] = store
			out, err := os.Create(filepath.Join(t.TempDir(), "altered.tar.zst"))
			if err != nil {
				t.Fatal(err)
			}
			if err := backup.WriteBundleStream(out, m, bytes.NewReader(payload), int64(len(payload))); err != nil {
				t.Fatal(err)
			}
			if err := out.Close(); err != nil {
				t.Fatal(err)
			}
			bundle.Path = out.Name()
			return bundle
		}},
		{"invalid SQLite constraint", "integrity", func(t *testing.T) *backup.InstanceResult {
			if _, err := f.db.Exec("CREATE TABLE force_integrity(value INTEGER CHECK(value>0)); PRAGMA ignore_check_constraints=ON; INSERT INTO force_integrity VALUES(-1); PRAGMA ignore_check_constraints=OFF"); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if _, err := f.db.Exec("DROP TABLE force_integrity"); err != nil {
					t.Error(err)
				}
			})
			return f.create(t, true)
		}},
		{"wrong kit key", "cannot open", func(t *testing.T) *backup.InstanceResult {
			t.Setenv(encryption.KeyEnvVar("v2"), strings.Repeat("d4", 32))
			return f.create(t, true)
		}},
		{"newer database", "newer", func(t *testing.T) *backup.InstanceResult {
			if _, err := f.db.Exec("INSERT INTO _migrations(version,name) VALUES(?,?)", database.MaxKnownMigrationVersion()+1, "future"); err != nil {
				t.Fatal(err)
			}
			return f.create(t, true)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bundle := tc.corrupt(t)
			dir := t.TempDir()
			marker := filepath.Join(dir, "operator.txt")
			if err := os.WriteFile(marker, []byte("old generation"), 0600); err != nil {
				t.Fatal(err)
			}
			before := snapshotDir(t, dir)
			rep, err := backup.RecoverInstance(t.Context(), backup.RecoverOptions{BundlePath: bundle.Path, Identities: []age.Identity{f.identity}, DataDir: dir, Force: true})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
			if rep == nil || rep.Result != backup.RestoreResultFailed || rep.PreviousDataDir != "" {
				t.Fatalf("incorrect precommit status: %+v", rep)
			}
			if after := snapshotDir(t, dir); after != before {
				t.Fatalf("target changed: %s", after)
			}
		})
	}
}

func TestForceValidationMigratesOlderDatabase(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", instKeyV1)
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "old.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := database.MigrateBefore(t.Context(), db, slog.New(slog.NewTextHandler(io.Discard, nil)), "convert_waitpoint_identities"); err != nil {
		t.Fatal(err)
	}
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	bundle, err := backup.CreateInstanceBackup(t.Context(), db, backup.InstanceOptions{OutputDir: t.TempDir(), Recipients: []age.Recipient{identity.Recipient()}, Actor: backup.Actor{UserID: "u_admin", Role: "instance_admin"}})
	if err != nil {
		t.Fatal(err)
	}
	rep, err := backup.RecoverInstance(t.Context(), backup.RecoverOptions{BundlePath: bundle.Path, Identities: []age.Identity{identity}, DataDir: t.TempDir(), Force: true})
	if err != nil {
		t.Fatal(err)
	}
	if rep.MigrationsApplied < 1 || rep.PreviousDataDir == "" {
		t.Fatalf("older schema was not upgraded before publication: %+v", rep)
	}
}

func TestForceValidationPublishedPaths(t *testing.T) {
	f := newInstanceFixture(t)
	bundle := f.create(t, true)
	dir := t.TempDir()
	report, err := backup.RecoverInstance(t.Context(), backup.RecoverOptions{BundlePath: bundle.Path, Identities: []age.Identity{f.identity}, DataDir: dir, Force: true})
	if err != nil {
		t.Fatal(err)
	}
	db, err := database.Open("file:" + report.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var body []byte
	if err := db.QueryRow("SELECT report FROM restore_reports WHERE id=?", report.ReportID).Scan(&body); err != nil {
		t.Fatal(err)
	}
	var stored backup.RecoverReport
	if err := json.Unmarshal(body, &stored); err != nil {
		t.Fatal(err)
	}
	if stored.PreviousDataDir != report.PreviousDataDir || stored.DataDir != dir || stored.KeysFile != filepath.Join(dir, backup.RecoveredKeysFile) || stored.Stores[backup.StoreMemory].Path != filepath.Join(dir, "memory") {
		t.Errorf("incorrect paths in stored report: %+v", stored)
	}
	env, err := backup.ReadEnvFile(stored.KeysFile)
	if err != nil {
		t.Fatal(err)
	}
	if env[journal.SigningKeysPathEnv] != filepath.Join(dir, journal.SigningKeysFile) {
		t.Error("staged signing key path was not relocated")
	}
	var ref string
	if err := db.QueryRow("SELECT payload_ref FROM memory_versions WHERE id='mv1'").Scan(&ref); err != nil {
		t.Fatal(err)
	}
	if ref != filepath.Join(dir, "memory", "versions", f.memSHA[:2], f.memSHA) {
		t.Errorf("memory reference = %s", ref)
	}
}

// v140 is a foreground data migration. A ledger missing it but carrying
// later entries exercises the same row-by-row decision Migrate makes on boot.
func TestForceValidationMigrationKeyAuthority(t *testing.T) {
	t.Setenv("ENCRYPTION_KEY", instKeyV1)
	t.Setenv("ENCRYPTION_KEY_V2", "")
	t.Setenv(encryption.KeyVersionEnvVar, "v1")
	db, err := database.Open("file:" + filepath.Join(t.TempDir(), "source.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := database.MigrateSkipping(t.Context(), db.DB, logger, "encrypt_webhook_secrets"); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO users(id,email,full_name) VALUES('u_admin','admin@example.test','Admin')`,
		`INSERT INTO workspaces(id,name,slug) VALUES('ws','Test','test')`,
		`INSERT INTO crews(id,workspace_id,name,slug) VALUES('crew','ws','Test','test')`,
		`INSERT INTO agents(id,crew_id,workspace_id,name,slug,status,webhook_secret) VALUES('agent','crew','ws','Test','test','IDLE','sample webhook')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	create := func(t *testing.T, kit bool) string {
		t.Helper()
		bundle, err := backup.CreateInstanceBackup(t.Context(), db.DB, backup.InstanceOptions{OutputDir: t.TempDir(), Recipients: []age.Recipient{identity.Recipient()}, Actor: backup.Actor{UserID: "u_admin", Role: "instance_admin"}, RecoveryKit: kit})
		if err != nil {
			t.Fatal(err)
		}
		return bundle.Path
	}
	recover := func(t *testing.T, path string) (*backup.RecoverReport, error) {
		t.Helper()
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "operator.txt"), []byte("existing instance"), 0640); err != nil {
			t.Fatal(err)
		}
		targetBefore := forceGenerationSnapshot(t, dir)
		sourceDir := filepath.Dir(path)
		sourceBefore := forceGenerationSnapshot(t, sourceDir)
		rep, err := backup.RecoverInstance(t.Context(), backup.RecoverOptions{BundlePath: path, Identities: []age.Identity{identity}, DataDir: dir, Force: true})
		assertOriginalGeneration(t, sourceDir, sourceBefore)
		if err != nil {
			assertOriginalGeneration(t, dir, targetBefore)
		}
		return rep, err
	}

	noKit := create(t, false)
	for _, tc := range []struct{ name, value, want string }{
		{"missing key below ledger maximum", "", "pending migrations"},
		{"malformed key with zero envelopes", "not-hex", "ENCRYPTION_KEY"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ENCRYPTION_KEY", tc.value)
			rep, err := recover(t, noKit)
			if err == nil || !strings.Contains(err.Error(), tc.want) || rep.Result != backup.RestoreResultFailed || rep.PreviousDataDir != "" {
				t.Fatalf("expected refusal containing %q, got %v, report %+v", tc.want, err, rep)
			}
		})
	}
	t.Run("kit overrides host mint selector", func(t *testing.T) {
		path := create(t, true)
		t.Setenv("ENCRYPTION_KEY_V2", instKeyV2)
		t.Setenv(encryption.KeyVersionEnvVar, "v2")
		rep, err := recover(t, path)
		if err != nil {
			t.Fatal(err)
		}
		if rep.MigrationsApplied != 1 {
			t.Fatalf("applied migrations = %d", rep.MigrationsApplied)
		}
		keys, err := backup.ReadEnvFile(rep.KeysFile)
		if err != nil {
			t.Fatal(err)
		}
		if keys[encryption.KeyVersionEnvVar] != "v1" || keys["ENCRYPTION_KEY_V2"] != "" {
			t.Fatal("recovered assignments did not select the kit's v1 key")
		}
		restored, err := database.Open("file:" + rep.DatabasePath)
		if err != nil {
			t.Fatal(err)
		}
		defer restored.Close()
		var envelope string
		if err := restored.QueryRow("SELECT webhook_secret FROM agents WHERE id='agent'").Scan(&envelope); err != nil {
			t.Fatal(err)
		}
		key, err := hex.DecodeString(keys["ENCRYPTION_KEY"])
		if err != nil {
			t.Fatal(err)
		}
		plain, err := encryption.DecryptWithKeys(envelope, map[string][]byte{"v1": key})
		if err != nil || plain != "sample webhook" {
			t.Fatalf("migrated webhook did not open with emitted v1 key: %v", err)
		}
		if os.Getenv(encryption.KeyVersionEnvVar) != "v2" || os.Getenv("ENCRYPTION_KEY_V2") != instKeyV2 {
			t.Fatal("host key settings were not restored")
		}
	})
	t.Run("recheck migrated envelope before publication", func(t *testing.T) {
		// Model a legacy schema whose trigger damages a newly written webhook
		// envelope. The snapshot itself contains only the valid plaintext row.
		if _, err := db.Exec(`CREATE TRIGGER force_bad_webhook AFTER UPDATE OF webhook_secret ON agents
			WHEN NEW.webhook_secret LIKE 'v1:%' AND NEW.webhook_secret != 'v1:damaged'
			BEGIN UPDATE agents SET webhook_secret='v1:damaged' WHERE id=NEW.id; END`); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if _, err := db.Exec("DROP TRIGGER force_bad_webhook"); err != nil {
				t.Error(err)
			}
		})
		path := create(t, true)
		rep, err := recover(t, path)
		if err == nil || !strings.Contains(err.Error(), "cannot open agents.webhook_secret") || rep.Result != backup.RestoreResultFailed || rep.PreviousDataDir != "" {
			t.Fatalf("damaged migrated envelope: report %+v, error %v", rep, err)
		}
	})
	t.Run("historical envelope uses base key", func(t *testing.T) {
		t.Setenv("ENCRYPTION_KEY_V7", "")
		key, err := hex.DecodeString(instKeyV1)
		if err != nil {
			t.Fatal(err)
		}
		ciphertext, err := encryption.EncryptWithKey("v7", key, "sample webhook")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec("UPDATE agents SET webhook_secret=? WHERE id='agent'", ciphertext); err != nil {
			t.Fatal(err)
		}
		path := create(t, false)
		if _, err = recover(t, path); err != nil {
			t.Fatal(err)
		}
		t.Run("wrong original key", func(t *testing.T) {
			t.Setenv("ENCRYPTION_KEY", instKeyV2)
			rep, err := recover(t, path)
			if err == nil || !strings.Contains(err.Error(), "cannot open agents.webhook_secret") || rep == nil || rep.Result != backup.RestoreResultFailed || rep.PreviousDataDir != "" {
				t.Fatalf("wrong original key: report %+v, error %v", rep, err)
			}
		})
	})
}
