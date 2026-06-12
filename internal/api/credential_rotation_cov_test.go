package api

// Coverage tests for credential_rotation.go — auth/error branches not
// reached by credential_rotation_test.go, audit-failure tolerance, the
// terminal-cancel no-op, and the expiry worker's initial error pass.

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
)

func covCRLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func TestCovCRRotate_NoUser401(t *testing.T) {
	setTestEncryptionKeyParallelSafe(t)
	db := setupTestDB(t)
	h := NewCredentialHandler(db, covCRLogger())
	req := httptest.NewRequest("POST", "/x", nil)
	req.SetPathValue("credentialId", "c1")
	req = req.WithContext(withWorkspace(req.Context(), "ws", "OWNER"))
	rec := httptest.NewRecorder()
	h.Rotate(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestCovCRRotate_ReadError500(t *testing.T) {
	setTestEncryptionKeyParallelSafe(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewCredentialHandler(db, covCRLogger())
	db.Close()
	req := rotationReq(t, "POST", "/x", `{"value":"v"}`, userID, wsID)
	req.SetPathValue("credentialId", "c1")
	rec := httptest.NewRecorder()
	h.Rotate(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

func TestCovCRRotate_InsertRotationError500(t *testing.T) {
	setTestEncryptionKeyParallelSafe(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	credID := "cred-cov-ins"
	seedCredentialEnc(t, db, wsID, userID, credID, "K", "old")
	if _, err := db.Exec(`DROP TABLE credential_rotations`); err != nil {
		t.Fatalf("drop: %v", err)
	}
	h := NewCredentialHandler(db, covCRLogger())
	req := rotationReq(t, "POST", "/x", `{"value":"new"}`, userID, wsID)
	req.SetPathValue("credentialId", credID)
	rec := httptest.NewRecorder()
	h.Rotate(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

// TestCovCRRotate_AuditFailureStill200 drops the audit table — the rotation
// must still succeed because the audit insert is best-effort by design.
func TestCovCRRotate_AuditFailureStill200(t *testing.T) {
	setTestEncryptionKeyParallelSafe(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	credID := "cred-cov-audit"
	seedCredentialEnc(t, db, wsID, userID, credID, "K", "old")
	if _, err := db.Exec(`DROP TABLE credential_audit`); err != nil {
		t.Fatalf("drop: %v", err)
	}
	h := NewCredentialHandler(db, covCRLogger())
	req := rotationReq(t, "POST", "/x", `{"value":"new"}`, userID, wsID)
	req.SetPathValue("credentialId", credID)
	rec := httptest.NewRecorder()
	h.Rotate(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 despite audit failure; body=%s", rec.Code, rec.Body.String())
	}
}

func TestCovCRListRotations_Errors(t *testing.T) {
	t.Run("check error 500", func(t *testing.T) {
		setTestEncryptionKeyParallelSafe(t)
		db := setupTestDB(t)
		userID := seedTestUser(t, db)
		wsID := seedTestWorkspace(t, db, userID)
		h := NewCredentialHandler(db, covCRLogger())
		db.Close()
		req := rotationReq(t, "GET", "/x", "", userID, wsID)
		req.SetPathValue("credentialId", "c1")
		rec := httptest.NewRecorder()
		h.ListRotations(rec, req)
		if rec.Code != http.StatusInternalServerError {
			t.Errorf("status = %d, want 500", rec.Code)
		}
	})

	t.Run("list error 500", func(t *testing.T) {
		setTestEncryptionKeyParallelSafe(t)
		db := setupTestDB(t)
		userID := seedTestUser(t, db)
		wsID := seedTestWorkspace(t, db, userID)
		credID := "cred-cov-list"
		seedCredentialEnc(t, db, wsID, userID, credID, "K", "v")
		if _, err := db.Exec(`DROP TABLE credential_rotations`); err != nil {
			t.Fatalf("drop: %v", err)
		}
		h := NewCredentialHandler(db, covCRLogger())
		req := rotationReq(t, "GET", "/x", "", userID, wsID)
		req.SetPathValue("credentialId", credID)
		rec := httptest.NewRecorder()
		h.ListRotations(rec, req)
		if rec.Code != http.StatusInternalServerError {
			t.Errorf("status = %d, want 500", rec.Code)
		}
	})
}

func TestCovCRCancelRotation_TerminalNoOp(t *testing.T) {
	setTestEncryptionKeyParallelSafe(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	credID := "cred-cov-cancel"
	seedCredentialEnc(t, db, wsID, userID, credID, "K", "v")
	if _, err := db.Exec(`INSERT INTO credential_rotations
		(id, credential_id, old_value, grace_seconds, rotated_at, expires_at, rotated_by, status)
		VALUES ('rot-term', ?, '', 60, datetime('now'), datetime('now'), ?, 'CANCELLED')`, credID, userID); err != nil {
		t.Fatalf("seed rotation: %v", err)
	}
	h := NewCredentialHandler(db, covCRLogger())
	req := rotationReq(t, "DELETE", "/x", "", userID, wsID)
	req.SetPathValue("rotationId", "rot-term")
	rec := httptest.NewRecorder()
	h.CancelRotation(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Body.String(); !contains(got, "already terminal") {
		t.Errorf("body = %q, want terminal no-op message", got)
	}
}

func TestCovCRCancelRotation_ReadError500(t *testing.T) {
	setTestEncryptionKeyParallelSafe(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewCredentialHandler(db, covCRLogger())
	db.Close()
	req := rotationReq(t, "DELETE", "/x", "", userID, wsID)
	req.SetPathValue("rotationId", "rot-x")
	rec := httptest.NewRecorder()
	h.CancelRotation(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

func TestCovCRExpireGracedRotations_DBError(t *testing.T) {
	db := setupTestDB(t)
	db.Close()
	if _, err := ExpireGracedRotations(context.Background(), db, covCRLogger()); err == nil {
		t.Error("expected error on closed DB")
	}
}

// TestCovCRExpiryWorker_InitialPassErrorAndStop starts the worker against a
// closed DB (initial pass errors, must not panic) and stops it cleanly.
func TestCovCRExpiryWorker_InitialPassErrorAndStop(t *testing.T) {
	db := setupTestDB(t)
	db.Close()
	stop := make(chan struct{})
	var wg sync.WaitGroup
	StartCredentialRotationExpiryWorker(db, covCRLogger(), stop, &wg)
	close(stop)
	wg.Wait() // must return promptly; the ticker case never fires
}
