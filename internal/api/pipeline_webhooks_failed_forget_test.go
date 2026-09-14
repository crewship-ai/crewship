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

// A FAILED routine run must NOT release its dedup key: a redelivery of the same
// event is a duplicate, not a re-fire.
//
// This test is the deliberate inversion of TestPipelineWebhooks_Fire_FailedRun_
// ReleasesIdempotencyKey, which pinned the opposite behaviour (#1429, 2.6) —
// the run ended FAILED, the key was deleted, and the sender's next redelivery
// re-executed the routine.
//
// The old behaviour has a case: a key wedged by a transient failure denies a
// re-fire for the whole retention window. The new one has a stronger case, and
// it is §4's. A run that FAILED did not necessarily fail before it touched
// anything: it may have posted the comment, opened the PR or charged the card
// and then failed on the next step. Re-running it because a provider's retry
// timer fired repeats effects that already happened, and the sender never asked
// for that — it asked for its event to be handled once. §4 is explicit that an
// unclear external effect needs verification, not a blind repeat.
//
// A re-run is therefore an authorized REPLAY — new work carrying replay_of, a
// reason and a fresh authorization — and not a consequence of a redelivery.
//
// What it costs, said plainly so nobody rediscovers it as a bug: a routine that
// failed for a genuinely transient reason no longer re-fires by itself when the
// sender retries. Until the dispatcher owns retry_wait, recovering that run is a
// manual replay.
func TestPipelineWebhooks_Fire_FailedRun_KeepsItsDeliveryRecorded(t *testing.T) {
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

	// Redelivery with the SAME key after a FAILED run is a DUPLICATE. It gets
	// the original run's id back and executes nothing.
	second := fire()
	if second["status"] != "DEDUPED" || second["deduped"] != true {
		t.Errorf("redelivery after a FAILED run = %v, want DEDUPED", second)
	}
	if second["run_id"] != firstRunID {
		t.Errorf("redelivery answered run_id %v, want the original %q", second["run_id"], firstRunID)
	}
	h.WaitWebhookDispatches()

	if runner.calls != 1 {
		t.Errorf("runner invoked %d times, want 1 — a redelivery must not repeat effects "+
			"the failed run may already have performed", runner.calls)
	}

	// The delivery is still on the record. Nothing is deleted on failure: that
	// deletion is what let an already-performed effect be repeated.
	var deliveries int
	if err := db.QueryRow(`SELECT COUNT(*) FROM routine_webhook_receipts WHERE workspace_id = ?`, wsID).
		Scan(&deliveries); err != nil {
		t.Fatalf("count deliveries: %v", err)
	}
	if deliveries != 1 {
		t.Errorf("delivery rows = %d, want 1 — the ledger must keep a failed delivery", deliveries)
	}
}
