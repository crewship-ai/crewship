package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

// pipeline_webhooks_cov_test.go — remaining branches: webhook create
// resolve failure, the Save/SoftDelete write failures via triggers, the
// auto-minted signing secret, and the unreadable-body 400 on
// FireWebhook. Helpers prefixed covPW.

func covPWFixture(t *testing.T) (*PipelineHandler, string, string, string) {
	t.Helper()
	h, db, userID, wsID := runsHandlerRig(t)
	h.SetWebhookStore(pipeline.NewWebhookStore(db))
	h.SetRunner(pipelineAgentRunnerStub{})
	seedRunsPipeline(t, db, wsID, "covpw-pipe", "covpw-pipe")
	return h, userID, wsID, "covpw-pipe"
}

func covPWCreate(h *PipelineHandler, userID, wsID string, body map[string]any) *httptest.ResponseRecorder {
	req := withWorkspaceUser(
		httptest.NewRequest("POST", "/api/v1/workspaces/"+wsID+"/pipeline-webhooks",
			jsonBody(body)),
		userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.CreateWebhook(rr, req)
	return rr
}

func TestCovPW_Create_UnresolvablePipeline_400(t *testing.T) {
	h, userID, wsID, _ := covPWFixture(t)
	rr := covPWCreate(h, userID, wsID, map[string]any{"target_pipeline_id": "ghost"})
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
}

// TestCovPW_Create_AutoMintsSigningSecret — submitting no secret must
// still produce an HMAC-signed webhook (64-char hex secret revealed on
// the create response only).
func TestCovPW_Create_AutoMintsSigningSecret(t *testing.T) {
	h, userID, wsID, pipeID := covPWFixture(t)
	rr := covPWCreate(h, userID, wsID, map[string]any{"target_pipeline_id": pipeID})
	if rr.Code != http.StatusCreated && rr.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rr.Code, rr.Body.String())
	}
	var secret string
	if err := h.db.QueryRow(`SELECT signing_secret FROM pipeline_webhooks WHERE workspace_id = ?`, wsID).
		Scan(&secret); err != nil {
		t.Fatalf("read webhook: %v", err)
	}
	if len(secret) != 64 {
		t.Errorf("signing_secret len = %d, want 64 hex chars (auto-minted)", len(secret))
	}
}

func TestCovPW_Create_SaveFailure_500(t *testing.T) {
	h, userID, wsID, pipeID := covPWFixture(t)
	execOrFatal(t, h.db, `CREATE TRIGGER covpw_block_ins BEFORE INSERT ON pipeline_webhooks
		BEGIN SELECT RAISE(ABORT, 'covpw forced'); END`)
	rr := covPWCreate(h, userID, wsID, map[string]any{"target_pipeline_id": pipeID})
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

func TestCovPW_Delete_SoftDeleteFailure_500(t *testing.T) {
	h, userID, wsID, pipeID := covPWFixture(t)
	wh, err := pipeline.NewWebhookStore(h.db).Save(t.Context(), pipeline.SaveWebhookInput{
		WorkspaceID:      wsID,
		Name:             "covpw-hook",
		TargetPipelineID: pipeID,
		SigningSecret:    "s3cret",
		Enabled:          true,
	})
	if err != nil {
		t.Fatalf("seed webhook: %v", err)
	}
	execOrFatal(t, h.db, `CREATE TRIGGER covpw_block_upd BEFORE UPDATE ON pipeline_webhooks
		BEGIN SELECT RAISE(ABORT, 'covpw forced'); END`)

	req := withWorkspaceUser(
		httptest.NewRequest("DELETE", "/api/v1/workspaces/"+wsID+"/pipeline-webhooks/"+wh.ID, nil),
		userID, wsID, "OWNER")
	req.SetPathValue("webhookId", wh.ID)
	rr := httptest.NewRecorder()
	h.DeleteWebhook(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500; body=%s", rr.Code, rr.Body.String())
	}
}

// covPWBrokenReader fails on the first Read, driving FireWebhook's
// could-not-read-body 400.
type covPWBrokenReader struct{}

func (covPWBrokenReader) Read(_ []byte) (int, error) { return 0, errors.New("stream reset") }

func TestCovPW_Fire_UnreadableBody_400(t *testing.T) {
	h, _, wsID, pipeID := covPWFixture(t)
	wh, err := pipeline.NewWebhookStore(h.db).Save(t.Context(), pipeline.SaveWebhookInput{
		WorkspaceID:      wsID,
		Name:             "covpw-fire",
		TargetPipelineID: pipeID,
		SigningSecret:    "s3cret",
		Enabled:          true,
	})
	if err != nil {
		t.Fatalf("seed webhook: %v", err)
	}
	req := httptest.NewRequest("POST", "/api/v1/webhooks/"+wh.Token, covPWBrokenReader{})
	req.SetPathValue("token", wh.Token)
	rr := httptest.NewRecorder()
	h.FireWebhook(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
}
