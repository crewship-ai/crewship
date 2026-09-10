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

// TestPipelineWebhooks_Fire_RecordsDeliveryAndWorkInOneCommit is W3.
//
// The reservation and the thing it reserved used to be two writes with a crash
// window between them: a row in pipeline_run_idempotency written synchronously,
// and the run created inside a goroutine after the 202 had gone out. A crash in
// that window left a reservation naming a run that never existed, and every
// redelivery was then answered "202 DEDUPED" pointing at the phantom.
func TestPipelineWebhooks_Fire_RecordsDeliveryAndWorkInOneCommit(t *testing.T) {
	h, token, secret, wsID := routineAcceptanceRig(t)

	rr := fireRoutineWebhook(t, h, token, secret, `{"event":"deploy"}`, "evt-accept-1")
	if rr.Code != 202 {
		t.Fatalf("status = %d, want 202; body=%s", rr.Code, rr.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	deliveryID, _ := got["delivery_id"].(string)
	workID, _ := got["work_id"].(string)
	runID, _ := got["run_id"].(string)
	if deliveryID == "" || workID == "" || runID == "" {
		t.Fatalf("202 must name the delivery, the work and the run: %v", got)
	}
	h.WaitWebhookDispatches()

	db := h.db
	var linked, domainKind, domainID, state string
	if err := db.QueryRow(`
		SELECT d.work_id, w.domain_kind, w.domain_id, w.state
		  FROM webhook_deliveries d JOIN work_items w ON w.id = d.work_id
		 WHERE d.id = ? AND d.workspace_id = ?`, deliveryID, wsID).
		Scan(&linked, &domainKind, &domainID, &state); err != nil {
		t.Fatalf("the 202 named rows that are not both there: %v", err)
	}
	if linked != workID {
		t.Errorf("delivery.work_id = %q, want %q", linked, workID)
	}
	if domainKind != "pipeline_run" || domainID != runID {
		t.Errorf("work domain = (%q, %q), want (pipeline_run, %q) — the record must "+
			"name the run the sender was handed", domainKind, domainID, runID)
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
	if work != 1 {
		t.Errorf("work items = %d, want 1 — a redelivery must not create a second piece of work", work)
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
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM webhook_deliveries`).Scan(&n); err != nil {
		t.Fatalf("count deliveries: %v", err)
	}
	if n != 1 {
		t.Errorf("delivery rows = %d, want 1 — the original record must be kept", n)
	}
}
