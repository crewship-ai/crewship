package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

// Tests for the "unsigned" ingress profile: bearer-token-only dispatch
// for senders who cannot sign (Coolify SendWebhookJobPosts plain JSON
// with no HMAC header). Every pre-existing crewship/github profile keeps
// its fail-closed signature check; unsigned is create-time opt-in only
// and cannot be mixed with a signing secret.

func TestPipelineWebhooks_UnsignedProfile_CreateAndFire(t *testing.T) {
	h, db, userID, wsID := webhookHandlerRig(t)
	seedWebhookPipeline(t, db, wsID, "pln_unsigned", "unsigned-target")

	createReq := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/workspaces/"+wsID+"/pipeline-webhooks",
			strings.NewReader(`{"name":"coolify notifications","target_pipeline_slug":"unsigned-target","ingress_profile":"unsigned","rate_limit_per_min":60}`)),
		userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.CreateWebhook(rr, createReq)
	if rr.Code != http.StatusCreated && rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 201; body=%s", rr.Code, rr.Body.String())
	}
	var resp webhookResponse
	if err := json.NewDecoder(rr.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.IngressProfile != "unsigned" {
		t.Fatalf("ingress_profile = %q, want unsigned", resp.IngressProfile)
	}
	if resp.SigningSecretSet || resp.SigningSecret != "" {
		t.Error("unsigned webhook must never mint/reveal a signing secret")
	}
	if !strings.HasPrefix(resp.Token, "wh_") {
		t.Fatalf("bearer token missing: %q", resp.Token)
	}

	// Dispatch shape Coolify actually sends: plain JSON POST, no
	// signature header. FireWebhook needs a wired runner.
	h.SetRunner(&stubRunner{output: "ok"})
	fire := httptest.NewRequest("POST", "/api/v1/webhooks/"+resp.Token, strings.NewReader(`{"event":"test"}`))
	fire.SetPathValue("token", resp.Token)
	fireRR := httptest.NewRecorder()
	h.FireWebhook(fireRR, fire)
	if fireRR.Code == http.StatusUnauthorized {
		t.Fatalf("unsigned dispatch rejected with 401: %s", fireRR.Body.String())
	}
	if fireRR.Code >= 500 {
		t.Fatalf("unsigned dispatch broke badly: %d %s", fireRR.Code, fireRR.Body.String())
	}
}

func TestPipelineWebhooks_UnsignedProfile_CannotMixSecret(t *testing.T) {
	h, db, userID, wsID := webhookHandlerRig(t)
	seedWebhookPipeline(t, db, wsID, "pln_mix", "mix-target")

	createReq := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/workspaces/"+wsID+"/pipeline-webhooks",
			strings.NewReader(`{"name":"x","target_pipeline_slug":"mix-target","ingress_profile":"unsigned","signing_secret":"leak"}`)),
		userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.CreateWebhook(rr, createReq)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
}

func TestPipelineWebhooks_SignedProfile_StillRejectsUnsignedBody(t *testing.T) {
	h, db, _, wsID := webhookHandlerRig(t)
	seedWebhookPipeline(t, db, wsID, "pln_signed", "signed-target")
	wh := seedWebhookRow(t, db, wsID, "pln_signed", "super-secret", true)
	h.SetRunner(&stubRunner{output: "ok"})

	req := httptest.NewRequest("POST", "/api/v1/webhooks/"+wh.Token, strings.NewReader(`{"x":1}`))
	req.SetPathValue("token", wh.Token)
	rr := httptest.NewRecorder()
	h.FireWebhook(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body=%s", rr.Code, rr.Body.String())
	}
}

// Unsigned must round-trip through the store too (store-level enum).
func TestPipelineWebhookStore_UnsignedProfile(t *testing.T) {
	_, db, _, wsID := webhookHandlerRig(t)
	seedWebhookPipeline(t, db, wsID, "pln_unsigned_store", "store-target")
	wh, err := pipeline.NewWebhookStore(db).Save(t.Context(), pipeline.SaveWebhookInput{
		WorkspaceID:      wsID,
		Name:             "coolify",
		TargetPipelineID: "pln_unsigned_store",
		IngressProfile:   "unsigned",
		Enabled:          true,
	})
	if err != nil {
		t.Fatalf("save unsigned webhook: %v", err)
	}
	if wh.IngressProfile != "unsigned" || wh.SigningSecret != "" {
		t.Fatalf("profile=%q secret=%q, want unsigned/empty", wh.IngressProfile, wh.SigningSecret)
	}
	got, err := pipeline.NewWebhookStore(db).GetByToken(t.Context(), wh.Token)
	if err == nil && got.IngressProfile != "unsigned" {
		t.Fatalf("re-lookup profile = %q, want unsigned", got.IngressProfile)
	}
	if _, derr := pipeline.NewWebhookStore(db).Save(t.Context(), pipeline.SaveWebhookInput{
		WorkspaceID:      wsID,
		Name:             "hybrid",
		TargetPipelineID: "pln_unsigned_store",
		IngressProfile:   "unsigned",
		SigningSecret:    "x",
		Enabled:          true,
	}); derr == nil {
		t.Fatal("unsigned + signing_secret must be refused at the store")
	}
}

// Guards are preserved: 503 without backend wiring.
func TestPipelineWebhooks_UnsignedProfile_CreateBadProfile(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	h := NewPipelineHandler(db, logger, nil, nil)
	h.SetWebhookStore(pipeline.NewWebhookStore(db))
	seedWebhookPipeline(t, db, wsID, "pln_bad", "bad-target")

	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/workspaces/"+wsID+"/pipeline-webhooks",
			strings.NewReader(`{"name":"x","target_pipeline_slug":"bad-target","ingress_profile":"plain","signing_secret":""}`)),
		userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.CreateWebhook(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
}
