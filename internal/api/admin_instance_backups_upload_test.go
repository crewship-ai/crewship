package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/crewship-ai/crewship/internal/backup"
)

func TestInstanceBackupUploadRequiresInstanceAdminAndRollsBackAuditFailure(t *testing.T) {
	f := newInstanceFixture(t)
	directory := t.TempDir()
	f.r.instanceBackups.SetRecovery(InstanceRecoveryConfig{OutputDir: directory})
	const path = "/api/v1/admin/instance/backups/bundles/upload"
	request := func(token string, body []byte, media string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", media)
		response := httptest.NewRecorder()
		f.r.ServeHTTP(response, req)
		return response
	}
	wantCode(t, request(f.wsAdmin, []byte("untrusted"), "application/octet-stream"), http.StatusForbidden, "workspace admin uploads")
	wantCode(t, request(f.boss, nil, "application/json"), http.StatusUnsupportedMediaType, "wrong content type")
	wantCode(t, request(f.boss, []byte("not an archive"), "application/octet-stream"), http.StatusUnprocessableEntity, "invalid encrypted archive")
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	manifest := &backup.Manifest{FormatVersion: backup.FormatVersion, Scope: backup.ScopeWorkspace, CompatibleTargets: []backup.Target{backup.TargetAnyInstance}, CreatedAt: time.Now(), CreatedBy: backup.Actor{UserID: "boss", Email: "boss@ex.com", Role: "OWNER"}, CrewshipVersionAtBackup: "1.0.0", SourceInstance: backup.Instance{Hostname: "test", Platform: "linux/amd64"},
		Contents: backup.Contents{Workspace: &backup.WorkspaceSummary{ID: "ws-new", Slug: "new", Name: "New"}}}
	var archive bytes.Buffer
	if err = backup.WriteBundle(&archive, manifest, strings.NewReader("encrypted fixture"), backup.WriteBundleOptions{Recipients: []age.Recipient{identity.Recipient()}}); err != nil {
		t.Fatal(err)
	}
	response := request(f.boss, archive.Bytes(), "application/octet-stream")
	wantCode(t, response, http.StatusCreated, "instance admin upload")
	receipt := decodeAs[backupUploadResponse](t, response.Body.Bytes())
	if receipt.ProofLevel != backup.ProofChecksum || receipt.Duplicate {
		t.Fatalf("unexpected proof: %+v", receipt)
	}
	if !f.audited("instance.backup_uploaded") {
		t.Fatal("missing atomic audit")
	}
	response = request(f.boss, archive.Bytes(), "application/octet-stream")
	wantCode(t, response, http.StatusCreated, "duplicate upload")
	if !decodeAs[backupUploadResponse](t, response.Body.Bytes()).Duplicate {
		t.Fatal("duplicate not recognized")
	}

	mustExec(t, f.db, `CREATE TRIGGER reject_upload_audit BEFORE INSERT ON instance_audit_logs BEGIN SELECT RAISE(ABORT,'audit unavailable'); END`)
	var second bytes.Buffer
	if err = backup.WriteBundle(&second, manifest, strings.NewReader("different encrypted fixture"), backup.WriteBundleOptions{Recipients: []age.Recipient{identity.Recipient()}}); err != nil {
		t.Fatal(err)
	}
	wantCode(t, request(f.boss, second.Bytes(), "application/octet-stream"), http.StatusInternalServerError, "audit failure")
	var rows int
	if err = f.db.QueryRow(`SELECT COUNT(*) FROM backup_catalog`).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("failed upload catalog rows=%d err=%v", rows, err)
	}
	files, err := os.ReadDir(directory)
	if err != nil || len(files) != 1 {
		t.Fatalf("upload leaked staging: %v %v", files, err)
	}
}
