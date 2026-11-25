//go:build linux

package backup

import (
	"filippo.io/age"
	"os"
	"path/filepath"
	"testing"
)

func TestForcedInstanceRecoveryReplacesQuotaStaging(t *testing.T) {
	db := openMigratedDBCov(t)
	_, crew := seedCovWorkspace(t, db, "forced-recovery")
	if _, err := db.Exec(`UPDATE crews SET services_json=? WHERE id=?`, `[{"name":"database","quota_enforced":true,"volumes":[{"name":"data","mount":"/data","quota_bytes":33554432,"generation":3}]}]`, crew); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO service_runtime_intents(id,crew_id,service_name,desired_state,version,updated_at) VALUES('forced-intent',?,'database','running',7,'2026-10-01T16:00:00Z')`, crew); err != nil {
		t.Fatal(err)
	}
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	result, err := CreateInstanceBackup(t.Context(), db, InstanceOptions{OutputDir: t.TempDir(), Recipients: []age.Recipient{identity.Recipient()}, Actor: covAdminActor(), ServiceSnapshots: &snapshotProbe{}})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	opts := RecoverOptions{BundlePath: result.Path, Identities: []age.Identity{identity}, DataDir: dir}
	if _, err = RecoverInstance(t.Context(), opts); err != nil {
		t.Fatal(err)
	}
	pattern := filepath.Join(dir, RecoveredServicesDir, "quota-image-*.ext4")
	files, err := filepath.Glob(pattern)
	if err != nil || len(files) != 1 {
		t.Fatalf("initial recovery did not stage one quota image: %v %v", files, err)
	}
	t.Run("force replaces the staged image", func(t *testing.T) {
		stale := filepath.Join(dir, RecoveredServicesDir, "quota-image-stale.ext4")
		if err := os.Rename(files[0], stale); err != nil {
			t.Fatal(err)
		}
		opts.Force = true
		if _, err := RecoverInstance(t.Context(), opts); err != nil {
			t.Fatal(err)
		}
		files, err := filepath.Glob(pattern)
		if err != nil || len(files) != 1 || files[0] == stale {
			t.Fatalf("old recovery images leaked: %v %v", files, err)
		}
	})
}
