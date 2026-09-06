package api

import (
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGoogleLogin_UnconfiguredRefreshIsNotADeadGrant(t *testing.T) {
	t.Setenv("CREWSHIP_GEMINI_OAUTH_CLIENT_ID", "")
	t.Setenv("CREWSHIP_GEMINI_OAUTH_CLIENT_SECRET", "")
	setTestEncryptionKeyParallelSafe(t)
	db := setupTestDB(t)
	user := seedTestUser(t, db)
	ws := seedTestWorkspace(t, db, user)
	h := NewCredentialHandler(db, quietLogger())
	status, body := plCreate(t, h, user, ws, map[string]any{
		"name": "Google config fixture", "type": "PROVIDER_LOGIN", "provider": "GOOGLE",
		"value": `{"access_token":"fake-access","refresh_token":"fake-refresh","expiry_date":1800000000000}`,
	})
	if status != 201 {
		t.Fatalf("create: %d %v", status, body)
	}
	id := body["id"].(string)
	req := plRequest(t, "GET", "/", "", user, ws, "OWNER")
	login, _, err := loadLoginView(req.Context(), db, quietLogger(), id)
	if err != nil || login == nil || login.Refresh.Supported || login.Refresh.Error == nil || !strings.Contains(*login.Refresh.Error, "not configured") {
		t.Fatalf("configuration status: %+v %v", login, err)
	}
	r := NewProviderLoginRefresher(db, quietLogger(), nil)
	if _, ok := r.refresherFor("GOOGLE"); ok {
		t.Fatal("unconfigured Google refresher registered")
	}
	if _, err := r.Refresh(req.Context(), id, true); !errors.Is(err, errRefreshConfiguration) {
		t.Fatalf("refresh: %v", err)
	}
	var failures int
	if err := db.QueryRow(`SELECT failures FROM provider_login_refresh WHERE credential_id=?`, id).Scan(&failures); err != nil || failures != 0 {
		t.Fatalf("grant penalized: %d %v", failures, err)
	}
	h.SetLoginRefresher(r)
	req.SetPathValue("credentialId", id)
	rr := httptest.NewRecorder()
	h.Refresh(rr, req)
	if rr.Code != 503 || !strings.Contains(rr.Body.String(), "not configured") {
		t.Fatalf("HTTP status: %d %s", rr.Code, rr.Body.String())
	}
}
