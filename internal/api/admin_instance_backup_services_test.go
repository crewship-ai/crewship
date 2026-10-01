package api

import (
	"context"
	"github.com/crewship-ai/crewship/internal/backup"
	"github.com/crewship-ai/crewship/internal/backupplan"
	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/quota"
	"io"
	"net/http"
	"path/filepath"
	"testing"
)

func TestServiceLandingRequiresInstanceAdmin(t *testing.T) {
	f := newInstanceFixture(t)
	wantCode(t, f.do(f.wsAdmin, "POST", "/api/v1/admin/instance/backups/services/land", `{}`), http.StatusForbidden, "workspace admin cannot land instance service images")
	f.r.instanceBackups.SetRecovery(InstanceRecoveryConfig{})
	wantCode(t, f.do(f.boss, "POST", "/api/v1/admin/instance/backups/services/land", `{}`), http.StatusServiceUnavailable, "missing recovery target")
	wantCode(t, f.do(f.boss, "POST", "/api/v1/admin/instance/backups/services/land", `{"dry_run":`), http.StatusBadRequest, "invalid request")
}

type apiServiceSnapshotProbe struct{ imports int }

func (*apiServiceSnapshotProbe) QuotaSnapshotNamespace() string { return "api-test-host" }
func (*apiServiceSnapshotProbe) StopCrewService(context.Context, string, string, string) error {
	return nil
}
func (*apiServiceSnapshotProbe) ExportQuotaVolume(_ context.Context, _ quota.Key, size int64, w io.Writer) error {
	_, err := io.CopyN(w, apiServiceZeros{}, size)
	return err
}
func (p *apiServiceSnapshotProbe) ImportQuotaVolume(_ context.Context, _ quota.Key, size int64, r io.Reader) error {
	p.imports++
	n, err := io.Copy(io.Discard, r)
	if err != nil {
		return err
	}
	if n != size {
		return io.ErrUnexpectedEOF
	}
	return nil
}

type apiServiceZeros struct{}

func (apiServiceZeros) Read(p []byte) (int, error) { clear(p); return len(p), nil }

func TestInstanceExecutorAndServiceLandingPreserveAuditAtomicity(t *testing.T) {
	f := newInstanceFixture(t)
	body := `[{"name":"database","quota_enforced":true,"volumes":[{"name":"data","mount":"/data","quota_bytes":33554432,"generation":3}]}]`
	mustExec(t, f.db, `INSERT INTO crews(id,workspace_id,name,slug,services_json) VALUES('quota-api-crew','ws-old','API crew','quota-api',?)`, body)
	mustExec(t, f.db, `INSERT INTO service_runtime_intents(id,crew_id,service_name,desired_state,version,updated_at) VALUES('api-intent','quota-api-crew','database','running',7,'2026-10-01T16:00:00Z')`)
	probe := &apiServiceSnapshotProbe{}
	h := f.r.instanceBackups
	h.SetRecovery(InstanceRecoveryConfig{OutputDir: t.TempDir(), ServiceSnapshots: probe})
	executor := &instanceExecutor{h: h, db: f.db}
	result, err := executor.Run(t.Context(), backupplan.RunSpec{Scope: "instance", Passphrase: "synthetic-api-backup-passphrase", Actor: backup.Actor{UserID: "boss", Role: "ADMIN"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Manifest.Contents.ServiceSnapshots != 1 {
		t.Fatal("instance executor dropped service transport")
	}
	dir := t.TempDir()
	_, err = backup.RecoverInstance(t.Context(), backup.RecoverOptions{BundlePath: result.Path, Passphrase: "synthetic-api-backup-passphrase", DataDir: dir, Drill: true})
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := database.Open("file:" + filepath.Join(dir, "crewship.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	h.db = recovered.DB
	h.SetRecovery(InstanceRecoveryConfig{DataDir: dir, ServiceSnapshots: probe})
	route := "/api/v1/admin/instance/backups/services/land"
	wantCode(t, f.do(f.boss, "POST", route, `{"dry_run":true}`), http.StatusOK, "service landing preview")
	if probe.imports != 0 {
		t.Fatal("preview imported an image")
	}
	mustExec(t, recovered.DB, `CREATE TRIGGER fail_service_landing_audit BEFORE INSERT ON instance_audit_logs BEGIN SELECT RAISE(ABORT,'audit unavailable');END`)
	wantCode(t, f.do(f.boss, "POST", route, `{}`), http.StatusInternalServerError, "failed audit")
	var held int
	if err = recovered.QueryRow(`SELECT COUNT(*) FROM service_backup_fences WHERE crew_id='quota-api-crew'`).Scan(&held); err != nil || held != 1 {
		t.Fatalf("failed audit released maintenance: %d %v", held, err)
	}
	mustExec(t, recovered.DB, `DROP TRIGGER fail_service_landing_audit`)
	mustExec(t, recovered.DB, `UPDATE service_backup_fences SET producer_until=''`)
	wantCode(t, f.do(f.boss, "POST", route, `{}`), http.StatusOK, "service landing")
	var audit int
	if err = recovered.QueryRow(`SELECT COUNT(*) FROM instance_audit_logs WHERE action='instance.services_landed'`).Scan(&audit); err != nil || audit != 1 {
		t.Fatalf("service landing audit: %d %v", audit, err)
	}
	if err = recovered.QueryRow(`SELECT COUNT(*) FROM service_backup_fences WHERE crew_id='quota-api-crew'`).Scan(&held); err != nil || held != 0 {
		t.Fatalf("successful audit retained maintenance: %d %v", held, err)
	}
}
