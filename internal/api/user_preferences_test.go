package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// userPrefRig wires the handler against a freshly-migrated test DB,
// seeds one authenticated user, and returns the bits each test needs.
func userPrefRig(t *testing.T) (*UserPreferencesHandler, string) {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	return NewUserPreferencesHandler(db, logger), userID
}

// withAuthedUser is a thinner helper than withWorkspaceUser — preferences
// are workspace-agnostic, so we only need the user context.
func withAuthedUser(req *http.Request, userID string) *http.Request {
	ctx := withUser(req.Context(), &AuthUser{ID: userID, Email: userID + "@example.com"})
	return req.WithContext(ctx)
}

// ── List ────────────────────────────────────────────────────────────────

func TestUserPreferences_List_Unauthenticated_Returns401(t *testing.T) {
	h, _ := userPrefRig(t)
	req := httptest.NewRequest("GET", "/api/v1/me/preferences", nil)
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rr.Code)
	}
}

func TestUserPreferences_List_EmptyUser_Returns200WithEmptyMap(t *testing.T) {
	h, userID := userPrefRig(t)
	req := withAuthedUser(httptest.NewRequest("GET", "/api/v1/me/preferences", nil), userID)
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	body := strings.TrimSpace(rr.Body.String())
	// {} not null — UI iterates Object.entries.
	if body != "{}" {
		t.Errorf("empty map = %q, want \"{}\"", body)
	}
}

// ── Set ─────────────────────────────────────────────────────────────────

func TestUserPreferences_Set_Unauthenticated_Returns401(t *testing.T) {
	h, _ := userPrefRig(t)
	req := httptest.NewRequest("PUT", "/api/v1/me/preferences/sidebar.width",
		strings.NewReader(`240`))
	req.SetPathValue("key", "sidebar.width")
	rr := httptest.NewRecorder()
	h.Set(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rr.Code)
	}
}

func TestUserPreferences_Set_InvalidKey_Returns400(t *testing.T) {
	h, userID := userPrefRig(t)
	// Slashes/spaces are explicitly rejected by validPrefKey because the
	// key lands in a URL path; anything beyond [a-zA-Z0-9._-] must 400.
	for _, k := range []string{"", "has space", "../etc/passwd", "a/b", strings.Repeat("x", 65)} {
		req := withAuthedUser(httptest.NewRequest("PUT", "/x",
			strings.NewReader(`1`)), userID)
		req.SetPathValue("key", k)
		rr := httptest.NewRecorder()
		h.Set(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("key=%q: status = %d, want 400", k, rr.Code)
		}
	}
}

func TestUserPreferences_Set_EmptyBody_Returns400(t *testing.T) {
	h, userID := userPrefRig(t)
	req := withAuthedUser(httptest.NewRequest("PUT", "/x",
		strings.NewReader(``)), userID)
	req.SetPathValue("key", "sidebar.width")
	rr := httptest.NewRecorder()
	h.Set(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestUserPreferences_Set_NonJSONBody_Returns400(t *testing.T) {
	h, userID := userPrefRig(t)
	req := withAuthedUser(httptest.NewRequest("PUT", "/x",
		strings.NewReader(`not json at all`)), userID)
	req.SetPathValue("key", "sidebar.width")
	rr := httptest.NewRecorder()
	h.Set(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestUserPreferences_Set_OversizedBody_Returns413(t *testing.T) {
	h, userID := userPrefRig(t)
	// MaxBytesReader cap is 16 KB; 32 KB of JSON-escaped 'a's blows past
	// it. Build a quoted JSON string so the body is structurally valid
	// (and would otherwise pass json.Valid) — we want the cap to be the
	// gate that fires, not the JSON parse.
	huge := `"` + strings.Repeat("a", 32*1024) + `"`
	req := withAuthedUser(httptest.NewRequest("PUT", "/x",
		strings.NewReader(huge)), userID)
	req.SetPathValue("key", "blob")
	rr := httptest.NewRecorder()
	h.Set(rr, req)
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413; body=%s", rr.Code, rr.Body.String())
	}
}

func TestUserPreferences_Set_HappyPath_PersistsRoundTrip(t *testing.T) {
	h, userID := userPrefRig(t)

	// Set
	setReq := withAuthedUser(httptest.NewRequest("PUT", "/x",
		strings.NewReader(`{"density":"compact","sidebar":280}`)), userID)
	setReq.SetPathValue("key", "ui.layout")
	setRR := httptest.NewRecorder()
	h.Set(setRR, setReq)
	if setRR.Code != http.StatusNoContent {
		t.Fatalf("set status = %d, want 204; body=%s", setRR.Code, setRR.Body.String())
	}

	// List should now reflect the value.
	listReq := withAuthedUser(httptest.NewRequest("GET", "/api/v1/me/preferences", nil), userID)
	listRR := httptest.NewRecorder()
	h.List(listRR, listReq)
	if listRR.Code != http.StatusOK {
		t.Fatalf("list status = %d, want 200", listRR.Code)
	}
	var got map[string]json.RawMessage
	if err := json.Unmarshal(listRR.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	raw, ok := got["ui.layout"]
	if !ok {
		t.Fatalf("ui.layout missing from list; got keys: %v", maps(got))
	}
	var parsed map[string]any
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("unmarshal stored value: %v", err)
	}
	if parsed["density"] != "compact" {
		t.Errorf("density round-trip lost: got %v", parsed["density"])
	}
	if parsed["sidebar"].(float64) != 280 {
		t.Errorf("sidebar round-trip lost: got %v", parsed["sidebar"])
	}
}

func TestUserPreferences_Set_Upsert_OverwritesExistingValue(t *testing.T) {
	// The handler uses ON CONFLICT DO UPDATE; repeated Set on the same
	// key must replace, not insert-and-conflict.
	h, userID := userPrefRig(t)
	for _, body := range []string{`1`, `2`, `3`} {
		req := withAuthedUser(httptest.NewRequest("PUT", "/x",
			strings.NewReader(body)), userID)
		req.SetPathValue("key", "counter")
		rr := httptest.NewRecorder()
		h.Set(rr, req)
		if rr.Code != http.StatusNoContent {
			t.Fatalf("set body=%q: status = %d", body, rr.Code)
		}
	}

	listReq := withAuthedUser(httptest.NewRequest("GET", "/api/v1/me/preferences", nil), userID)
	listRR := httptest.NewRecorder()
	h.List(listRR, listReq)
	var got map[string]json.RawMessage
	_ = json.Unmarshal(listRR.Body.Bytes(), &got)
	if string(got["counter"]) != "3" {
		t.Errorf("final counter = %s, want 3 (upsert collapsed?)", string(got["counter"]))
	}
}

// ── Delete ──────────────────────────────────────────────────────────────

func TestUserPreferences_Delete_Unauthenticated_Returns401(t *testing.T) {
	h, _ := userPrefRig(t)
	req := httptest.NewRequest("DELETE", "/x", nil)
	req.SetPathValue("key", "anything")
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rr.Code)
	}
}

func TestUserPreferences_Delete_InvalidKey_Returns400(t *testing.T) {
	h, userID := userPrefRig(t)
	req := withAuthedUser(httptest.NewRequest("DELETE", "/x", nil), userID)
	req.SetPathValue("key", "../escape")
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestUserPreferences_Delete_Idempotent(t *testing.T) {
	// Deleting a non-existent key is fine; the underlying SQL is a no-op
	// when no rows match. The handler should treat that as 204, not 404
	// — UI delete buttons would otherwise flash an error on a stale row.
	h, userID := userPrefRig(t)
	req := withAuthedUser(httptest.NewRequest("DELETE", "/x", nil), userID)
	req.SetPathValue("key", "never.existed")
	rr := httptest.NewRecorder()
	h.Delete(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (idempotent)", rr.Code)
	}
}

func TestUserPreferences_Delete_RemovesPersistedKey(t *testing.T) {
	h, userID := userPrefRig(t)

	// Seed via Set
	setReq := withAuthedUser(httptest.NewRequest("PUT", "/x",
		strings.NewReader(`42`)), userID)
	setReq.SetPathValue("key", "answer")
	h.Set(httptest.NewRecorder(), setReq)

	// Delete
	delReq := withAuthedUser(httptest.NewRequest("DELETE", "/x", nil), userID)
	delReq.SetPathValue("key", "answer")
	delRR := httptest.NewRecorder()
	h.Delete(delRR, delReq)
	if delRR.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d, want 204", delRR.Code)
	}

	// List should no longer include the key.
	listReq := withAuthedUser(httptest.NewRequest("GET", "/api/v1/me/preferences", nil), userID)
	listRR := httptest.NewRecorder()
	h.List(listRR, listReq)
	var got map[string]json.RawMessage
	_ = json.Unmarshal(listRR.Body.Bytes(), &got)
	if _, exists := got["answer"]; exists {
		t.Errorf("deleted key still present in list")
	}
}

// maps is a small helper so test failure messages can include the keys
// of a map[string]json.RawMessage without leaking the values.
func maps(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
