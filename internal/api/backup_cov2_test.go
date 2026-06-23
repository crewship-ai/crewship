package api

// Second coverage pass for backup.go: Create/Restore's missing-user 401s,
// Create with an age recipient (encrypted bundle) + the catalog-upsert
// warn branch, Restore's invalid-identity 400 and the dry-run path against
// a bundle produced by a real Create, and validateBackupPath's symlink
// rejection.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"

	"github.com/crewship-ai/crewship/internal/backup"
)

// covBk3NoUserReq builds a request that carries workspace + role but NO
// authenticated user — the 401 guard must fire after the role gate.
func covBk3NoUserReq(method, target, body, wsID, role string) *http.Request {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	return req.WithContext(withWorkspace(req.Context(), wsID, role))
}

func TestBK3_Create_NoUser401(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	h, _, wsID := backupMutationRig(t)
	rr := httptest.NewRecorder()
	h.Create(rr, covBk3NoUserReq("POST", "/x", `{"scope":"workspace","no_encrypt":true}`, wsID, "OWNER"))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body=%s", rr.Code, rr.Body.String())
	}
}

func TestBK3_Restore_NoUser401(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	h, _, wsID := backupMutationRig(t)
	rr := httptest.NewRecorder()
	h.Restore(rr, covBk3NoUserReq("POST", "/x", `{"path":"x"}`, wsID, "OWNER"))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body=%s", rr.Code, rr.Body.String())
	}
}

func TestBK3_Create_AgeRecipient_EncryptedBundle(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	h, userID, wsID := backupMutationRig(t)

	// The catalog-upsert failure must stay non-fatal: bundle on disk,
	// 201 to the caller, warn in the log.
	if _, err := h.db.Exec(`
		CREATE TRIGGER bk3_block_catalog BEFORE INSERT ON backup_catalog
		BEGIN SELECT RAISE(ABORT, 'bk3 no catalog'); END`); err != nil {
		t.Fatalf("create trigger: %v", err)
	}

	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatalf("generate age identity: %v", err)
	}
	body := `{"scope":"workspace","recipient":"` + id.Recipient().String() + `"}`
	req := withWorkspaceUser(httptest.NewRequest("POST", "/x", strings.NewReader(body)), userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
	var resp createResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.Encrypted {
		t.Error("bundle must report encrypted=true with an age recipient")
	}
	if _, err := os.Stat(resp.Path); err != nil {
		t.Errorf("bundle not on disk: %v", err)
	}
}

// covBk3CreateBundle runs a real no-encrypt Create and returns the path.
func covBk3CreateBundle(t *testing.T, h *BackupHandler, userID, wsID string) string {
	t.Helper()
	body := `{"scope":"workspace","no_encrypt":true}`
	req := withWorkspaceUser(httptest.NewRequest("POST", "/x", strings.NewReader(body)), userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create bundle: status = %d; body=%s", rr.Code, rr.Body.String())
	}
	var resp createResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return resp.Path
}

func TestBK3_Restore_InvalidIdentity400(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	h, userID, wsID := backupMutationRig(t)
	path := covBk3CreateBundle(t, h, userID, wsID)

	body := `{"path":"` + path + `","identity":"AGE-SECRET-KEY-NOT-REAL"}`
	rr := httptest.NewRecorder()
	h.Restore(rr, withWorkspaceUser(httptest.NewRequest("POST", "/x", strings.NewReader(body)), userID, wsID, "OWNER"))
	if rr.Code != http.StatusBadRequest || !strings.Contains(rr.Body.String(), "invalid age identity") {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestBK3_Restore_DryRunOwnBundle(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	h, userID, wsID := backupMutationRig(t)
	path := covBk3CreateBundle(t, h, userID, wsID)

	body := `{"path":"` + path + `","dry_run":true}`
	rr := httptest.NewRecorder()
	h.Restore(rr, withWorkspaceUser(httptest.NewRequest("POST", "/x", strings.NewReader(body)), userID, wsID, "OWNER"))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := out["manifest"]; !ok {
		t.Error("response missing manifest")
	}
	// Dry-run gets its own audit action.
	var n int
	if err := h.db.QueryRow(
		`SELECT COUNT(*) FROM audit_logs WHERE action = 'backup.restore.dry_run'`).Scan(&n); err == nil && n == 0 {
		t.Error("no backup.restore.dry_run audit row")
	}
}

func TestBK3_ValidateBackupPath_SymlinkRejected(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir, err := backup.DefaultBackupsDir()
	if err != nil {
		t.Fatalf("DefaultBackupsDir: %v", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	real := filepath.Join(dir, "real.tar.zst")
	if err := os.WriteFile(real, []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	link := filepath.Join(dir, "link.tar.zst")
	if err := os.Symlink(real, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if err := validateBackupPath(link); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Errorf("err = %v, want symlink rejection", err)
	}
}
