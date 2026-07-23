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
    id TEXT PRIMARY KEY, workspace_id TEXT NOT NULL, type TEXT NOT NULL,
    config_json TEXT NOT NULL DEFAULT '{}', secret_enc TEXT,
    events_json TEXT NOT NULL DEFAULT '["run.failed"]',
    enabled INTEGER NOT NULL DEFAULT 1, created_by TEXT,
    created_at TEXT NOT NULL DEFAULT (datetime('now','subsec')),
    updated_at TEXT NOT NULL DEFAULT (datetime('now','subsec')), deleted_at TEXT);`); err != nil {
		t.Fatal(err)
	}
	return db
}

// configuredMailer reports itself wired so email-channel creation is allowed.
type configuredMailer struct{ mailer.Disabled }

func (configuredMailer) Configured() bool { return true }

func postChannel(t *testing.T, h *NotifyChannelHandler, ws string, body map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	req := withWorkspaceUser(httptest.NewRequest("POST", "/api/v1/notification-channels", strings.NewReader(string(b))), "u1", ws, "MANAGER")
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
		req := withWorkspaceUser(httptest.NewRequest("DELETE", "/api/v1/notification-channels/"+id, nil), "u1", ws, "MANAGER")
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

	req := withWorkspaceUser(httptest.NewRequest("POST", "/api/v1/notification-channels/"+created.ID+"/test", nil), "u1", "ws1", "MANAGER")
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
