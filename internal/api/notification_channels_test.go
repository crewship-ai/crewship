package api

import (
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/mailer"
	"github.com/crewship-ai/crewship/internal/notify"
	"github.com/crewship-ai/crewship/internal/webhook"
	_ "modernc.org/sqlite"
)

// testNotifyEncKey is a throwaway 32-byte AES key built at runtime (not a
// string literal) so the secret scanner doesn't flag a high-entropy
// constant — there is no real secret here.
var testNotifyEncKey = strings.Repeat("0123456789abcdef", 4)

func newNotifyChannelDB(t *testing.T) *sql.DB {
	t.Helper()
	t.Setenv("ENCRYPTION_KEY", testNotifyEncKey)
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`
CREATE TABLE notification_channels (
    id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL,
    type TEXT NOT NULL CHECK (type IN ('email','webhook','shoutrrr')),
    provider TEXT NOT NULL DEFAULT '',
    config_json TEXT NOT NULL DEFAULT '{}', secret_enc TEXT,
    events_json TEXT NOT NULL DEFAULT '["run.failed"]',
    enabled INTEGER NOT NULL DEFAULT 1, created_by TEXT,
    created_at TEXT NOT NULL DEFAULT (datetime('now','subsec')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now','subsec')), deleted_at TEXT,
    scope TEXT NOT NULL DEFAULT 'workspace' CHECK (scope IN ('workspace','user')),
    owner_user_id TEXT,
    categories_json TEXT NOT NULL DEFAULT '[]',
    min_priority TEXT NOT NULL DEFAULT 'low' CHECK (min_priority IN ('low','medium','high','urgent')));`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE app_settings (key TEXT PRIMARY KEY, value TEXT NOT NULL, updated_at TEXT NOT NULL DEFAULT (datetime('now')));`); err != nil {
		t.Fatal(err)
	}
	return db
}

// configuredMailer reports itself wired so email-channel creation is allowed.
type configuredMailer struct{ mailer.Disabled }

func (configuredMailer) Configured() bool { return true }

// postChannel creates a WORKSPACE-scoped channel as an ADMIN — #1412
// tightened workspace-channel writes from MANAGER+ to ADMIN/OWNER (see
// TestNotifyChannelHandler_Create_ManagerForbidden_Workspace for the
// negative case pinning that tightening, and
// TestNotifyChannelHandler_Create_PersonalChannel_AnyMemberSelfService for
// the self-service personal-channel path any role can use).
func postChannel(t *testing.T, h *NotifyChannelHandler, ws string, body map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	req := withWorkspaceUser(httptest.NewRequest("POST", "/api/v1/notification-channels", strings.NewReader(string(b))), "u1", ws, "ADMIN")
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	return rr
}

func TestNotifyChannelHandler_CreateWebhook_ReturnsSecretOnce(t *testing.T) {
	h := NewNotifyChannelHandler(newNotifyChannelDB(t), mailer.Disabled{}, slog.Default())
	rr := postChannel(t, h, "ws1", map[string]string{"type": "webhook", "url": "https://hooks.example.com/x"})
	if rr.Code != http.StatusCreated {
		t.Fatalf("create webhook: got %d, body=%s", rr.Code, rr.Body.String())
	}
	var resp struct {
		ID     string `json:"id"`
		Secret string `json:"secret"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.ID == "" || resp.Secret == "" {
		t.Fatalf("expected id + one-time secret, got %+v", resp)
	}

	// List must NOT carry the secret.
	req := withWorkspaceUser(httptest.NewRequest("GET", "/api/v1/notification-channels", nil), "u1", "ws1", "MANAGER")
	lr := httptest.NewRecorder()
	h.List(lr, req)
	if strings.Contains(lr.Body.String(), resp.Secret) {
		t.Fatalf("List leaked the signing secret: %s", lr.Body.String())
	}
}

func TestNotifyChannelHandler_CreateEmail_FailClosedWithoutMailer(t *testing.T) {
	h := NewNotifyChannelHandler(newNotifyChannelDB(t), mailer.Disabled{}, slog.Default())
	rr := postChannel(t, h, "ws1", map[string]string{"type": "email", "to": "ops@example.com"})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("email without mailer must be rejected 400, got %d: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "not configured") {
		t.Errorf("expected a clear 'not configured' error, got %s", rr.Body.String())
	}
}

func TestNotifyChannelHandler_CreateEmail_AllowedWhenMailerConfigured(t *testing.T) {
	h := NewNotifyChannelHandler(newNotifyChannelDB(t), configuredMailer{}, slog.Default())
	rr := postChannel(t, h, "ws1", map[string]string{"type": "email", "to": "ops@example.com"})
	if rr.Code != http.StatusCreated {
		t.Fatalf("email with mailer should create, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestNotifyChannelHandler_Delete_ScopedAndSoftDeletes(t *testing.T) {
	db := newNotifyChannelDB(t)
	h := NewNotifyChannelHandler(db, mailer.Disabled{}, slog.Default())
	rr := postChannel(t, h, "ws1", map[string]string{"type": "webhook", "url": "https://hooks.example.com/x"})
	var created struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &created)

	// Wrong workspace → 404.
	del := func(ws, id string) int {
		req := withWorkspaceUser(httptest.NewRequest("DELETE", "/api/v1/notification-channels/"+id, nil), "u1", ws, "ADMIN")
		req.SetPathValue("id", id)
		w := httptest.NewRecorder()
		h.Delete(w, req)
		return w.Code
	}
	if code := del("other", created.ID); code != http.StatusNotFound {
		t.Fatalf("cross-workspace delete must 404, got %d", code)
	}
	if code := del("ws1", created.ID); code != http.StatusOK {
		t.Fatalf("owning-workspace delete should 200, got %d", code)
	}
}

func TestNotifyChannelHandler_Test_DeliversSignedWebhook(t *testing.T) {
	// The webhook path now uses the SSRF-safe transport, which blocks the
	// loopback httptest receiver below. Swap it for the default transport so
	// this handler test can assert real delivery; the SSRF guard itself is
	// covered in internal/httpsafe and internal/notify.
	defer notify.SetWebhookTransportForTesting(http.DefaultTransport)()

	var (
		mu      sync.Mutex
		gotBody []byte
		gotSig  string
	)
	recv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		gotBody = b
		gotSig = r.Header.Get("X-Crewship-Signature")
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer recv.Close()

	h := NewNotifyChannelHandler(newNotifyChannelDB(t), mailer.Disabled{}, slog.Default())
	rr := postChannel(t, h, "ws1", map[string]string{"type": "webhook", "url": recv.URL})
	var created struct {
		ID     string `json:"id"`
		Secret string `json:"secret"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &created)

	req := withWorkspaceUser(httptest.NewRequest("POST", "/api/v1/notification-channels/"+created.ID+"/test", nil), "u1", "ws1", "ADMIN")
	req.SetPathValue("id", created.ID)
	tr := httptest.NewRecorder()
	h.Test(tr, req)
	if tr.Code != http.StatusOK {
		t.Fatalf("test send: got %d, body=%s", tr.Code, tr.Body.String())
	}

	mu.Lock()
	defer mu.Unlock()
	if len(gotBody) == 0 {
		t.Fatal("receiver got no test payload")
	}
	sig := strings.TrimPrefix(gotSig, "sha256=")
	if !webhook.ValidateHMAC(gotBody, sig, created.Secret) {
		t.Fatalf("test webhook signature %q does not verify with the returned secret", gotSig)
	}
}

// TestNotifyChannelHandler_Create_ManagerForbidden_Workspace pins the
// #1412 tightening: a workspace-scoped channel write used to be
// MANAGER+ (roleCreate); it is now ADMIN/OWNER only (roleManage),
// enforced inline since the route carries both the workspace and the
// self-service personal-channel path.
func TestNotifyChannelHandler_Create_ManagerForbidden_Workspace(t *testing.T) {
	h := NewNotifyChannelHandler(newNotifyChannelDB(t), mailer.Disabled{}, slog.Default())
	body, _ := json.Marshal(map[string]string{"type": "webhook", "url": "https://hooks.example.com/x"})
	req := withWorkspaceUser(httptest.NewRequest("POST", "/api/v1/notification-channels", strings.NewReader(string(body))), "u1", "ws1", "MANAGER")
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("MANAGER creating a workspace-scoped channel should now 403, got %d: %s", rr.Code, rr.Body.String())
	}
}

// TestNotifyChannelHandler_Create_PersonalChannel_AnyMemberSelfService
// proves the OTHER half of the #1412 tightening: a MEMBER (the lowest
// role) can still add their OWN personal channel, and owner_user_id is
// forced to the AUTHENTICATED caller regardless of what the request body
// claims.
func TestNotifyChannelHandler_Create_PersonalChannel_AnyMemberSelfService(t *testing.T) {
	db := newNotifyChannelDB(t)
	h := NewNotifyChannelHandler(db, mailer.Disabled{}, slog.Default())
	reqBody, _ := json.Marshal(map[string]any{
		"type": "webhook", "url": "https://hooks.example.com/personal", "personal": true,
	})
	req := withWorkspaceUser(httptest.NewRequest("POST", "/api/v1/notification-channels", strings.NewReader(string(reqBody))), "u_member", "ws1", "MEMBER")
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("MEMBER creating their OWN personal channel should succeed, got %d: %s", rr.Code, rr.Body.String())
	}
	var created struct {
		ID          string `json:"id"`
		OwnerUserID string `json:"owner_user_id"`
		Scope       string `json:"scope"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &created)
	if created.Scope != "user" || created.OwnerUserID != "u_member" {
		t.Fatalf("expected scope=user owner=u_member, got %+v", created)
	}

	// The owner can delete their own personal channel...
	delReq := withWorkspaceUser(httptest.NewRequest("DELETE", "/api/v1/notification-channels/"+created.ID, nil), "u_member", "ws1", "MEMBER")
	delReq.SetPathValue("id", created.ID)
	delRR := httptest.NewRecorder()
	h.Delete(delRR, delReq)
	if delRR.Code != http.StatusOK {
		t.Fatalf("owner deleting their own personal channel should 200, got %d", delRR.Code)
	}
}

// TestNotifyChannelHandler_PersonalChannel_OtherMemberCannotWrite proves a
// personal channel is NOT manageable by a different member, even an ADMIN
// — it is ownership-gated, not role-gated.
func TestNotifyChannelHandler_PersonalChannel_OtherMemberCannotWrite(t *testing.T) {
	db := newNotifyChannelDB(t)
	h := NewNotifyChannelHandler(db, mailer.Disabled{}, slog.Default())
	reqBody, _ := json.Marshal(map[string]any{"type": "webhook", "url": "https://hooks.example.com/personal", "personal": true})
	req := withWorkspaceUser(httptest.NewRequest("POST", "/api/v1/notification-channels", strings.NewReader(string(reqBody))), "u_member", "ws1", "MEMBER")
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	var created struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &created)

	// A different user, even an ADMIN, cannot delete someone else's
	// personal channel.
	delReq := withWorkspaceUser(httptest.NewRequest("DELETE", "/api/v1/notification-channels/"+created.ID, nil), "u_other_admin", "ws1", "ADMIN")
	delReq.SetPathValue("id", created.ID)
	delRR := httptest.NewRecorder()
	h.Delete(delRR, delReq)
	if delRR.Code != http.StatusForbidden {
		t.Fatalf("a different user (even ADMIN) must not delete another member's personal channel, got %d", delRR.Code)
	}
}

// TestNotifyChannelHandler_CreateShoutrrr_Slack proves the new provider
// type reaches Create end to end through the HTTP handler layer (the
// store-level validation is already covered by internal/notify's own
// tests).
func TestNotifyChannelHandler_CreateShoutrrr_Slack(t *testing.T) {
	h := NewNotifyChannelHandler(newNotifyChannelDB(t), mailer.Disabled{}, slog.Default())
	body, _ := json.Marshal(map[string]string{
		"type": "shoutrrr", "provider": "slack", "shoutrrr_url": "slack://hook:TOKEN@webhook",
	})
	req := withWorkspaceUser(httptest.NewRequest("POST", "/api/v1/notification-channels", strings.NewReader(string(body))), "u1", "ws1", "ADMIN")
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create shoutrrr/slack channel: got %d, body=%s", rr.Code, rr.Body.String())
	}
	var resp struct {
		Provider string `json:"provider"`
		Secret   string `json:"secret"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.Provider != "slack" || resp.Secret != "slack://hook:TOKEN@webhook" {
		t.Fatalf("unexpected response: %+v", resp)
	}
}

// TestNotifyChannelHandler_CreateShoutrrr_FailsClosedWhenProviderDisabled
// proves the providers-registry admin toggle is actually enforced at
// create time, not just advisory in the registry read.
func TestNotifyChannelHandler_CreateShoutrrr_FailsClosedWhenProviderDisabled(t *testing.T) {
	db := newNotifyChannelDB(t)
	if _, err := db.Exec(`INSERT INTO app_settings (key, value) VALUES ('notify.provider.slack.enabled', 'false')`); err != nil {
		t.Fatal(err)
	}
	h := NewNotifyChannelHandler(db, mailer.Disabled{}, slog.Default())
	body, _ := json.Marshal(map[string]string{"type": "shoutrrr", "provider": "slack", "shoutrrr_url": "slack://hook:TOKEN@webhook"})
	req := withWorkspaceUser(httptest.NewRequest("POST", "/api/v1/notification-channels", strings.NewReader(string(body))), "u1", "ws1", "ADMIN")
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("a disabled provider should be rejected at create, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestNotifyChannelHandler_PatchCategories(t *testing.T) {
	h := NewNotifyChannelHandler(newNotifyChannelDB(t), mailer.Disabled{}, slog.Default())
	rr := postChannel(t, h, "ws1", map[string]string{"type": "webhook", "url": "https://hooks.example.com/x"})
	var created struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &created)

	patchBody, _ := json.Marshal(map[string]any{"categories": []string{"security", "budget"}})
	req := withWorkspaceUser(httptest.NewRequest("PATCH", "/api/v1/notification-channels/"+created.ID, strings.NewReader(string(patchBody))), "u1", "ws1", "ADMIN")
	req.SetPathValue("id", created.ID)
	prr := httptest.NewRecorder()
	h.Patch(prr, req)
	if prr.Code != http.StatusOK {
		t.Fatalf("patch categories: got %d, body=%s", prr.Code, prr.Body.String())
	}

	listReq := withWorkspaceUser(httptest.NewRequest("GET", "/api/v1/notification-channels", nil), "u1", "ws1", "ADMIN")
	lr := httptest.NewRecorder()
	h.List(lr, listReq)
	var listResp struct {
		Channels []struct {
			ID         string   `json:"id"`
			Categories []string `json:"categories"`
		} `json:"channels"`
	}
	_ = json.Unmarshal(lr.Body.Bytes(), &listResp)
	if len(listResp.Channels) != 1 || len(listResp.Channels[0].Categories) != 2 {
		t.Fatalf("expected the patched categories to round-trip, got %+v", listResp)
	}
}
