package api

// Second coverage pass for backup_admin.go: Download/Delete/Rotate against
// a real bundle created through the pure-DB Create path, Unlock failure,
// and the SelfTest pipeline driven by a fake DockerOps.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/backup"
)

// covBak2Rig sandboxes HOME, builds the handler, and returns the rig.
func covBak2Rig(t *testing.T) (*BackupHandler, *sql.DB, string, string, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir, err := backup.DefaultBackupsDir()
	if err != nil {
		t.Fatalf("default backups dir: %v", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewBackupHandler(db, newTestLogger(), nil, "test-version")
	return h, db, userID, wsID, dir
}

// covBak2CreateBundle runs the production Create handler in pure-DB mode and
// returns the bundle path on disk.
func covBak2CreateBundle(t *testing.T, h *BackupHandler, userID, wsID string) string {
	t.Helper()
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/admin/backups",
			strings.NewReader(`{"scope":"workspace","no_encrypt":true}`)),
		userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create bundle: status = %d; body=%s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Path == "" {
		t.Fatal("empty bundle path")
	}
	return resp.Path
}

// --- Download success --------------------------------------------------------

func TestCovBak2Download_Success(t *testing.T) {
	h, _, userID, wsID, _ := covBak2Rig(t)
	path := covBak2CreateBundle(t, h, userID, wsID)
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read bundle: %v", err)
	}

	req := withWorkspaceUser(
		httptest.NewRequest("GET", "/api/v1/admin/backups/download?path="+path, nil),
		userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Download(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("Content-Type"); got != "application/zstd" {
		t.Errorf("content-type = %q", got)
	}
	if cd := rr.Header().Get("Content-Disposition"); !strings.Contains(cd, filepath.Base(path)) {
		t.Errorf("content-disposition = %q missing filename", cd)
	}
	if rr.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("cache-control = %q, want no-store", rr.Header().Get("Cache-Control"))
	}
	body, _ := io.ReadAll(rr.Body)
	if len(body) != len(want) {
		t.Errorf("streamed %d bytes, want %d", len(body), len(want))
	}
}

// --- Delete success + guards ---------------------------------------------------

func TestCovBak2Delete_Success(t *testing.T) {
	h, db, userID, wsID, _ := covBak2Rig(t)
	path := covBak2CreateBundle(t, h, userID, wsID)

	req := withWorkspaceUser(
		httptest.NewRequest("DELETE", "/api/v1/admin/backups?path="+path, nil),
		userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d; body=%s", rr.Code, rr.Body.String())
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("bundle still on disk after delete: %v", err)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM backup_catalog WHERE path = ?`, path).Scan(&n); err == nil && n != 0 {
		t.Errorf("catalog row not removed (n=%d)", n)
	}
}

func TestCovBak2Delete_NoUser401(t *testing.T) {
	h, _, _, wsID, _ := covBak2Rig(t)
	req := httptest.NewRequest("DELETE", "/api/v1/admin/backups?path=/x", nil)
	req = req.WithContext(withWorkspace(req.Context(), wsID, "OWNER"))
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rr.Code)
	}
}

// --- Rotate ---------------------------------------------------------------------

// TestCovBak2Rotate_NonDryRunDeletes creates a real bundle plus an older
// copy and rotates with keep_last=1 — exactly one file must be removed and
// the audit/catalog sync loop must run.
func TestCovBak2Rotate_NonDryRunDeletes(t *testing.T) {
	h, _, userID, wsID, dir := covBak2Rig(t)
	path := covBak2CreateBundle(t, h, userID, wsID)

	// Plant a second, identical bundle under an older filename. The
	// manifest CreatedAt ties, so keep_last=1 keeps one and deletes one.
	older := filepath.Join(dir, "crewship-workspace-test-20200101T000000Z.tar.zst")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if err := os.WriteFile(older, data, 0o600); err != nil {
		t.Fatalf("write older bundle: %v", err)
	}

	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/admin/backups/rotate",
			strings.NewReader(`{"keep_last":1}`)),
		userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Rotate(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Deleted []string `json:"deleted"`
		DryRun  bool     `json:"dry_run"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.DryRun {
		t.Error("dry_run echoed true on a real rotation")
	}
	if len(resp.Deleted) != 1 {
		t.Fatalf("deleted = %v, want exactly one path", resp.Deleted)
	}
	if _, err := os.Stat(resp.Deleted[0]); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("rotated bundle still on disk: %v", err)
	}
}

func TestCovBak2Rotate_HomeUnresolvable500(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewBackupHandler(db, newTestLogger(), nil, "v")
	t.Setenv("HOME", "")

	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/x", strings.NewReader(`{"keep_last":1}`)),
		userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Rotate(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovBak2Rotate_ListFails500(t *testing.T) {
	h, _, userID, wsID, dir := covBak2Rig(t)
	// Replace the backups dir with a regular file so ListBackups errors.
	if err := os.RemoveAll(dir); err != nil {
		t.Fatalf("rm: %v", err)
	}
	if err := os.WriteFile(dir, []byte("not a dir"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/x", strings.NewReader(`{"keep_last":1}`)),
		userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Rotate(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

// --- Unlock error path ------------------------------------------------------------

func TestCovBak2Unlock_DBError500(t *testing.T) {
	h, db, userID, wsID, _ := covBak2Rig(t)
	db.Close()
	req := withWorkspaceUser(httptest.NewRequest("POST", "/x", nil), userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Unlock(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rr.Code)
	}
}

// --- SelfTest -----------------------------------------------------------------------

// covBak2Ops is a minimal DockerOps fake; only ContainerExists matters for
// the branches under test.
type covBak2Ops struct {
	exists    bool
	existsErr error
}

func (o *covBak2Ops) Pause(context.Context, string) error   { return nil }
func (o *covBak2Ops) Unpause(context.Context, string) error { return nil }
func (o *covBak2Ops) CopyFrom(context.Context, string, string) (io.ReadCloser, error) {
	return io.NopCloser(strings.NewReader("")), nil
}
func (o *covBak2Ops) CopyTo(context.Context, string, string, io.Reader) error       { return nil }
func (o *covBak2Ops) CopyToVolume(context.Context, string, string, io.Reader) error { return nil }
func (o *covBak2Ops) CopyToSystem(context.Context, string, string, io.Reader) error { return nil }
func (o *covBak2Ops) ContainerExists(context.Context, string) (bool, error) {
	return o.exists, o.existsErr
}
func (o *covBak2Ops) Exec(context.Context, string, []string) (int, []byte, error) {
	return 0, nil, nil
}

func covBak2SelfTestRig(t *testing.T, ops backup.DockerOps) (*BackupHandler, *sql.DB, string, string) {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	if _, err := db.Exec(`INSERT INTO crews (id, workspace_id, name, slug) VALUES ('crew-st', ?, 'ST', 'st')`, wsID); err != nil {
		t.Fatalf("seed crew: %v", err)
	}
	return NewBackupHandler(db, newTestLogger(), ops, "v"), db, userID, wsID
}

func covBak2SelfTestReq(userID, wsID, body string) *http.Request {
	return withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/admin/backups/self-test", strings.NewReader(body)),
		userID, wsID, "OWNER")
}

func TestCovBak2SelfTest_BodyValidation(t *testing.T) {
	h, _, userID, wsID := covBak2SelfTestRig(t, &covBak2Ops{})

	t.Run("bad JSON 400", func(t *testing.T) {
		rr := httptest.NewRecorder()
		h.SelfTest(rr, covBak2SelfTestReq(userID, wsID, `{nope`))
		if rr.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", rr.Code)
		}
	})

	t.Run("missing crew_id 400", func(t *testing.T) {
		rr := httptest.NewRecorder()
		h.SelfTest(rr, covBak2SelfTestReq(userID, wsID, `{"crew_id":"  "}`))
		if rr.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", rr.Code)
		}
	})

	t.Run("crew not found 404", func(t *testing.T) {
		rr := httptest.NewRecorder()
		h.SelfTest(rr, covBak2SelfTestReq(userID, wsID, `{"crew_id":"ghost"}`))
		if rr.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404", rr.Code)
		}
	})
}

func TestCovBak2SelfTest_ContainerMissing200NotOK(t *testing.T) {
	h, _, userID, wsID := covBak2SelfTestRig(t, &covBak2Ops{exists: false})
	rr := httptest.NewRecorder()
	h.SelfTest(rr, covBak2SelfTestReq(userID, wsID, `{"crew_id":"crew-st"}`))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rr.Code, rr.Body.String())
	}
	var resp struct {
		OK       bool   `json:"ok"`
		CrewSlug string `json:"crew_slug"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.OK {
		t.Error("ok = true with missing container")
	}
	if resp.CrewSlug != "st" {
		t.Errorf("crew_slug = %q, want st", resp.CrewSlug)
	}
}

func TestCovBak2SelfTest_PipelineError500(t *testing.T) {
	h, _, userID, wsID := covBak2SelfTestRig(t, &covBak2Ops{existsErr: errors.New("daemon down")})
	rr := httptest.NewRecorder()
	h.SelfTest(rr, covBak2SelfTestReq(userID, wsID, `{"crew_id":"crew-st"}`))
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovBak2SelfTest_CrewLookupError500(t *testing.T) {
	h, db, userID, wsID := covBak2SelfTestRig(t, &covBak2Ops{})
	db.Close()
	rr := httptest.NewRecorder()
	h.SelfTest(rr, covBak2SelfTestReq(userID, wsID, `{"crew_id":"crew-st"}`))
	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rr.Code)
	}
}
