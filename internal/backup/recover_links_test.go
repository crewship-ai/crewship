package backup

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/journal"
)

// Every fixed recovery location is guarded, as a final component and as an
// ancestor, and a clean directory passes.
func TestRefuseLinkedRecoveryPathsCoversEveryFixedLocation(t *testing.T) {
	names := []string{"crewship.db", "crewship.db-wal", "crewship.db-shm", "crewship.db-journal",
		recoveredSecretsFile, RecoveredKeysFile, journal.SigningKeysFile,
		RecoveredServicesDir, RecoveredCrewsDir, RecoveredEnvironmentsDir}
	for _, store := range InstanceStores {
		rel, err := filepath.Rel("/d", RecoveredStoreDir("/d", store))
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, rel)
	}
	if err := refuseLinkedRecoveryPaths(t.TempDir()); err != nil {
		t.Fatalf("clean directory refused: %v", err)
	}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			elsewhere := t.TempDir()
			if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(elsewhere, filepath.Join(dir, name)); err != nil {
				t.Fatal(err)
			}
			err := refuseLinkedRecoveryPaths(dir)
			if err == nil || !strings.Contains(err.Error(), name) {
				t.Fatalf("link at %s not refused (or not named): %v", name, err)
			}
		})
	}
	t.Run("a link above a nested location is refused", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Symlink(t.TempDir(), filepath.Join(dir, filepath.Dir(RecoveredServicesDir))); err != nil {
			t.Fatal(err)
		}
		if err := refuseLinkedRecoveryPaths(dir); err == nil {
			t.Fatal("ancestor link accepted")
		}
	})
	t.Run("all links are listed at once", func(t *testing.T) {
		dir := t.TempDir()
		for _, name := range []string{"output", "memory", "secrets.env"} {
			if err := os.Symlink(t.TempDir(), filepath.Join(dir, name)); err != nil {
				t.Fatal(err)
			}
		}
		err := refuseLinkedRecoveryPaths(dir)
		if err == nil || !strings.Contains(err.Error(), "3 path(s)") {
			t.Fatalf("err = %v", err)
		}
	})
}
