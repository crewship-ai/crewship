package api

// The HTTP half of the disaster-recovery resume (#1716).
//
// The package-level flow is covered end to end in
// internal/backup/e2e_dr_files_only_test.go; what only the handler can
// get wrong is the wiring: forwarding `files_only` from the request
// body, and supplying the CALLER's workspace as the workspace whose
// provenance authorises the resume. Passing the bundle's workspace
// there instead — an easy mistake, since every other field in that
// options struct comes off the bundle — would authorise a resume
// against whichever workspace the bundle names rather than the one the
// operator is standing in.

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/backup"
)

// restoreRig returns a handler over a migrated DB with a seeded user and
// workspace, plus a real unencrypted bundle of that workspace sitting in
// the sandboxed backups dir so validateBackupPath accepts it.
func restoreRig(t *testing.T) (h *BackupHandler, userID, wsID, bundlePath string) {
	t.Helper()
	db := setupTestDB(t)
	userID = seedTestUser(t, db)
	wsID = seedTestWorkspace(t, db, userID)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	h = NewBackupHandler(db, logger, nil, "test-version")

	dir, err := backup.DefaultBackupsDir()
	if err != nil {
		t.Fatalf("default backups dir: %v", err)
	}
	res, err := backup.CreateBackup(context.Background(), db, backup.CreateOptions{
		Scope:       backup.ScopeWorkspace,
		WorkspaceID: wsID,
		OutputDir:   dir,
		Actor:       backup.Actor{UserID: userID, Email: "a@b.c", Role: "OWNER"},
		NoEncrypt:   true,
	})
	if err != nil {
		t.Fatalf("CreateBackup: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(res.Path) })
	return h, userID, wsID, res.Path
}

func postRestore(t *testing.T, h *BackupHandler, userID, wsID string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/admin/backups/restore", jsonBody(body)),
		userID, wsID, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.Restore(rr, req)
	return rr
}

// TestBackup_Restore_FilesOnly_RejectsRewriteCombination pins the
// handler forwarding `files_only` at all: if the field were dropped, the
// request below would be an ordinary restore of the caller's own bundle
// into their own workspace and would succeed with 200.
func TestBackup_Restore_FilesOnly_RejectsRewriteCombination(t *testing.T) {
	h, userID, wsID, bundlePath := restoreRig(t)

	rr := postRestore(t, h, userID, wsID, map[string]any{
		"path":         bundlePath,
		"files_only":   true,
		"as_workspace": "acme-dr",
	})
	if rr.Code == http.StatusOK {
		t.Fatalf("files_only + as_workspace was accepted; the handler is not forwarding files_only")
	}
	if !strings.Contains(rr.Body.String(), "files-only") {
		t.Errorf("rejection should name the flag; got %s", rr.Body.String())
	}
}

// TestBackup_Restore_FilesOnly_UsesCallerWorkspaceForProvenance is the
// wiring assertion. The caller stands in a workspace that has no
// provenance for this bundle, so the resume must be refused — and
// refused for THAT reason, naming the caller's workspace. A handler that
// passed the bundle's workspace id instead would sail past the check,
// because the bundle's own workspace is exactly the one whose id the
// manifest carries.
func TestBackup_Restore_FilesOnly_UsesCallerWorkspaceForProvenance(t *testing.T) {
	h, userID, wsID, bundlePath := restoreRig(t)

	rr := postRestore(t, h, userID, wsID, map[string]any{
		"path":       bundlePath,
		"files_only": true,
	})
	if rr.Code == http.StatusOK {
		t.Fatalf("a files-only resume with no recorded provenance returned 200")
	}
	var resp map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	msg, _ := resp["error"].(string)
	if !strings.Contains(msg, "not created by restoring this bundle") {
		t.Fatalf("refusal should be the provenance one; got %q", msg)
	}
	if !strings.Contains(msg, wsID) {
		t.Errorf("refusal should name the CALLER's workspace %s, so the operator can see which identity was checked; got %q", wsID, msg)
	}
}

// TestBackup_Restore_ReportsDroppedCrewFilesystems pins the field the
// CLI needs to tell an operator which crews still want the resume. It
// was computed by the restore runner and dropped on the floor here, so
// the CLI could only ever print the generic warning.
func TestBackup_Restore_ReportsDroppedCrewFilesystems(t *testing.T) {
	h, userID, wsID, bundlePath := restoreRig(t)

	rr := postRestore(t, h, userID, wsID, map[string]any{
		"path":         bundlePath,
		"as_workspace": "acme-dr",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("rewrite restore: status %d body %s", rr.Code, rr.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if skipped, _ := resp["docker_phase_skipped"].(bool); !skipped {
		t.Fatalf("a rewrite restore must report docker_phase_skipped")
	}
	if _, present := resp["dropped_crew_filesystems"]; !present {
		t.Fatalf("response omits dropped_crew_filesystems; the CLI cannot name the crews that still need --files-only")
	}
}
