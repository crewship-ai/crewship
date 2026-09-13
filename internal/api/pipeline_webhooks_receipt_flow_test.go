package api

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/pipeline"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

// Whole-flow checks over the routine receipt contract, through the production
// FireWebhook handler and real SQLite: the dedup window, its expiry by the
// real sweeper, receipts that predate the retention columns, and both
// supported signature shapes.

func fireAccepted(t *testing.T, h *PipelineHandler, token, secret, body, key string) map[string]any {
	t.Helper()
	rr := fireRoutineWebhook(t, h, token, secret, body, key)
	if rr.Code != 202 {
		t.Fatalf("status = %d, want 202; body=%s", rr.Code, rr.Body.String())
	}
	var got map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &got)
	return got
}

// Accept → redelivery inside the window is the same receipt and run → the
// window closes and the REAL sweeper removes the receipt → the same identity
// is accepted again as new work, exactly as the documentation says.
func TestPipelineWebhooks_ReceiptExpiryMakesTheSameIdentityNewWork(t *testing.T) {
	h, token, secret, _ := routineAcceptanceRig(t)
	body := `{"event":"deploy"}`

	first := fireAccepted(t, h, token, secret, body, "evt-expire")
	h.WaitWebhookDispatches()
	inside := fireAccepted(t, h, token, secret, body, "evt-expire")
	if inside["deduped"] != true || inside["run_id"] != first["run_id"] || inside["delivery_id"] != first["delivery_id"] {
		t.Fatalf("redelivery inside the window = %v, want the original receipt %v", inside, first)
	}

	// Thirty-one days later the sweeper runs. This is the production sweep,
	// with a clock, not a DELETE typed into the test.
	later := time.Now().Add(31 * 24 * time.Hour)
	if n, err := pipeline.SweepRoutineWebhookReceipts(context.Background(), h.db, later); err != nil || n != 1 {
		t.Fatalf("sweep = %d, %v; want the one expired receipt removed", n, err)
	}
	if n := countWebhookRows(t, h.db, `SELECT COUNT(*) FROM routine_webhook_receipts`); n != 0 {
		t.Fatalf("%d receipts left after the window closed", n)
	}
	// The executor keeps its own 24-hour idempotency key; by day 31 it is
	// long gone too. Age it the same way rather than leaving the test to
	// exercise a window the contract does not describe.
	if _, err := h.db.Exec(`UPDATE pipeline_run_idempotency SET expires_at = ?`, tsformat.Format(time.Now().Add(-time.Hour))); err != nil {
		t.Fatalf("age the executor's idempotency key: %v", err)
	}

	again := fireAccepted(t, h, token, secret, body, "evt-expire")
	h.WaitWebhookDispatches()
	if again["deduped"] != false || again["run_id"] == first["run_id"] || again["delivery_id"] == first["delivery_id"] {
		t.Fatalf("redelivery after the window = %v, want new work with a new run, not %v", again, first)
	}
	if n := countWebhookRows(t, h.db, `SELECT COUNT(*) FROM routine_webhook_receipts WHERE run_id = ?`, again["run_id"]); n != 1 {
		t.Fatalf("the new acceptance left %d receipts for its run, want 1", n)
	}
}

// A receipt written before the retention columns existed is, after the
// migration, dated from the migration and still deduplicates: a redelivery of
// its identity answers the ORIGINAL run, and a changed body is a conflict.
func TestPipelineWebhooks_MigratedLegacyReceiptStillDeduplicates(t *testing.T) {
	h, token, secret, wsID := routineAcceptanceRig(t)
	body := `{"event":"legacy"}`
	sum := sha256.Sum256([]byte(body))
	var whID string
	if err := h.db.QueryRow(`SELECT id FROM pipeline_webhooks WHERE workspace_id = ?`, wsID).Scan(&whID); err != nil {
		t.Fatal(err)
	}
	// The shape the retention migration leaves a legacy row in: dated from
	// the migration, full window, no body size or profile recorded.
	now := time.Now().UTC()
	if _, err := h.db.Exec(`INSERT INTO routine_webhook_receipts
		(id, workspace_id, endpoint_id, source_delivery_id, body_sha256, run_id, received_at, dedup_expires_at, body_bytes, profile)
		VALUES ('rcpt-legacy', ?, ?, 'evt-legacy', ?, 'run-legacy', ?, ?, 0, '')`,
		wsID, whID, hex.EncodeToString(sum[:]), tsformat.Format(now), tsformat.Format(pipeline.RoutineReceiptDedupExpiry(now))); err != nil {
		t.Fatal(err)
	}

	got := fireAccepted(t, h, token, secret, body, "evt-legacy")
	if got["deduped"] != true || got["run_id"] != "run-legacy" || got["delivery_id"] != "rcpt-legacy" {
		t.Fatalf("redelivery of a migrated receipt = %v, want DEDUPED to run-legacy", got)
	}
	if rr := fireRoutineWebhook(t, h, token, secret, `{"event":"changed"}`, "evt-legacy"); rr.Code != 409 {
		t.Fatalf("changed body against a migrated receipt = %d, want 409", rr.Code)
	}
	h.WaitWebhookDispatches()
	if n := countWebhookRows(t, h.db, `SELECT COUNT(*) FROM routine_webhook_receipts`); n != 1 {
		t.Fatalf("%d receipts, want only the migrated one", n)
	}
}

// Both legacy signature shapes are recorded and deduplicate. A timestamped
// redelivery carries a fresh timestamp and therefore a fresh signature; its
// identity is the sender's key, not the signature, so it is still a
// duplicate.
func TestPipelineWebhooks_BothSignatureProfilesRecordAndDeduplicate(t *testing.T) {
	h, token, secret, _ := routineAcceptanceRig(t)
	body := `{"event":"signed"}`

	fireTimestamped := func(key string) *httptest.ResponseRecorder {
		t.Helper()
		ts := strconv.FormatInt(time.Now().Unix(), 10)
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write([]byte(ts + "."))
		mac.Write([]byte(body))
		req := httptest.NewRequest("POST", "/api/v1/webhooks/"+token, strings.NewReader(body))
		req.SetPathValue("token", token)
		req.Header.Set("X-Crewship-Signature", hex.EncodeToString(mac.Sum(nil)))
		req.Header.Set("X-Crewship-Timestamp", ts)
		req.Header.Set("Idempotency-Key", key)
		rr := httptest.NewRecorder()
		h.FireWebhook(rr, req)
		return rr
	}

	plain := fireAccepted(t, h, token, secret, body, "evt-plain")
	tsRR := fireTimestamped("evt-ts")
	if tsRR.Code != 202 {
		t.Fatalf("timestamped fire = %d: %s", tsRR.Code, tsRR.Body.String())
	}
	var stamped map[string]any
	_ = json.Unmarshal(tsRR.Body.Bytes(), &stamped)
	h.WaitWebhookDispatches()

	var plainProfile, tsProfile string
	if err := h.db.QueryRow(`SELECT profile FROM routine_webhook_receipts WHERE id = ?`, plain["delivery_id"]).Scan(&plainProfile); err != nil {
		t.Fatal(err)
	}
	if err := h.db.QueryRow(`SELECT profile FROM routine_webhook_receipts WHERE id = ?`, stamped["delivery_id"]).Scan(&tsProfile); err != nil {
		t.Fatal(err)
	}
	if plainProfile != "legacy-routine-hmac" || tsProfile != "legacy-routine-ts-hmac" {
		t.Fatalf("profiles = %q / %q, want legacy-routine-hmac / legacy-routine-ts-hmac", plainProfile, tsProfile)
	}

	// A second timestamped delivery: new timestamp, new signature, same key.
	time.Sleep(1100 * time.Millisecond)
	again := fireTimestamped("evt-ts")
	if again.Code != 202 {
		t.Fatalf("timestamped redelivery = %d: %s", again.Code, again.Body.String())
	}
	var dup map[string]any
	_ = json.Unmarshal(again.Body.Bytes(), &dup)
	if dup["deduped"] != true || dup["run_id"] != stamped["run_id"] {
		t.Fatalf("timestamped redelivery = %v, want DEDUPED to %v", dup, stamped["run_id"])
	}
	if n := countWebhookRows(t, h.db, `SELECT COUNT(*) FROM routine_webhook_receipts`); n != 2 {
		t.Fatalf("%d receipts, want 2 (one per identity)", n)
	}
}
