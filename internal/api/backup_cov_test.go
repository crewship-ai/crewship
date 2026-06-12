package api

// Coverage for backup.go — the Restore handler's guard chain (role, body,
// path validation, existence, cross-tenant authorization, identity parse,
// RestoreBackup failure mapping) and allowRestore's slug-match / anchorless
// paths that backup_authorize_test.go doesn't reach. Reuses the covBk2
// bundle-builder helpers from backup_extra_cov_test.go.
//
// A full RestoreBackup success needs a real DB dump payload (and Docker
// for the volume phase) — out of scope; the failure mapping after a valid
// authorization is asserted instead.

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/backup"
)

func covRestorePost(t *testing.T, h *BackupHandler, userID, wsID, role, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/v1/admin/backups/restore", strings.NewReader(body))
	req = withWorkspaceUser(req, userID, wsID, role)
	rr := httptest.NewRecorder()
	h.Restore(rr, req)
	return rr
}

func TestBackupRestore_GuardChain(t *testing.T) {
	h, userID, wsID, dir := covBk2Rig(t)

	t.Run("member forbidden", func(t *testing.T) {
		if rr := covRestorePost(t, h, userID, wsID, "MEMBER", `{"path":"x"}`); rr.Code != http.StatusForbidden {
			t.Errorf("status = %d, want 403", rr.Code)
		}
	})
	t.Run("bad body", func(t *testing.T) {
		if rr := covRestorePost(t, h, userID, wsID, "OWNER", "{nope"); rr.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", rr.Code)
		}
	})
	t.Run("missing path", func(t *testing.T) {
		if rr := covRestorePost(t, h, userID, wsID, "OWNER", `{}`); rr.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", rr.Code)
		}
	})
	t.Run("path outside backups dir", func(t *testing.T) {
		rr := covRestorePost(t, h, userID, wsID, "OWNER", `{"path":"/etc/passwd"}`)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", rr.Code)
		}
		if !strings.Contains(rr.Body.String(), "invalid backup path") {
			t.Errorf("body must stay generic, got %q", rr.Body.String())
		}
	})
	t.Run("path traversal", func(t *testing.T) {
		rr := covRestorePost(t, h, userID, wsID, "OWNER", `{"path":"`+dir+`/../escape.tar.zst"}`)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", rr.Code)
		}
	})
	t.Run("nonexistent bundle 404", func(t *testing.T) {
		ghost := filepath.Join(dir, "crewship-workspace-ghost-2026-01-01T00-00-00Z.tar.zst")
		rr := covRestorePost(t, h, userID, wsID, "OWNER", `{"path":"`+ghost+`"}`)
		if rr.Code != http.StatusNotFound {
			t.Errorf("status = %d, want 404; body=%s", rr.Code, rr.Body.String())
		}
	})
}

// covWriteForeignBundle writes a workspace-scoped bundle whose id AND
// slug both differ from the caller's workspace, so neither allowRestore
// path can match.
func covWriteForeignBundle(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, backup.BundleFileName(backup.ScopeWorkspace, "foreign", time.Now().UTC()))
	m := &backup.Manifest{
		FormatVersion:           backup.FormatVersion,
		CrewshipVersionAtBackup: "test-version",
		Scope:                   backup.ScopeWorkspace,
		ScopeLevel:              backup.ScopeLevelStandard,
		CompatibleTargets:       []backup.Target{backup.TargetAnyInstance},
		CreatedAt:               time.Now().UTC(),
		CreatedBy:               backup.Actor{UserID: "u", Email: "e@x", Role: "OWNER"},
		Contents: backup.Contents{
			Workspace: &backup.WorkspaceSummary{ID: "ws-some-other-tenant", Slug: "foreign", Name: "Foreign"},
		},
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer f.Close()
	if err := backup.WriteBundle(f, m, bytes.NewReader([]byte("payload")), backup.WriteBundleOptions{NoEncrypt: true}); err != nil {
		t.Fatalf("WriteBundle: %v", err)
	}
	return path
}

func TestBackupRestore_CrossTenantBundleDenied(t *testing.T) {
	h, userID, wsID, dir := covBk2Rig(t)
	path := covWriteForeignBundle(t, dir)
	rr := covRestorePost(t, h, userID, wsID, "OWNER", `{"path":"`+path+`"}`)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", rr.Code, rr.Body.String())
	}
	// The deny reason must not echo the bundle's workspace identity.
	if strings.Contains(rr.Body.String(), "ws-some-other-tenant") {
		t.Errorf("deny reason leaks bundle identity: %q", rr.Body.String())
	}
}

func TestBackupRestore_InvalidAgeIdentity400(t *testing.T) {
	h, userID, wsID, dir := covBk2Rig(t)
	path := covBk2WriteBundle(t, dir, wsID)
	rr := covRestorePost(t, h, userID, wsID, "OWNER",
		`{"path":"`+path+`","identity":"AGE-SECRET-KEY-NOT-REALLY"}`)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "invalid age identity") {
		t.Errorf("body = %q", rr.Body.String())
	}
}

func TestBackupRestore_AuthorizedButUnrestorablePayload(t *testing.T) {
	// Same-workspace bundle passes every guard; RestoreBackup then fails
	// on the synthetic payload, and the handler must map the error via
	// statusForBackupError instead of 200-ing.
	h, userID, wsID, dir := covBk2Rig(t)
	path := covBk2WriteBundle(t, dir, wsID)
	rr := covRestorePost(t, h, userID, wsID, "OWNER", `{"path":"`+path+`","dry_run":true}`)
	if rr.Code == http.StatusOK || rr.Code == http.StatusForbidden || rr.Code == http.StatusNotFound {
		t.Fatalf("status = %d; expected an error mapped from RestoreBackup; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "error") {
		t.Errorf("body = %q", rr.Body.String())
	}
}

// ---- allowRestore ----

func TestAllowRestore_SlugMatchAllowsPostNukeDR(t *testing.T) {
	h, _, wsID, dir := covBk2Rig(t)
	// Bundle anchored to a DIFFERENT workspace id but the SAME slug
	// ("test" — covBk2WriteBundle hard-codes the slug, and
	// seedTestWorkspace creates the caller's row with slug "test").
	path := covBk2WriteBundle(t, dir, "ws-old-cuid-before-nuke")

	allowed, reason, err := allowRestore(context.Background(), h.db, path, wsID)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !allowed {
		t.Errorf("slug-match DR path denied: %q", reason)
	}
}

func TestAllowRestore_IDMatchAllows(t *testing.T) {
	h, _, wsID, dir := covBk2Rig(t)
	path := covBk2WriteBundle(t, dir, wsID)
	allowed, reason, err := allowRestore(context.Background(), h.db, path, wsID)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if !allowed {
		t.Errorf("id-match denied: %q", reason)
	}
}

func TestAllowRestore_NoWorkspaceAnchorDefers(t *testing.T) {
	h, _, wsID, dir := covBk2Rig(t)
	// Hand-build a bundle with no Contents.Workspace — allowRestore must
	// defer (allow) and let the restore flow fail loudly later.
	path := filepath.Join(dir, backup.BundleFileName(backup.ScopeInstance, "inst", time.Now().UTC()))
	m := &backup.Manifest{
		FormatVersion:           backup.FormatVersion,
		CrewshipVersionAtBackup: "test-version",
		Scope:                   backup.ScopeInstance,
		ScopeLevel:              backup.ScopeLevelStandard,
		CompatibleTargets:       []backup.Target{backup.TargetAnyInstance},
		CreatedAt:               time.Now().UTC(),
		CreatedBy:               backup.Actor{UserID: "u", Email: "e@x", Role: "OWNER"},
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer f.Close()
	if err := backup.WriteBundle(f, m, bytes.NewReader([]byte("payload")), backup.WriteBundleOptions{NoEncrypt: true}); err != nil {
		t.Fatalf("WriteBundle: %v", err)
	}

	allowed, reason, aerr := allowRestore(context.Background(), h.db, path, wsID)
	if aerr != nil {
		t.Fatalf("err = %v", aerr)
	}
	if !allowed {
		t.Errorf("anchorless bundle should defer to restore flow, got deny: %q", reason)
	}
}

func TestAllowRestore_MismatchDeniesGenerically(t *testing.T) {
	h, _, wsID, dir := covBk2Rig(t)
	// Different id AND different slug: change the caller's slug so the
	// slug path can't match either.
	if _, err := h.db.Exec(`UPDATE workspaces SET slug = 'renamed' WHERE id = ?`, wsID); err != nil {
		t.Fatalf("update slug: %v", err)
	}
	path := covBk2WriteBundle(t, dir, "ws-foreign")
	allowed, reason, err := allowRestore(context.Background(), h.db, path, wsID)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if allowed {
		t.Fatal("foreign bundle allowed")
	}
	if strings.Contains(reason, "ws-foreign") || strings.Contains(reason, "test") {
		t.Errorf("deny reason leaks bundle identity: %q", reason)
	}
}
