package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/crewship-ai/crewship/internal/auth"
	"github.com/crewship-ai/crewship/internal/auth/sessions"
)

// /api/auth/session is the one place the whole UI asks "who am I" — the top
// bar's name and avatar come from nothing else. It used to answer purely from
// the access token's claims, which are a snapshot taken when the token was
// minted, so a profile edit stayed invisible until the token happened to
// rotate. That is what made uploading an avatar look like it had done nothing.
//
// These tests pin the handler to the live users row.

func sessionForUser(t *testing.T, h *NextAuthHandler, v *auth.JWTValidator, store sessions.Store, userID, claimName, claimEmail string) map[string]interface{} {
	t.Helper()
	sess, err := store.Create(t.Context(), userID, "test", "127.0.0.1", auth.RefreshTokenTTL)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	tok, err := v.IssueAccessToken(userID, sess.ID, claimName, claimEmail)
	if err != nil {
		t.Fatalf("issue access: %v", err)
	}

	req := httptest.NewRequest("GET", "/api/auth/session", nil)
	req.AddCookie(&http.Cookie{Name: "authjs.session-token", Value: tok})
	rr := httptest.NewRecorder()
	h.Session(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}

	var body map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	user, _ := body["user"].(map[string]interface{})
	if user == nil {
		t.Fatalf("no user in session payload: %s", rr.Body.String())
	}
	return user
}

func newIdentityHandler(t *testing.T) (*NextAuthHandler, *auth.JWTValidator, sessions.Store, string) {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	v, err := auth.NewJWTValidator("test-secret-for-jwt-signing-32chars!!")
	if err != nil {
		t.Fatalf("validator: %v", err)
	}
	db := setupTestDB(t)
	store := sessions.NewDBStore(db)
	h := NewNextAuthHandler(db, logger, v, store)
	userID := seedTestUser(t, db)
	if _, err := db.Exec(`UPDATE users SET avatar_url = ? WHERE id = ?`,
		"/api/v1/users/test-user-id/avatar?v=1700000000", userID); err != nil {
		t.Fatalf("set avatar: %v", err)
	}
	return h, v, store, userID
}

func TestSession_CarriesAvatarURL(t *testing.T) {
	t.Parallel()
	h, v, store, userID := newIdentityHandler(t)

	user := sessionForUser(t, h, v, store, userID, "Test User", "test@example.com")
	if got := user["avatar_url"]; got != "/api/v1/users/test-user-id/avatar?v=1700000000" {
		t.Errorf("avatar_url = %v, want the stored URL", got)
	}
}

func TestSession_PrefersTheLiveRowOverStaleClaims(t *testing.T) {
	t.Parallel()
	h, v, store, userID := newIdentityHandler(t)

	// Claims deliberately carry the pre-edit identity, exactly as a token
	// minted before the user renamed themselves would.
	user := sessionForUser(t, h, v, store, userID, "Stale Name", "stale@example.com")

	if got := user["name"]; got != "Test User" {
		t.Errorf("name = %v, want the live row's value (not the claim)", got)
	}
	if got := user["email"]; got != "test@example.com" {
		t.Errorf("email = %v, want the live row's value (not the claim)", got)
	}
}

func TestSession_AvatarlessUserGetsEmptyNotNull(t *testing.T) {
	t.Parallel()
	h, v, store, userID := newIdentityHandler(t)
	// Most users never upload one; avatar_url is nullable in the schema.
	if _, err := h.db.Exec(`UPDATE users SET avatar_url = NULL WHERE id = ?`, userID); err != nil {
		t.Fatalf("clear avatar: %v", err)
	}

	user := sessionForUser(t, h, v, store, userID, "Test User", "test@example.com")
	if got, ok := user["avatar_url"]; !ok || got != "" {
		t.Errorf("avatar_url = %v (present=%v), want empty string", got, ok)
	}
}

func TestSession_NullFullNameDoesNotBreakTheSession(t *testing.T) {
	t.Parallel()
	h, v, store, userID := newIdentityHandler(t)
	// full_name is nullable in the schema. Scanning it into a bare string
	// would error, and an error here must never cost someone their session.
	if _, err := h.db.Exec(`UPDATE users SET full_name = NULL WHERE id = ?`, userID); err != nil {
		t.Fatalf("clear name: %v", err)
	}

	user := sessionForUser(t, h, v, store, userID, "Claim Name", "test@example.com")
	if user["email"] != "test@example.com" {
		t.Errorf("session lost its user on a NULL full_name: %+v", user)
	}
}
