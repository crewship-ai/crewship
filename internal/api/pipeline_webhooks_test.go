package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

// webhookHandlerRig wires a PipelineHandler against the full-migration
// test DB so the production pipeline_webhooks schema (v82) is in play.
// SetWebhookStore is wired so endpoints don't short-circuit with 503;
// the FireWebhook path also needs SetRunner to advance past the
// service-unavailable guard, so callers that exercise dispatch should
// wire one explicitly.
func webhookHandlerRig(t *testing.T) (*PipelineHandler, *sql.DB, string, string) {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	h := NewPipelineHandler(db, logger, nil, nil)
	h.SetWebhookStore(pipeline.NewWebhookStore(db))
	return h, db, userID, wsID
}

// seedWebhookPipeline mirrors seedPipelineRow from
// pipeline_schedules_test.go — the webhook only consults id/slug/
// workspace_id from the pipelines join, so a minimal row is enough.
func seedWebhookPipeline(t *testing.T, db *sql.DB, wsID, id, slug string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := db.Exec(`
		INSERT INTO pipelines (id, workspace_id, slug, name, definition_json, definition_hash, created_at, updated_at, last_test_run_at)
		VALUES (?, ?, ?, ?, '{"name":"x","steps":[]}', 'hash', ?, ?, ?)`,
		id, wsID, slug, slug, now, now, now); err != nil {
		t.Fatalf("seed pipeline: %v", err)
	}
}

// seedWebhookRow inserts a webhook directly via the WebhookStore so
// each test composes from a known-good base shape (token + id minted by
// the store, matching production semantics).
func seedWebhookRow(t *testing.T, db *sql.DB, wsID, pipelineID, signingSecret string, enabled bool) *pipeline.Webhook {
	t.Helper()
	wh, err := pipeline.NewWebhookStore(db).Save(context.Background(), pipeline.SaveWebhookInput{
		WorkspaceID:      wsID,
		Name:             "test-hook",
		TargetPipelineID: pipelineID,
		SigningSecret:    signingSecret,
		Enabled:          enabled,
	})
	if err != nil {
		t.Fatalf("seed webhook: %v", err)
	}
	return wh
}

// ── CreateWebhook ───────────────────────────────────────────────────────

// TestPipelineWebhooks_Create_NoBackend_Returns503 confirms the
// dependency-missing guard: with no SetWebhookStore the endpoint must
// announce unavailability rather than panic.
func TestPipelineWebhooks_Create_NoBackend_Returns503(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	h := NewPipelineHandler(db, logger, nil, nil) // no SetWebhookStore

	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/workspaces/"+wsID+"/pipeline-webhooks",
			strings.NewReader(`{"target_pipeline_slug":"x"}`)),
		userID, wsID, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.CreateWebhook(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rr.Code)
	}
}

// TestPipelineWebhooks_Create_BadJSON_Returns400 — malformed payload
// must short-circuit at the decoder before any DB write.
func TestPipelineWebhooks_Create_BadJSON_Returns400(t *testing.T) {
	h, _, userID, wsID := webhookHandlerRig(t)

	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/workspaces/"+wsID+"/pipeline-webhooks",
			strings.NewReader(`{NOT_JSON`)),
		userID, wsID, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.CreateWebhook(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
}

// TestPipelineWebhooks_Create_MissingTarget_Returns400 — neither slug
// nor id supplied; resolveWebhookPipelineID must reject this so we
// never silently create a webhook bound to nothing.
func TestPipelineWebhooks_Create_MissingTarget_Returns400(t *testing.T) {
	h, _, userID, wsID := webhookHandlerRig(t)

	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/workspaces/"+wsID+"/pipeline-webhooks",
			strings.NewReader(`{"name":"orphan"}`)),
		userID, wsID, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.CreateWebhook(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
}

// TestPipelineWebhooks_Create_UnknownSlug_Returns400 — slug doesn't
// resolve in this workspace; the handler must return a user-facing 400
// rather than 500 from a downstream nil-deref.
func TestPipelineWebhooks_Create_UnknownSlug_Returns400(t *testing.T) {
	h, _, userID, wsID := webhookHandlerRig(t)

	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/workspaces/"+wsID+"/pipeline-webhooks",
			strings.NewReader(`{"target_pipeline_slug":"does-not-exist"}`)),
		userID, wsID, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.CreateWebhook(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

// TestPipelineWebhooks_Create_HappyPath_Returns201WithSecret verifies
// the one-shot signing-secret reveal. Per the inline doc the secret is
// surfaced ONLY in the create response; later GET/list responses must
// hide it. We test the create-time visibility here; List hides it in a
// separate test below.
func TestPipelineWebhooks_Create_HappyPath_Returns201WithSecret(t *testing.T) {
	h, db, userID, wsID := webhookHandlerRig(t)
	seedWebhookPipeline(t, db, wsID, "pln_a", "ping-hosts")

	body := `{
		"name": "stripe-events",
		"target_pipeline_slug": "ping-hosts",
		"signing_secret": "shh-very-secret",
		"rate_limit_per_min": 60
	}`
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/workspaces/"+wsID+"/pipeline-webhooks",
			strings.NewReader(body)),
		userID, wsID, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.CreateWebhook(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}

	var resp webhookResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.WorkspaceID != wsID {
		t.Errorf("workspace echo = %q, want %q", resp.WorkspaceID, wsID)
	}
	if resp.TargetPipelineSlug != "ping-hosts" {
		t.Errorf("slug echo = %q", resp.TargetPipelineSlug)
	}
	if resp.Token == "" {
		t.Errorf("token must be minted on create")
	}
	// One-shot reveal: signing_secret present in the create response.
	if resp.SigningSecret != "shh-very-secret" {
		t.Errorf("signing_secret on create = %q, want shh-very-secret", resp.SigningSecret)
	}
	if !resp.SigningSecretSet {
		t.Errorf("signing_secret_set flag should be true")
	}
}

// TestPipelineWebhooks_Create_EmptySecret_AutoGenerates pins audit M2:
// a CreateWebhook request that omits signing_secret used to silently
// land an unsigned webhook (pipeline.Webhook.Verify short-circuits to
// nil for empty SigningSecret), letting anyone who learned the
// webhook URL forge requests. The handler must now mint a secret
// server-side and surface it once in the create response so the
// caller can configure their sender.
func TestPipelineWebhooks_Create_EmptySecret_AutoGenerates(t *testing.T) {
	h, db, userID, wsID := webhookHandlerRig(t)
	seedWebhookPipeline(t, db, wsID, "pln_b", "auto-secret")

	body := `{
		"target_pipeline_slug": "auto-secret"
	}`
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/workspaces/"+wsID+"/pipeline-webhooks",
			strings.NewReader(body)),
		userID, wsID, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.CreateWebhook(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}

	var resp webhookResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	// 32 bytes hex-encoded = 64 chars.
	if len(resp.SigningSecret) != 64 {
		t.Errorf("auto-generated secret length = %d, want 64 hex chars", len(resp.SigningSecret))
	}
	if !resp.SigningSecretSet {
		t.Errorf("signing_secret_set flag must be true on auto-generate path")
	}
	// Sanity: two independent creates produce different secrets.
	seedWebhookPipeline(t, db, wsID, "pln_c", "auto-secret-2")
	req2 := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/workspaces/"+wsID+"/pipeline-webhooks",
			strings.NewReader(`{"target_pipeline_slug":"auto-secret-2"}`)),
		userID, wsID, "OWNER",
	)
	rr2 := httptest.NewRecorder()
	h.CreateWebhook(rr2, req2)
	var resp2 webhookResponse
	_ = json.Unmarshal(rr2.Body.Bytes(), &resp2)
	if resp2.SigningSecret == resp.SigningSecret {
		t.Errorf("two auto-generated secrets collided: both = %q", resp.SigningSecret)
	}
}

// ── ListWebhooks ────────────────────────────────────────────────────────

// TestPipelineWebhooks_List_NoBackend_Returns503 — without the store
// the list endpoint must surface unavailability, not panic.
func TestPipelineWebhooks_List_NoBackend_Returns503(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	h := NewPipelineHandler(db, logger, nil, nil)

	req := withWorkspaceUser(
		httptest.NewRequest("GET", "/api/v1/workspaces/"+wsID+"/pipeline-webhooks", nil),
		userID, wsID, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.ListWebhooks(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rr.Code)
	}
}

// TestPipelineWebhooks_List_Empty_Returns200WithEmptyArray guards the
// JSON-null-instead-of-[] regression. Handler initialises with
// make([]webhookResponse, 0, …) which must serialise as [].
func TestPipelineWebhooks_List_Empty_Returns200WithEmptyArray(t *testing.T) {
	h, _, userID, wsID := webhookHandlerRig(t)

	req := withWorkspaceUser(
		httptest.NewRequest("GET", "/api/v1/workspaces/"+wsID+"/pipeline-webhooks", nil),
		userID, wsID, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.ListWebhooks(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	body := strings.TrimSpace(rr.Body.String())
	if body == "null" {
		t.Errorf("empty list serialised as null — UI expects []")
	}
	var out []webhookResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out) != 0 {
		t.Errorf("len = %d, want 0", len(out))
	}
}

// TestPipelineWebhooks_List_HidesSigningSecret confirms the one-shot
// secret contract. After creation the secret is in the DB but every
// subsequent list response must hide it (signing_secret_set still true
// so the UI knows HMAC is configured).
func TestPipelineWebhooks_List_HidesSigningSecret(t *testing.T) {
	h, db, userID, wsID := webhookHandlerRig(t)
	seedWebhookPipeline(t, db, wsID, "pln_a", "ours")
	_ = seedWebhookRow(t, db, wsID, "pln_a", "hidden-after-create", true)

	req := withWorkspaceUser(
		httptest.NewRequest("GET", "/api/v1/workspaces/"+wsID+"/pipeline-webhooks", nil),
		userID, wsID, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.ListWebhooks(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}

	var out []webhookResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("len = %d, want 1", len(out))
	}
	if out[0].SigningSecret != "" {
		t.Errorf("signing_secret leaked in list: %q", out[0].SigningSecret)
	}
	if !out[0].SigningSecretSet {
		t.Errorf("signing_secret_set should still be true")
	}
}

// TestPipelineWebhooks_List_HidesOtherWorkspaces is the tenant-
// isolation check: a webhook in workspace B must NOT surface under
// workspace A's list.
func TestPipelineWebhooks_List_HidesOtherWorkspaces(t *testing.T) {
	h, db, userID, wsA := webhookHandlerRig(t)
	seedWebhookPipeline(t, db, wsA, "pln_a", "ours")
	_ = seedWebhookRow(t, db, wsA, "pln_a", "", true)

	// Foreign workspace + own webhook.
	otherWS := "ws_other"
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Other', 'other')`, otherWS); err != nil {
		t.Fatalf("seed other ws: %v", err)
	}
	seedWebhookPipeline(t, db, otherWS, "pln_b", "theirs")
	_ = seedWebhookRow(t, db, otherWS, "pln_b", "", true)

	req := withWorkspaceUser(
		httptest.NewRequest("GET", "/api/v1/workspaces/"+wsA+"/pipeline-webhooks", nil),
		userID, wsA, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.ListWebhooks(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}

	var out []webhookResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &out)
	if len(out) != 1 {
		t.Fatalf("len = %d, want exactly 1 (own ws only); got=%+v", len(out), out)
	}
	if out[0].WorkspaceID != wsA {
		t.Errorf("tenant leak: workspace_id = %q, want %q", out[0].WorkspaceID, wsA)
	}
}

// ── DeleteWebhook ───────────────────────────────────────────────────────

// TestPipelineWebhooks_Delete_MissingID_Returns400 — path value empty
// must short-circuit before the DB lookup.
func TestPipelineWebhooks_Delete_MissingID_Returns400(t *testing.T) {
	h, _, userID, wsID := webhookHandlerRig(t)

	req := withWorkspaceUser(
		httptest.NewRequest("DELETE", "/api/v1/workspaces/"+wsID+"/pipeline-webhooks/", nil),
		userID, wsID, "OWNER",
	)
	rr := httptest.NewRecorder()
	h.DeleteWebhook(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
}

// TestPipelineWebhooks_Delete_UnknownID_Returns404 — the store returns
// ErrNotFound; the handler must map it to 404 (not 500).
func TestPipelineWebhooks_Delete_UnknownID_Returns404(t *testing.T) {
	h, _, userID, wsID := webhookHandlerRig(t)

	req := withWorkspaceUser(
		httptest.NewRequest("DELETE", "/api/v1/workspaces/"+wsID+"/pipeline-webhooks/pwh_nope", nil),
		userID, wsID, "OWNER",
	)
	req.SetPathValue("webhookId", "pwh_nope")
	rr := httptest.NewRecorder()
	h.DeleteWebhook(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
}

// TestPipelineWebhooks_Delete_CrossWorkspace_Returns404 — a webhook
// belonging to workspace A must not be deletable under workspace B's
// context, even if the caller knows the exact id. 404 (not 403) is
// deliberate: matches the contract for unknown ids so the error never
// leaks existence.
func TestPipelineWebhooks_Delete_CrossWorkspace_Returns404(t *testing.T) {
	h, db, userID, wsA := webhookHandlerRig(t)
	seedWebhookPipeline(t, db, wsA, "pln_a", "ours")
	wh := seedWebhookRow(t, db, wsA, "pln_a", "", true)

	otherWS := "ws_other"
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Other', 'other')`, otherWS); err != nil {
		t.Fatalf("seed other ws: %v", err)
	}

	req := withWorkspaceUser(
		httptest.NewRequest("DELETE", "/api/v1/workspaces/"+otherWS+"/pipeline-webhooks/"+wh.ID, nil),
		userID, otherWS, "OWNER",
	)
	req.SetPathValue("webhookId", wh.ID)
	rr := httptest.NewRecorder()
	h.DeleteWebhook(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("cross-workspace DELETE leaked: status = %d, want 404", rr.Code)
	}
}

// TestPipelineWebhooks_Delete_HappyPath_Returns204 — happy path soft-
// deletes the row. Verifies the response status AND that a subsequent
// list hides the row (the store's WHERE deleted_at IS NULL contract).
func TestPipelineWebhooks_Delete_HappyPath_Returns204(t *testing.T) {
	h, db, userID, wsID := webhookHandlerRig(t)
	seedWebhookPipeline(t, db, wsID, "pln_a", "ours")
	wh := seedWebhookRow(t, db, wsID, "pln_a", "", true)

	req := withWorkspaceUser(
		httptest.NewRequest("DELETE", "/api/v1/workspaces/"+wsID+"/pipeline-webhooks/"+wh.ID, nil),
		userID, wsID, "OWNER",
	)
	req.SetPathValue("webhookId", wh.ID)
	rr := httptest.NewRecorder()
	h.DeleteWebhook(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204; body=%s", rr.Code, rr.Body.String())
	}

	// Confirm soft-delete hid the row from the workspace list.
	listReq := withWorkspaceUser(
		httptest.NewRequest("GET", "/api/v1/workspaces/"+wsID+"/pipeline-webhooks", nil),
		userID, wsID, "OWNER",
	)
	listRR := httptest.NewRecorder()
	h.ListWebhooks(listRR, listReq)
	var out []webhookResponse
	_ = json.Unmarshal(listRR.Body.Bytes(), &out)
	for _, w := range out {
		if w.ID == wh.ID {
			t.Errorf("soft-deleted webhook %s still surfaces in List", wh.ID)
		}
	}
}

// ── FireWebhook ─────────────────────────────────────────────────────────

// TestPipelineWebhooks_Fire_NoBackend_Returns503 — neither store nor
// runner is wired. The endpoint must announce unavailability so a
// misconfigured deployment is obvious rather than silently 404ing.
func TestPipelineWebhooks_Fire_NoBackend_Returns503(t *testing.T) {
	db := setupTestDB(t)
	_ = seedTestUser(t, db)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	h := NewPipelineHandler(db, logger, nil, nil) // neither runner nor webhooks wired

	req := httptest.NewRequest("POST", "/api/v1/webhooks/anything", strings.NewReader(`{}`))
	req.SetPathValue("token", "anything")
	rr := httptest.NewRecorder()
	h.FireWebhook(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body=%s", rr.Code, rr.Body.String())
	}
}

// TestPipelineWebhooks_Fire_UnknownToken_Returns404 — wired store +
// runner, but token doesn't match any row. Per the inline doc the
// handler must answer 404 (not 401/403) to avoid leaking which tokens
// exist via response-code differential.
func TestPipelineWebhooks_Fire_UnknownToken_Returns404(t *testing.T) {
	h, _, _, _ := webhookHandlerRig(t)
	h.SetRunner(&stubRunner{output: "irrelevant"})

	req := httptest.NewRequest("POST", "/api/v1/webhooks/no-such-token",
		strings.NewReader(`{}`))
	req.SetPathValue("token", "no-such-token")
	rr := httptest.NewRecorder()
	h.FireWebhook(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body=%s", rr.Code, rr.Body.String())
	}
}

// TestPipelineWebhooks_Fire_DisabledWebhook_Returns404 — a disabled row
// exists but must look identical (404) to a non-existent one. Same
// no-leak rationale as unknown-token.
func TestPipelineWebhooks_Fire_DisabledWebhook_Returns404(t *testing.T) {
	h, db, _, wsID := webhookHandlerRig(t)
	h.SetRunner(&stubRunner{output: "irrelevant"})
	seedWebhookPipeline(t, db, wsID, "pln_a", "ours")
	wh := seedWebhookRow(t, db, wsID, "pln_a", "", false) // enabled=false

	req := httptest.NewRequest("POST", "/api/v1/webhooks/"+wh.Token, strings.NewReader(`{}`))
	req.SetPathValue("token", wh.Token)
	rr := httptest.NewRecorder()
	h.FireWebhook(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("disabled webhook leaked: status = %d, want 404", rr.Code)
	}
}

// TestPipelineWebhooks_Fire_BadSignature_Returns401 — webhook has a
// signing secret configured; an absent / wrong X-Crewship-Signature
// header must be rejected with 401 before rate-limit slots are
// consumed.
func TestPipelineWebhooks_Fire_BadSignature_Returns401(t *testing.T) {
	h, db, _, wsID := webhookHandlerRig(t)
	h.SetRunner(&stubRunner{output: "ok"})
	seedWebhookPipeline(t, db, wsID, "pln_a", "ours")
	wh := seedWebhookRow(t, db, wsID, "pln_a", "real-secret", true)

	req := httptest.NewRequest("POST", "/api/v1/webhooks/"+wh.Token,
		strings.NewReader(`{"hello":"world"}`))
	req.SetPathValue("token", wh.Token)
	// Deliberately wrong signature.
	req.Header.Set("X-Crewship-Signature", "deadbeef")
	rr := httptest.NewRecorder()
	h.FireWebhook(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body=%s", rr.Code, rr.Body.String())
	}
}

// TestPipelineWebhooks_Fire_ValidSignature_Accepts202 — same setup as
// BadSignature, but the caller computes a real HMAC-SHA256 hex digest
// over the body. Together these two tests pin the HMAC gate contract:
// reject when wrong, accept when right.
//
// Note: we deliberately don't assert on the run_id contents because the
// executor path is exercised end-to-end (real pipeline.Executor,
// stubRunner). The contract verified here is the HTTP signature gate.
func TestPipelineWebhooks_Fire_ValidSignature_Accepts202(t *testing.T) {
	h, db, _, wsID := webhookHandlerRig(t)
	h.SetRunner(&stubRunner{output: "ok"})
	seedWebhookPipeline(t, db, wsID, "pln_a", "ours")
	wh := seedWebhookRow(t, db, wsID, "pln_a", "real-secret", true)

	body := `{"hello":"world"}`
	mac := hmac.New(sha256.New, []byte("real-secret"))
	mac.Write([]byte(body))
	sig := hex.EncodeToString(mac.Sum(nil))

	req := httptest.NewRequest("POST", "/api/v1/webhooks/"+wh.Token,
		strings.NewReader(body))
	req.SetPathValue("token", wh.Token)
	req.Header.Set("X-Crewship-Signature", sig)
	rr := httptest.NewRecorder()
	h.FireWebhook(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body=%s", rr.Code, rr.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := resp["run_id"]; !ok {
		t.Errorf("response missing run_id: %v", resp)
	}
}

// TestPipelineWebhooks_Fire_NoSignatureRequired_AcceptsAnyBody — when
// signing_secret is empty the webhook skips HMAC entirely. A naked
// request body must still produce 202. Without this test a refactor
// that always required signatures would break every Stripe-style sender
// without one.
func TestPipelineWebhooks_Fire_NoSignatureRequired_AcceptsAnyBody(t *testing.T) {
	h, db, _, wsID := webhookHandlerRig(t)
	h.SetRunner(&stubRunner{output: "ok"})
	seedWebhookPipeline(t, db, wsID, "pln_a", "ours")
	wh := seedWebhookRow(t, db, wsID, "pln_a", "" /* no secret */, true)

	req := httptest.NewRequest("POST", "/api/v1/webhooks/"+wh.Token,
		strings.NewReader(`anything goes`)) // not even valid JSON
	req.SetPathValue("token", wh.Token)
	rr := httptest.NewRecorder()
	h.FireWebhook(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body=%s", rr.Code, rr.Body.String())
	}
}
