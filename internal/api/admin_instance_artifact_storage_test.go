package api

import (
	"encoding/json"
	"net/http"
	"testing"
)

type artifactStorageAdminBody struct {
	WorkspaceLimit int64  `json:"workspace_limit_bytes"`
	InstanceLimit  int64  `json:"instance_limit_bytes"`
	Used           int64  `json:"used_bytes"`
	Level          string `json:"level"`
	Retention      string `json:"retention"`
}

func TestArtifactStorageAdminIsForInstanceAdminsOnly(t *testing.T) {
	f := newInstanceFixture(t)
	for _, c := range []struct{ method, path, body string }{
		{"GET", "/api/v1/admin/instance/artifact-storage", ""},
		{"PUT", "/api/v1/admin/instance/artifact-storage", `{"instance_limit_bytes":2097152}`},
		{"POST", "/api/v1/admin/instance/artifact-storage/recompute", `{}`},
	} {
		wantCode(t, f.do(f.wsAdmin, c.method, c.path, c.body), http.StatusForbidden, "workspace ADMIN "+c.method+" "+c.path)
	}
	var limit int64
	if err := f.db.QueryRow(`SELECT max_bytes FROM artifact_storage_global WHERE id=1`).Scan(&limit); err != nil || limit == 2097152 {
		t.Fatalf("a refused save still wrote: %d %v", limit, err)
	}
}

func TestArtifactStorageAdminLimitsDefaultsSetValidateAndAudit(t *testing.T) {
	f := newInstanceFixture(t)
	rr := f.do(f.boss, "GET", "/api/v1/admin/instance/artifact-storage", "")
	wantCode(t, rr, http.StatusOK, "get")
	got := decodeAs[artifactStorageAdminBody](t, rr.Body.Bytes())
	if got.WorkspaceLimit != 32<<30 || got.InstanceLimit != 64<<30 || got.Retention != "keep-all" {
		t.Fatalf("defaults changed: %+v", got)
	}
	for _, bad := range []string{`{}`, `{"instance_limit_bytes":5}`, `{"workspace_limit_bytes":-1}`, `{"unknown":1}`, `not json`} {
		wantCode(t, f.do(f.boss, "PUT", "/api/v1/admin/instance/artifact-storage", bad), http.StatusBadRequest, "reject "+bad)
	}
	rr = f.do(f.boss, "PUT", "/api/v1/admin/instance/artifact-storage", `{"workspace_limit_bytes":5242880,"instance_limit_bytes":10485760}`)
	wantCode(t, rr, http.StatusOK, "set")
	got = decodeAs[artifactStorageAdminBody](t, rr.Body.Bytes())
	if got.WorkspaceLimit != 5<<20 || got.InstanceLimit != 10<<20 {
		t.Fatalf("limits not applied: %+v", got)
	}
	var n int
	if err := f.db.QueryRow(`SELECT count(*) FROM instance_audit_logs WHERE action='instance.artifact_storage_limits_set'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("audit rows %d %v", n, err)
	}
	// Only the instance limit: the workspace limit stays.
	rr = f.do(f.boss, "PUT", "/api/v1/admin/instance/artifact-storage", `{"instance_limit_bytes":20971520}`)
	wantCode(t, rr, http.StatusOK, "set instance only")
	got = decodeAs[artifactStorageAdminBody](t, rr.Body.Bytes())
	if got.WorkspaceLimit != 5<<20 || got.InstanceLimit != 20<<20 {
		t.Fatalf("partial update: %+v", got)
	}
}

func TestArtifactStorageAdminRecomputeReportsDriftAndIsIdempotent(t *testing.T) {
	f := newInstanceFixture(t)
	mustExec(t, f.db, `UPDATE artifact_storage_global SET reserved_bytes=77`)
	dry := f.do(f.boss, "POST", "/api/v1/admin/instance/artifact-storage/recompute", `{"dry_run":true}`)
	wantCode(t, dry, http.StatusOK, "dry run")
	var report struct {
		DryRun   bool `json:"dry_run"`
		Changed  bool `json:"changed"`
		Instance struct {
			ReservedBefore int64 `json:"reserved_bytes_before"`
			ReservedAfter  int64 `json:"reserved_bytes_after"`
		} `json:"instance"`
		DataDeleted bool `json:"data_deleted"`
	}
	if err := json.Unmarshal(dry.Body.Bytes(), &report); err != nil || !report.DryRun || !report.Changed || report.Instance.ReservedBefore != 77 || report.Instance.ReservedAfter != 0 {
		t.Fatalf("dry run %s %v", dry.Body, err)
	}
	var reserved int64
	_ = f.db.QueryRow(`SELECT reserved_bytes FROM artifact_storage_global`).Scan(&reserved)
	if reserved != 77 {
		t.Fatalf("dry run wrote: %d", reserved)
	}
	for i, wantChanged := range []bool{true, false} {
		rr := f.do(f.boss, "POST", "/api/v1/admin/instance/artifact-storage/recompute", "")
		wantCode(t, rr, http.StatusOK, "recompute")
		report.Changed = !wantChanged
		if err := json.Unmarshal(rr.Body.Bytes(), &report); err != nil || report.Changed != wantChanged || report.DataDeleted {
			t.Fatalf("run %d: %s %v", i, rr.Body, err)
		}
	}
	_ = f.db.QueryRow(`SELECT reserved_bytes FROM artifact_storage_global`).Scan(&reserved)
	if reserved != 0 {
		t.Fatalf("recompute left drift: %d", reserved)
	}
}
