package api

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/backup"
)

// backup_msg_sec_test.go pins the information-disclosure hardening on the
// backup admin endpoints: validateBackupPath's rejection reason (which may
// embed the absolute backups directory) is logged server-side only. The
// client-facing 400 response must carry a generic "invalid backup path"
// and never echo the host filesystem path. The validation behaviour
// itself (still rejecting out-of-tree paths with 400) is unchanged — these
// tests assert both halves so a regression that re-leaks the path, or one
// that stops rejecting, fails here.

func backupSecRig(t *testing.T) (*BackupHandler, string, string) {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	h := NewBackupHandler(db, logger, nil, "test-version")
	return h, userID, wsID
}

// Create with an output_dir outside the backups tree must 400, and the
// response body must not contain the resolved default backups directory.
func TestSecBackupMsg_CreateOutputDir_NoPathLeak(t *testing.T) {
	h, userID, wsID := backupSecRig(t)
	t.Setenv("HOME", t.TempDir()) // hermetic DefaultBackupsDir resolution
	defaultDir, err := backup.DefaultBackupsDir()
	if err != nil {
		t.Fatalf("DefaultBackupsDir: %v", err)
	}
	body := jsonBody(map[string]any{
		"scope":      "workspace",
		"no_encrypt": true,
		"output_dir": "/tmp/elsewhere",
	})
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/admin/backups", body),
		userID, wsID, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.Create(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
	respBody := rr.Body.String()
	if strings.Contains(respBody, defaultDir) {
		t.Fatalf("response leaks backups dir %q: %s", defaultDir, respBody)
	}
	if !strings.Contains(respBody, "invalid backup path") {
		t.Fatalf("response should carry generic message; got: %s", respBody)
	}
}

// Restore with a path outside the backups tree must 400, and the response
// body must not contain the resolved default backups directory.
func TestSecBackupMsg_RestorePath_NoPathLeak(t *testing.T) {
	h, userID, wsID := backupSecRig(t)
	t.Setenv("HOME", t.TempDir())
	defaultDir, err := backup.DefaultBackupsDir()
	if err != nil {
		t.Fatalf("DefaultBackupsDir: %v", err)
	}
	body := jsonBody(map[string]any{
		"path": "/tmp/elsewhere/bundle.tar.gz.age",
	})
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/admin/backups/restore", body),
		userID, wsID, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.Restore(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
	respBody := rr.Body.String()
	if strings.Contains(respBody, defaultDir) {
		t.Fatalf("response leaks backups dir %q: %s", defaultDir, respBody)
	}
	if !strings.Contains(respBody, "invalid backup path") {
		t.Fatalf("response should carry generic message; got: %s", respBody)
	}
}
