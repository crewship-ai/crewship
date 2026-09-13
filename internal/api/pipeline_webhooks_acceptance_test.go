package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/webhook"
)

// The routine webhook surface on the durable acceptance path.

// fireRoutineWebhook is the shared drive: sign a body for a seeded webhook row
// and fire it.
func fireRoutineWebhook(t *testing.T, h *PipelineHandler, token, secret, body, idemKey string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/v1/webhooks/"+token, strings.NewReader(body))
	req.SetPathValue("token", token)
	req.Header.Set("X-Crewship-Signature", covPSWSign(secret, body))
	if idemKey != "" {
		req.Header.Set("Idempotency-Key", idemKey)
	}
	rr := httptest.NewRecorder()
	h.FireWebhook(rr, req)
	return rr
}

// routineAcceptanceRig seeds one runnable routine behind one enabled webhook.
func routineAcceptanceRig(t *testing.T) (*PipelineHandler, string, string, string) {
	t.Helper()
	kb := make([]byte, 32)
	if _, err := rand.Read(kb); err != nil {
		t.Fatalf("gen key: %v", err)
	}
	t.Setenv("ENCRYPTION_KEY", hex.EncodeToString(kb))

	h, db, _, wsID := webhookHandlerRig(t)
	// A runner that fails every step: the dispatch is not what these tests
	// assert on, and a failing one keeps them off the orchestrator entirely.
	h.SetRunner(&webhookErroringRunner{})
	seedAgentRunPipeline(t, db, wsID, "pln_accept", "accept-target")
	wh := seedWebhookRow(t, db, wsID, "pln_accept", "accept-secret", true)
	return h, wh.Token, "accept-secret", wsID
}

// TestPipelineWebhooks_Fire_OversizedBodyIs413 is W8 on the routine surface.
//
// The body was read through io.LimitReader, which does not fail — it stops at
// the limit and reports EOF. A 2 MiB delivery was therefore truncated to its
// first mebibyte, failed HMAC verification against the bytes the sender
// actually signed, and came back as 401 "signature mismatch". The sender was
// told its signature was wrong when the truth was that we refused to read its
// request, and no status ever said so.
func TestPipelineWebhooks_Fire_OversizedBodyIs413(t *testing.T) {
	h, token, secret, _ := routineAcceptanceRig(t)

	body := `{"event":"` + strings.Repeat("x", webhook.MaxBodyBytes) + `"}`
	rr := fireRoutineWebhook(t, h, token, secret, body, "")

	if rr.Code != 413 {
		t.Fatalf("status = %d, want 413 (a truncated read used to answer 401); body=%s",
			rr.Code, rr.Body.String())
	}
}

// The pipeline engine is the only lifecycle owner: no phantom queued work.
func TestPipelineWebhooks_Fire_RecordsReceiptWithoutPhantomWork(t *testing.T) {
	h, token, secret, wsID := routineAcceptanceRig(t)
	rr := fireRoutineWebhook(t, h, token, secret, `{"event":"deploy"}`, "evt-accept-1")
	if rr.Code != 202 {
		t.Fatalf("HTTP %d: %s", rr.Code, rr.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if _, present := got["work_id"]; present {
		t.Fatalf("receipt advertises work-item control: %v", got)
	}
	var runID string
	if err := h.db.QueryRow(`SELECT run_id FROM routine_webhook_receipts WHERE id = ? AND workspace_id = ?`, got["delivery_id"], wsID).Scan(&runID); err != nil {
		t.Fatal(err)
	}
	if runID == "" || runID != got["run_id"] {
		t.Fatalf("receipt points to %q, response %v", runID, got)
	}
	h.WaitWebhookDispatches()
	if n := countWebhookRows(t, h.db, `SELECT COUNT(*) FROM work_items`); n != 0 {
		t.Fatalf("%d phantom work items", n)
	}
}

// TestPipelineWebhooks_Fire_DuplicateAnswersTheOriginalRun: a redelivery inside
// the retention window is matched against the LEDGER now, and gets the original
// run's id back rather than a fresh handle.
func TestPipelineWebhooks_Fire_DuplicateAnswersTheOriginalRun(t *testing.T) {
	h, token, secret, _ := routineAcceptanceRig(t)

	firstRR := fireRoutineWebhook(t, h, token, secret, `{"event":"deploy"}`, "evt-dup-1")
	if firstRR.Code != 202 {
		t.Fatalf("first status = %d, want 202; body=%s", firstRR.Code, firstRR.Body.String())
	}
	var first map[string]any
	_ = json.Unmarshal(firstRR.Body.Bytes(), &first)
	h.WaitWebhookDispatches()

	secondRR := fireRoutineWebhook(t, h, token, secret, `{"event":"deploy"}`, "evt-dup-1")
	if secondRR.Code != 202 {
		t.Fatalf("second status = %d, want 202; body=%s", secondRR.Code, secondRR.Body.String())
	}
	var second map[string]any
	_ = json.Unmarshal(secondRR.Body.Bytes(), &second)

	if second["deduped"] != true || second["status"] != "DEDUPED" {
		t.Errorf("redelivery = %v, want DEDUPED", second)
	}
	if second["run_id"] != first["run_id"] {
		t.Errorf("redelivery run_id = %v, want the original %v", second["run_id"], first["run_id"])
	}
	if second["work_id"] != first["work_id"] {
		t.Errorf("redelivery work_id = %v, want the original %v", second["work_id"], first["work_id"])
	}

	var work int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM work_items`).Scan(&work); err != nil {
		t.Fatalf("count work: %v", err)
	}
	if work != 0 {
		t.Errorf("work items = %d, want 0 — routine execution must not create phantom work", work)
	}
}

// TestPipelineWebhooks_Fire_SameKeyDifferentBodyIsConflict: §5's 409 row. The
// original record is kept.
func TestPipelineWebhooks_Fire_SameKeyDifferentBodyIsConflict(t *testing.T) {
	h, token, secret, _ := routineAcceptanceRig(t)

	if rr := fireRoutineWebhook(t, h, token, secret, `{"event":"a"}`, "evt-conflict"); rr.Code != 202 {
		t.Fatalf("first status = %d, want 202; body=%s", rr.Code, rr.Body.String())
	}
	h.WaitWebhookDispatches()

	rr := fireRoutineWebhook(t, h, token, secret, `{"event":"DIFFERENT"}`, "evt-conflict")
	if rr.Code != 409 {
		t.Fatalf("same key + different body status = %d, want 409; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "delivery_conflict") {
		t.Errorf("409 body = %s, want it to name delivery_conflict", rr.Body.String())
	}

	var n int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM routine_webhook_receipts`).Scan(&n); err != nil {
		t.Fatalf("count deliveries: %v", err)
	}
	if n != 1 {
		t.Errorf("delivery rows = %d, want 1 — the original record must be kept", n)
	}
}

// TestPipelineWebhooks_Fire_ThrottledRedeliveryAnswersTheOriginalReceipt: the
// per-token rate gate used to run BEFORE the receipt lookup, so a sender
// retrying a delivery we had already accepted was told 429 "rate limit
// exceeded" instead of being handed the receipt it already owned. §5 is the
// other way round: a duplicate of accepted work is never refused for fullness;
// only NEW work is. The same-id-different-body conflict and the unavailable
// ledger keep their own answers under throttling too — a 409 must not become a
// 429, and a broken lookup must not be reported as "you are sending too fast".
func TestPipelineWebhooks_Fire_ThrottledRedeliveryAnswersTheOriginalReceipt(t *testing.T) {
	h, token, secret, _ := routineAcceptanceRig(t)
	// One delivery per minute: the first accepted fire consumes the whole
	// budget, so every request after it meets a closed gate.
	if _, err := h.db.Exec(`UPDATE pipeline_webhooks SET rate_limit_per_min = 1`); err != nil {
		t.Fatalf("pin the rate limit: %v", err)
	}

	firstRR := fireRoutineWebhook(t, h, token, secret, `{"event":"deploy"}`, "evt-throttled")
	if firstRR.Code != 202 {
		t.Fatalf("first status = %d, want 202; body=%s", firstRR.Code, firstRR.Body.String())
	}
	var first map[string]any
	_ = json.Unmarshal(firstRR.Body.Bytes(), &first)
	h.WaitWebhookDispatches()

	// The gate is closed for new work — and stays closed.
	if rr := fireRoutineWebhook(t, h, token, secret, `{"event":"other"}`, "evt-new-under-throttle"); rr.Code != 429 {
		t.Fatalf("new delivery under throttle status = %d, want 429; body=%s", rr.Code, rr.Body.String())
	}

	// Same id, same body: the receipt the sender already holds, no second run.
	dupRR := fireRoutineWebhook(t, h, token, secret, `{"event":"deploy"}`, "evt-throttled")
	if dupRR.Code != 202 {
		t.Fatalf("throttled redelivery status = %d, want 202 with the original receipt; body=%s", dupRR.Code, dupRR.Body.String())
	}
	var dup map[string]any
	_ = json.Unmarshal(dupRR.Body.Bytes(), &dup)
	if dup["deduped"] != true || dup["run_id"] != first["run_id"] || dup["delivery_id"] != first["delivery_id"] {
		t.Errorf("throttled redelivery = %v, want DEDUPED with run %v / delivery %v", dup, first["run_id"], first["delivery_id"])
	}

	// Same id, different body: a conflict, throttled or not.
	if rr := fireRoutineWebhook(t, h, token, secret, `{"event":"DIFFERENT"}`, "evt-throttled"); rr.Code != 409 {
		t.Errorf("throttled conflicting body status = %d, want 409; body=%s", rr.Code, rr.Body.String())
	}

	// A bad signature learns nothing from the receipt ledger, throttled or not.
	req := httptest.NewRequest("POST", "/api/v1/webhooks/"+token, strings.NewReader(`{"event":"deploy"}`))
	req.SetPathValue("token", token)
	req.Header.Set("X-Crewship-Signature", covPSWSign("wrong-secret", `{"event":"deploy"}`))
	req.Header.Set("Idempotency-Key", "evt-throttled")
	rr := httptest.NewRecorder()
	h.FireWebhook(rr, req)
	if rr.Code != 401 || strings.Contains(rr.Body.String(), first["run_id"].(string)) {
		t.Errorf("unauthenticated redelivery = %d %s, want 401 without the receipt", rr.Code, rr.Body.String())
	}

	var runs int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM routine_webhook_receipts`).Scan(&runs); err != nil {
		t.Fatalf("count receipts: %v", err)
	}
	if runs != 1 {
		t.Errorf("receipts = %d, want 1 — nothing above may have accepted a second delivery", runs)
	}

	// An unavailable ledger is 503, not a guessed duplicate and not a 429.
	if _, err := h.db.Exec(`ALTER TABLE routine_webhook_receipts RENAME TO routine_webhook_receipts_gone`); err != nil {
		t.Fatalf("take the ledger away: %v", err)
	}
	if rr := fireRoutineWebhook(t, h, token, secret, `{"event":"deploy"}`, "evt-throttled"); rr.Code != 503 {
		t.Errorf("throttled redelivery with the ledger unavailable status = %d, want 503; body=%s", rr.Code, rr.Body.String())
	}
}
