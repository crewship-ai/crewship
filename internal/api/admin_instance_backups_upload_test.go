package api

import (
	"bytes"
	"io"
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

func uploadFixtureArchive(t *testing.T) []byte {
	t.Helper()
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	manifest := &backup.Manifest{FormatVersion: backup.FormatVersion, Scope: backup.ScopeWorkspace, CompatibleTargets: []backup.Target{backup.TargetAnyInstance}, CreatedAt: time.Now(), CreatedBy: backup.Actor{UserID: "boss", Email: "boss@ex.com", Role: "OWNER"}, CrewshipVersionAtBackup: "1.0.0", SourceInstance: backup.Instance{Hostname: "test", Platform: "linux/amd64"},
		Contents: backup.Contents{Workspace: &backup.WorkspaceSummary{ID: "ws-slow", Slug: "slow", Name: "Slow"}}}
	var archive bytes.Buffer
	if err = backup.WriteBundle(&archive, manifest, strings.NewReader("slowly uploaded fixture"), backup.WriteBundleOptions{Recipients: []age.Recipient{identity.Recipient()}}); err != nil {
		t.Fatal(err)
	}
	return archive.Bytes()
}

// dribble sends data in small pieces, pausing between them, then stalls for
// stall before closing (zero: close right after the last piece).
func dribble(data []byte, pieces int, pause, stall time.Duration) io.Reader {
	reader, writer := io.Pipe()
	go func() {
		step := (len(data) + pieces - 1) / pieces
		for len(data) > 0 {
			n := min(step, len(data))
			if _, err := writer.Write(data[:n]); err != nil {
				return
			}
			data = data[n:]
			if len(data) > 0 {
				time.Sleep(pause)
			}
		}
		time.Sleep(stall)
		_ = writer.Close()
	}()
	return reader
}

// The server's ReadTimeout covers a whole request. A large upload outlives it
// and must still complete while it keeps making progress; one that stalls
// longer than the idle timeout ends as an interrupted upload, not a 500.
func TestInstanceBackupUploadOutlivesReadTimeoutButNotAStall(t *testing.T) {
	f := newInstanceFixture(t)
	directory := t.TempDir()
	f.r.instanceBackups.SetRecovery(InstanceRecoveryConfig{OutputDir: directory})
	server := httptest.NewUnstartedServer(f.r)
	server.Config.ReadTimeout = 300 * time.Millisecond
	server.Start()
	defer server.Close()
	upload := func(body io.Reader) (int, error) {
		req, err := http.NewRequest(http.MethodPost, server.URL+instanceBackupUploadPath, body)
		if err != nil {
			return 0, err
		}
		req.Header.Set("Authorization", "Bearer "+f.boss)
		req.Header.Set("Content-Type", "application/octet-stream")
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			return 0, err
		}
		defer response.Body.Close()
		_, _ = io.Copy(io.Discard, response.Body)
		return response.StatusCode, nil
	}

	archive := uploadFixtureArchive(t)
	if status, err := upload(dribble(archive, 8, 150*time.Millisecond, 0)); err != nil || status != http.StatusCreated {
		t.Fatalf("progressing upload past ReadTimeout: status=%d err=%v", status, err)
	}

	previous := uploadIdleTimeout
	uploadIdleTimeout = 200 * time.Millisecond
	t.Cleanup(func() { uploadIdleTimeout = previous })
	status, err := upload(dribble(uploadFixtureArchive(t), 2, 0, 2*time.Second))
	if err == nil && status != http.StatusBadRequest {
		t.Fatalf("stalled upload: status=%d, want 400 or a closed connection", status)
	}
	var rows int
	if err = f.db.QueryRow(`SELECT COUNT(*) FROM backup_catalog`).Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("stalled upload catalog rows=%d err=%v", rows, err)
	}
	files, err := os.ReadDir(directory)
	if err != nil || len(files) != 1 {
		t.Fatalf("stalled upload left staging: %v %v", files, err)
	}
}

// A body over the API cap reaches the upload handler, which enforces its own
// limit instead of the global 16 MiB one.
func TestInstanceBackupUploadIsExemptFromTheAPIBodyCap(t *testing.T) {
	f := newInstanceFixture(t)
	f.r.instanceBackups.SetRecovery(InstanceRecoveryConfig{OutputDir: t.TempDir()})
	req := httptest.NewRequest(http.MethodPost, instanceBackupUploadPath, io.LimitReader(zeroReader{}, maxAPIBodyBytes+1))
	req.ContentLength = maxAPIBodyBytes + 1
	req.Header.Set("Authorization", "Bearer "+f.boss)
	req.Header.Set("Content-Type", "application/octet-stream")
	response := httptest.NewRecorder()
	f.r.ServeHTTP(response, req)
	wantCode(t, response, http.StatusUnprocessableEntity, "oversized for the API cap, judged by the upload")

	other := httptest.NewRequest(http.MethodPost, "/api/v1/admin/instance/backups/plans", io.LimitReader(zeroReader{}, maxAPIBodyBytes+1))
	other.ContentLength = maxAPIBodyBytes + 1
	other.Header.Set("Authorization", "Bearer "+f.boss)
	response = httptest.NewRecorder()
	f.r.ServeHTTP(response, other)
	wantCode(t, response, http.StatusRequestEntityTooLarge, "every other route keeps the cap")
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}
