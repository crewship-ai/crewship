package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// credential_rotation.go — coverage gaps after existing rotation tests:
//
//   - ListRotations: history endpoint (404 cross-workspace, sort order,
//     old_value_gone flag derivation per status).
//   - Rotate: error paths not exercised by happy-path tests (no auth,
//     non-manage role, invalid JSON, empty value, grace bounds).
//   - StartCredentialRotationExpiryWorker: startup pass scrubs expired
//     rotations, stop channel cleanly halts the goroutine.
// ---------------------------------------------------------------------------

func seedRotationRow(t *testing.T, h *CredentialHandler, id, credID, oldVal, status, userID string, rotatedAt, expiresAt time.Time) {
	t.Helper()
	_, err := h.db.Exec(`
		INSERT INTO credential_rotations
		    (id, credential_id, old_value, grace_seconds, rotated_at, expires_at, rotated_by, status)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		id, credID, oldVal, 3600,
		rotatedAt.UTC().Format(time.RFC3339),
		expiresAt.UTC().Format(time.RFC3339),
		userID, status)
	if err != nil {
		t.Fatalf("seed rotation %s: %v", id, err)
	}
}

func TestListRotations_404CrossWorkspace(t *testing.T) {
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsA := seedTestWorkspace(t, db, userID)

	// Credential lives in a different workspace; the caller is in wsA.
	wsB := "ws-b"
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'B', 'b')`, wsB); err != nil {
		t.Fatalf("seed wsB: %v", err)
	}
	seedCredentialEnc(t, db, wsB, userID, "cred-b", "OTHER", "v")

	req := httptest.NewRequest("GET", "/api/v1/credentials/cred-b/rotations", nil)
	req.SetPathValue("credentialId", "cred-b")
	req = withWorkspaceUser(req, userID, wsA, "OWNER")
	rr := httptest.NewRecorder()
	h.ListRotations(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("cross-workspace list code = %d, want 404", rr.Code)
	}
}

func TestListRotations_404Unknown(t *testing.T) {
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	req := httptest.NewRequest("GET", "/api/v1/credentials/does-not-exist/rotations", nil)
	req.SetPathValue("credentialId", "does-not-exist")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.ListRotations(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("unknown cred list code = %d, want 404", rr.Code)
	}
}

func TestListRotations_404SoftDeletedCredential(t *testing.T) {
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedCredentialEnc(t, db, wsID, userID, "cred-gone", "K", "v")
	if _, err := db.Exec(`UPDATE credentials SET deleted_at = datetime('now') WHERE id = 'cred-gone'`); err != nil {
		t.Fatalf("soft delete: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/v1/credentials/cred-gone/rotations", nil)
	req.SetPathValue("credentialId", "cred-gone")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.ListRotations(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("soft-deleted cred list code = %d, want 404", rr.Code)
	}
}

func TestListRotations_SortAndOldValueGoneFlag(t *testing.T) {
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedCredentialEnc(t, db, wsID, userID, "cred-h", "K", "v")

	now := time.Now().UTC()
	// Seed three rotations: one ACTIVE (oldest), one EXPIRED, one CANCELLED.
	// Sort order is rotated_at DESC, so the newest (CANCELLED) must come first.
	seedRotationRow(t, h, "r-active", "cred-h", "still-here", "ACTIVE", userID, now.Add(-3*time.Hour), now.Add(1*time.Hour))
	seedRotationRow(t, h, "r-expired", "cred-h", "", "EXPIRED", userID, now.Add(-2*time.Hour), now.Add(-1*time.Hour))
	seedRotationRow(t, h, "r-cancelled", "cred-h", "", "CANCELLED", userID, now.Add(-1*time.Hour), now.Add(1*time.Hour))

	req := httptest.NewRequest("GET", "/api/v1/credentials/cred-h/rotations", nil)
	req.SetPathValue("credentialId", "cred-h")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.ListRotations(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("list status = %d body=%s", rr.Code, rr.Body.String())
	}
	var got []rotationResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d rotations, want 3", len(got))
	}
	// Newest first
	wantOrder := []string{"r-cancelled", "r-expired", "r-active"}
	for i, id := range wantOrder {
		if got[i].ID != id {
			t.Errorf("row[%d] = %s, want %s (DESC sort)", i, got[i].ID, id)
		}
	}
	// OldValueGone derivation: true for EXPIRED/CANCELLED, false for ACTIVE
	byID := map[string]rotationResponse{}
	for _, r := range got {
		byID[r.ID] = r
	}
	if byID["r-active"].OldValueGone {
		t.Error("ACTIVE rotation: OldValueGone should be false")
	}
	if !byID["r-expired"].OldValueGone {
		t.Error("EXPIRED rotation: OldValueGone should be true")
	}
	if !byID["r-cancelled"].OldValueGone {
		t.Error("CANCELLED rotation: OldValueGone should be true")
	}
}

func TestListRotations_EmptyHistory_ReturnsEmptyArray(t *testing.T) {
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedCredentialEnc(t, db, wsID, userID, "cred-fresh", "K", "v")

	req := httptest.NewRequest("GET", "/api/v1/credentials/cred-fresh/rotations", nil)
	req.SetPathValue("credentialId", "cred-fresh")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.ListRotations(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	// Must be `[]`, never `null` — the UI assumes an iterable.
	body := rr.Body.String()
	if body != "[]\n" {
		t.Errorf("empty rotations body = %q, want \"[]\\n\"", body)
	}
}

func TestRotate_Forbidden_VIEWER(t *testing.T) {
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedCredentialEnc(t, db, wsID, userID, "cred-v", "K", "v")

	body := bytes.NewBufferString(`{"value":"new"}`)
	req := httptest.NewRequest("POST", "/api/v1/credentials/cred-v/rotate", body)
	req.SetPathValue("credentialId", "cred-v")
	req = withWorkspaceUser(req, userID, wsID, "VIEWER")
	rr := httptest.NewRecorder()
	h.Rotate(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("VIEWER rotate code = %d, want 403", rr.Code)
	}
}

func TestRotate_InvalidJSON(t *testing.T) {
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedCredentialEnc(t, db, wsID, userID, "cred-j", "K", "v")

	req := httptest.NewRequest("POST", "/api/v1/credentials/cred-j/rotate", bytes.NewBufferString(`not-json`))
	req.SetPathValue("credentialId", "cred-j")
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.Rotate(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("invalid json code = %d, want 400", rr.Code)
	}
}

func TestRotate_EmptyValue(t *testing.T) {
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedCredentialEnc(t, db, wsID, userID, "cred-e", "K", "v")

	for _, body := range []string{`{"value":""}`, `{"value":"   "}`} {
		req := httptest.NewRequest("POST", "/api/v1/credentials/cred-e/rotate", bytes.NewBufferString(body))
		req.SetPathValue("credentialId", "cred-e")
		req = withWorkspaceUser(req, userID, wsID, "OWNER")
		rr := httptest.NewRecorder()
		h.Rotate(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("empty value (%q) code = %d, want 400", body, rr.Code)
		}
	}
}

func TestRotate_GraceBounds(t *testing.T) {
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedCredentialEnc(t, db, wsID, userID, "cred-g", "K", "v")

	cases := []struct {
		name string
		body string
		want int
	}{
		{"negative", `{"value":"new","grace_seconds":-1}`, http.StatusBadRequest},
		{"too-large", `{"value":"new","grace_seconds":` + strconv.Itoa(maxGraceSeconds+1) + `}`, http.StatusBadRequest},
		{"zero-allowed", `{"value":"new","grace_seconds":0}`, http.StatusOK},
		{"max-allowed", `{"value":"new","grace_seconds":` + strconv.Itoa(maxGraceSeconds) + `}`, http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// re-seed for the OK cases since they mutate state; cheap.
			if tc.want == http.StatusOK {
				if _, err := db.Exec(`DELETE FROM credential_rotations WHERE credential_id = 'cred-g'`); err != nil {
					t.Fatalf("cleanup rotations: %v", err)
				}
			}
			req := httptest.NewRequest("POST", "/api/v1/credentials/cred-g/rotate", bytes.NewBufferString(tc.body))
			req.SetPathValue("credentialId", "cred-g")
			req = withWorkspaceUser(req, userID, wsID, "OWNER")
			rr := httptest.NewRecorder()
			h.Rotate(rr, req)
			if rr.Code != tc.want {
				t.Errorf("%s: code = %d, want %d (body=%s)", tc.name, rr.Code, tc.want, rr.Body.String())
			}
		})
	}
}

func TestStartCredentialRotationExpiryWorker_InitialPassAndShutdown(t *testing.T) {
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedCredentialEnc(t, db, wsID, userID, "cred-w", "K", "v")

	now := time.Now().UTC()
	// One ACTIVE rotation whose grace has already expired — the worker's
	// initial pass must scrub it before the first tick.
	seedRotationRow(t, h, "r-stale", "cred-w", "stale", "ACTIVE", userID, now.Add(-2*time.Hour), now.Add(-1*time.Hour))
	// One ACTIVE rotation still within its grace — must remain untouched.
	seedRotationRow(t, h, "r-fresh", "cred-w", "fresh", "ACTIVE", userID, now.Add(-10*time.Minute), now.Add(1*time.Hour))

	stop := make(chan struct{})
	var wg sync.WaitGroup
	StartCredentialRotationExpiryWorker(db, h.logger, stop, &wg)

	// The worker's initial pass runs synchronously inside the goroutine
	// before the ticker is armed. Poll briefly until the scrub lands.
	deadline := time.Now().Add(2 * time.Second)
	var staleStatus, staleOld string
	for time.Now().Before(deadline) {
		_ = db.QueryRow(`SELECT status, old_value FROM credential_rotations WHERE id = 'r-stale'`).Scan(&staleStatus, &staleOld)
		if staleStatus == "EXPIRED" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if staleStatus != "EXPIRED" {
		t.Errorf("stale rotation status = %q, want EXPIRED (initial pass)", staleStatus)
	}
	if staleOld != "" {
		t.Errorf("stale rotation old_value = %q, want empty (scrubbed)", staleOld)
	}

	var freshStatus, freshOld string
	_ = db.QueryRow(`SELECT status, old_value FROM credential_rotations WHERE id = 'r-fresh'`).Scan(&freshStatus, &freshOld)
	if freshStatus != "ACTIVE" {
		t.Errorf("fresh rotation status = %q, want ACTIVE (not yet expired)", freshStatus)
	}
	if freshOld != "fresh" {
		t.Errorf("fresh rotation old_value = %q, want 'fresh' (not scrubbed)", freshOld)
	}

	// Stop channel must cleanly terminate the goroutine.
	close(stop)
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
		// graceful
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not stop within 2s of close(stop)")
	}
}

func TestExpireGracedRotations_NoActiveRows_NoOp(t *testing.T) {
	h, db := newCredHandler(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	seedCredentialEnc(t, db, wsID, userID, "cred-n", "K", "v")
	now := time.Now().UTC()
	seedRotationRow(t, h, "r-already-expired", "cred-n", "", "EXPIRED", userID, now.Add(-1*time.Hour), now.Add(-30*time.Minute))

	n, err := ExpireGracedRotations(context.Background(), db, h.logger)
	if err != nil {
		t.Fatalf("expire: %v", err)
	}
	if n != 0 {
		t.Errorf("expired count = %d, want 0 (already EXPIRED rows untouched)", n)
	}
}
