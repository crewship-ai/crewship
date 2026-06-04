package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Status is unauthenticated: it reports whether the install still needs
// bootstrapping (zero users) and mirrors the signup flag so the login
// page can render the right call-to-action.

func TestSetupStatus_NeedsBootstrapWhenNoUsers(t *testing.T) {
	h := NewSetupStatusHandler(setupTestDB(t), newTestLogger(), false)
	req := httptest.NewRequest("GET", "/api/v1/system/setup-status", nil)
	rr := httptest.NewRecorder()
	h.Status(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var resp setupStatusResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.NeedsBootstrap {
		t.Errorf("needs_bootstrap=false want true on empty users table")
	}
	if resp.AllowSignup {
		t.Errorf("allow_signup=true want false (flag was off)")
	}
}

func TestSetupStatus_NoBootstrapWithUsersAndSignupFlag(t *testing.T) {
	db := setupTestDB(t)
	seedTestUser(t, db)

	h := NewSetupStatusHandler(db, newTestLogger(), true)
	req := httptest.NewRequest("GET", "/api/v1/system/setup-status", nil)
	rr := httptest.NewRecorder()
	h.Status(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
	}
	var resp setupStatusResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.NeedsBootstrap {
		t.Errorf("needs_bootstrap=true want false when a user exists")
	}
	if !resp.AllowSignup {
		t.Errorf("allow_signup=false want true (flag was on)")
	}
}
