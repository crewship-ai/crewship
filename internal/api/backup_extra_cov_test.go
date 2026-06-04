package api

// Success-path coverage for the backup admin lifecycle handlers
// (backup_admin.go) and the catalog-backed List path (backup_query.go).
//
// The pre-existing backup_query_test.go / backup_admin_test.go /
// backup_mutation_test.go families only reach 403 / 400 / 404 because
// bundleBelongsToWorkspace re-inspects a REAL .tar.zst on disk and the
// earlier tests never write one. This file builds a minimal but
// structurally valid workspace-scoped bundle with backup.WriteBundle so
// Inspect accepts it, then drives Delete / Download / Inspect / Verify
// through their success branches and the catalog through its
// upsert/list/reconcile loop.
//
// SKIPPED (require Docker or a DB-dump binary, out of scope here):
//   - BackupHandler.Create  (CreateBackup → DB dump + optional Docker)
//   - BackupHandler.Restore (RestoreBackup → DB import + Docker phase)
//   - BackupHandler.SelfTest (needs wired dockerOps; nil here → 503)
//
// All helpers added here are prefixed covBk2; all test funcs TestCovBk2.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/backup"
)

// covBk2Rig builds a BackupHandler against a freshly-migrated test DB
// with HOME repointed at a temp dir so backup.DefaultBackupsDir resolves
// under it. Returns the handler, the seeded user + workspace IDs, and
// the resolved backups directory (already created on disk).
func covBk2Rig(t *testing.T) (h *BackupHandler, userID, wsID, backupsDir string) {
	t.Helper()
	// os.UserHomeDir reads $HOME on unix; repoint it so every
	// DefaultBackupsDir call in this test lands inside the temp tree
	// and validateBackupPath's prefix check passes.
	t.Setenv("HOME", t.TempDir())

	db := setupTestDB(t)
	userID = seedTestUser(t, db)
	wsID = seedTestWorkspace(t, db, userID)
	h = NewBackupHandler(db, newTestLogger(), nil, "test-version")

	dir, err := backup.DefaultBackupsDir()
	if err != nil {
		t.Fatalf("DefaultBackupsDir: %v", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir backups dir: %v", err)
	}
	return h, userID, wsID, dir
}

// covBk2WriteBundle assembles a real, inspectable, workspace-scoped
// bundle at dir/<canonical-name> and returns its path. The manifest
// anchors Contents.Workspace.ID to wsID so bundleBelongsToWorkspace
// returns true for the matching workspace and false for any other.
//
// NoEncrypt keeps the payload plaintext — Verify checks the sealed-byte
// SHA-256 which WriteBundle computes for us, so the round-trip is valid
// without any key material.
func covBk2WriteBundle(t *testing.T, dir, wsID string) string {
	t.Helper()
	return covBk2WriteBundleAt(t, dir, wsID, time.Now().UTC())
}

// covBk2WriteBundleAt is covBk2WriteBundle with an explicit CreatedAt.
// Rotate orders bundles by Manifest.CreatedAt (not file mtime), so
// tests that need a deterministic newest/oldest split set it directly.
func covBk2WriteBundleAt(t *testing.T, dir, wsID string, ts time.Time) string {
	t.Helper()
	ts = ts.UTC()
	path := filepath.Join(dir, backup.BundleFileName(backup.ScopeWorkspace, "test", ts))

	m := &backup.Manifest{
		FormatVersion:           backup.FormatVersion,
		CrewshipVersionAtBackup: "test-version",
		Scope:                   backup.ScopeWorkspace,
		ScopeLevel:              backup.ScopeLevelStandard,
		CompatibleTargets:       []backup.Target{backup.TargetAnyInstance},
		CreatedAt:               ts,
		CreatedBy:               backup.Actor{UserID: "test-user-id", Email: "test@example.com", Role: "OWNER"},
		Contents: backup.Contents{
			Workspace: &backup.WorkspaceSummary{ID: wsID, Slug: "test", Name: "Test"},
		},
	}

	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create bundle file: %v", err)
	}
	defer func() { _ = f.Close() }()

	// WriteBundle populates Encryption + Checksums.PayloadSHA256 itself.
	// Pass an (empty) reader, not nil — WriteBundle io.Copy's the payload
	// unconditionally and a nil src panics.
	payload := bytes.NewReader([]byte("crewship-test-payload"))
	if err := backup.WriteBundle(f, m, payload, backup.WriteBundleOptions{NoEncrypt: true}); err != nil {
		t.Fatalf("WriteBundle: %v", err)
	}
	return path
}

// ── Delete success ────────────────────────────────────────────────────

func TestCovBk2_Delete_RealBundle_Returns204AndRemovesFile(t *testing.T) {
	h, userID, wsID, dir := covBk2Rig(t)
	path := covBk2WriteBundle(t, dir, wsID)

	req := withWorkspaceUser(
		httptest.NewRequest("DELETE", "/api/v1/admin/backups?path="+path, nil),
		userID, wsID, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.Delete(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%s", rr.Code, rr.Body.String())
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("bundle still on disk after delete (stat err=%v)", err)
	}
}

func TestCovBk2_Delete_OtherWorkspaceBundle_Returns404(t *testing.T) {
	// Cross-tenant guard: a bundle anchored to a DIFFERENT workspace
	// must 404 even though it lives in the shared backups dir and
	// passes path validation. This is the security-relevant branch the
	// success test above does not exercise.
	h, userID, wsID, dir := covBk2Rig(t)
	path := covBk2WriteBundle(t, dir, "some-other-workspace")

	req := withWorkspaceUser(
		httptest.NewRequest("DELETE", "/api/v1/admin/backups?path="+path, nil),
		userID, wsID, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.Delete(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rr.Code, rr.Body.String())
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("other-workspace bundle was removed — cross-tenant delete leak: %v", err)
	}
}

// ── Download success ──────────────────────────────────────────────────

func TestCovBk2_Download_RealBundle_StreamsBytesAndHeaders(t *testing.T) {
	h, userID, wsID, dir := covBk2Rig(t)
	path := covBk2WriteBundle(t, dir, wsID)

	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read bundle: %v", err)
	}

	req := withWorkspaceUser(
		httptest.NewRequest("GET", "/api/v1/admin/backups/download?path="+path, nil),
		userID, wsID, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.Download(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("Content-Type"); got != "application/zstd" {
		t.Errorf("Content-Type = %q, want application/zstd", got)
	}
	if got := rr.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	if got := rr.Header().Get("Content-Disposition"); got == "" {
		t.Errorf("Content-Disposition header missing")
	}
	if got := rr.Body.Bytes(); len(got) != len(want) {
		t.Errorf("streamed %d bytes, want %d", len(got), len(want))
	}
}

func TestCovBk2_Download_OtherWorkspaceBundle_Returns404(t *testing.T) {
	h, userID, wsID, dir := covBk2Rig(t)
	path := covBk2WriteBundle(t, dir, "some-other-workspace")

	req := withWorkspaceUser(
		httptest.NewRequest("GET", "/api/v1/admin/backups/download?path="+path, nil),
		userID, wsID, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.Download(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rr.Code, rr.Body.String())
	}
}

// ── Inspect success ───────────────────────────────────────────────────

func TestCovBk2_Inspect_RealBundle_Returns200WithManifest(t *testing.T) {
	h, userID, wsID, dir := covBk2Rig(t)
	path := covBk2WriteBundle(t, dir, wsID)

	req := withWorkspaceUser(
		httptest.NewRequest("GET", "/api/v1/admin/backups/inspect?path="+path, nil),
		userID, wsID, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.Inspect(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var m backup.Manifest
	if err := json.Unmarshal(rr.Body.Bytes(), &m); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	if m.Contents.Workspace == nil || m.Contents.Workspace.ID != wsID {
		t.Errorf("manifest workspace id = %v, want %q", m.Contents.Workspace, wsID)
	}
	if m.Scope != backup.ScopeWorkspace {
		t.Errorf("scope = %q, want workspace", m.Scope)
	}
}

// ── Verify success ────────────────────────────────────────────────────

func TestCovBk2_Verify_RealBundle_Returns200ValidTrue(t *testing.T) {
	h, userID, wsID, dir := covBk2Rig(t)
	path := covBk2WriteBundle(t, dir, wsID)

	req := withWorkspaceUser(
		httptest.NewRequest("GET", "/api/v1/admin/backups/verify?path="+path, nil),
		userID, wsID, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.Verify(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Valid bool   `json:"valid"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.Valid {
		t.Errorf("valid = false on a freshly-written bundle; error=%q", resp.Error)
	}
	if resp.Error != "" {
		t.Errorf("error = %q, want empty on a valid bundle", resp.Error)
	}
}

// ── List via the catalog-backed fast path ─────────────────────────────

func TestCovBk2_List_CatalogRow_Returns200WithEntry(t *testing.T) {
	// Seed a real bundle + its catalog row so List takes the
	// ListCatalog branch (len(cat) > 0) and ReconcileCatalog keeps the
	// row because the backing file exists. This exercises the
	// catalog-preferred path the empty-DB test in backup_query_test.go
	// never reaches.
	h, userID, wsID, dir := covBk2Rig(t)
	path := covBk2WriteBundle(t, dir, wsID)

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat bundle: %v", err)
	}
	ctx := context.Background()
	entry := backup.CatalogEntry{
		FilePath:      path,
		WorkspaceID:   wsID,
		Scope:         string(backup.ScopeWorkspace),
		ScopeLevel:    string(backup.ScopeLevelStandard),
		Size:          info.Size(),
		Encrypted:     false,
		FormatVersion: backup.FormatVersion,
		CreatedAt:     time.Now().UTC(),
		CreatedBy:     "test@example.com",
	}
	if err := backup.UpsertCatalogEntry(ctx, h.db, entry); err != nil {
		t.Fatalf("UpsertCatalogEntry: %v", err)
	}

	req := withWorkspaceUser(
		httptest.NewRequest("GET", "/api/v1/admin/backups", nil),
		userID, wsID, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.List(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Data []struct {
			Path     string `json:"path"`
			FileName string `json:"file_name"`
			Scope    string `json:"scope"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Data) != 1 {
		t.Fatalf("data len = %d, want 1 (catalog row)", len(resp.Data))
	}
	if resp.Data[0].Path != path {
		t.Errorf("path = %q, want %q", resp.Data[0].Path, path)
	}
	if resp.Data[0].Scope != string(backup.ScopeWorkspace) {
		t.Errorf("scope = %q, want workspace", resp.Data[0].Scope)
	}
}

func TestCovBk2_List_CatalogRowMissingFile_ReconciledAway(t *testing.T) {
	// A catalog row whose backing file disappeared (out-of-band rm)
	// must be pruned by ReconcileCatalog, leaving List to fall through
	// to the empty filesystem scan and return an empty data set.
	h, userID, wsID, dir := covBk2Rig(t)
	path := filepath.Join(dir, backup.BundleFileName(backup.ScopeWorkspace, "test", time.Now().UTC()))
	// Note: no file written at path.

	ctx := context.Background()
	entry := backup.CatalogEntry{
		FilePath:      path,
		WorkspaceID:   wsID,
		Scope:         string(backup.ScopeWorkspace),
		ScopeLevel:    string(backup.ScopeLevelStandard),
		Size:          123,
		FormatVersion: backup.FormatVersion,
		CreatedAt:     time.Now().UTC(),
		CreatedBy:     "test@example.com",
	}
	if err := backup.UpsertCatalogEntry(ctx, h.db, entry); err != nil {
		t.Fatalf("UpsertCatalogEntry: %v", err)
	}

	req := withWorkspaceUser(
		httptest.NewRequest("GET", "/api/v1/admin/backups", nil),
		userID, wsID, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.List(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Data []any `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Data) != 0 {
		t.Errorf("data len = %d, want 0 after reconcile pruned the dangling row", len(resp.Data))
	}
}

// ── Rotate ────────────────────────────────────────────────────────────

func TestCovBk2_Rotate_DryRun_ListsCandidatesWithoutDeleting(t *testing.T) {
	// Two real workspace-scoped bundles; keep_last=1 with dry_run=true
	// should report the older one as a delete candidate while leaving
	// both files on disk. Exercises the Rotate success path + the
	// dry-run short-circuit that skips the catalog-sync loop.
	h, userID, wsID, dir := covBk2Rig(t)
	now := time.Now().UTC()
	older := covBk2WriteBundleAt(t, dir, wsID, now.Add(-48*time.Hour))
	newer := covBk2WriteBundleAt(t, dir, wsID, now)

	body := jsonBody(map[string]any{"keep_last": 1, "dry_run": true})
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/admin/backups/rotate", body),
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
		t.Errorf("dry_run = false, want true echo")
	}
	// Both files must still exist after a dry run.
	for _, p := range []string{older, newer} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("bundle %s removed during dry run: %v", filepath.Base(p), err)
		}
	}
	if len(resp.Deleted) == 0 {
		t.Errorf("dry-run deleted list empty; expected the older bundle as a candidate")
	}
}

func TestCovBk2_Rotate_Applied_DeletesAndSyncsCatalog(t *testing.T) {
	// Non-dry-run rotate with keep_last=1 deletes the older bundle for
	// real and walks the catalog-delete + audit-log loop. The newer
	// bundle survives.
	h, userID, wsID, dir := covBk2Rig(t)
	now := time.Now().UTC()
	old := now.Add(-48 * time.Hour)
	older := covBk2WriteBundleAt(t, dir, wsID, old)
	newer := covBk2WriteBundleAt(t, dir, wsID, now)

	// Catalog the older bundle so the post-rotate DeleteCatalogEntry
	// branch has a row to drop.
	info, _ := os.Stat(older)
	_ = backup.UpsertCatalogEntry(context.Background(), h.db, backup.CatalogEntry{
		FilePath:      older,
		WorkspaceID:   wsID,
		Scope:         string(backup.ScopeWorkspace),
		ScopeLevel:    string(backup.ScopeLevelStandard),
		Size:          info.Size(),
		FormatVersion: backup.FormatVersion,
		CreatedAt:     old,
		CreatedBy:     "test@example.com",
	})

	body := jsonBody(map[string]any{"keep_last": 1, "dry_run": false})
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/admin/backups/rotate", body),
		userID, wsID, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.Rotate(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if _, err := os.Stat(older); !os.IsNotExist(err) {
		t.Errorf("older bundle survived applied rotate (stat err=%v)", err)
	}
	if _, err := os.Stat(newer); err != nil {
		t.Errorf("newer bundle removed by keep_last=1 rotate: %v", err)
	}
}

func TestCovBk2_Rotate_BadBody_Returns400(t *testing.T) {
	h, userID, wsID, _ := covBk2Rig(t)
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/admin/backups/rotate", jsonBody("not-an-object")),
		userID, wsID, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.Rotate(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovBk2_Rotate_Negative_Returns400(t *testing.T) {
	h, userID, wsID, _ := covBk2Rig(t)
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/admin/backups/rotate", jsonBody(map[string]any{"keep_last": -1})),
		userID, wsID, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.Rotate(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovBk2_Rotate_NoRuleSet_Returns400(t *testing.T) {
	h, userID, wsID, _ := covBk2Rig(t)
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/admin/backups/rotate", jsonBody(map[string]any{})),
		userID, wsID, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.Rotate(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovBk2_Rotate_MemberRole_Returns403(t *testing.T) {
	h, userID, wsID, _ := covBk2Rig(t)
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/admin/backups/rotate", jsonBody(map[string]any{"keep_last": 1})),
		userID, wsID, "MEMBER",
	)
	rr := httptest.NewRecorder()
	h.Rotate(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rr.Code, rr.Body.String())
	}
}

// ── Unlock ────────────────────────────────────────────────────────────

func TestCovBk2_Unlock_OwnerNoLock_Returns204(t *testing.T) {
	// ForceReleaseLock on a never-locked workspace is a no-op DELETE
	// that still returns 204 — the canonical "clear a stale lock that
	// already expired" path. Drives the Unlock success branch.
	h, userID, wsID, _ := covBk2Rig(t)
	req := withWorkspaceUser(
		httptest.NewRequest("DELETE", "/api/v1/admin/backups/status", nil),
		userID, wsID, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.Unlock(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovBk2_Unlock_MemberRole_Returns403(t *testing.T) {
	h, userID, wsID, _ := covBk2Rig(t)
	req := withWorkspaceUser(
		httptest.NewRequest("DELETE", "/api/v1/admin/backups/status", nil),
		userID, wsID, "MEMBER",
	)
	rr := httptest.NewRecorder()
	h.Unlock(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rr.Code, rr.Body.String())
	}
}

// ── SelfTest (dockerOps nil → 503) ────────────────────────────────────

func TestCovBk2_SelfTest_NoDockerOps_Returns503(t *testing.T) {
	// dockerOps is nil in covBk2Rig, so SelfTest must short-circuit to
	// 503 before touching any crew. The full pipeline (collect →
	// restore → verify) needs Docker and is intentionally NOT covered.
	h, userID, wsID, _ := covBk2Rig(t)
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/admin/backups/self-test", jsonBody(map[string]any{"crew_id": "c1"})),
		userID, wsID, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.SelfTest(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body=%s", rr.Code, rr.Body.String())
	}
}
