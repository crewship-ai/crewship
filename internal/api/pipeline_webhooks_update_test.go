package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

// B9 (#2362) / F21: webhooks could only be edited by delete + recreate,
// which rotated the token and therefore the URL a sender already has
// configured. UpdateWebhook must let name / rate limit / inputs template
// change in place while the token (and, unless explicitly asked, the
// signing secret) survive untouched.

func TestUpdateWebhook_NoBackend_Returns503(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	h := NewPipelineHandler(db, testLogger(), nil, nil) // no SetWebhookStore

	req := withWorkspaceUser(httptest.NewRequest("PATCH",
		"/api/v1/workspaces/"+wsID+"/pipeline-webhooks/wh1", strings.NewReader(`{}`)),
		userID, wsID, "OWNER")
	req.SetPathValue("webhookId", "wh1")
	rr := httptest.NewRecorder()
	h.UpdateWebhook(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rr.Code)
	}
}

func TestUpdateWebhook_NotFound_Returns404(t *testing.T) {
	h, _, userID, wsID := webhookHandlerRig(t)
	req := withWorkspaceUser(httptest.NewRequest("PATCH",
		"/api/v1/workspaces/"+wsID+"/pipeline-webhooks/does-not-exist", strings.NewReader(`{"name":"x"}`)),
		userID, wsID, "OWNER")
	req.SetPathValue("webhookId", "does-not-exist")
	rr := httptest.NewRecorder()
	h.UpdateWebhook(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body=%s", rr.Code, rr.Body.String())
	}
}

func TestUpdateWebhook_WrongWorkspace_Returns404(t *testing.T) {
	h, db, _, wsID := webhookHandlerRig(t)
	pipelineID := "pw-wrong-ws"
	seedWebhookPipeline(t, db, wsID, pipelineID, "wrong-ws-pipe")
	wh := seedWebhookRow(t, db, wsID, pipelineID, "secret", true)

	otherUser := "other-user-id"
	otherWS := "other-workspace-id"
	if _, err := db.Exec(`INSERT INTO users (id, email, full_name) VALUES (?, 'other@example.com', 'Other User')`, otherUser); err != nil {
		t.Fatalf("insert other user: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO workspaces (id, name, slug) VALUES (?, 'Other', 'other')`, otherWS); err != nil {
		t.Fatalf("insert other workspace: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO workspace_members (id, workspace_id, user_id, role) VALUES ('m2', ?, ?, 'OWNER')`, otherWS, otherUser); err != nil {
		t.Fatalf("insert other member: %v", err)
	}

	req := withWorkspaceUser(httptest.NewRequest("PATCH",
		"/api/v1/workspaces/"+otherWS+"/pipeline-webhooks/"+wh.ID, strings.NewReader(`{"name":"x"}`)),
		otherUser, otherWS, "OWNER")
	req.SetPathValue("webhookId", wh.ID)
	rr := httptest.NewRecorder()
	h.UpdateWebhook(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body=%s", rr.Code, rr.Body.String())
	}
}

// TestUpdateWebhook_NameRateLimitAndInputsTemplate_ChangeInPlace is the F21
// accept line proven directly: name, rate_limit_per_min and inputs_template
// all change, and the token / token hash never do — the same webhook URL a
// sender already configured keeps working.
func TestUpdateWebhook_NameRateLimitAndInputsTemplate_ChangeInPlace(t *testing.T) {
	h, db, userID, wsID := webhookHandlerRig(t)
	pipelineID := "pw-edit"
	seedWebhookPipeline(t, db, wsID, pipelineID, "edit-pipe")
	wh := seedWebhookRow(t, db, wsID, pipelineID, "original-secret", true)
	originalTokenHash := wh.TokenHash

	body := `{"name":"renamed hook","rate_limit_per_min":42,"inputs_template":{"source":"edited"}}`
	req := withWorkspaceUser(httptest.NewRequest("PATCH",
		"/api/v1/workspaces/"+wsID+"/pipeline-webhooks/"+wh.ID, strings.NewReader(body)),
		userID, wsID, "OWNER")
	req.SetPathValue("webhookId", wh.ID)
	rr := httptest.NewRecorder()
	h.UpdateWebhook(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rr.Code, rr.Body.String())
	}
	var out webhookResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.Name != "renamed hook" {
		t.Errorf("name = %q, want %q", out.Name, "renamed hook")
	}
	if out.RateLimitPerMin != 42 {
		t.Errorf("rate_limit_per_min = %d, want 42", out.RateLimitPerMin)
	}
	if out.InputsTemplate["source"] != "edited" {
		t.Errorf("inputs_template[source] = %v, want %q", out.InputsTemplate["source"], "edited")
	}
	// The URL never rotates on an ordinary edit: signing_secret_set stays
	// true and the response never reveals a secret (only create/rotate do).
	if out.SigningSecret != "" {
		t.Errorf("signing_secret leaked on a non-rotating update: %q", out.SigningSecret)
	}
	if !out.SigningSecretSet {
		t.Errorf("signing_secret_set = false, want true (secret preserved)")
	}

	reloaded, err := pipeline.NewWebhookStore(db).GetByID(t.Context(), wh.ID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.TokenHash != originalTokenHash {
		t.Fatalf("token_hash changed on an ordinary update — the webhook URL rotated: before=%q after=%q",
			originalTokenHash, reloaded.TokenHash)
	}
	if reloaded.SigningSecret != "original-secret" {
		t.Fatalf("signing secret changed on an update that did not ask to rotate it: got %q", reloaded.SigningSecret)
	}
}

// TestUpdateWebhook_RotateSecret_IsOptIn proves the other half of F21's
// "explicit, opt-in token rotation" — WITHOUT rotate_secret the secret is
// preserved (previous test); WITH it, a new one is minted and revealed
// exactly once, same as create.
// TestUpdateWebhook_ExplicitZeroRateLimit_IsHonoured — an earlier version of
// the merge gate was `mentioned && body.RateLimitPerMin > 0`, which silently
// dropped an explicit 0 (or negative) with a 200 that echoed the OLD limit,
// indistinguishable from success to a caller resetting the rate limit.
// Mentioned-only, matching every other field in this handler and matching
// CreateWebhook (which stores whatever value it's given verbatim).
func TestUpdateWebhook_ExplicitZeroRateLimit_IsHonoured(t *testing.T) {
	h, db, userID, wsID := webhookHandlerRig(t)
	pipelineID := "pw-zero-rate"
	seedWebhookPipeline(t, db, wsID, pipelineID, "zero-rate-pipe")
	wh := seedWebhookRow(t, db, wsID, pipelineID, "s", true)

	req := withWorkspaceUser(httptest.NewRequest("PATCH",
		"/api/v1/workspaces/"+wsID+"/pipeline-webhooks/"+wh.ID, strings.NewReader(`{"rate_limit_per_min":0}`)),
		userID, wsID, "OWNER")
	req.SetPathValue("webhookId", wh.ID)
	rr := httptest.NewRecorder()
	h.UpdateWebhook(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rr.Code, rr.Body.String())
	}
	var out webhookResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.RateLimitPerMin != 0 {
		t.Fatalf("rate_limit_per_min = %d, want 0 (explicit value must be honoured, not silently ignored)", out.RateLimitPerMin)
	}
}

func TestUpdateWebhook_RotateSecret_IsOptIn(t *testing.T) {
	h, db, userID, wsID := webhookHandlerRig(t)
	pipelineID := "pw-rotate"
	seedWebhookPipeline(t, db, wsID, pipelineID, "rotate-pipe")
	wh := seedWebhookRow(t, db, wsID, pipelineID, "original-secret", true)
	originalTokenHash := wh.TokenHash

	req := withWorkspaceUser(httptest.NewRequest("PATCH",
		"/api/v1/workspaces/"+wsID+"/pipeline-webhooks/"+wh.ID, strings.NewReader(`{"rotate_secret":true}`)),
		userID, wsID, "OWNER")
	req.SetPathValue("webhookId", wh.ID)
	rr := httptest.NewRecorder()
	h.UpdateWebhook(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rr.Code, rr.Body.String())
	}
	var out webhookResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.SigningSecret == "" || out.SigningSecret == "original-secret" {
		t.Fatalf("expected a freshly minted signing secret in the response, got %q", out.SigningSecret)
	}

	reloaded, err := pipeline.NewWebhookStore(db).GetByID(t.Context(), wh.ID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.SigningSecret == "original-secret" {
		t.Fatalf("rotate_secret:true did not rotate the stored secret")
	}
	// The token (and thus the URL) NEVER rotates through this endpoint —
	// only the HMAC signing secret does. Rotating the URL still requires
	// delete + recreate, which is the one thing F21 says must stay opt-in
	// by a completely different, more destructive action.
	if reloaded.TokenHash != originalTokenHash {
		t.Fatalf("rotate_secret must not rotate the token/URL: before=%q after=%q", originalTokenHash, reloaded.TokenHash)
	}
}

// TestUpdateWebhook_RetargetAndVersionPin exercises the parity fields PATCH
// shares with UpdateSchedule (target_pipeline_slug + target_pipeline_version)
// — same absent-keeps-existing / explicit-null-clears convention, and again,
// none of it touches the token.
func TestUpdateWebhook_RetargetAndVersionPin(t *testing.T) {
	h, db, userID, wsID := webhookHandlerRig(t)
	origPipelineID := "pw-retarget-orig"
	seedWebhookPipeline(t, db, wsID, origPipelineID, "retarget-orig")
	newPipelineID := "pw-retarget-new"
	seedWebhookPipeline(t, db, wsID, newPipelineID, "retarget-new")
	wh := seedWebhookRow(t, db, wsID, origPipelineID, "s", true)
	originalTokenHash := wh.TokenHash

	body := `{"target_pipeline_slug":"retarget-new","target_pipeline_version":3}`
	req := withWorkspaceUser(httptest.NewRequest("PATCH",
		"/api/v1/workspaces/"+wsID+"/pipeline-webhooks/"+wh.ID, strings.NewReader(body)),
		userID, wsID, "OWNER")
	req.SetPathValue("webhookId", wh.ID)
	rr := httptest.NewRecorder()
	h.UpdateWebhook(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rr.Code, rr.Body.String())
	}
	var out webhookResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.TargetPipelineID != newPipelineID {
		t.Errorf("target_pipeline_id = %q, want %q", out.TargetPipelineID, newPipelineID)
	}
	if out.TargetPipelineSlug != "retarget-new" {
		t.Errorf("target_pipeline_slug = %q, want retarget-new", out.TargetPipelineSlug)
	}
	if out.TargetPipelineVersion == nil || *out.TargetPipelineVersion != 3 {
		t.Errorf("target_pipeline_version = %v, want 3", out.TargetPipelineVersion)
	}

	reloaded, err := pipeline.NewWebhookStore(db).GetByID(t.Context(), wh.ID)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	if reloaded.TokenHash != originalTokenHash {
		t.Fatalf("retargeting must not rotate the token/URL: before=%q after=%q", originalTokenHash, reloaded.TokenHash)
	}

	// A second PATCH that mentions neither field keeps both — same
	// absent-keeps-existing convention UpdateSchedule uses.
	req2 := withWorkspaceUser(httptest.NewRequest("PATCH",
		"/api/v1/workspaces/"+wsID+"/pipeline-webhooks/"+wh.ID, strings.NewReader(`{"name":"unrelated edit"}`)),
		userID, wsID, "OWNER")
	req2.SetPathValue("webhookId", wh.ID)
	rr2 := httptest.NewRecorder()
	h.UpdateWebhook(rr2, req2)
	if rr2.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rr2.Code, rr2.Body.String())
	}
	var out2 webhookResponse
	if err := json.Unmarshal(rr2.Body.Bytes(), &out2); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out2.TargetPipelineID != newPipelineID {
		t.Errorf("an unrelated edit lost the retarget: target_pipeline_id = %q, want %q", out2.TargetPipelineID, newPipelineID)
	}
	if out2.TargetPipelineVersion == nil || *out2.TargetPipelineVersion != 3 {
		t.Errorf("an unrelated edit lost the version pin: got %v, want 3", out2.TargetPipelineVersion)
	}
}

func TestUpdateWebhook_Forbidden_BelowManageRole(t *testing.T) {
	h, db, userID, wsID := webhookHandlerRig(t)
	pipelineID := "pw-forbidden"
	seedWebhookPipeline(t, db, wsID, pipelineID, "forbidden-pipe")
	wh := seedWebhookRow(t, db, wsID, pipelineID, "s", true)

	req := withWorkspaceUser(httptest.NewRequest("PATCH",
		"/api/v1/workspaces/"+wsID+"/pipeline-webhooks/"+wh.ID, strings.NewReader(`{"name":"x"}`)),
		userID, wsID, "MEMBER")
	req.SetPathValue("webhookId", wh.ID)
	rr := httptest.NewRecorder()
	h.UpdateWebhook(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403, body=%s", rr.Code, rr.Body.String())
	}
}
