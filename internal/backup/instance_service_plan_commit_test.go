package backup

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/crewship-ai/crewship/internal/servicelifecycle"
)

func TestInstanceServicePlanIsNotPublishedBeforeDatabaseCommit(t *testing.T) {
	db := openMigratedDBCov(t)
	_, crew := seedCovWorkspace(t, db, "plan-commit-failure")
	if _, err := servicelifecycle.BeginBackupFence(t.Context(), db, crew, "backup"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE deferred_plan_failure(id TEXT REFERENCES crews(id) DEFERRABLE INITIALLY DEFERRED);
 CREATE TRIGGER fail_plan_commit AFTER DELETE ON service_backup_fences BEGIN INSERT INTO deferred_plan_failure VALUES('missing-crew'); END;`); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, RecoveredServicesDir), 0700); err != nil {
		t.Fatal(err)
	}
	payload := &ExtractedPayload{storage: LocalStorageOps{}, tempDir: filepath.Join(dir, RecoveredServicesDir), serviceImages: map[string]string{}, serviceMetadata: map[string]serviceSnapshot{}}
	if err := stageInstanceServiceRecovery(t.Context(), db, payload, 0, dir); err == nil {
		t.Fatal("failed database commit accepted")
	}
	if _, err := os.Stat(filepath.Join(dir, RecoveredServicesDir, instanceServicePlanFile)); !os.IsNotExist(err) {
		t.Fatalf("uncommitted plan published: %v", err)
	}
	pending, err := filepath.Glob(filepath.Join(dir, RecoveredServicesDir, ".landing-*.json"))
	if err != nil || len(pending) != 0 {
		t.Fatalf("failed plan staging leaked: %v %v", pending, err)
	}
	var fences int
	if err := db.QueryRow(`SELECT COUNT(*) FROM service_backup_fences`).Scan(&fences); err != nil || fences != 1 {
		t.Fatalf("source fence lost on rollback: %d %v", fences, err)
	}
}
