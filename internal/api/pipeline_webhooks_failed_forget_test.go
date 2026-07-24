package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

// webhookErroringRunner fails every step so the triggered run ends FAILED.
type webhookErroringRunner struct{ calls int }

func (r *webhookErroringRunner) RunStep(_ context.Context, _ pipeline.AgentStepRequest) (pipeline.AgentStepResult, error) {
	r.calls++
	return pipeline.AgentStepResult{}, errors.New("boom")
}

// #1429 (2.6) — a FAILED webhook run must RELEASE its idempotency key so a
// sender redelivery RE-FIRES a fresh run. Previously the key was only freed
// when Run returned an error; a run that executed and FAILED wedged the key
// for 24h and every redelivery deduped onto the failed run.
func TestPipelineWebhooks_Fire_FailedRun_ReleasesIdempotencyKey(t *testing.T) {
	// The webhook store refuses to persist a plaintext signing secret; give it
	// a usable key (these async tests otherwise rely on a leaked process env).
	// Generated at runtime so no secret literal lands in source.
	kb := make([]byte, 32)
	if _, err := rand.Read(kb); err != nil {
		t.Fatalf("gen key: %v", err)
	}
	t.Setenv("ENCRYPTION_KEY", hex.EncodeToString(kb))

	runner := &webhookErroringRunner{}
	h, db, _, wsID := webhookHandlerRig(t)
	h.SetRunner(runner)
	seedAgentRunPipeline(t, db, wsID, "pln_fail", "fail-target")
	wh := seedWebhookRow(t, db, wsID, "pln_fail", "fail-secret", true)

	body := `{"event":"deploy"}`
	fire := func() map[string]any {
		req := httptest.NewRequest("POST", "/api/v1/webhooks/"+wh.Token, strings.NewReader(body))
		req.SetPathValue("token", wh.Token)
		req.Header.Set("X-Crewship-Signature", covPSWSign("fail-secret", body))
		req.Header.Set("Idempotency-Key", "evt-fail")
		rr := httptest.NewRecorder()
		h.FireWebhook(rr, req)
		if rr.Code != 202 {
			t.Fatalf("status = %d, want 202; body=%s", rr.Code, rr.Body.String())
		}
		var resp map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return resp
	}

	first := fire()
	firstRunID, _ := first["run_id"].(string)
	h.WaitWebhookDispatches()

	// Redelivery with the SAME key must NOT dedupe onto the failed run — the
	// key was released, so this re-fires a fresh run.
	second := fire()
	if second["status"] != "PENDING" || second["deduped"] == true {
		t.Errorf("redelivery of a FAILED run = %v, want a fresh PENDING run (key released)", second)
	}
	if second["run_id"] == firstRunID {
		t.Errorf("redelivery reused the failed run id %q — the key was not released", firstRunID)
	}
	h.WaitWebhookDispatches()

	if runner.calls != 2 {
		t.Errorf("runner invoked %d times, want 2 (delivery + a re-fired redelivery)", runner.calls)
	}
}
