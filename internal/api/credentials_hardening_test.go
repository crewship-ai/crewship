package api

// Credentials hardening regression tests (P1 audit follow-up):
//
//   B2 — the public rotate route honours CapabilityCredentialRotate:
//        a MANAGER/MEMBER holding an explicit `credential.rotate` grant
//        can rotate (and cancel a rotation) without ADMIN role; a
//        caller with neither role nor capability still 403s.
//   B3 — credential values (and refresh tokens) are capped at 64 KiB
//        in create / update / rotate; both sides of the boundary are
//        exercised. The stricter 2048-byte ENDPOINT_URL cap keeps
//        applying on top.
//   B4 — audit rows ride the SAME transaction as the mutation they
//        describe (create / rotate), so a failed audit insert rolls
//        the mutation back instead of silently dropping the compliance
//        event. Paths without a surrounding tx stay best-effort but
//        bump a drop counter exposed for /metrics.

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// quietLogger drops everything below ERROR so the deliberate failure
// paths in these tests don't spam the test output.
func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

// --- B2: capability gate on rotate / cancel-rotation -----------------

func TestRotateCapabilityGate(t *testing.T) {
	t.Parallel()
	setTestEncryptionKeyParallelSafe(t)

	cases := []struct {
		name       string
		role       string
		caps       string
		wantStatus int
	}{
		{"ADMIN without capability grants (legacy role path)", "ADMIN", `["chat"]`, http.StatusOK},
		{"MANAGER with credential.rotate grants", "MANAGER", `["chat","credential.rotate"]`, http.StatusOK},
		{"MANAGER without capability denies", "MANAGER", `["chat"]`, http.StatusForbidden},
		{"MEMBER with credential.rotate grants", "MEMBER", `["chat","credential.rotate"]`, http.StatusOK},
		{"MEMBER without capability denies", "MEMBER", `["chat"]`, http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := setupTestDB(t)
			ownerID := seedTestUser(t, db)
			wsID := seedTestWorkspace(t, db, ownerID)
			callerID := "rotcap-" + strings.ReplaceAll(tc.name, " ", "-")
			if _, err := db.Exec(`INSERT INTO users (id, email, full_name) VALUES (?, ?, 'T')`,
				callerID, callerID+"@x"); err != nil {
				t.Fatalf("seed user: %v", err)
			}
			if _, err := db.Exec(`INSERT INTO workspace_members (id, workspace_id, user_id, role, capabilities) VALUES (?, ?, ?, ?, ?)`,
				"m-"+callerID, wsID, callerID, tc.role, tc.caps); err != nil {
				t.Fatalf("seed member: %v", err)
			}
			InvalidateCapabilityCache(wsID, callerID)

			credID := "cred-" + callerID
			seedCredentialEnc(t, db, wsID, ownerID, credID, "ROT_CAP_KEY", "old-value")

			h := NewCredentialHandler(db, quietLogger())
			body, _ := json.Marshal(map[string]any{"value": "new-value"})
			req := httptest.NewRequest("POST", "/api/v1/credentials/"+credID+"/rotate", bytes.NewReader(body))
			ctx := withUser(req.Context(), &AuthUser{ID: callerID})
			ctx = withWorkspace(ctx, wsID, tc.role)
			req = req.WithContext(ctx)
			req.SetPathValue("credentialId", credID)

			rr := httptest.NewRecorder()
			h.Rotate(rr, req)
			if rr.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d — body=%s", rr.Code, tc.wantStatus, rr.Body.String())
			}
		})
	}
}

func TestCancelRotationCapabilityGate(t *testing.T) {
	t.Parallel()
	setTestEncryptionKeyParallelSafe(t)

	cases := []struct {
		name       string
		caps       string
		wantStatus int
	}{
		{"MEMBER with credential.rotate cancels", `["chat","credential.rotate"]`, http.StatusOK},
		{"MEMBER without capability denies", `["chat"]`, http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := setupTestDB(t)
			ownerID := seedTestUser(t, db)
			wsID := seedTestWorkspace(t, db, ownerID)
			credID := "cred-cancel-" + strings.ReplaceAll(tc.name, " ", "-")
			seedCredentialEnc(t, db, wsID, ownerID, credID, "CANCEL_CAP_KEY", "old-value")

			h := NewCredentialHandler(db, quietLogger())

			// Owner performs the rotation that will be cancelled.
			body, _ := json.Marshal(map[string]any{"value": "new-value"})
			rotReq := httptest.NewRequest("POST", "/x", bytes.NewReader(body))
			rotCtx := withUser(rotReq.Context(), &AuthUser{ID: ownerID})
			rotCtx = withWorkspace(rotCtx, wsID, "OWNER")
			rotReq = rotReq.WithContext(rotCtx)
			rotReq.SetPathValue("credentialId", credID)
			rotRR := httptest.NewRecorder()
			h.Rotate(rotRR, rotReq)
			if rotRR.Code != http.StatusOK {
				t.Fatalf("seed rotation: status=%d body=%s", rotRR.Code, rotRR.Body.String())
			}
			var rot rotationResponse
			if err := json.Unmarshal(rotRR.Body.Bytes(), &rot); err != nil {
				t.Fatalf("unmarshal rotation: %v", err)
			}

			callerID := "cancap-" + strings.ReplaceAll(tc.name, " ", "-")
			if _, err := db.Exec(`INSERT INTO users (id, email, full_name) VALUES (?, ?, 'T')`,
				callerID, callerID+"@x"); err != nil {
				t.Fatalf("seed user: %v", err)
			}
			if _, err := db.Exec(`INSERT INTO workspace_members (id, workspace_id, user_id, role, capabilities) VALUES (?, ?, ?, ?, ?)`,
				"m-"+callerID, wsID, callerID, "MEMBER", tc.caps); err != nil {
				t.Fatalf("seed member: %v", err)
			}
			InvalidateCapabilityCache(wsID, callerID)

			req := httptest.NewRequest("DELETE", "/api/v1/credential-rotations/"+rot.ID, nil)
			ctx := withUser(req.Context(), &AuthUser{ID: callerID})
			ctx = withWorkspace(ctx, wsID, "MEMBER")
			req = req.WithContext(ctx)
			req.SetPathValue("rotationId", rot.ID)
			rr := httptest.NewRecorder()
			h.CancelRotation(rr, req)
			if rr.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d — body=%s", rr.Code, tc.wantStatus, rr.Body.String())
			}
		})
	}
}

// --- B3: 64 KiB per-value cap ----------------------------------------

func TestCredentialValueSizeCap_Create(t *testing.T) {
	t.Parallel()
	setTestEncryptionKeyParallelSafe(t)

	atCap := strings.Repeat("a", maxCredentialValueLen)
	overCap := strings.Repeat("a", maxCredentialValueLen+1)

	cases := []struct {
		name       string
		body       map[string]any
		wantStatus int
	}{
		{"value exactly at cap accepted", map[string]any{
			"name": "cap-ok", "type": "SECRET", "value": atCap}, http.StatusCreated},
		{"value one byte over cap rejected", map[string]any{
			"name": "cap-over", "type": "SECRET", "value": overCap}, http.StatusBadRequest},
		{"refresh_token over cap rejected", map[string]any{
			"name": "cap-refresh", "type": "SECRET", "value": "small", "refresh_token": overCap}, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := setupTestDB(t)
			userID := seedTestUser(t, db)
			wsID := seedTestWorkspace(t, db, userID)
			h := NewCredentialHandler(db, quietLogger())

			raw, _ := json.Marshal(tc.body)
			req := httptest.NewRequest("POST", "/api/v1/credentials", bytes.NewReader(raw))
			ctx := withUser(req.Context(), &AuthUser{ID: userID})
			ctx = withWorkspace(ctx, wsID, "OWNER")
			req = req.WithContext(ctx)
			rr := httptest.NewRecorder()
			h.Create(rr, req)
			if rr.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d — body=%s", rr.Code, tc.wantStatus, truncateForLog(rr.Body.String()))
			}
		})
	}
}

func TestCredentialValueSizeCap_Update(t *testing.T) {
	t.Parallel()
	setTestEncryptionKeyParallelSafe(t)

	cases := []struct {
		name       string
		value      string
		wantStatus int
	}{
		{"value at cap accepted", strings.Repeat("b", maxCredentialValueLen), http.StatusOK},
		{"value over cap rejected", strings.Repeat("b", maxCredentialValueLen+1), http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := setupTestDB(t)
			userID := seedTestUser(t, db)
			wsID := seedTestWorkspace(t, db, userID)
			credID := "cred-upd-cap-" + strings.ReplaceAll(tc.name, " ", "-")
			seedCredentialEnc(t, db, wsID, userID, credID, "UPD_CAP_KEY", "old")
			h := NewCredentialHandler(db, quietLogger())

			raw, _ := json.Marshal(map[string]any{"value": tc.value})
			req := httptest.NewRequest("PATCH", "/api/v1/credentials/"+credID, bytes.NewReader(raw))
			ctx := withUser(req.Context(), &AuthUser{ID: userID})
			ctx = withWorkspace(ctx, wsID, "OWNER")
			req = req.WithContext(ctx)
			req.SetPathValue("credentialId", credID)
			rr := httptest.NewRecorder()
			h.Update(rr, req)
			if rr.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d — body=%s", rr.Code, tc.wantStatus, truncateForLog(rr.Body.String()))
			}
		})
	}
}

func TestCredentialValueSizeCap_Rotate(t *testing.T) {
	t.Parallel()
	setTestEncryptionKeyParallelSafe(t)

	cases := []struct {
		name       string
		value      string
		wantStatus int
	}{
		{"value at cap accepted", strings.Repeat("c", maxCredentialValueLen), http.StatusOK},
		{"value over cap rejected", strings.Repeat("c", maxCredentialValueLen+1), http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := setupTestDB(t)
			userID := seedTestUser(t, db)
			wsID := seedTestWorkspace(t, db, userID)
			credID := "cred-rot-cap-" + strings.ReplaceAll(tc.name, " ", "-")
			seedCredentialEnc(t, db, wsID, userID, credID, "ROT_CAP_SZ_KEY", "old")
			h := NewCredentialHandler(db, quietLogger())

			raw, _ := json.Marshal(map[string]any{"value": tc.value})
			req := httptest.NewRequest("POST", "/x", bytes.NewReader(raw))
			ctx := withUser(req.Context(), &AuthUser{ID: userID})
			ctx = withWorkspace(ctx, wsID, "OWNER")
			req = req.WithContext(ctx)
			req.SetPathValue("credentialId", credID)
			rr := httptest.NewRecorder()
			h.Rotate(rr, req)
			if rr.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d — body=%s", rr.Code, tc.wantStatus, truncateForLog(rr.Body.String()))
			}
		})
	}
}

// TestEndpointURLCapStillApplies pins the pre-existing, stricter
// 2048-byte ENDPOINT_URL cap: the generic 64 KiB gate must not have
// widened it.
func TestEndpointURLCapStillApplies(t *testing.T) {
	t.Parallel()
	long := "http://example.com/" + strings.Repeat("p", maxEndpointValueLen)
	req := &createCredentialRequest{Name: "e", Type: CredTypeEndpointURL, Value: long}
	if msg := validateCredentialPayload(req); msg == "" {
		t.Fatal("oversized ENDPOINT_URL value passed validation")
	}
}

func truncateForLog(s string) string {
	if len(s) > 200 {
		return s[:200] + "…"
	}
	return s
}

// --- B4: audit reliability -------------------------------------------

// TestRecordCredentialEventTx_RollbackDiscards proves the tx-scoped
// writer really rides the caller's transaction: rollback discards the
// audit row, commit persists it.
func TestRecordCredentialEventTx_RollbackDiscards(t *testing.T) {
	t.Parallel()
	setTestEncryptionKeyParallelSafe(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	credID := "cred-audit-tx"
	seedCredentialEnc(t, db, wsID, userID, credID, "AUD_TX_KEY", "v")

	count := func() int {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM credential_audit WHERE credential_id = ?`, credID).Scan(&n); err != nil {
			t.Fatalf("count audit rows: %v", err)
		}
		return n
	}

	tx, err := db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := RecordCredentialEventTx(context.Background(), tx, credID, AuditEventTest, "", "", nil); err != nil {
		t.Fatalf("RecordCredentialEventTx: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if got := count(); got != 0 {
		t.Fatalf("audit rows after rollback = %d, want 0", got)
	}

	tx, err = db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := RecordCredentialEventTx(context.Background(), tx, credID, AuditEventUse, "", "10.0.0.9", nil); err != nil {
		t.Fatalf("RecordCredentialEventTx: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if got := count(); got != 1 {
		t.Fatalf("audit rows after commit = %d, want 1", got)
	}
	// USE snapshot rode the same tx.
	var lastUsed *string
	if err := db.QueryRow(`SELECT last_used_at FROM credentials WHERE id = ?`, credID).Scan(&lastUsed); err != nil {
		t.Fatalf("read last_used_at: %v", err)
	}
	if lastUsed == nil || *lastUsed == "" {
		t.Fatal("last_used_at not stamped by in-tx USE event")
	}
}

// TestCreateAuditRidesCreateTx: with the audit table gone, the CREATED
// audit insert fails — and because it now rides the create transaction,
// the credential row must be rolled back (no silently unaudited
// credential materializes).
func TestCreateAuditRidesCreateTx(t *testing.T) {
	t.Parallel()
	setTestEncryptionKeyParallelSafe(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	if _, err := db.Exec(`DROP TABLE credential_audit`); err != nil {
		t.Fatalf("drop audit table: %v", err)
	}
	h := NewCredentialHandler(db, quietLogger())

	raw, _ := json.Marshal(map[string]any{"name": "atomic-create", "type": "SECRET", "value": "s3cret-value"})
	req := httptest.NewRequest("POST", "/api/v1/credentials", bytes.NewReader(raw))
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, "OWNER")
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 when the audit insert fails", rr.Code)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM credentials WHERE workspace_id = ? AND name = 'atomic-create'`, wsID).Scan(&n); err != nil {
		t.Fatalf("count credentials: %v", err)
	}
	if n != 0 {
		t.Fatalf("credential row persisted (%d) despite failed audit insert — audit is not in the create tx", n)
	}
}

// TestRotateAuditRidesRotateTx: same shape for rotation — a failed
// ROTATE audit insert must roll back both the credential_rotations row
// and the new encrypted value.
func TestRotateAuditRidesRotateTx(t *testing.T) {
	t.Parallel()
	setTestEncryptionKeyParallelSafe(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	credID := "cred-atomic-rot"
	seedCredentialEnc(t, db, wsID, userID, credID, "ATOMIC_ROT_KEY", "old-value")

	var encBefore string
	if err := db.QueryRow(`SELECT encrypted_value FROM credentials WHERE id = ?`, credID).Scan(&encBefore); err != nil {
		t.Fatalf("read encrypted_value: %v", err)
	}
	if _, err := db.Exec(`DROP TABLE credential_audit`); err != nil {
		t.Fatalf("drop audit table: %v", err)
	}
	h := NewCredentialHandler(db, quietLogger())

	raw, _ := json.Marshal(map[string]any{"value": "new-value"})
	req := httptest.NewRequest("POST", "/x", bytes.NewReader(raw))
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, "OWNER")
	req = req.WithContext(ctx)
	req.SetPathValue("credentialId", credID)
	rr := httptest.NewRecorder()
	h.Rotate(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 when the audit insert fails", rr.Code)
	}

	var encAfter string
	if err := db.QueryRow(`SELECT encrypted_value FROM credentials WHERE id = ?`, credID).Scan(&encAfter); err != nil {
		t.Fatalf("read encrypted_value: %v", err)
	}
	if encAfter != encBefore {
		t.Fatal("encrypted_value changed despite failed audit insert — rotation is not atomic with its audit row")
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM credential_rotations WHERE credential_id = ?`, credID).Scan(&n); err != nil {
		t.Fatalf("count rotations: %v", err)
	}
	if n != 0 {
		t.Fatalf("rotation row persisted (%d) despite failed audit insert", n)
	}
}

// TestBestEffortAuditBumpsDropCounter: paths without a surrounding tx
// (here: revoke) stay best-effort — the mutation succeeds even when the
// audit write fails — but the failure is counted for /metrics instead
// of vanishing into a Warn line.
func TestBestEffortAuditBumpsDropCounter(t *testing.T) {
	t.Parallel()
	setTestEncryptionKeyParallelSafe(t)
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	credID := "cred-drop-count"
	seedCredentialEnc(t, db, wsID, userID, credID, "DROP_CNT_KEY", "v")
	if _, err := db.Exec(`DROP TABLE credential_audit`); err != nil {
		t.Fatalf("drop audit table: %v", err)
	}
	h := NewCredentialHandler(db, quietLogger())

	before := CredentialAuditDroppedTotal()
	req := httptest.NewRequest("DELETE", "/api/v1/credentials/"+credID, nil)
	ctx := withUser(req.Context(), &AuthUser{ID: userID})
	ctx = withWorkspace(ctx, wsID, "OWNER")
	req = req.WithContext(ctx)
	req.SetPathValue("credentialId", credID)
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("revoke status = %d, want 200 (audit failure must stay best-effort here) — body=%s", rr.Code, rr.Body.String())
	}
	if got := CredentialAuditDroppedTotal(); got <= before {
		t.Fatalf("dropped counter = %d, want > %d after a failed best-effort audit write", got, before)
	}
}
