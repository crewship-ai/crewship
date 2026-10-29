package testutil

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/database"
)

// Full export is deliberately opt-in and its package-pass elapsed time is
// charged by the API race aggregate. Ordinary invocations verify refusal of
// an unauthorized export; they do not produce a bundle or bootstrap evidence.
func TestCIRaceTemplateBootstrap(t *testing.T) {
	dir, configured := os.LookupEnv(exportBundleEnv)
	if !configured {
		if err := exportMigratedBundle("", migratedBundleMetadata{}); err == nil || !strings.Contains(err.Error(), "actual race") {
			t.Fatalf("unauthorized export was not refused before migrations: %v", err)
		}
		return
	}
	if _, importing := os.LookupEnv(importBundleEnv); importing {
		t.Fatal("bootstrap must run genuine migrations, not import another bundle")
	}
	identity, err := currentBundleIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if err := exportMigratedBundle(dir, identity); err != nil {
		t.Fatal(err)
	}
}

func tinyMigratedBundle(t *testing.T) (string, migratedBundleMetadata) {
	t.Helper()
	dir := t.TempDir()
	db, err := database.Open("file:"+filepath.Join(dir, bundleDatabaseName), testSynchronous)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.DB.Exec("CREATE TABLE bundle_probe(value TEXT); INSERT INTO bundle_probe VALUES('baseline')"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, bundleDatabaseName))
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(data)
	identity := migratedBundleMetadata{
		Version: 1, SourceSHA: strings.Repeat("a", 40), RunID: "123", RunAttempt: "1",
		MigrationFingerprint: strings.Repeat("b", 64), FixtureKey: "source-closure",
		GoVersion: "test-go", GOOS: "linux", GOARCH: "amd64", Race: true,
		TemplateSize: int64(len(data)), TemplateSHA256: hex.EncodeToString(hash[:]),
	}
	writeBundleMetadata(t, dir, identity)
	return dir, identity
}

func writeBundleMetadata(t *testing.T, dir string, metadata migratedBundleMetadata) {
	t.Helper()
	data, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, bundleMetadataName), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestMigratedBundleRejectsIdentityAndInstrumentationDrift(t *testing.T) {
	_, good := tinyMigratedBundle(t)
	for _, field := range []string{"version", "source", "run", "future-attempt", "bad-attempt", "fingerprint", "closure", "go", "os", "arch", "race", "size", "hash"} {
		t.Run(field, func(t *testing.T) {
			dir, _ := tinyMigratedBundle(t)
			bad := good
			switch field {
			case "version":
				bad.Version++
			case "source":
				bad.SourceSHA = strings.Repeat("c", 40)
			case "run":
				bad.RunID = "124"
			case "future-attempt":
				bad.RunAttempt = "2"
			case "bad-attempt":
				bad.RunAttempt = "0"
			case "fingerprint":
				bad.MigrationFingerprint = strings.Repeat("d", 64)
			case "closure":
				bad.FixtureKey = "other-closure"
			case "go":
				bad.GoVersion = "another-go"
			case "os":
				bad.GOOS = "another-os"
			case "arch":
				bad.GOARCH = "another-arch"
			case "race":
				bad.Race = false
			case "size":
				bad.TemplateSize++
			case "hash":
				bad.TemplateSHA256 = strings.Repeat("f", 64)
			}
			writeBundleMetadata(t, dir, bad)
			if path, err := importMigratedBundleFor(dir, good); err == nil {
				os.RemoveAll(filepath.Dir(path))
				t.Fatal("import accepted changed " + field)
			}
		})
	}
	dir, identity := tinyMigratedBundle(t)
	identity.Race = false
	if _, err := importMigratedBundleFor(dir, identity); err == nil {
		t.Fatal("non-race consumer accepted race evidence")
	}
	if err := exportMigratedBundle(dir, identity); err == nil || !strings.Contains(err.Error(), "actual race") {
		t.Fatalf("non-race export: %v", err)
	}
}

func TestMigratedBundleAllowsEarlierAttemptOnlyWithinSameRun(t *testing.T) {
	dir, identity := tinyMigratedBundle(t)
	identity.RunAttempt = "2"
	path, err := importMigratedBundleFor(dir, identity)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(filepath.Dir(path)) })
	identity.RunID = "125"
	if _, err := importMigratedBundleFor(dir, identity); err == nil {
		t.Fatal("earlier attempt from another run was accepted")
	}
}

func TestMigratedBundleRejectsMissingCorruptAndSymlinkEntries(t *testing.T) {
	for _, kind := range []string{"missing-metadata", "missing-db", "metadata-link", "db-link", "directory-link", "corrupt-db", "trailing-json", "oversized-metadata"} {
		t.Run(kind, func(t *testing.T) {
			dir, identity := tinyMigratedBundle(t)
			metadata, db := filepath.Join(dir, bundleMetadataName), filepath.Join(dir, bundleDatabaseName)
			switch kind {
			case "missing-metadata":
				os.Remove(metadata)
			case "missing-db":
				os.Remove(db)
			case "metadata-link", "db-link", "directory-link":
				path := map[string]string{"metadata-link": metadata, "db-link": db, "directory-link": dir}[kind]
				if err := os.Rename(path, path+"-real"); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { os.RemoveAll(path + "-real") })
				if err := os.Symlink(path+"-real", path); err != nil {
					t.Fatalf("create required symlink fixture: %v", err)
				}
			case "corrupt-db":
				data, err := os.ReadFile(db)
				if err != nil {
					t.Fatal(err)
				}
				data[len(data)-1] ^= 1
				if err := os.WriteFile(db, data, 0o600); err != nil {
					t.Fatal(err)
				}
			case "trailing-json", "oversized-metadata":
				file, err := os.OpenFile(metadata, os.O_APPEND|os.O_WRONLY, 0)
				if err != nil {
					t.Fatal(err)
				}
				suffix := "{}"
				if kind == "oversized-metadata" {
					suffix = strings.Repeat(" ", 64<<10)
				}
				if _, err := file.WriteString(suffix); err != nil {
					t.Fatal(err)
				}
				if err := file.Close(); err != nil {
					t.Fatal(err)
				}
			}
			if path, err := importMigratedBundleFor(dir, identity); err == nil {
				os.RemoveAll(filepath.Dir(path))
				t.Fatal("import accepted " + kind)
			}
		})
	}
}

func TestMigratedBundlePrivateCopiesKeepRowsAndProductionPragmas(t *testing.T) {
	dir, identity := tinyMigratedBundle(t)
	template, err := importMigratedBundleFor(dir, identity)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(filepath.Dir(template)) })
	if template == filepath.Join(dir, bundleDatabaseName) {
		t.Fatal("import reused downloaded source directly")
	}
	info, err := os.Stat(template)
	if err != nil || info.Mode().Perm() != 0o400 {
		t.Fatalf("template is not immutable: %v %v", info, err)
	}
	var copies []*database.DB
	for i := 0; i < 2; i++ {
		path := filepath.Join(t.TempDir(), "private.db")
		if err := copyFileForTest(template, path); err != nil {
			t.Fatal(err)
		}
		db, err := database.Open("file:"+path, testSynchronous)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { quiesce(db, path) })
		copies = append(copies, db)
		for pragma, expected := range map[string]string{"foreign_keys": "1", "journal_mode": "wal", "synchronous": "1"} {
			var got string
			if err := db.DB.QueryRow("PRAGMA " + pragma).Scan(&got); err != nil || got != expected {
				t.Fatalf("%s=%q: %v", pragma, got, err)
			}
		}
		if db.DB.Stats().MaxOpenConnections != 5 {
			t.Fatal("production connection cap changed")
		}
	}
	if _, err := copies[0].DB.Exec("INSERT INTO bundle_probe VALUES('private-first')"); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := copies[1].DB.QueryRow("SELECT count(*) FROM bundle_probe").Scan(&count); err != nil || count != 1 {
		t.Fatalf("rows leaked between copies: %d %v", count, err)
	}
	data, err := os.ReadFile(template)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(data)
	if hex.EncodeToString(hash[:]) != identity.TemplateSHA256 {
		t.Fatal("test writes changed imported template")
	}
	// Subsequent mutation of the downloaded artifact cannot change the
	// already-verified local immutable template.
	if err := os.WriteFile(filepath.Join(dir, bundleDatabaseName), []byte("replaced"), 0o600); err != nil {
		t.Fatal(err)
	}
	again, err := os.ReadFile(template)
	if err != nil || string(again) != string(data) {
		t.Fatal("import retained an alias of downloaded bytes")
	}
}

func TestMigratedBundleConfiguredImportFailsClosed(t *testing.T) {
	if os.Getenv("CREWSHIP_TEST_BUNDLE_CHILD") == "1" {
		if _, err := templatePath(); err == nil {
			t.Fatal("invalid explicit import fell back to the local template")
		}
		return
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, executable, "-test.run=^TestMigratedBundleConfiguredImportFailsClosed$")
	command.Env = append(os.Environ(), "CREWSHIP_TEST_BUNDLE_CHILD=1", importBundleEnv+"=", "GITHUB_SHA="+strings.Repeat("a", 40), "GITHUB_RUN_ID=123", "GITHUB_RUN_ATTEMPT=1")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("fail-closed child: %v %s", err, output)
	}
}

// Exercise the actual templatePath -> openMigratedCopy path in a fresh process,
// without modifying the package's sync.Once or building a full schema here.
// Non-race runs verify that a bundle claiming race cannot enable inheritance;
// race runs verify that its actual rows reach two isolated normal fixtures.
func TestMigratedBundleConfiguredImportUsesVerifiedRows(t *testing.T) {
	if mode := os.Getenv("CREWSHIP_TEST_BUNDLE_VALID_CHILD"); mode != "" {
		_, err := templatePath()
		if mode == "reject" {
			if err == nil || !strings.Contains(err.Error(), "race instrumentation") {
				t.Fatalf("non-race consumer accepted claimed instrumentation: %v", err)
			}
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		first, second := MigratedSQLDB(t), MigratedSQLDB(t)
		if _, err := first.Exec("INSERT INTO bundle_probe VALUES('child-private')"); err != nil {
			t.Fatal(err)
		}
		var count, foreignKeys int
		if err := second.QueryRow("SELECT count(*) FROM bundle_probe").Scan(&count); err != nil || count != 1 {
			t.Fatalf("imported fixture rows lost or leaked: %d %v", count, err)
		}
		if err := second.QueryRow("PRAGMA foreign_keys").Scan(&foreignKeys); err != nil || foreignKeys != 1 {
			t.Fatalf("normal fixture foreign keys differ: %d %v", foreignKeys, err)
		}
		return
	}
	t.Setenv("GITHUB_SHA", strings.Repeat("a", 40))
	t.Setenv("GITHUB_RUN_ID", "123")
	t.Setenv("GITHUB_RUN_ATTEMPT", "2")
	identity, err := currentBundleIdentity()
	if err != nil {
		t.Fatal(err)
	}
	dir, tiny := tinyMigratedBundle(t)
	mode := "accept"
	if !identity.Race {
		mode = "reject"
	}
	identity.Race = true
	identity.RunAttempt = "1" // a failed-job rerun may reuse the earlier bootstrap
	identity.TemplateSize, identity.TemplateSHA256 = tiny.TemplateSize, tiny.TemplateSHA256
	writeBundleMetadata(t, dir, identity)
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, executable, "-test.run=^TestMigratedBundleConfiguredImportUsesVerifiedRows$")
	command.Env = append(os.Environ(), "CREWSHIP_TEST_BUNDLE_VALID_CHILD="+mode, importBundleEnv+"="+dir)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("actual bundle fixture child: %v %s", err, output)
	}
}

func TestMigratedBundleExportRefusesExistingFilesBeforeMigration(t *testing.T) {
	dir, identity := tinyMigratedBundle(t)
	if err := exportMigratedBundle(dir, identity); err == nil || !strings.Contains(err.Error(), "existing destination") {
		t.Fatalf("existing export was not refused before migrations: %v", err)
	}
}
