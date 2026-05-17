package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/backup"
)

// backupAdminRig builds the workspace+user fixtures every admin test
// needs, plus a constructed BackupHandler. dockerOps is nil so SelfTest
// short-circuits to 503 (documented behaviour) — the role/body gates
// in front of the docker call are still exercised. Returning userID
// and wsID keeps the call sites compact while still letting tenant-
// isolation tests construct a second workspace.
func backupAdminRig(t *testing.T) (*BackupHandler, string, string) {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	h := NewBackupHandler(db, logger, nil, "test-version")
	return h, userID, wsID
}

// sandboxBackupsHome redirects DefaultBackupsDir() into a per-test temp
// directory and creates the .crewship/backups subtree. Without this,
// validateBackupPath would compare against the developer's real home
// dir and either reject paths we constructed or — worse — accept a
// real bundle path from the host machine. Returns the resolved
// backups dir for tests that need to construct in-bounds path strings.
func sandboxBackupsHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir, err := backup.DefaultBackupsDir()
	if err != nil {
		t.Fatalf("default backups dir: %v", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir backups: %v", err)
	}
	return dir
}

// ── Unlock ──────────────────────────────────────────────────────────────
//
// Unlock force-releases a stuck advisory lock — destructive enough that
// MEMBER roles must NEVER reach the DELETE statement. The role gate is
// the first check, before the auth-context check, so we test it
// independently. The handler also doubles as an idempotency surface:
// calling Unlock on a workspace that has no lock row must succeed
// (HTTP 204) so an admin can hit it speculatively without first
// running Status.

func TestBackupAdmin_Unlock_MemberRole_Returns403(t *testing.T) {
	// MEMBER → 403. canRole("manage") permits only OWNER + ADMIN; a
	// regression that flipped this to allow MEMBER would let any team
	// member release another admin's active backup lock.
	h, userID, wsID := backupAdminRig(t)
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

func TestBackupAdmin_Unlock_MissingWorkspaceContext_Returns401(t *testing.T) {
	// The role gate is checked first; with an OWNER role but no
	// workspace context the handler then bails on user==nil ||
	// workspaceID=="" with 401. Documented in backup_admin.go:31-34.
	// Spec said "missing workspace -> 400" but the code path
	// returns 401 — assert what the production code actually does
	// (we can't modify production code per the task rules).
	h, _, _ := backupAdminRig(t)
	// Build a context with role=OWNER but no workspace id and no user.
	ctx := withWorkspace(context.Background(), "", "OWNER")
	req := httptest.NewRequest("POST", "/api/v1/admin/backups/unlock", nil).WithContext(ctx)
	rr := httptest.NewRecorder()
	h.Unlock(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rr.Code)
	}
}

func TestBackupAdmin_Unlock_OwnerNeverLocked_Returns204(t *testing.T) {
	// Idempotent: Unlock against a workspace that has no existing
	// lock row succeeds with 204 No Content. ForceReleaseLock issues
	// a DELETE which is a no-op when zero rows match. Without this
	// guarantee an admin who hit Unlock by reflex would get a
	// spurious 500.
	h, userID, wsID := backupAdminRig(t)
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

func TestBackupAdmin_Unlock_OwnerReleasesExistingLock_Returns204(t *testing.T) {
	// Seed a real row in backup_locks (the actual production schema,
	// migration 48). Verify the lock is gone after Unlock so we
	// catch a regression where the handler returns 204 without
	// actually deleting (e.g. a typo in the WHERE clause).
	h, userID, wsID := backupAdminRig(t)
	if _, err := h.db.Exec(
		`INSERT INTO backup_locks (workspace_id, acquired_at, acquired_by, expires_at)
		 VALUES (?, datetime('now'), ?, datetime('now', '+1 hour'))`,
		wsID, userID,
	); err != nil {
		t.Fatalf("seed lock: %v", err)
	}

	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/admin/backups/unlock", nil),
		userID, wsID, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.Unlock(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%s", rr.Code, rr.Body.String())
	}

	// Verify the row vanished — a 204 with the lock still held would
	// be a silent regression (lock still blocks future Create calls).
	var n int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM backup_locks WHERE workspace_id = ?`, wsID).Scan(&n); err != nil {
		t.Fatalf("count locks: %v", err)
	}
	if n != 0 {
		t.Errorf("lock row count = %d after Unlock, want 0", n)
	}
}

// ── Rotate ──────────────────────────────────────────────────────────────
//
// Rotate is the only admin endpoint that takes a JSON body, so input
// validation matters: a malformed body, negative retention counts, or
// an "all zeroes" request must each be rejected with 400 (not 500 or
// silent success). The happy path against an empty backups dir
// should still return 200 with an empty `deleted` array — the
// caller's CLI loops over the array and a null would be a contract
// break.

func TestBackupAdmin_Rotate_MemberRole_Returns403(t *testing.T) {
	// MEMBER must be rejected before the body decode. Otherwise a
	// member could trigger filesystem walks on the backups dir via
	// a no-op body — fingerprinting / log-noise vector.
	h, userID, wsID := backupAdminRig(t)
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/admin/backups/rotate",
			strings.NewReader(`{"keep_last":5}`)),
		userID, wsID, "MEMBER",
	)
	rr := httptest.NewRecorder()
	h.Rotate(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rr.Code)
	}
}

func TestBackupAdmin_Rotate_MalformedBody_Returns400(t *testing.T) {
	// A truncated/invalid JSON body must surface as 400, not 500.
	// json.NewDecoder.Decode returns an error before any backup
	// package code runs, so this is a pure handler-level test.
	h, userID, wsID := backupAdminRig(t)
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/admin/backups/rotate",
			strings.NewReader(`{NOT_JSON`)),
		userID, wsID, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.Rotate(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestBackupAdmin_Rotate_BothRulesZero_Returns400(t *testing.T) {
	// keep_last=0 + keep_days=0 disables both rules — the caller
	// almost certainly meant to send positives. The handler's
	// "at least one must be positive" gate catches the no-op
	// request before it reaches the rotate logic.
	h, userID, wsID := backupAdminRig(t)
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

func TestBackupAdmin_Rotate_NegativeKeepLast_Returns400(t *testing.T) {
	// Negative inputs slipped past the "all zeroes" gate (the
	// positive companion satisfied the OR), so a dedicated negative
	// check is required. Documented in backup_admin.go:79-82.
	h, userID, wsID := backupAdminRig(t)
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/admin/backups/rotate",
			strings.NewReader(`{"keep_last":-1,"keep_days":7}`)),
		userID, wsID, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.Rotate(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestBackupAdmin_Rotate_OwnerEmptyDir_Returns200WithEmptyDeleted(t *testing.T) {
	// Happy path against a sandboxed empty backups dir. We're not
	// asserting that any files were deleted (there are none); we're
	// asserting the rotate code path runs end-to-end without a 5xx
	// and emits the documented response shape: deleted (array) +
	// dry_run (bool). CLI scripts iterate over `deleted`.
	sandboxBackupsHome(t)
	h, userID, wsID := backupAdminRig(t)
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/admin/backups/rotate",
			strings.NewReader(`{"keep_last":5,"dry_run":true}`)),
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
	if len(resp.Deleted) != 0 {
		t.Errorf("deleted = %v, want empty list (no bundles in sandbox)", resp.Deleted)
	}
	if !resp.DryRun {
		t.Errorf("dry_run echo = false, want true")
	}
}

// ── Delete ──────────────────────────────────────────────────────────────
//
// Delete consumes a `path` query param. The validateBackupPath gate
// defends against directory escapes; bundleBelongsToWorkspace defends
// against tenant cross-talk. Both must fail closed: a regression that
// allowed `../../etc/passwd` or a workspace-B bundle through would be
// a critical security bug.

func TestBackupAdmin_Delete_MemberRole_Returns403(t *testing.T) {
	// MEMBER hits the canRole gate before path validation; without
	// this the handler would proceed to Inspect arbitrary paths and
	// leak filesystem hints via the error body.
	h, userID, wsID := backupAdminRig(t)
	req := withWorkspaceUser(
		httptest.NewRequest("DELETE", "/api/v1/admin/backups?path=/whatever", nil),
		userID, wsID, "MEMBER",
	)
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rr.Code)
	}
}

func TestBackupAdmin_Delete_MissingPath_Returns400(t *testing.T) {
	// Empty `path` query param → 400 before any backup pkg call.
	// CLI sends `crewship backup delete <path>`; an empty arg
	// reaching the server is a client bug worth surfacing loudly.
	h, userID, wsID := backupAdminRig(t)
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

func TestBackupAdmin_Delete_PathTraversal_Returns400(t *testing.T) {
	// validateBackupPath defends against directory escapes; without
	// the gate an OWNER could nuke arbitrary host files via Delete.
	// Each form (relative ../, absolute /etc/...) must be rejected.
	sandboxBackupsHome(t)
	h, userID, wsID := backupAdminRig(t)
	for _, p := range []string{
		"../../etc/passwd",
		"/etc/shadow",
		"relative/with/../escape.tar.zst",
	} {
		req := withWorkspaceUser(
			httptest.NewRequest("DELETE", "/api/v1/admin/backups?path="+p, nil),
			userID, wsID, "OWNER",
		)
		rr := httptest.NewRecorder()
		h.Delete(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("path=%q: status = %d, want 400", p, rr.Code)
		}
	}
}

func TestBackupAdmin_Delete_UnknownPathUnderDir_Returns404(t *testing.T) {
	// A path that passes validateBackupPath but doesn't actually
	// exist (or isn't a real Crewship bundle) is rejected at the
	// bundleBelongsToWorkspace step with 404. This also covers the
	// cross-workspace case at the HTTP-status level — both
	// "unknown" and "workspace-B's bundle" yield the same 404
	// (Inspect either errors on a missing file or the manifest
	// workspace id doesn't match). A real cross-workspace bundle
	// fixture would need on-disk tar.zst with a valid MANIFEST.json
	// — too fragile for a handler-layer test, deferred to the
	// internal/backup integration suite.
	dir := sandboxBackupsHome(t)
	h, userID, wsID := backupAdminRig(t)
	nonexistent := filepath.Join(dir, "crewship-workspace-ghost-20260101T000000Z.tar.zst")
	req := withWorkspaceUser(
		httptest.NewRequest("DELETE", "/api/v1/admin/backups?path="+nonexistent, nil),
		userID, wsID, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rr.Code, rr.Body.String())
	}
}

// ── Download ────────────────────────────────────────────────────────────
//
// Download streams raw bundle bytes — leaking another workspace's
// bundle would leak the plaintext metadata (manifest contents are not
// encrypted even when the payload is). Same validation layers as
// Delete apply: role gate → missing param → path validation →
// workspace ownership check. The actual happy-path streaming is
// covered by integration tests with a real fixture; here we lock
// down the rejection paths.

func TestBackupAdmin_Download_MemberRole_Returns403(t *testing.T) {
	// MEMBER role must be denied before the stream begins — once
	// io.Copy starts there's no way to retract the bytes.
	h, userID, wsID := backupAdminRig(t)
	req := withWorkspaceUser(
		httptest.NewRequest("GET", "/api/v1/admin/backups/download?path=/whatever", nil),
		userID, wsID, "MEMBER",
	)
	rr := httptest.NewRecorder()
	h.Download(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rr.Code)
	}
}

func TestBackupAdmin_Download_MissingPath_Returns400(t *testing.T) {
	// Empty `path` → 400. Without the explicit check the handler
	// would attempt os.Open("") and return a less useful 404.
	h, userID, wsID := backupAdminRig(t)
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

func TestBackupAdmin_Download_PathTraversal_Returns400(t *testing.T) {
	// Directory escapes are the most dangerous failure mode here —
	// 200 with file bytes for /etc/shadow would be a critical bug.
	// Each traversal flavour must be rejected with 400.
	sandboxBackupsHome(t)
	h, userID, wsID := backupAdminRig(t)
	for _, p := range []string{
		"../../etc/passwd",
		"/etc/shadow",
		"relative/with/../escape.tar.zst",
	} {
		req := withWorkspaceUser(
			httptest.NewRequest("GET", "/api/v1/admin/backups/download?path="+p, nil),
			userID, wsID, "OWNER",
		)
		rr := httptest.NewRecorder()
		h.Download(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("path=%q: status = %d, want 400", p, rr.Code)
		}
	}
}

func TestBackupAdmin_Download_UnknownPathUnderDir_Returns404(t *testing.T) {
	// Path under the sandboxed backups dir that doesn't exist (or
	// isn't a real bundle) → 404 at bundleBelongsToWorkspace. We
	// must NOT see 200 with empty body here; that would mean the
	// handler proceeded to stream a non-existent file.
	dir := sandboxBackupsHome(t)
	h, userID, wsID := backupAdminRig(t)
	nonexistent := filepath.Join(dir, "crewship-workspace-ghost-20260101T000000Z.tar.zst")
	req := withWorkspaceUser(
		httptest.NewRequest("GET", "/api/v1/admin/backups/download?path="+nonexistent, nil),
		userID, wsID, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.Download(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rr.Code, rr.Body.String())
	}
}

// ── SelfTest ────────────────────────────────────────────────────────────
//
// SelfTest runs a server-side canary round-trip and depends on a wired
// dockerOps. With dockerOps=nil (our test rig) the handler 503s right
// after the role+auth checks; we therefore can only meaningfully test
// the rejection prefix here. The happy path needs a real Docker
// connection — deferred to an integration suite, see the skip comment
// on the deferred test.

func TestBackupAdmin_SelfTest_MemberRole_Returns403(t *testing.T) {
	// MEMBER role rejected before the dockerOps nil check, so this
	// surfaces the gate independently of Docker availability.
	h, userID, wsID := backupAdminRig(t)
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/admin/backups/self-test",
			strings.NewReader(`{"crew_id":"any"}`)),
		userID, wsID, "MEMBER",
	)
	rr := httptest.NewRecorder()
	h.SelfTest(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rr.Code)
	}
}

func TestBackupAdmin_SelfTest_DockerOpsNil_Returns503(t *testing.T) {
	// Our test rig wires dockerOps=nil (pure-DB mode). SelfTest
	// explicitly returns 503 in that case — the seed CLI relies on
	// this status to decide whether to skip the canary check. A
	// regression that returned 500 instead would surface as a hard
	// CI failure instead of a documented skip.
	h, userID, wsID := backupAdminRig(t)
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/admin/backups/self-test",
			strings.NewReader(`{"crew_id":"any"}`)),
		userID, wsID, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.SelfTest(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body=%s", rr.Code, rr.Body.String())
	}
}

func TestBackupAdmin_SelfTest_HappyPath_DeferredToIntegration(t *testing.T) {
	// Skipped: the happy path requires a wired backup.DockerOps
	// (real Docker engine), a live crew container with an injected
	// canary file, and the full collect→destroy→restore pipeline.
	// Exercising it from a handler-level test would mean either a
	// large mock surface (every DockerOps method) or a Docker-
	// dependent CI lane. The internal/backup package owns end-to-end
	// SelfTest coverage; this handler test file deliberately leaves
	// that gap to the integration suite.
	t.Skip("SelfTest happy-path requires real DockerOps + crew container; covered by internal/backup integration tests")
}
