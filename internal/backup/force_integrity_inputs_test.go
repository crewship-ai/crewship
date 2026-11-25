//go:build linux

package backup_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"

	"github.com/crewship-ai/crewship/internal/backup"
)

func TestForceIntegrityVerificationPreservesInputs(t *testing.T) {
	f := newInstanceFixture(t)
	good := f.create(t, true)
	// Bundle names have timestamp precision. Keep both inputs independently
	// addressable even if both backups complete within the same second.
	f.outputDir = t.TempDir()
	if _, err := f.db.Exec(`CREATE TABLE fixture_constraint(value INTEGER CHECK(value>0));
		PRAGMA ignore_check_constraints=ON;
		INSERT INTO fixture_constraint VALUES(-1);
		PRAGMA ignore_check_constraints=OFF`); err != nil {
		t.Fatal(err)
	}
	bad := f.create(t, true)

	var sourceDB string
	if err := f.db.QueryRow("SELECT file FROM pragma_database_list WHERE name='main'").Scan(&sourceDB); err != nil {
		t.Fatal(err)
	}
	// Copy the idle installation with real WAL frames and
	// their matching SHM index. Closing the setup handle normally checkpoints
	// and removes these files; the installed copy must retain them verbatim.
	if _, err := f.db.Exec("INSERT INTO fixture_constraint VALUES(1)"); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "installation")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		body, err := os.ReadFile(sourceDB + suffix)
		if err != nil || len(body) == 0 {
			t.Fatalf("read installed fixture %s%s: %d bytes, %v", sourceDB, suffix, len(body), err)
		}
		if err := os.WriteFile(filepath.Join(target, "crewship.db"+suffix), body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	// A cold rollback journal has a zeroed header. Preserve this inactive
	// journal too, alongside the current WAL-mode installation.
	if err := os.WriteFile(filepath.Join(target, "crewship.db-journal"), make([]byte, 512), 0600); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Close(); err != nil {
		t.Fatal(err)
	}

	// The full directory snapshots fingerprint existing SQLite sidecars and
	// detect creation of any previously absent sidecar, including -journal.
	sources := map[string]map[string]forceEntry{}
	for _, dir := range []string{filepath.Dir(sourceDB), filepath.Dir(good.Path), filepath.Dir(bad.Path)} {
		sources[dir] = forceGenerationSnapshot(t, dir)
	}
	installed := forceGenerationSnapshot(t, target)
	opts := backup.RecoverOptions{BundlePath: bad.Path, Identities: []age.Identity{f.identity}, DataDir: target, Force: true}
	rep, err := backup.RecoverInstance(t.Context(), opts)
	if err == nil || !strings.Contains(err.Error(), "CHECK constraint failed in fixture_constraint") {
		t.Fatalf("invalid staged database: %v", err)
	}
	if rep == nil || rep.Result != backup.RestoreResultFailed || rep.PreviousDataDir != "" {
		t.Fatalf("unexpected refusal report: %+v", rep)
	}
	assertOriginalGeneration(t, target, installed)
	for dir, before := range sources {
		assertOriginalGeneration(t, dir, before)
	}

	// The installed DB still contains the invalid row. A successful restore
	// of the good bundle therefore also identifies which DB was checked.
	opts.BundlePath = good.Path
	rep, err = backup.RecoverInstance(t.Context(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Result != backup.RestoreResultOK || rep.PreviousDataDir == "" {
		t.Fatalf("unexpected success report: %+v", rep)
	}
	assertOriginalGeneration(t, rep.PreviousDataDir, installed)
	for dir, before := range sources {
		assertOriginalGeneration(t, dir, before)
	}
}
