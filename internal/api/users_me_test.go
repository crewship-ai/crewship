package api

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/auth/sessions"
	"golang.org/x/crypto/bcrypt"
)

// Tests for /api/v1/users/me (#867.1) — self-service profile edit +
// password change with sibling-session invalidation.

func newProfileRig(t *testing.T) (*UserProfileHandler, sessions.Store, string) {
	t.Helper()
	dbh := setupTestDB(t)
	userID := seedTestUserWithPassword(t, dbh, "me@example.com", "oldpassword1")
	store := sessions.NewDBStore(dbh)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	h := NewUserProfileHandler(dbh, logger, store)
	return h, store, userID
}

func selfReq(t *testing.T, method, path string, payload any, userID, sessionID string) *http.Request {
	t.Helper()
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	return req.WithContext(withUser(req.Context(), &AuthUser{ID: userID, Email: "me@example.com", SessionID: sessionID}))
}

// ── UpdateProfile ─────────────────────────────────────────────────────

func TestUpdateProfile_ChangesFullName(t *testing.T) {
	h, _, userID := newProfileRig(t)
	rr := httptest.NewRecorder()
	h.UpdateProfile(rr, selfReq(t, "PATCH", "/api/v1/users/me", map[string]string{"full_name": "New Name"}, userID, ""))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var resp userProfileResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if resp.FullName == nil || *resp.FullName != "New Name" {
		t.Fatalf("full_name = %v, want New Name", resp.FullName)
	}
	var got string
	_ = h.db.QueryRow("SELECT full_name FROM users WHERE id = ?", userID).Scan(&got)
	if got != "New Name" {
		t.Fatalf("db full_name = %q", got)
	}
}

func TestUpdateProfile_RejectsBlankName(t *testing.T) {
	h, _, userID := newProfileRig(t)
	rr := httptest.NewRecorder()
	h.UpdateProfile(rr, selfReq(t, "PATCH", "/api/v1/users/me", map[string]string{"full_name": "   "}, userID, ""))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestUpdateProfile_NoFields_BadRequest(t *testing.T) {
	h, _, userID := newProfileRig(t)
	rr := httptest.NewRecorder()
	h.UpdateProfile(rr, selfReq(t, "PATCH", "/api/v1/users/me", map[string]string{}, userID, ""))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

// ── ChangePassword ────────────────────────────────────────────────────

func TestChangePassword_WrongCurrent_Unauthorized(t *testing.T) {
	h, _, userID := newProfileRig(t)
	rr := httptest.NewRecorder()
	h.ChangePassword(rr, selfReq(t, "POST", "/api/v1/users/me/password",
		map[string]string{"current_password": "WRONG", "new_password": "brandnew123"}, userID, ""))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body=%s", rr.Code, rr.Body.String())
	}
	// Hash must be unchanged.
	var hash string
	_ = h.db.QueryRow("SELECT hashed_password FROM users WHERE id = ?", userID).Scan(&hash)
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte("oldpassword1")) != nil {
		t.Fatalf("password hash changed despite wrong current password")
	}
}

func TestChangePassword_ShortNew_BadRequest(t *testing.T) {
	h, _, userID := newProfileRig(t)
	rr := httptest.NewRecorder()
	h.ChangePassword(rr, selfReq(t, "POST", "/api/v1/users/me/password",
		map[string]string{"current_password": "oldpassword1", "new_password": "short"}, userID, ""))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestChangePassword_Success_RehashesAndKeepsCurrentSession(t *testing.T) {
	h, store, userID := newProfileRig(t)

	// Two active sessions: "current" (kept) and "other" (must be revoked).
	current, err := store.Create(t.Context(), userID, "ua-current", "1.1.1.1", time.Hour)
	if err != nil {
		t.Fatalf("create current session: %v", err)
	}
	other, err := store.Create(t.Context(), userID, "ua-other", "2.2.2.2", time.Hour)
	if err != nil {
		t.Fatalf("create other session: %v", err)
	}

	rr := httptest.NewRecorder()
	h.ChangePassword(rr, selfReq(t, "POST", "/api/v1/users/me/password",
		map[string]string{"current_password": "oldpassword1", "new_password": "brandnew123"}, userID, current.ID))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}

	// New password verifies; old does not.
	var hash string
	_ = h.db.QueryRow("SELECT hashed_password FROM users WHERE id = ?", userID).Scan(&hash)
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte("brandnew123")) != nil {
		t.Fatalf("new password does not verify against stored hash")
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte("oldpassword1")) == nil {
		t.Fatalf("old password still verifies")
	}

	// Current session preserved, other revoked.
	cur, err := store.Get(t.Context(), current.ID)
	if err != nil {
		t.Fatalf("get current: %v", err)
	}
	if cur.RevokedAt != nil {
		t.Fatalf("current session was revoked")
	}
	oth, err := store.Get(t.Context(), other.ID)
	if err != nil {
		t.Fatalf("get other: %v", err)
	}
	if oth.RevokedAt == nil {
		t.Fatalf("other session was NOT revoked")
	}
	if oth.RevokedReason != sessions.ReasonPasswordChange {
		t.Fatalf("revoke reason = %q, want %q", oth.RevokedReason, sessions.ReasonPasswordChange)
	}

	// Response reports one revoked sibling.
	var resp struct {
		SessionsRevoked int `json:"sessions_revoked"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.SessionsRevoked != 1 {
		t.Fatalf("sessions_revoked = %d, want 1", resp.SessionsRevoked)
	}
}
