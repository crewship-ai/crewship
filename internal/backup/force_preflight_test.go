package backup_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"filippo.io/age"

	"github.com/crewship-ai/crewship/internal/backup"
)

func TestForcePreflightRefusesDrillAndUnsafeDatabasePath(t *testing.T) {
	for _, tc := range []struct {
		target string
		drill  bool
		want   string
	}{
		{"data", true, "drill"},
		{"data?options", false, "SQLite URI delimiters"},
		{"data#fragment", false, "SQLite URI delimiters"},
		{"data%2Fother", false, "SQLite URI delimiters"},
		{"parent%2Fother/data", false, "SQLite URI delimiters"},
	} {
		t.Run(tc.target, func(t *testing.T) {
			parent := t.TempDir()
			_, err := backup.RecoverInstance(t.Context(), backup.RecoverOptions{
				BundlePath: filepath.Join(parent, "absent.tar.zst"), Passphrase: "test",
				DataDir: filepath.Join(parent, tc.target), Force: true, Drill: tc.drill,
			})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected %q preflight refusal, got %v", tc.want, err)
			}
			if entries, err := os.ReadDir(parent); err != nil || len(entries) != 0 {
				t.Fatalf("after refusal: entries %v, error %v", entries, err)
			}
		})
	}
}

func TestForcePreflightRefusesForeignKeyViolation(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("force publication requires Linux")
	}
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
