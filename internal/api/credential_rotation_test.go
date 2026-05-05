package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

// rotationCtx wires the auth + workspace context the rotation handlers
// need. setTestEncryptionKeyParallelSafe must already have run.
func rotationReq(t *testing.T, method, path, body string, userID, wsID string) *http.Request {
	t.Helper()
	var rdr *bytes.Buffer
	if body != "" {
		rdr = bytes.NewBufferString(body)
	} else {
		rdr = &bytes.Buffer{}
	}
	req := httptest.NewRequest(method, path, rdr)
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, "OWNER")
	req = req.WithContext(ctx)
	return req
}

// TestRotate_HappyPath verifies the central design intent: new value
// flips into credentials.encrypted_value immediately (so fresh agents
// get the new key) AND a credential_rotations row preserves the old
// value for the grace window so the sidecar can fall back during
// in-flight runs.
func TestRotate_HappyPath(t *testing.T) {
	t.Parallel()
	setTestEncryptionKeyParallelSafe(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	credID := "cred-rot-1"
	seedCredentialEnc(t, db, wsID, userID, credID, "TEST_KEY", "old-value")

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	h := NewCredentialHandler(db, logger)

	body, _ := json.Marshal(map[string]any{"value": "new-value"})
	req := rotationReq(t, "POST", "/api/v1/credentials/"+credID+"/rotate", string(body), userID, wsID)
	req.SetPathValue("credentialId", credID)
	rr := httptest.NewRecorder()
	h.Rotate(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var got rotationResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Status != "ACTIVE" {
		t.Errorf("status = %q, want ACTIVE", got.Status)
	}
	if got.GraceSeconds != defaultGraceSeconds {
		t.Errorf("default grace = %d, want %d", got.GraceSeconds, defaultGraceSeconds)
	}

	// Old value preserved in credential_rotations for sidecar fallback.
	var oldValue string
	var status string
	if err := db.QueryRow(`SELECT old_value, status FROM credential_rotations WHERE id = ?`, got.ID).Scan(&oldValue, &status); err != nil {
		t.Fatalf("read rotation row: %v", err)
	}
	if oldValue == "" {
		t.Error("old_value missing on ACTIVE rotation")
	}
	if status != "ACTIVE" {
		t.Errorf("rotation status = %q", status)
	}

	// New value flipped into credentials.encrypted_value.
	var encNow string
	if err := db.QueryRow(`SELECT encrypted_value FROM credentials WHERE id = ?`, credID).Scan(&encNow); err != nil {
		t.Fatalf("read credential: %v", err)
	}
	if encNow == oldValue {
		t.Error("credentials.encrypted_value still holds the old value after rotate")
	}

	// Audit event recorded.
	var auditCount int
	_ = db.QueryRow(`SELECT COUNT(*) FROM credential_audit WHERE credential_id = ? AND event_type = 'ROTATE'`, credID).Scan(&auditCount)
	if auditCount != 1 {
		t.Errorf("audit ROTATE count = %d, want 1", auditCount)
	}
}

// TestRotate_CustomGrace verifies the configurable window (0..7d).
func TestRotate_CustomGrace(t *testing.T) {
	t.Parallel()
	setTestEncryptionKeyParallelSafe(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	credID := "cred-rot-grace"
	seedCredentialEnc(t, db, wsID, userID, credID, "TEST_KEY", "old")

	h := NewCredentialHandler(db, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})))

	// 0 grace = immediate (matches GitLab default).
	body, _ := json.Marshal(map[string]any{"value": "new", "grace_seconds": 0})
	req := rotationReq(t, "POST", "/", string(body), userID, wsID)
	req.SetPathValue("credentialId", credID)
	rr := httptest.NewRecorder()
	h.Rotate(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("immediate grace status=%d body=%s", rr.Code, rr.Body.String())
	}
	var got rotationResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &got)
	if got.GraceSeconds != 0 {
		t.Errorf("grace_seconds = %d, want 0", got.GraceSeconds)
	}

	// > 7d should reject.
	body, _ = json.Marshal(map[string]any{"value": "new2", "grace_seconds": maxGraceSeconds + 1})
	req = rotationReq(t, "POST", "/", string(body), userID, wsID)
	req.SetPathValue("credentialId", credID)
	rr = httptest.NewRecorder()
	h.Rotate(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("over-cap grace status = %d, want 400", rr.Code)
	}
}

// TestExpireGracedRotations is the regression guard for the cron path:
// a rotation whose expires_at is in the past must transition to
// EXPIRED and have old_value scrubbed.
func TestExpireGracedRotations(t *testing.T) {
	t.Parallel()
	setTestEncryptionKeyParallelSafe(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	credID := "cred-rot-expire"
	seedCredentialEnc(t, db, wsID, userID, credID, "TEST_KEY", "old")

	// Hand-craft an ACTIVE rotation in the past — bypasses the
	// handler so we can fast-forward without sleeping.
	pastExpiry := time.Now().UTC().Add(-1 * time.Hour).Format(time.RFC3339)
	if _, err := db.Exec(`
		INSERT INTO credential_rotations (id, credential_id, old_value, grace_seconds, expires_at, rotated_by, status)
		VALUES ('r1', ?, 'enc-old', 3600, ?, ?, 'ACTIVE')`, credID, pastExpiry, userID); err != nil {
		t.Fatalf("seed expired rotation: %v", err)
	}
	// And a still-active one for negative control.
	futureExpiry := time.Now().UTC().Add(1 * time.Hour).Format(time.RFC3339)
	if _, err := db.Exec(`
		INSERT INTO credential_rotations (id, credential_id, old_value, grace_seconds, expires_at, rotated_by, status)
		VALUES ('r2', ?, 'enc-old-2', 3600, ?, ?, 'ACTIVE')`, credID, futureExpiry, userID); err != nil {
		t.Fatalf("seed active rotation: %v", err)
	}

	n, err := ExpireGracedRotations(context.Background(), db, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})))
	if err != nil {
		t.Fatalf("ExpireGracedRotations: %v", err)
	}
	if n != 1 {
		t.Errorf("expired count = %d, want 1", n)
	}

	var status, oldValue string
	_ = db.QueryRow(`SELECT status, old_value FROM credential_rotations WHERE id = 'r1'`).Scan(&status, &oldValue)
	if status != "EXPIRED" {
		t.Errorf("r1 status = %q, want EXPIRED", status)
	}
	if oldValue != "" {
		t.Errorf("r1 old_value not scrubbed: %q", oldValue)
	}

	// r2 untouched
	_ = db.QueryRow(`SELECT status, old_value FROM credential_rotations WHERE id = 'r2'`).Scan(&status, &oldValue)
	if status != "ACTIVE" || oldValue != "enc-old-2" {
		t.Errorf("r2 mutated: status=%q value=%q", status, oldValue)
	}
}

// TestCancelRotation_Idempotent verifies that cancel on an already-
// terminal rotation is a no-op 200, matching GitLab's "already revoked"
// semantics (avoids racing two cancels).
func TestCancelRotation_Idempotent(t *testing.T) {
	t.Parallel()
	setTestEncryptionKeyParallelSafe(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	credID := "cred-rot-cancel"
	seedCredentialEnc(t, db, wsID, userID, credID, "TEST_KEY", "old")
	if _, err := db.Exec(`
		INSERT INTO credential_rotations (id, credential_id, old_value, grace_seconds, expires_at, rotated_by, status)
		VALUES ('r1', ?, 'enc-old', 3600, ?, ?, 'EXPIRED')`,
		credID, time.Now().Format(time.RFC3339), userID); err != nil {
		t.Fatalf("seed: %v", err)
	}

	h := NewCredentialHandler(db, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})))
	req := rotationReq(t, "DELETE", "/", "", userID, wsID)
	req.SetPathValue("rotationId", "r1")
	rr := httptest.NewRecorder()
	h.CancelRotation(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("idempotent cancel status = %d, want 200", rr.Code)
	}
	// Status must remain EXPIRED — cancel doesn't down-shift terminals.
	var status string
	_ = db.QueryRow(`SELECT status FROM credential_rotations WHERE id = 'r1'`).Scan(&status)
	if status != "EXPIRED" {
		t.Errorf("status mutated by idempotent cancel: %q", status)
	}
}

// TestCancelRotation_Active scrubs old_value on the happy path.
func TestCancelRotation_Active(t *testing.T) {
	t.Parallel()
	setTestEncryptionKeyParallelSafe(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	credID := "cred-rot-cancel-active"
	seedCredentialEnc(t, db, wsID, userID, credID, "TEST_KEY", "old")
	if _, err := db.Exec(`
		INSERT INTO credential_rotations (id, credential_id, old_value, grace_seconds, expires_at, rotated_by, status)
		VALUES ('r1', ?, 'enc-old', 3600, ?, ?, 'ACTIVE')`,
		credID, time.Now().Add(time.Hour).Format(time.RFC3339), userID); err != nil {
		t.Fatalf("seed: %v", err)
	}

	h := NewCredentialHandler(db, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})))
	req := rotationReq(t, "DELETE", "/", "", userID, wsID)
	req.SetPathValue("rotationId", "r1")
	rr := httptest.NewRecorder()
	h.CancelRotation(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var status, oldValue string
	_ = db.QueryRow(`SELECT status, old_value FROM credential_rotations WHERE id = 'r1'`).Scan(&status, &oldValue)
	if status != "CANCELLED" {
		t.Errorf("status = %q, want CANCELLED", status)
	}
	if oldValue != "" {
		t.Errorf("old_value not scrubbed: %q", oldValue)
	}
}

// TestRotate_NotFoundCrossWorkspace ensures workspace isolation —
// rotating someone else's credential must 404, never leak existence.
func TestRotate_NotFoundCrossWorkspace(t *testing.T) {
	t.Parallel()
	setTestEncryptionKeyParallelSafe(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsA := seedTestWorkspace(t, db, userID)
	credID := "cred-rot-iso"
	seedCredentialEnc(t, db, wsA, userID, credID, "TEST_KEY", "old")

	otherWS := "test-other-ws"
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Other', 'other')`, otherWS); err != nil {
		t.Fatalf("seed other ws: %v", err)
	}

	h := NewCredentialHandler(db, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})))
	body, _ := json.Marshal(map[string]any{"value": "new"})
	req := rotationReq(t, "POST", "/", string(body), userID, otherWS)
	req.SetPathValue("credentialId", credID)
	rr := httptest.NewRecorder()
	h.Rotate(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("cross-workspace status = %d, want 404", rr.Code)
	}
}

// suppress unused import on builds that don't reach SQL fallback paths
var _ = sql.ErrNoRows
