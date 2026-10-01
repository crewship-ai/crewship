package backup

import (
	"context"
	"database/sql"
	"errors"
	"filippo.io/age"
	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/quota"
	"io"
	"path/filepath"
	"testing"
)

func TestInstanceBackupIncludesQuotaServiceData(t *testing.T) {
	db := openMigratedDBCov(t)
	_, crew := seedCovWorkspace(t, db, "instance-quota-snapshot")
	body := `[{"name":"database","quota_enforced":true,"volumes":[{"name":"data","mount":"/data","quota_bytes":33554432,"generation":3}]}]`
	if _, err := db.Exec(`UPDATE crews SET services_json=? WHERE id=?`, body, crew); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO service_runtime_intents(id,crew_id,service_name,desired_state,version,updated_at) VALUES('instance-quota-intent',?,'database','running',7,'2026-10-01T16:00:00Z')`, crew); err != nil {
		t.Fatal(err)
	}
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	probe := &snapshotProbe{}
	result, err := CreateInstanceBackup(t.Context(), db, InstanceOptions{OutputDir: t.TempDir(), Actor: covAdminActor(), Recipients: []age.Recipient{identity.Recipient()}, ServiceSnapshots: probe})
	if err != nil {
		t.Fatal(err)
	}
	if result.Manifest.Contents.ServiceSnapshots != 1 || probe.stop != 1 || probe.export != 1 {
		t.Fatalf("quota data absent: count=%d stop=%d export=%d", result.Manifest.Contents.ServiceSnapshots, probe.stop, probe.export)
	}
	proof, err := CheckBundleContents(t.Context(), result.Path, []age.Identity{identity}, "")
	if err != nil || !proof.OK {
		t.Fatalf("quota contents check: %#v %v", proof, err)
	}
	var fences int
	if err = db.QueryRow(`SELECT COUNT(*) FROM service_backup_fences`).Scan(&fences); err != nil || fences != 0 {
		t.Fatalf("successful backup retained maintenance: %d %v", fences, err)
	}
}

func TestInstanceRecoveryKeepsQuotaImagesAndMaintenance(t *testing.T) {
	db := openMigratedDBCov(t)
	_, crew := seedCovWorkspace(t, db, "instance-quota-recovery")
	body := `[{"name":"database","quota_enforced":true,"volumes":[{"name":"data","mount":"/data","quota_bytes":33554432,"generation":3}]}]`
	if _, err := db.Exec(`UPDATE crews SET services_json=? WHERE id=?`, body, crew); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO service_runtime_intents(id,crew_id,service_name,desired_state,version,updated_at) VALUES('recovery-intent',?,'database','running',7,'2026-10-01T16:00:00Z')`, crew); err != nil {
		t.Fatal(err)
	}
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	result, err := CreateInstanceBackup(t.Context(), db, InstanceOptions{OutputDir: t.TempDir(), Actor: covAdminActor(), Recipients: []age.Recipient{identity.Recipient()}, ServiceSnapshots: &snapshotProbe{}})
	if err != nil {
		t.Fatal(err)
	}
	target := t.TempDir()
	_, err = RecoverInstance(t.Context(), RecoverOptions{BundlePath: result.Path, Identities: []age.Identity{identity}, DataDir: target, Drill: true})
	if err != nil {
		t.Fatal(err)
	}
	files, err := filepath.Glob(filepath.Join(target, "restore-staging", "services", "quota-image-*.ext4"))
	if err != nil || len(files) != 1 {
		t.Fatalf("quota image discarded: files=%v err=%v", files, err)
	}
	restored, err := database.Open("file:" + filepath.Join(target, "crewship.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	var count int
	if err = restored.QueryRow(`SELECT COUNT(*) FROM service_backup_fences WHERE crew_id=? AND operation='restore'`, crew).Scan(&count); err != nil || count != 1 {
		t.Fatalf("restored service unprotected: %d %v", count, err)
	}
	var restoredBody string
	if err = restored.QueryRow(`SELECT services_json FROM crews WHERE id=?`, crew).Scan(&restoredBody); err != nil {
		t.Fatal(err)
	}
	if restoredBody == body {
		t.Fatal("source quota generation reused")
	}
	probe := &instanceImportProbe{}
	n, err := LandRecoveredServices(t.Context(), restored.DB, target, probe, ServiceLandingOptions{DryRun: true})
	if err != nil || n != 1 || probe.imports != 0 {
		t.Fatalf("dry run changed host: %d %v imports=%d", n, err, probe.imports)
	}
	auditFailure := errors.New("audit unavailable")
	_, err = LandRecoveredServices(t.Context(), restored.DB, target, probe, ServiceLandingOptions{Finalize: func(context.Context, *sql.Tx, int) error { return auditFailure }})
	if !errors.Is(err, auditFailure) {
		t.Fatalf("audit failure lost: %v", err)
	}
	if err = restored.QueryRow(`SELECT COUNT(*) FROM service_backup_fences WHERE crew_id=?`, crew).Scan(&count); err != nil || count != 1 {
		t.Fatalf("audit failure resumed service: %d %v", count, err)
	}
	if _, err = LandRecoveredServices(t.Context(), restored.DB, target, probe); err == nil {
		t.Fatal("concurrent producer accepted")
	}
	// Simulate the failed producer's lease expiry in this isolated restored DB.
	if _, err = restored.Exec(`UPDATE service_backup_fences SET producer_until=''`); err != nil {
		t.Fatal(err)
	}
	probe.alreadyImported = true
	n, err = LandRecoveredServices(t.Context(), restored.DB, target, probe)
	if err != nil || n != 1 {
		t.Fatalf("verified retry failed: %d %v", n, err)
	}
	if err = restored.QueryRow(`SELECT COUNT(*) FROM service_backup_fences WHERE crew_id=?`, crew).Scan(&count); err != nil || count != 0 {
		t.Fatalf("successful landing retained fence: %d %v", count, err)
	}
}

type instanceImportProbe struct {
	snapshotProbe
	imports         int
	alreadyImported bool
}

func (p *instanceImportProbe) ImportQuotaVolume(_ context.Context, _ quota.Key, size int64, r io.Reader) error {
	p.imports++
	if p.alreadyImported {
		return errors.New("already imported")
	}
	n, err := io.Copy(io.Discard, r)
	if err != nil {
		return err
	}
	if n != size {
		return io.ErrUnexpectedEOF
	}
	return nil
}
