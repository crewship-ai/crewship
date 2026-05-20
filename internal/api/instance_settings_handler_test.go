package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// newInstanceSettingsHandler builds a handler over a fresh sqlite DB,
// plus a seeded workspace + user. Mirrors the shape of
// newTriageHandler in recurring_triage_test.go.
func newInstanceSettingsHandler(t *testing.T) (*InstanceSettingsHandler, string, string) {
	t.Helper()
	db := setupTestDB(t)
	logger := newTestLogger()
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	return NewInstanceSettingsHandler(db, nil, logger), userID, wsID
}

// seedSetting inserts a row directly, bypassing the handler.
func seedSetting(t *testing.T, h *InstanceSettingsHandler, key, value string) {
	t.Helper()
	if _, err := h.db.Exec(
		`INSERT INTO app_settings (key, value, updated_at) VALUES (?, ?, datetime('now'))`,
		key, value,
	); err != nil {
		t.Fatalf("seed setting %q: %v", key, err)
	}
}

// putRequest builds a PUT request for a single key.
func putRequest(t *testing.T, key, value, userID, wsID, role string) *http.Request {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"value": value})
	req := httptest.NewRequest("PUT", "/api/v1/instance/settings/"+key, bytes.NewReader(body))
	req.SetPathValue("key", key)
	return withWorkspaceUser(req, userID, wsID, role)
}

// ── isSensitiveKey unit table ─────────────────────────────────────────

func TestInstanceSettings_isSensitiveKey(t *testing.T) {
	cases := []struct {
		key  string
		want bool
	}{
		{"smtp.password", true},
		{"smtp.password.legacy", true},
		{"smtp.host", false},
		{"oauth.google.client_secret", true},
		{"oauth.linear.client_secret", true},
		{"oauth.google.client_id", false},
		{"webhook.github.secret", true},
		{"webhook.linear.secret", true},
		{"webhook.linear.url", false},
		{"branding.product_name", false},
		{"", false},
	}
	for _, tc := range cases {
		t.Run(tc.key, func(t *testing.T) {
			if got := isSensitiveKey(tc.key); got != tc.want {
				t.Errorf("isSensitiveKey(%q) = %v, want %v", tc.key, got, tc.want)
			}
		})
	}
}

// ── List ──────────────────────────────────────────────────────────────

func TestInstanceSettings_List_Empty(t *testing.T) {
	h, userID, wsID := newInstanceSettingsHandler(t)

	req := httptest.NewRequest("GET", "/api/v1/instance/settings", nil)
	req = withWorkspaceUser(req, userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.List(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var out []instanceSetting
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v body=%s", err, rr.Body.String())
	}
	if len(out) != 0 {
		t.Errorf("len = %d, want 0 (always an array, never null)", len(out))
	}
	// Critical: the JSON literal must be `[]`, not `null`. Clients lean
	// on this — see writeJSON behaviour for nil slices.
	if strings.TrimSpace(rr.Body.String()) == "null" {
		t.Errorf("body = null, want []")
	}
}

func TestInstanceSettings_List_RedactsSensitive(t *testing.T) {
	h, userID, wsID := newInstanceSettingsHandler(t)
	seedSetting(t, h, "smtp.host", "smtp.gmail.com")
	seedSetting(t, h, "smtp.password", "supersecret")
	seedSetting(t, h, "oauth.google.client_secret", "googsec")
	seedSetting(t, h, "oauth.google.client_id", "googid")
	seedSetting(t, h, "webhook.linear.secret", "linsec")
	seedSetting(t, h, "branding.product_name", "Crewship")

	req := httptest.NewRequest("GET", "/api/v1/instance/settings", nil)
	req = withWorkspaceUser(req, userID, wsID, "ADMIN")
	rr := httptest.NewRecorder()
	h.List(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	var out []instanceSetting
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}

	want := map[string]string{
		"smtp.host":                  "smtp.gmail.com",
		"smtp.password":              "***",
		"oauth.google.client_secret": "***",
		"oauth.google.client_id":     "googid",
		"webhook.linear.secret":      "***",
		"branding.product_name":      "Crewship",
	}
	if len(out) != len(want) {
		t.Fatalf("len = %d, want %d", len(out), len(want))
	}
	for _, s := range out {
		w, ok := want[s.Key]
		if !ok {
			t.Errorf("unexpected key %q in response", s.Key)
			continue
		}
		if s.Value != w {
			t.Errorf("key %q: got value %q, want %q", s.Key, s.Value, w)
		}
	}
}

func TestInstanceSettings_List_RBAC(t *testing.T) {
	// canRole("create") allows OWNER/ADMIN/MANAGER; MEMBER and VIEWER are
	// rejected. Empty role → 403 (no anonymous reads even though the
	// route is auth-gated upstream).
	h, userID, wsID := newInstanceSettingsHandler(t)
	seedSetting(t, h, "smtp.host", "x")

	cases := []struct {
		role string
		want int
	}{
		{"OWNER", http.StatusOK},
		{"ADMIN", http.StatusOK},
		{"MANAGER", http.StatusOK},
		{"MEMBER", http.StatusForbidden},
		{"VIEWER", http.StatusForbidden},
		{"", http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run("role="+tc.role, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/api/v1/instance/settings", nil)
			req = withWorkspaceUser(req, userID, wsID, tc.role)
			rr := httptest.NewRecorder()
			h.List(rr, req)
			if rr.Code != tc.want {
				t.Errorf("role=%q got %d want %d", tc.role, rr.Code, tc.want)
			}
		})
	}
}

// ── Get ───────────────────────────────────────────────────────────────

func TestInstanceSettings_Get_HappyAndRedaction(t *testing.T) {
	h, userID, wsID := newInstanceSettingsHandler(t)
	seedSetting(t, h, "smtp.host", "smtp.gmail.com")
	seedSetting(t, h, "smtp.password", "supersecret")

	t.Run("plain key", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/v1/instance/settings/smtp.host", nil)
		req.SetPathValue("key", "smtp.host")
		req = withWorkspaceUser(req, userID, wsID, "MANAGER")
		rr := httptest.NewRecorder()
		h.Get(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
		}
		var s instanceSetting
		_ = json.Unmarshal(rr.Body.Bytes(), &s)
		if s.Value != "smtp.gmail.com" {
			t.Errorf("value = %q, want smtp.gmail.com", s.Value)
		}
	})

	t.Run("sensitive key redacted", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/api/v1/instance/settings/smtp.password", nil)
		req.SetPathValue("key", "smtp.password")
		req = withWorkspaceUser(req, userID, wsID, "MANAGER")
		rr := httptest.NewRecorder()
		h.Get(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
		}
		var s instanceSetting
		_ = json.Unmarshal(rr.Body.Bytes(), &s)
		if s.Value != "***" {
			t.Errorf("value = %q, want ***", s.Value)
		}
	})
}

func TestInstanceSettings_Get_NotFound(t *testing.T) {
	h, userID, wsID := newInstanceSettingsHandler(t)

	req := httptest.NewRequest("GET", "/api/v1/instance/settings/no.such.key", nil)
	req.SetPathValue("key", "no.such.key")
	req = withWorkspaceUser(req, userID, wsID, "ADMIN")
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

func TestInstanceSettings_Get_RBAC_MemberRejected(t *testing.T) {
	h, userID, wsID := newInstanceSettingsHandler(t)
	seedSetting(t, h, "smtp.host", "x")

	req := httptest.NewRequest("GET", "/api/v1/instance/settings/smtp.host", nil)
	req.SetPathValue("key", "smtp.host")
	req = withWorkspaceUser(req, userID, wsID, "MEMBER")
	rr := httptest.NewRecorder()
	h.Get(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", rr.Code)
	}
}

// ── Put ───────────────────────────────────────────────────────────────

func TestInstanceSettings_Put_CreateThenUpdate(t *testing.T) {
	h, userID, wsID := newInstanceSettingsHandler(t)

	// Create
	rr := httptest.NewRecorder()
	h.Put(rr, putRequest(t, "branding.product_name", "Crewship", userID, wsID, "ADMIN"))
	if rr.Code != http.StatusOK {
		t.Fatalf("create: status=%d body=%s", rr.Code, rr.Body.String())
	}

	// Confirm via SELECT
	var v string
	if err := h.db.QueryRow(`SELECT value FROM app_settings WHERE key = ?`, "branding.product_name").Scan(&v); err != nil {
		t.Fatalf("select: %v", err)
	}
	if v != "Crewship" {
		t.Errorf("stored value = %q, want Crewship", v)
	}

	// Update (same key, new value) — exercises ON CONFLICT branch
	rr2 := httptest.NewRecorder()
	h.Put(rr2, putRequest(t, "branding.product_name", "Crewship 2", userID, wsID, "ADMIN"))
	if rr2.Code != http.StatusOK {
		t.Fatalf("update: status=%d", rr2.Code)
	}
	if err := h.db.QueryRow(`SELECT value FROM app_settings WHERE key = ?`, "branding.product_name").Scan(&v); err != nil {
		t.Fatalf("select after update: %v", err)
	}
	if v != "Crewship 2" {
		t.Errorf("stored value after update = %q, want Crewship 2", v)
	}
}

func TestInstanceSettings_Put_SensitiveStoredButRedactedOnEcho(t *testing.T) {
	// Sensitive values still hit disk (write path is not redacted —
	// otherwise the setting would never take effect), but the response
	// body redacts the echo so logs don't leak credentials.
	h, userID, wsID := newInstanceSettingsHandler(t)

	rr := httptest.NewRecorder()
	h.Put(rr, putRequest(t, "smtp.password", "supersecret", userID, wsID, "OWNER"))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", rr.Code, rr.Body.String())
	}
	var s instanceSetting
	if err := json.Unmarshal(rr.Body.Bytes(), &s); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if s.Value != "***" {
		t.Errorf("echo value = %q, want ***", s.Value)
	}
	// And the real value is on disk:
	var v string
	if err := h.db.QueryRow(`SELECT value FROM app_settings WHERE key = ?`, "smtp.password").Scan(&v); err != nil {
		t.Fatalf("select: %v", err)
	}
	if v != "supersecret" {
		t.Errorf("stored value = %q, want supersecret (writes must persist the real value)", v)
	}
}

func TestInstanceSettings_Put_RBAC(t *testing.T) {
	// Write tier is "manage" — OWNER + ADMIN. MANAGER read-only must NOT
	// be allowed to write (defends against role-creep where someone
	// promoted from MANAGER to ADMIN later inherits write power
	// implicitly).
	h, userID, wsID := newInstanceSettingsHandler(t)

	cases := []struct {
		role string
		want int
	}{
		{"OWNER", http.StatusOK},
		{"ADMIN", http.StatusOK},
		{"MANAGER", http.StatusForbidden},
		{"MEMBER", http.StatusForbidden},
		{"VIEWER", http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run("role="+tc.role, func(t *testing.T) {
			rr := httptest.NewRecorder()
			h.Put(rr, putRequest(t, "k."+tc.role, "v", userID, wsID, tc.role))
			if rr.Code != tc.want {
				t.Errorf("role=%q got %d want %d body=%s", tc.role, rr.Code, tc.want, rr.Body.String())
			}
		})
	}
}

func TestInstanceSettings_Put_BadInputs(t *testing.T) {
	h, userID, wsID := newInstanceSettingsHandler(t)

	t.Run("invalid json", func(t *testing.T) {
		req := httptest.NewRequest("PUT", "/api/v1/instance/settings/k", bytes.NewBufferString("not-json"))
		req.SetPathValue("key", "k")
		req = withWorkspaceUser(req, userID, wsID, "OWNER")
		rr := httptest.NewRecorder()
		h.Put(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", rr.Code)
		}
	})

	t.Run("missing value field", func(t *testing.T) {
		req := httptest.NewRequest("PUT", "/api/v1/instance/settings/k", bytes.NewBufferString(`{}`))
		req.SetPathValue("key", "k")
		req = withWorkspaceUser(req, userID, wsID, "OWNER")
		rr := httptest.NewRecorder()
		h.Put(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Errorf("status = %d, want 400", rr.Code)
		}
	})

	t.Run("empty string IS valid", func(t *testing.T) {
		// "" clears a value without deleting the row — distinct from
		// DELETE. The handler must accept it.
		req := httptest.NewRequest("PUT", "/api/v1/instance/settings/banner", bytes.NewBufferString(`{"value":""}`))
		req.SetPathValue("key", "banner")
		req = withWorkspaceUser(req, userID, wsID, "OWNER")
		rr := httptest.NewRecorder()
		h.Put(rr, req)
		if rr.Code != http.StatusOK {
			t.Errorf("status = %d, want 200 (empty string is a valid value)", rr.Code)
		}
	})
}

// ── Delete ────────────────────────────────────────────────────────────

func TestInstanceSettings_Delete_HappyPath(t *testing.T) {
	h, userID, wsID := newInstanceSettingsHandler(t)
	seedSetting(t, h, "branding.product_name", "Crewship")

	req := httptest.NewRequest("DELETE", "/api/v1/instance/settings/branding.product_name", nil)
	req.SetPathValue("key", "branding.product_name")
	req = withWorkspaceUser(req, userID, wsID, "ADMIN")
	rr := httptest.NewRecorder()
	h.Delete(rr, req)

	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 body=%s", rr.Code, rr.Body.String())
	}
	var n int
	_ = h.db.QueryRow(`SELECT COUNT(*) FROM app_settings WHERE key = ?`, "branding.product_name").Scan(&n)
	if n != 0 {
		t.Errorf("row count after delete = %d, want 0", n)
	}
}

func TestInstanceSettings_Delete_NotFound(t *testing.T) {
	h, userID, wsID := newInstanceSettingsHandler(t)

	req := httptest.NewRequest("DELETE", "/api/v1/instance/settings/missing", nil)
	req.SetPathValue("key", "missing")
	req = withWorkspaceUser(req, userID, wsID, "ADMIN")
	rr := httptest.NewRecorder()
	h.Delete(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rr.Code)
	}
}

func TestInstanceSettings_Delete_ProtectedKeysRejected(t *testing.T) {
	// Protected-key guard must fire BEFORE any DB lookup so that even
	// pre-seeded protected rows survive the request, AND the response
	// must be 403 + application/problem+json so the manifest layer can
	// distinguish "you're not allowed" from "transient DB error".
	h, userID, wsID := newInstanceSettingsHandler(t)
	seedSetting(t, h, "instance.bootstrap_at", "2025-01-01T00:00:00Z")
	seedSetting(t, h, "instance.first_user_id", "u-1")
	seedSetting(t, h, "schema.version", "88")

	for _, key := range []string{"instance.bootstrap_at", "instance.first_user_id", "schema.version"} {
		t.Run(key, func(t *testing.T) {
			req := httptest.NewRequest("DELETE", "/api/v1/instance/settings/"+key, nil)
			req.SetPathValue("key", key)
			req = withWorkspaceUser(req, userID, wsID, "OWNER")
			rr := httptest.NewRecorder()
			h.Delete(rr, req)
			if rr.Code != http.StatusForbidden {
				t.Errorf("status = %d, want 403 body=%s", rr.Code, rr.Body.String())
			}
			if ct := rr.Header().Get("Content-Type"); ct != "application/problem+json" {
				t.Errorf("Content-Type = %q, want application/problem+json (RFC 7807)", ct)
			}
			// Row must still exist — guard runs before DELETE.
			var n int
			_ = h.db.QueryRow(`SELECT COUNT(*) FROM app_settings WHERE key = ?`, key).Scan(&n)
			if n != 1 {
				t.Errorf("row count for protected key %q = %d, want 1 (rejection must not delete)", key, n)
			}
		})
	}
}

func TestInstanceSettings_Delete_RBAC(t *testing.T) {
	// Write tier — OWNER + ADMIN only. MANAGER cannot delete.
	h, userID, wsID := newInstanceSettingsHandler(t)

	cases := []struct {
		role string
		// Pre-seed each so the happy-path roles get a 204 rather than 404.
		want int
	}{
		{"OWNER", http.StatusNoContent},
		{"ADMIN", http.StatusNoContent},
		{"MANAGER", http.StatusForbidden},
		{"MEMBER", http.StatusForbidden},
		{"VIEWER", http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run("role="+tc.role, func(t *testing.T) {
			key := "k.del." + tc.role
			seedSetting(t, h, key, "v")

			req := httptest.NewRequest("DELETE", "/api/v1/instance/settings/"+key, nil)
			req.SetPathValue("key", key)
			req = withWorkspaceUser(req, userID, wsID, tc.role)
			rr := httptest.NewRecorder()
			h.Delete(rr, req)
			if rr.Code != tc.want {
				t.Errorf("role=%q got %d want %d", tc.role, rr.Code, tc.want)
			}
		})
	}
}
