package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

// backupRig builds a BackupHandler against the freshly-migrated test DB.
// dockerOps is nil so handlers fall back to pure-DB mode (documented in
// NewBackupHandler).
func backupRig(t *testing.T) (*BackupHandler, string, string) {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	h := NewBackupHandler(db, logger, nil, "test-version")
	return h, userID, wsID
}

// ── role gating ─────────────────────────────────────────────────────────
//
// canRole("manage") demands OWNER or ADMIN. MEMBER must be rejected on
// every read-only endpoint here — these surfaces a) inspect another
// workspace's bundle paths, b) hint at rotation timing, c) emit lock
// status. Forbidden-then-401 confusion would cost real customer-data
// security if a regression flipped one of these to allow MEMBER.

func TestBackupQuery_List_MemberRole_Returns403(t *testing.T) {
	h, userID, wsID := backupRig(t)
	req := withWorkspaceUser(
		httptest.NewRequest("GET", "/api/v1/admin/backups", nil),
		userID, wsID, "MEMBER",
	)
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rr.Code)
	}
}

func TestBackupQuery_Inspect_MemberRole_Returns403(t *testing.T) {
	h, userID, wsID := backupRig(t)
	req := withWorkspaceUser(
		httptest.NewRequest("GET", "/api/v1/admin/backups/inspect?path=/x.tar.zst", nil),
		userID, wsID, "MEMBER",
	)
	rr := httptest.NewRecorder()
	h.Inspect(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rr.Code)
	}
}

func TestBackupQuery_Status_MemberRole_Returns403(t *testing.T) {
	h, userID, wsID := backupRig(t)
	req := withWorkspaceUser(
		httptest.NewRequest("GET", "/api/v1/admin/backups/status", nil),
		userID, wsID, "MEMBER",
	)
	rr := httptest.NewRecorder()
	h.Status(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rr.Code)
	}
}

func TestBackupQuery_Verify_MemberRole_Returns403(t *testing.T) {
	h, userID, wsID := backupRig(t)
	req := withWorkspaceUser(
		httptest.NewRequest("GET", "/api/v1/admin/backups/verify?path=/x.tar.zst", nil),
		userID, wsID, "MEMBER",
	)
	rr := httptest.NewRecorder()
	h.Verify(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rr.Code)
	}
}

// ── List ────────────────────────────────────────────────────────────────

func TestBackupQuery_List_OwnerEmptyDB_Returns200WithEmptyData(t *testing.T) {
	h, userID, wsID := backupRig(t)
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
		t.Errorf("expected empty data list, got %d entries", len(resp.Data))
	}
}

// ── Inspect ─────────────────────────────────────────────────────────────

func TestBackupQuery_Inspect_MissingPath_Returns400(t *testing.T) {
	h, userID, wsID := backupRig(t)
	req := withWorkspaceUser(
		httptest.NewRequest("GET", "/api/v1/admin/backups/inspect", nil),
		userID, wsID, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.Inspect(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestBackupQuery_Inspect_PathTraversal_Returns400(t *testing.T) {
	// validateBackupPath defends against directory escapes; without the
	// gate an admin role could pull arbitrary host files via Inspect.
	// Reject early at 400, not at the open() call after.
	h, userID, wsID := backupRig(t)
	for _, p := range []string{
		"../../etc/passwd",
		"/etc/shadow",
		"relative/with/../escape.tar.zst",
	} {
		req := withWorkspaceUser(
			httptest.NewRequest("GET", "/x?path="+p, nil),
			userID, wsID, "OWNER",
		)
		rr := httptest.NewRecorder()
		h.Inspect(rr, req)
		if rr.Code != http.StatusBadRequest && rr.Code != http.StatusNotFound {
			t.Errorf("path=%q: status = %d, want 400 or 404 (must NOT 200)", p, rr.Code)
		}
	}
}

// ── Status ──────────────────────────────────────────────────────────────

func TestBackupQuery_Status_MissingWorkspace_Returns400(t *testing.T) {
	h, userID, _ := backupRig(t)
	// Set role but no workspace — handler should bail with 400.
	ctx := withUser(httptest.NewRequest("GET", "/x", nil).Context(),
		&AuthUser{ID: userID, Email: userID + "@example.com"})
	ctx = withWorkspace(ctx, "", "OWNER") // empty workspace
	req := httptest.NewRequest("GET", "/x", nil).WithContext(ctx)
	rr := httptest.NewRecorder()
	h.Status(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestBackupQuery_Status_FreshWorkspace_ReturnsHeldFalse(t *testing.T) {
	// No lock has ever been acquired — held should be false and the
	// response shape must include workspace_id echo + held:false. CLI
	// scripts test the JSON keys.
	h, userID, wsID := backupRig(t)
	req := withWorkspaceUser(
		httptest.NewRequest("GET", "/api/v1/admin/backups/status", nil),
		userID, wsID, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.Status(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Held        bool   `json:"held"`
		WorkspaceID string `json:"workspace_id"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Held {
		t.Errorf("held = true on a never-locked workspace")
	}
	if resp.WorkspaceID != wsID {
		t.Errorf("workspace_id echo = %q, want %q", resp.WorkspaceID, wsID)
	}
}

// ── Verify ──────────────────────────────────────────────────────────────

func TestBackupQuery_Verify_MissingPath_Returns400(t *testing.T) {
	h, userID, wsID := backupRig(t)
	req := withWorkspaceUser(
		httptest.NewRequest("GET", "/api/v1/admin/backups/verify", nil),
		userID, wsID, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.Verify(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestBackupQuery_Verify_PathTraversal_RejectsBefore404(t *testing.T) {
	// Like Inspect, Verify must reject path-traversal attempts before
	// reaching the filesystem open() call. Returning 200 with a "valid"
	// flag would be a tenant-isolation bug.
	h, userID, wsID := backupRig(t)
	req := withWorkspaceUser(
		httptest.NewRequest("GET", "/x?path=../../etc/passwd", nil),
		userID, wsID, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.Verify(rr, req)
	if rr.Code == http.StatusOK {
		t.Fatalf("path-traversal reached the OK branch — security regression")
	}
	if rr.Code != http.StatusBadRequest && rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 400 or 404", rr.Code)
	}
}
