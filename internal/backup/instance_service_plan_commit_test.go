package backup

import (
	"crypto/sha256"
	"encoding/hex"
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

func TestCommittedServicePlanPublicationFailureCanBeRetried(t *testing.T) {
	for _, scenario := range []string{"valid", "changed-epoch", "ambiguous"} {
		t.Run(scenario, func(t *testing.T) {
			db := openMigratedDBCov(t)
			_, crew := seedCovWorkspace(t, db, "plan-publication-retry")
			body := `[{"name":"database","quota_enforced":true,"volumes":[{"name":"data","mount":"/data","quota_bytes":33554432,"generation":3}]}]`
			if _, err := db.Exec(`UPDATE crews SET services_json=? WHERE id=?`, body, crew); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO service_runtime_intents(id,crew_id,service_name,desired_state,version,updated_at) VALUES('retry-intent',?,'database','stopped',7,'2026-10-01T16:00:00Z')`, crew); err != nil {
				t.Fatal(err)
			}
			var slug string
			if err := db.QueryRow(`SELECT slug FROM crews WHERE id=?`, crew).Scan(&slug); err != nil {
				t.Fatal(err)
			}
			specs, err := declaredServiceSnapshots(body, crew, slug)
			if err != nil {
				t.Fatal(err)
			}
			source := specs[0]
			source.Namespace = "synthetic-instance"
			source.DesiredState = "stopped"
			source.IntentVersion = 7
			data := make([]byte, source.Bytes)
			digest := sha256.Sum256(data)
			source.SHA256 = hex.EncodeToString(digest[:])
			root := t.TempDir()
			dir := filepath.Join(root, RecoveredServicesDir)
			if err = os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			image := filepath.Join(dir, "quota-image-retry.ext4")
			if err = os.WriteFile(image, data, 0600); err != nil {
				t.Fatal(err)
			}
			payload := &ExtractedPayload{storage: LocalStorageOps{}, tempDir: dir, serviceImages: map[string]string{source.name(): image}, serviceMetadata: map[string]serviceSnapshot{source.name(): source}}
			blocker := filepath.Join(dir, instanceServicePlanFile)
			if err = os.Mkdir(blocker, 0700); err != nil {
				t.Fatal(err)
			}
			if err = stageInstanceServiceRecovery(t.Context(), db, payload, 1, root); err == nil {
				t.Fatal("publication failure was accepted")
			}
			if !payload.serviceRecoveryCommitted {
				t.Fatal("committed images not retained")
			}
			pending, err := filepath.Glob(filepath.Join(dir, ".landing-*.json"))
			if err != nil || len(pending) != 1 {
				t.Fatalf("committed plan discarded: %v %v", pending, err)
			}
			if err = os.Remove(blocker); err != nil {
				t.Fatal(err)
			}
			if scenario == "changed-epoch" {
				if _, err = db.Exec(`UPDATE service_backup_fences SET token='ffffffffffffffffffffffffffffffff' WHERE crew_id=?`, crew); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "ambiguous" {
				raw, readErr := os.ReadFile(pending[0])
				if readErr != nil {
					t.Fatal(readErr)
				}
				if err = os.WriteFile(filepath.Join(dir, ".landing-duplicate.json"), raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			probe := &instanceImportProbe{}
			if scenario != "valid" {
				if _, err = LandRecoveredServices(t.Context(), db, root, probe); err == nil {
					t.Fatal("unverifiable pending plan accepted")
				}
				if probe.imports != 0 {
					t.Fatal("unverifiable plan imported an image")
				}
				if _, err = os.Stat(blocker); !os.IsNotExist(err) {
					t.Fatalf("unverifiable plan published: %v", err)
				}
				return
			}
			count, err := LandRecoveredServices(t.Context(), db, root, probe, ServiceLandingOptions{DryRun: true})
			if err != nil || count != 1 || probe.imports != 0 {
				t.Fatalf("publication retry dry run: %d %v imports=%d", count, err, probe.imports)
			}
			count, err = LandRecoveredServices(t.Context(), db, root, probe)
			if err != nil || count != 1 || probe.imports != 1 {
				t.Fatalf("publication retry import: %d %v imports=%d", count, err, probe.imports)
			}
		})
	}
}
