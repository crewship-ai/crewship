package api

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/pipeline"
	"github.com/crewship-ai/crewship/internal/tsformat"
)

// The receipt a routine delivery leaves behind is readable: identity, body
// fingerprint, the real pipeline run, and the window in which a redelivery is
// deduplicated. It is fenced to the workspace and pages like every other
// ledger read.
func TestRoutineReceipts_ListGetFenceAndPaginate(t *testing.T) {
	h, token, secret, wsID := routineAcceptanceRig(t)
	body := `{"event":"deploy","n":1}`
	rr := fireRoutineWebhook(t, h, token, secret, body, "evt-rcpt-1")
	if rr.Code != 202 {
		t.Fatalf("fire status = %d, want 202; body=%s", rr.Code, rr.Body.String())
	}
	var accepted map[string]any
	decodeJSON(t, rr, &accepted)
	h.WaitWebhookDispatches()
	runID, _ := accepted["run_id"].(string)
	receiptID, _ := accepted["delivery_id"].(string)
	if runID == "" || receiptID == "" {
		t.Fatalf("202 names no run/receipt: %v", accepted)
	}

	receipts := NewRoutineReceiptsHandler(h.db, quietLogger())
	list := func(ws, query string) (routineReceiptPage, *httptest.ResponseRecorder) {
		t.Helper()
		req := workReq(t, "GET", "/routine-webhook-receipts"+query, "", "reader", ws, "MEMBER")
		rec := httptest.NewRecorder()
		receipts.List(rec, req)
		var page routineReceiptPage
		if rec.Code == http.StatusOK {
			decodeJSON(t, rec, &page)
		}
		return page, rec
	}
	get := func(ws, id string) (routineReceiptView, *httptest.ResponseRecorder) {
		t.Helper()
		req := workReq(t, "GET", "/routine-webhook-receipts/"+id, "", "reader", ws, "MEMBER")
		req.SetPathValue("receiptId", id)
		rec := httptest.NewRecorder()
		receipts.Get(rec, req)
		var v routineReceiptView
		if rec.Code == http.StatusOK {
			decodeJSON(t, rec, &v)
		}
		return v, rec
	}

	// The engine's own run row decides run_status; without one it is null,
	// never an empty string that reads like a status.
	if _, err := h.db.Exec(`DELETE FROM pipeline_runs WHERE id = ?`, runID); err != nil {
		t.Fatal(err)
	}
	page, rec := list(wsID, "")
	if rec.Code != 200 || len(page.Items) != 1 || page.NextCursor != nil {
		t.Fatalf("list = %d %s", rec.Code, rec.Body.String())
	}
	got := page.Items[0]
	sum := sha256.Sum256([]byte(body))
	received, err := time.Parse(time.RFC3339Nano, got.ReceivedAt)
	if err != nil {
		t.Fatalf("received_at %q: %v", got.ReceivedAt, err)
	}
	expires, err := time.Parse(time.RFC3339Nano, got.DedupExpiresAt)
	if err != nil {
		t.Fatalf("dedup_expires_at %q: %v", got.DedupExpiresAt, err)
	}
	switch {
	case got.ID != receiptID:
		t.Errorf("id = %q, want the 202's delivery_id %q", got.ID, receiptID)
	case got.WorkspaceID != wsID, got.WebhookID == "":
		t.Errorf("identity = %+v, want workspace %s and a webhook id", got, wsID)
	case got.SourceDeliveryID != "evt-rcpt-1":
		t.Errorf("source_delivery_id = %q", got.SourceDeliveryID)
	case got.RunID != runID:
		t.Errorf("run_id = %q, want %q", got.RunID, runID)
	case got.RunStatus != nil:
		t.Errorf("run_status = %v, want null with no pipeline_runs row", *got.RunStatus)
	case got.BodySHA256 != hex.EncodeToString(sum[:]), got.BodyBytes != int64(len(body)):
		t.Errorf("body = %s/%d, want %s/%d", got.BodySHA256, got.BodyBytes, hex.EncodeToString(sum[:]), len(body))
	case got.Profile != "legacy-routine-hmac":
		t.Errorf("profile = %q", got.Profile)
	case expires.Sub(received) != pipeline.RoutineReceiptDedupWindow:
		t.Errorf("dedup window = %s, want %s", expires.Sub(received), pipeline.RoutineReceiptDedupWindow)
	case time.Since(received) > time.Minute:
		t.Errorf("received_at %s is not now", got.ReceivedAt)
	}

	// With a run row, its status comes through — the real run, not a work id.
	if _, err := h.db.Exec(`INSERT INTO pipeline_runs (id, workspace_id, pipeline_id, pipeline_slug, status, started_at, triggered_via)
		VALUES (?, ?, 'pln_accept', 'accept-target', 'failed', ?, 'webhook')`, runID, wsID, tsformat.Format(time.Now().UTC())); err != nil {
		t.Fatal(err)
	}
	detail, rec := get(wsID, receiptID)
	if rec.Code != 200 || detail.RunStatus == nil || *detail.RunStatus != "failed" {
		t.Fatalf("get = %d %s, want run_status failed", rec.Code, rec.Body.String())
	}

	// The identity question: both halves, or a 400.
	if page, rec := list(wsID, "?webhook_id="+got.WebhookID+"&source_delivery_id=evt-rcpt-1"); rec.Code != 200 || len(page.Items) != 1 {
		t.Errorf("identity lookup = %d %s", rec.Code, rec.Body.String())
	}
	if page, rec := list(wsID, "?webhook_id="+got.WebhookID+"&source_delivery_id=never-sent"); rec.Code != 200 || len(page.Items) != 0 {
		t.Errorf("unknown identity = %d %s, want an empty page", rec.Code, rec.Body.String())
	}
	if _, rec := list(wsID, "?source_delivery_id=evt-rcpt-1"); rec.Code != http.StatusBadRequest {
		t.Errorf("source id alone = %d, want 400", rec.Code)
	}

	// Another workspace sees nothing: absent, not forbidden.
	if page, rec := list("ws-other", ""); rec.Code != 200 || len(page.Items) != 0 {
		t.Errorf("cross-workspace list = %d %s", rec.Code, rec.Body.String())
	}
	if _, rec := get("ws-other", receiptID); rec.Code != http.StatusNotFound {
		t.Errorf("cross-workspace get = %d, want 404", rec.Code)
	}
	if _, rec := get(wsID, "no-such-receipt"); rec.Code != http.StatusNotFound {
		t.Errorf("unknown receipt = %d, want 404", rec.Code)
	}

	// Keyset pagination at 100. Ids sort after the real receipt so the page
	// boundary falls inside the seeded rows.
	now := tsformat.Format(time.Now().UTC())
	for i := 0; i < 101; i++ {
		if _, err := h.db.Exec(`INSERT INTO routine_webhook_receipts
			(id, workspace_id, endpoint_id, source_delivery_id, body_sha256, run_id, received_at, dedup_expires_at, body_bytes, profile)
			VALUES (?, ?, 'pwh-page', ?, 'sha', ?, ?, ?, 1, 'legacy-routine-hmac')`,
			fmt.Sprintf("zz-page-%03d", i), wsID, fmt.Sprintf("src-%03d", i), fmt.Sprintf("run-%03d", i), now, now); err != nil {
			t.Fatal(err)
		}
	}
	first, rec := list(wsID, "")
	if rec.Code != 200 || len(first.Items) != 100 || first.NextCursor == nil {
		t.Fatalf("page 1 = %d items, cursor %v", len(first.Items), first.NextCursor)
	}
	second, rec := list(wsID, "?after="+*first.NextCursor)
	if rec.Code != 200 || len(second.Items) != 2 || second.NextCursor != nil {
		t.Fatalf("page 2 = %d items, cursor %v; want the remaining 2", len(second.Items), second.NextCursor)
	}
	if second.Items[0].ID <= *first.NextCursor {
		t.Errorf("page 2 starts at %q, not after cursor %q", second.Items[0].ID, *first.NextCursor)
	}
}

// The receipt exists from the 202; the run record only once the executor
// starts. Read in that window the receipt names its run with a null status —
// no reason attached, because the absence has none — and read again once the
// run exists it carries the run's real status.
func TestRoutineReceipts_RunStatusIsNullBeforeTheRunRecordAndRealAfter(t *testing.T) {
	h, token, secret, wsID := routineAcceptanceRig(t)
	barrier := make(chan struct{})
	h.webhookDispatchBarrier = barrier

	rr := fireRoutineWebhook(t, h, token, secret, `{"event":"deploy"}`, "evt-rcpt-pending")
	if rr.Code != 202 {
		t.Fatalf("fire status = %d, want 202; body=%s", rr.Code, rr.Body.String())
	}
	var accepted map[string]any
	decodeJSON(t, rr, &accepted)
	receiptID, _ := accepted["delivery_id"].(string)
	runID, _ := accepted["run_id"].(string)

	receipts := NewRoutineReceiptsHandler(h.db, quietLogger())
	read := func() routineReceiptView {
		t.Helper()
		req := workReq(t, "GET", "/routine-webhook-receipts/"+receiptID, "", "reader", wsID, "MEMBER")
		req.SetPathValue("receiptId", receiptID)
		rec := httptest.NewRecorder()
		receipts.Get(rec, req)
		if rec.Code != 200 {
			t.Fatalf("get = %d %s", rec.Code, rec.Body.String())
		}
		var v routineReceiptView
		decodeJSON(t, rec, &v)
		return v
	}

	// Execution is held before the executor is entered: the receipt is
	// readable, the run is named, and there is no record to report on.
	before := read()
	if before.RunID != runID {
		t.Fatalf("run_id = %q, want %q", before.RunID, runID)
	}
	if before.RunStatus != nil {
		t.Fatalf("run_status = %q before the run record exists, want null", *before.RunStatus)
	}
	if n := countWebhookRows(t, h.db, `SELECT COUNT(*) FROM pipeline_runs WHERE id = '`+runID+`'`); n != 0 {
		t.Fatalf("the barrier did not hold: %d run records exist", n)
	}

	// Release the run. The erroring runner fails it; the record now exists
	// and the receipt reports its real status rather than a guess.
	close(barrier)
	h.WaitWebhookDispatches()
	after := read()
	if after.RunStatus == nil {
		t.Fatal("run_status still null after the run record was written")
	}
	var recorded string
	if err := h.db.QueryRow(`SELECT status FROM pipeline_runs WHERE id = ?`, runID).Scan(&recorded); err != nil {
		t.Fatalf("read the run record: %v", err)
	}
	if *after.RunStatus != recorded {
		t.Fatalf("run_status = %q, want the record's %q", *after.RunStatus, recorded)
	}
}
