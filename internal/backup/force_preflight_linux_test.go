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

func TestForcePreflightRefusesForeignKeyViolation(t *testing.T) {
	f := newInstanceFixture(t)
	// The snapshot has valid SQLite pages and a valid digest, but a missing
	// parent. WITHOUT ROWID also exercises the nullable foreign_key_check rowid.
	_, err := f.db.ExecContext(t.Context(), `
		CREATE TABLE force_parent(id TEXT PRIMARY KEY);
		CREATE TABLE force_child(id TEXT PRIMARY KEY REFERENCES force_parent(id)) WITHOUT ROWID;
		PRAGMA foreign_keys=OFF;
		INSERT INTO force_child VALUES ('orphan');
		PRAGMA foreign_keys=ON;`)
	if err != nil {
		t.Fatal(err)
	}
	bundle := f.create(t, true)
	target := t.TempDir()
	if err := os.WriteFile(filepath.Join(target, "operator.txt"), []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	before := forceGenerationSnapshot(t, target)
	report, err := backup.RecoverInstance(t.Context(), backup.RecoverOptions{
		BundlePath: bundle.Path, Identities: []age.Identity{f.identity}, DataDir: target, Force: true,
	})
	if err == nil || !strings.Contains(err.Error(), "foreign_key_check") || !strings.Contains(err.Error(), "force_child") {
		t.Fatalf("expected refusal for orphan in force_child, got %v", err)
	}
	if report == nil || report.Result != backup.RestoreResultFailed || report.PreviousDataDir != "" {
		t.Fatalf("unexpected refusal report: %+v", report)
	}
	assertOriginalGeneration(t, target, before)
}
