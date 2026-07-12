package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

// The audit timeline is a FORENSIC record and must outlive the credential.
// REVOKE is written AFTER the row is soft-deleted, and the read path's
// existence check filtered `deleted_at IS NULL`, so `credential audit` 404'd
// for any deleted credential — hiding the REVOKE event and all prior ROTATE
// history exactly when it matters most ("who deleted this, and when?").
func TestAuditTimeline_VisibleAfterSoftDelete(t *testing.T) {
	setTestEncryptionKeyParallelSafe(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	credID := "cred-audit-afterdel"
	seedCredentialEnc(t, db, wsID, userID, credID, "TEST_KEY", "v")
	ctx := context.Background()

	_ = RecordCredentialEvent(ctx, db, auditLogger(), credID, AuditEventCreated, "", "", nil)
	_ = RecordCredentialEvent(ctx, db, auditLogger(), credID, AuditEventRotate, "", "", nil)
	// Soft-delete (as the Delete handler does), then record REVOKE after it.
	if _, err := db.Exec(`UPDATE credentials SET deleted_at = '2026-02-01T00:00:00Z' WHERE id = ?`, credID); err != nil {
		t.Fatalf("soft delete: %v", err)
	}
	_ = RecordCredentialEvent(ctx, db, auditLogger(), credID, AuditEventRevoke, "", "", nil)

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	h := NewCredentialHandler(db, logger)

	req := httptest.NewRequest("GET", "/api/v1/credentials/"+credID+"/audit", nil)
	req.SetPathValue("credentialId", credID)
	rctx := withUser(req.Context(), &AuthUser{ID: userID})
	rctx = withWorkspace(rctx, wsID, "OWNER")
	req = req.WithContext(rctx)
	rr := httptest.NewRecorder()
	h.AuditTimeline(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("audit timeline 404s after soft-delete: got %d, want 200", rr.Code)
	}
	var events []auditEventResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &events); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var sawRotate, sawRevoke bool
	for _, e := range events {
		sawRotate = sawRotate || e.EventType == "ROTATE"
		sawRevoke = sawRevoke || e.EventType == "REVOKE"
	}
	if !sawRotate || !sawRevoke {
		t.Fatalf("post-delete timeline missing events: ROTATE=%v REVOKE=%v (want both visible)", sawRotate, sawRevoke)
	}
}
