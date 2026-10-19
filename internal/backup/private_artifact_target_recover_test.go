package backup_test

import (
	"path/filepath"
	"testing"

	"filippo.io/age"

	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/database"
)

// The first recovery prepares a real installation from the earlier encrypted
// snapshot. Only then do we add newer output. The second, forced recovery must
// refuse before replacing that installation's database or retained log files.
func TestPrivateArtifactTargetForceRecover(t *testing.T) {
	for _, kind := range []string{"consent", "created", "ordinary"} {
		t.Run(kind, func(t *testing.T) {
			f := newInstanceFixture(t)
			seedReplacementChat(t, f.db, f.paths.Output)
			bundle := f.create(t, true)
			if !bundle.Manifest.Encryption.Enabled {
				t.Fatal("snapshot must be encrypted")
			}
			dir := filepath.Join(t.TempDir(), "installation")
			opts := backup.RecoverOptions{BundlePath: bundle.Path, Identities: []age.Identity{f.identity}, DataDir: dir, Actor: "u_admin"}
			report, err := backup.RecoverInstance(t.Context(), opts)
			if err != nil {
				t.Fatalf("prepare earlier installation: %v", err)
			}
			target, err := database.Open("file:" + report.DatabasePath)
			if err != nil {
				t.Fatal(err)
			}
			logPath, before := advanceReplacementTarget(t, target.DB, backup.RecoveredStoreDir(dir, backup.StoreOutput), filepath.Join(dir, "logs"), kind)
			// Offline recovery owns its database: close all target connections
			// before invoking the supported force operation.
			if err := target.Close(); err != nil {
				t.Fatal(err)
			}
			opts.Force = true
			_, recoverErr := backup.RecoverInstance(t.Context(), opts)
			target, err = database.Open("file:" + report.DatabasePath)
			if err != nil {
				t.Fatal(err)
			}
			defer target.Close()
			// Physical recovery replaces SQL with the archived one-message snapshot.
			assertReplacementOutcome(t, target.DB, logPath, before, kind, 1, recoverErr)
		})
	}
}
