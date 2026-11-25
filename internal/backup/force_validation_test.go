//go:build linux

package backup_test

import (
	"bytes"
	"database/sql"
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
