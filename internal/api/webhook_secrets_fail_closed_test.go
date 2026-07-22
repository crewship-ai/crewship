package api

// #1254 item 1 — webhook/signing secrets must fail CLOSED at the API surface.
//
// The encryption package already refuses to hand plaintext back when no key
// is configured (fail_closed_test.go). These tests pin the two HTTP callers:
// the agent webhook-secret rotate endpoint and pipeline-webhook create. The
// failure must be actionable — the response names ENCRYPTION_KEY and the
// explicit opt-out flag — and the opt-out must restore the legacy plaintext
// write end-to-end.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/encryption"
)

// clearKeyEnvAPI removes every input that could make an encryption key
// resolvable for the duration of the test. Using t.Setenv also keeps the
// test sequential, so the package's parallel tests (which rely on the
// process-wide test key from setTestEncryptionKeyParallelSafe) are never in
// flight at the same time.
func clearKeyEnvAPI(t *testing.T) {
	t.Helper()
	t.Setenv("ENCRYPTION_KEY", "")
	t.Setenv("ENCRYPTION_KEY_V2", "")
	t.Setenv(encryption.KeyVersionEnvVar, "")
	t.Setenv(encryption.AllowPlaintextSecretsEnvVar, "")
}

// assertActionableEncryptionError checks that an error body tells the
// operator both the fix (ENCRYPTION_KEY) and the explicit opt-out flag.
func assertActionableEncryptionError(t *testing.T, body string) {
	t.Helper()
	if !strings.Contains(body, "ENCRYPTION_KEY") {
		t.Errorf("error must tell the operator to set ENCRYPTION_KEY; body=%s", body)
	}
	if !strings.Contains(body, encryption.AllowPlaintextSecretsEnvVar) {
		t.Errorf("error must name the %s opt-out; body=%s", encryption.AllowPlaintextSecretsEnvVar, body)
	}
}

// RED #1254-1a: with no key and no opt-out, rotating an agent webhook secret
// must refuse — no plaintext write, stored value untouched, actionable error.
func TestRotateWebhookSecret_NoKey_FailsClosedWithActionableError(t *testing.T) {
	clearKeyEnvAPI(t)
	h, userID, wsID := covAUHandler(t)
	seedRotateAgent(t, h, wsID, "agent-fc", "whsec_old")

	rr := rotateReq(t, h, userID, wsID, "OWNER", "agent-fc")
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("no-key rotate: status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
	assertActionableEncryptionError(t, rr.Body.String())

	var stored string
	if err := h.db.QueryRow(`SELECT webhook_secret FROM agents WHERE id = 'agent-fc'`).Scan(&stored); err != nil {
		t.Fatalf("readback: %v", err)
	}
	if stored != "whsec_old" {
		t.Errorf("failed rotate must not touch the stored secret; got %q", stored)
	}
}

// RED #1254-1b: the explicit opt-out restores the legacy behaviour end-to-end
// — rotate succeeds and the secret lands plaintext (the operator asked).
func TestRotateWebhookSecret_NoKey_OptOut_StoresPlaintext(t *testing.T) {
	clearKeyEnvAPI(t)
	t.Setenv(encryption.AllowPlaintextSecretsEnvVar, "true")
	h, userID, wsID := covAUHandler(t)
	seedRotateAgent(t, h, wsID, "agent-oo", "whsec_old")

	rr := rotateReq(t, h, userID, wsID, "OWNER", "agent-oo")
	if rr.Code != http.StatusOK {
		t.Fatalf("opt-out rotate: status = %d, body=%s", rr.Code, rr.Body.String())
	}
	var resp struct {
		WebhookSecret string `json:"webhook_secret"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.WebhookSecret == "" || resp.WebhookSecret == "whsec_old" {
		t.Fatalf("opt-out rotate must still mint a NEW secret; got %q", resp.WebhookSecret)
	}

	var stored string
	if err := h.db.QueryRow(`SELECT webhook_secret FROM agents WHERE id = 'agent-oo'`).Scan(&stored); err != nil {
		t.Fatalf("readback: %v", err)
	}
	if encryption.IsEncrypted(stored) {
		t.Errorf("no key is configured — an envelope here would be undecryptable: %q", stored)
	}
	if stored != resp.WebhookSecret {
		t.Errorf("opt-out stores the plaintext (legacy behaviour); stored=%q returned=%q", stored, resp.WebhookSecret)
	}
}

// RED #1254-1c: creating a pipeline webhook (which always carries an HMAC
// signing secret — supplied or minted) must fail closed with no key: no row
// is written and the error is actionable.
func TestPipelineWebhooks_Create_NoKey_FailsClosedWithActionableError(t *testing.T) {
	clearKeyEnvAPI(t)
	h, db, userID, wsID := webhookHandlerRig(t)
	seedWebhookPipeline(t, db, wsID, "pln_fc", "fail-closed")

	body := `{"target_pipeline_slug": "fail-closed", "signing_secret": "user-supplied-hmac"}`
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/workspaces/"+wsID+"/pipeline-webhooks",
			strings.NewReader(body)),
		userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.CreateWebhook(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("no-key create: status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
	assertActionableEncryptionError(t, rr.Body.String())

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pipeline_webhooks`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("failed create must not persist a webhook row; got %d rows", n)
	}
}

// RED #1254-1d: with the opt-out set, pipeline-webhook create succeeds and
// the signing secret lands plaintext (legacy behaviour, explicitly chosen).
func TestPipelineWebhooks_Create_NoKey_OptOut_StoresPlaintext(t *testing.T) {
	clearKeyEnvAPI(t)
	t.Setenv(encryption.AllowPlaintextSecretsEnvVar, "true")
	h, db, userID, wsID := webhookHandlerRig(t)
	seedWebhookPipeline(t, db, wsID, "pln_oo", "opt-out")

	body := `{"target_pipeline_slug": "opt-out", "signing_secret": "user-supplied-hmac"}`
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/workspaces/"+wsID+"/pipeline-webhooks",
			strings.NewReader(body)),
		userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.CreateWebhook(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("opt-out create: status = %d, body=%s", rr.Code, rr.Body.String())
	}

	var stored string
	if err := db.QueryRow(`SELECT signing_secret FROM pipeline_webhooks LIMIT 1`).Scan(&stored); err != nil {
		t.Fatalf("readback: %v", err)
	}
	if stored != "user-supplied-hmac" {
		t.Errorf("opt-out stores the plaintext (legacy behaviour); stored=%q", stored)
	}
}
