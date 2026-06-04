package api

// Coverage for backup_admin.go — the admin lifecycle handlers Unlock,
// Rotate, Delete, Download and SelfTest. We exercise the auth/role
// gates (403/401), request-body validation (400), path-traversal
// rejection (400) and not-found (404), plus the happy paths that can run
// without a live Docker daemon or a real bundle on disk.
//
// SKIPPED (need a real .tar.zst bundle with a MANIFEST.json, a live DB
// snapshot binary, or a Docker daemon):
//   - Delete / Download success against a real bundle that
//     bundleBelongsToWorkspace accepts (backup.Inspect must parse a
//     valid manifest scoped to the workspace).
//   - SelfTest happy path (BackupSelfTest needs DockerOps + container).
//
// Bundle storage is rooted at $HOME/.crewship/backups; we point $HOME at
// a t.TempDir() so validateBackupPath's "must live under <default dir>"
// gate accepts our synthetic paths and DefaultBackupsDir resolves to an
// empty (or absent) directory.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// covBakRig builds a BackupHandler with dockerOps=nil (pure-DB mode) and
// a seeded user/workspace. Mirrors backupRig from backup_query_test.go
// but lives here so this file is self-contained against helper churn.
func covBakRig(t *testing.T) (*BackupHandler, string, string) {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewBackupHandler(db, newTestLogger(), nil, "test-version")
	return h, userID, wsID
}

// covBakHomeDir points $HOME at a temp dir so backup.DefaultBackupsDir
// resolves under it, and returns that backups dir. The dir is created so
// validateBackupPath's symlink-resolution walks the same canonical
// ancestor for both the requested path and the default dir (on macOS
// /tmp → /private/tmp, so an UNcreated dir would mismatch the prefix
// check and 400 instead of reaching the not-found path).
func covBakHomeDir(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".crewship", "backups")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir backups dir: %v", err)
	}
	return dir
}

// ── Unlock ──────────────────────────────────────────────────────────────

func TestCovBakUnlock_MemberRole_Returns403(t *testing.T) {
	h, userID, wsID := covBakRig(t)
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/admin/backups/unlock", nil),
		userID, wsID, "MEMBER",
	)
	rr := httptest.NewRecorder()
	h.Unlock(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rr.Code)
	}
}

func TestCovBakUnlock_NoWorkspace_Returns401(t *testing.T) {
	h, userID, _ := covBakRig(t)
	ctx := withUser(
		httptest.NewRequest("POST", "/x", nil).Context(),
		&AuthUser{ID: userID, Email: userID + "@example.com"},
	)
	ctx = withWorkspace(ctx, "", "OWNER") // role passes manage gate, ws empty
	req := httptest.NewRequest("POST", "/x", nil).WithContext(ctx)
	rr := httptest.NewRecorder()
	h.Unlock(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rr.Code)
	}
}

func TestCovBakUnlock_OwnerNoLock_Returns204(t *testing.T) {
	// ForceReleaseLock is an idempotent DELETE — releasing a lock that
	// was never held is a no-op success, not an error.
	h, userID, wsID := covBakRig(t)
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/admin/backups/unlock", nil),
		userID, wsID, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.Unlock(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%s", rr.Code, rr.Body.String())
	}
}

// ── Rotate ──────────────────────────────────────────────────────────────

func TestCovBakRotate_MemberRole_Returns403(t *testing.T) {
	h, userID, wsID := covBakRig(t)
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/admin/backups/rotate",
			strings.NewReader(`{"keep_last":3}`)),
		userID, wsID, "MEMBER",
	)
	rr := httptest.NewRecorder()
	h.Rotate(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rr.Code)
	}
}

func TestCovBakRotate_NoWorkspace_Returns401(t *testing.T) {
	h, userID, _ := covBakRig(t)
	ctx := withUser(
		httptest.NewRequest("POST", "/x", nil).Context(),
		&AuthUser{ID: userID, Email: userID + "@example.com"},
	)
	ctx = withWorkspace(ctx, "", "OWNER")
	req := httptest.NewRequest("POST", "/x",
		strings.NewReader(`{"keep_last":3}`)).WithContext(ctx)
	rr := httptest.NewRecorder()
	h.Rotate(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rr.Code)
	}
}

func TestCovBakRotate_BadBody_Returns400(t *testing.T) {
	h, userID, wsID := covBakRig(t)
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/admin/backups/rotate",
			strings.NewReader(`{not json`)),
		userID, wsID, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.Rotate(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestCovBakRotate_NegativeKeep_Returns400(t *testing.T) {
	h, userID, wsID := covBakRig(t)
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/admin/backups/rotate",
			strings.NewReader(`{"keep_last":-1,"keep_days":2}`)),
		userID, wsID, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.Rotate(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestCovBakRotate_BothZero_Returns400(t *testing.T) {
	// keep_last==0 && keep_days==0 disables every rule — a no-op rotate
	// is almost certainly a caller mistake, so reject it loudly.
	h, userID, wsID := covBakRig(t)
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/admin/backups/rotate",
			strings.NewReader(`{"keep_last":0,"keep_days":0}`)),
		userID, wsID, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.Rotate(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestCovBakRotate_DryRunEmptyDir_Returns200(t *testing.T) {
	// Empty/absent backups dir → nothing to delete. Dry-run echoes the
	// dry_run flag and an empty deleted list without touching the DB
	// catalog (the catalog-sync loop is skipped for dry runs).
	covBakHomeDir(t)
	h, userID, wsID := covBakRig(t)
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/admin/backups/rotate",
			strings.NewReader(`{"keep_last":3,"dry_run":true}`)),
		userID, wsID, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.Rotate(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Deleted []string `json:"deleted"`
		DryRun  bool     `json:"dry_run"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.DryRun {
		t.Errorf("dry_run echo = false, want true")
	}
	if len(resp.Deleted) != 0 {
		t.Errorf("deleted = %v, want empty on an empty dir", resp.Deleted)
	}
}

func TestCovBakRotate_RealEmptyDir_Returns200(t *testing.T) {
	// Non-dry-run path through the catalog-sync loop. With an empty dir
	// the loop body never runs, but we still exercise the !DryRun branch
	// and the 200 write.
	covBakHomeDir(t)
	h, userID, wsID := covBakRig(t)
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/admin/backups/rotate",
			strings.NewReader(`{"keep_days":7}`)),
		userID, wsID, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.Rotate(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Deleted []string `json:"deleted"`
		DryRun  bool     `json:"dry_run"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.DryRun {
		t.Errorf("dry_run echo = true, want false")
	}
	if len(resp.Deleted) != 0 {
		t.Errorf("deleted = %v, want empty on an empty dir", resp.Deleted)
	}
}

// ── Delete ──────────────────────────────────────────────────────────────

func TestCovBakDelete_MemberRole_Returns403(t *testing.T) {
	h, userID, wsID := covBakRig(t)
	req := withWorkspaceUser(
		httptest.NewRequest("DELETE", "/api/v1/admin/backups?path=/x.tar.zst", nil),
		userID, wsID, "MEMBER",
	)
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rr.Code)
	}
}

func TestCovBakDelete_MissingPath_Returns400(t *testing.T) {
	h, userID, wsID := covBakRig(t)
	req := withWorkspaceUser(
		httptest.NewRequest("DELETE", "/api/v1/admin/backups", nil),
		userID, wsID, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestCovBakDelete_PathTraversal_Returns400(t *testing.T) {
	// validateBackupPath must reject the escape before bundleBelongs /
	// backup.Delete ever touch the filesystem.
	h, userID, wsID := covBakRig(t)
	for _, p := range []string{"../../etc/passwd", "/etc/shadow", "rel/../x.tar.zst"} {
		req := withWorkspaceUser(
			httptest.NewRequest("DELETE", "/x?path="+p, nil),
			userID, wsID, "OWNER",
		)
		rr := httptest.NewRecorder()
		h.Delete(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("path=%q: status = %d, want 400", p, rr.Code)
		}
	}
}

func TestCovBakDelete_ValidPathNonexistentBundle_Returns404(t *testing.T) {
	// Path passes validateBackupPath (lives under the default backups
	// dir, no "..", not a symlink) but there is no bundle there, so
	// bundleBelongsToWorkspace (which Inspects the file) returns false →
	// 404, before backup.Delete runs.
	backupsDir := covBakHomeDir(t)
	h, userID, wsID := covBakRig(t)
	missing := filepath.Join(backupsDir, "crewship-workspace-ghost-20240101T000000Z.tar.zst")
	req := withWorkspaceUser(
		httptest.NewRequest("DELETE", "/x?path="+missing, nil),
		userID, wsID, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rr.Code, rr.Body.String())
	}
}

// ── Download ────────────────────────────────────────────────────────────

func TestCovBakDownload_MemberRole_Returns403(t *testing.T) {
	h, userID, wsID := covBakRig(t)
	req := withWorkspaceUser(
		httptest.NewRequest("GET", "/api/v1/admin/backups/download?path=/x.tar.zst", nil),
		userID, wsID, "MEMBER",
	)
	rr := httptest.NewRecorder()
	h.Download(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rr.Code)
	}
}

func TestCovBakDownload_MissingPath_Returns400(t *testing.T) {
	h, userID, wsID := covBakRig(t)
	req := withWorkspaceUser(
		httptest.NewRequest("GET", "/api/v1/admin/backups/download", nil),
		userID, wsID, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.Download(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestCovBakDownload_PathTraversal_Returns400(t *testing.T) {
	h, userID, wsID := covBakRig(t)
	req := withWorkspaceUser(
		httptest.NewRequest("GET", "/x?path=../../etc/passwd", nil),
		userID, wsID, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.Download(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestCovBakDownload_ValidPathNonexistentBundle_Returns404(t *testing.T) {
	// Same as Delete: validation passes, the bundle is absent, so the
	// workspace-ownership probe returns 404 before os.Open.
	backupsDir := covBakHomeDir(t)
	h, userID, wsID := covBakRig(t)
	missing := filepath.Join(backupsDir, "crewship-workspace-ghost-20240101T000000Z.tar.zst")
	req := withWorkspaceUser(
		httptest.NewRequest("GET", "/x?path="+missing, nil),
		userID, wsID, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.Download(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rr.Code, rr.Body.String())
	}
}

// ── SelfTest ────────────────────────────────────────────────────────────

func TestCovBakSelfTest_MemberRole_Returns403(t *testing.T) {
	h, userID, wsID := covBakRig(t)
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/admin/backups/self-test",
			strings.NewReader(`{"crew_id":"c1"}`)),
		userID, wsID, "MEMBER",
	)
	rr := httptest.NewRecorder()
	h.SelfTest(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rr.Code)
	}
}

func TestCovBakSelfTest_NoWorkspace_Returns401(t *testing.T) {
	h, userID, _ := covBakRig(t)
	ctx := withUser(
		httptest.NewRequest("POST", "/x", nil).Context(),
		&AuthUser{ID: userID, Email: userID + "@example.com"},
	)
	ctx = withWorkspace(ctx, "", "OWNER")
	req := httptest.NewRequest("POST", "/x",
		strings.NewReader(`{"crew_id":"c1"}`)).WithContext(ctx)
	rr := httptest.NewRecorder()
	h.SelfTest(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rr.Code)
	}
}

func TestCovBakSelfTest_DockerNil_Returns503(t *testing.T) {
	// covBakRig wires dockerOps=nil, so SelfTest short-circuits with a
	// 503 before reading the body or touching the DB.
	h, userID, wsID := covBakRig(t)
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/admin/backups/self-test",
			strings.NewReader(`{"crew_id":"c1"}`)),
		userID, wsID, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.SelfTest(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovBakSelfTest_DockerNilWiresBeforeBody(t *testing.T) {
	// Even with an invalid body the docker-nil 503 wins (the nil check
	// precedes body decode). Pins that ordering so a future refactor
	// that moves body-decode earlier doesn't silently leak a 400 to
	// callers that just lack Docker.
	h, userID, wsID := covBakRig(t)
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/admin/backups/self-test",
			strings.NewReader(`{bad`)),
		userID, wsID, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.SelfTest(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body=%s", rr.Code, rr.Body.String())
	}
}
