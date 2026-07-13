package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestSecCred_Rotate_Grace0_ScrubsOldValueSync is the #1053 regression. A
// rotation stored old_value=oldEncrypted with status='ACTIVE' regardless of
// grace, so a grace_seconds=0 rotation (caller explicitly wants no overlap)
// left the old ciphertext at rest until the hourly ExpireGracedRotations — a
// retention window (DB backup / key compromise). The fix stores no old value
// and marks the rotation born-expired when grace=0.
func TestSecCred_Rotate_Grace0_ScrubsOldValueSync(t *testing.T) {
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedCredentialEnc(t, db, wsID, userID, "r0", "K", "old-secret")

	req := httptest.NewRequest("POST", "/api/v1/credentials/r0/rotate",
		bytes.NewBufferString(`{"value":"new-secret","grace_seconds":0}`))
	req.SetPathValue("credentialId", "r0")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Rotate(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("code = %d body=%s", rr.Code, rr.Body.String())
	}

	var oldVal, status string
	if err := db.QueryRow(
		`SELECT old_value, status FROM credential_rotations WHERE credential_id='r0'`).
		Scan(&oldVal, &status); err != nil {
		t.Fatalf("read rotation: %v", err)
	}
	if oldVal != "" {
		t.Errorf("old_value retained (%d bytes), want empty — grace=0 must scrub synchronously", len(oldVal))
	}
	if status == "ACTIVE" {
		t.Errorf("rotation status = ACTIVE, want a terminal status for grace=0 (no fallback window)")
	}
}

// TestSecCred_Rotate_ReactivatesRevoked pins the #1062 decision: rotating a
// REVOKED/EXPIRED (non-deleted) credential with a fresh value reactivates it to
// ACTIVE. This is the intended recovery path (an operator supplies a new token
// through the privileged rotate capability after the upstream revoked the old
// one); the test locks it so the behaviour is documented + pinned rather than
// an unnoticed lifecycle surprise.
func TestSecCred_Rotate_ReactivatesRevoked(t *testing.T) {
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedCredentialEnc(t, db, wsID, userID, "rv", "K", "old")
	execOrFatal(t, db, `UPDATE credentials SET status='REVOKED' WHERE id='rv'`)

	req := httptest.NewRequest("POST", "/api/v1/credentials/rv/rotate",
		bytes.NewBufferString(`{"value":"fresh-token","grace_seconds":0}`))
	req.SetPathValue("credentialId", "rv")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Rotate(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("code = %d body=%s", rr.Code, rr.Body.String())
	}
	var status string
	if err := db.QueryRow(`SELECT status FROM credentials WHERE id='rv'`).Scan(&status); err != nil {
		t.Fatalf("read status: %v", err)
	}
	if status != "ACTIVE" {
		t.Errorf("status = %q, want ACTIVE (rotation is the documented recovery path — #1062)", status)
	}
}

// TestSecCred_CancelRotation_RecordsAuditEvent is the #1052 regression. Ending a
// grace window early scrubs old_value (destroys the fallback secret) but wrote
// nothing to credential_audit — the timeline silently omitted a
// security-relevant destruction. The fix records a REVOKE event marked as a
// rotation cancel.
func TestSecCred_CancelRotation_RecordsAuditEvent(t *testing.T) {
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedCredentialEnc(t, db, wsID, userID, "rc", "K", "old")
	execOrFatal(t, db, `
		INSERT INTO credential_rotations (id, credential_id, old_value, grace_seconds, rotated_at, expires_at, rotated_by, status)
		VALUES ('rot1','rc','oldenc',3600, datetime('now'), datetime('now','+1 hour'), ?, 'ACTIVE')`, userID)

	req := httptest.NewRequest("POST", "/api/v1/credentials/rotations/rot1/cancel", nil)
	req.SetPathValue("rotationId", "rot1")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.CancelRotation(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("code = %d body=%s", rr.Code, rr.Body.String())
	}

	var n int
	if err := db.QueryRow(
		`SELECT COUNT(*) FROM credential_audit WHERE credential_id='rc' AND event_type='REVOKE'`).
		Scan(&n); err != nil {
		t.Fatalf("count audit: %v", err)
	}
	if n == 0 {
		t.Errorf("no audit event recorded for rotation cancel (#1052) — the fallback-secret destruction is invisible on the timeline")
	}
}
