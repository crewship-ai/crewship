package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

// An immediate start that finds its concurrency slot full is accepted into the
// deferred queue (#3025) instead of being refused with 429: the same receipt,
// retry and TTL contract as any deferred start (#3015).

const queueIfBusyDSL = `{"name":"serial-job","concurrency_key":"serial","steps":[{"id":"a","type":"agent_run","agent_slug":"agent_lead","prompt":"hi"}]}`

func queueIfBusyRig(t *testing.T) (*PipelineHandler, *sql.DB, string, string, func()) {
	t.Helper()
	h, db, user, ws := runsHandlerRig(t)
	h.SetRunner(newBlockingRunner())
	h.SetRunStore(pipeline.NewRunStore(db))
	reg := pipeline.NewRunRegistry()
	h.SetRunRegistry(reg)
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := db.Exec(`
		INSERT INTO pipelines (id, workspace_id, slug, name, definition_json, definition_hash, head_version, created_at, updated_at, last_test_run_at)
		VALUES ('serial_pipeline', ?, 'serial-job', 'serial-job', ?, 'hq', 1, ?, ?, ?)`, ws, queueIfBusyDSL, now, now, now); err != nil {
		t.Fatalf("seed pipeline: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO pipeline_versions (id, pipeline_id, version, definition_json, definition_hash, author_type, author_id, created_at)
		VALUES ('plnv_serial_v1', 'serial_pipeline', 1, ?, 'hq', 'user', 'u_test', ?)`, queueIfBusyDSL, now); err != nil {
		t.Fatalf("seed version: %v", err)
	}
	// Another run holds the routine's only slot.
	_, release, err := reg.Acquire(context.Background(), pipeline.AcquireOpts{
		RunID: "holder", WorkspaceID: ws, PipelineID: "serial_pipeline", PipelineSlug: "serial-job", ConcurrencyKey: "serial",
	})
	if err != nil {
		t.Fatalf("hold slot: %v", err)
	}
	return h, db, user, ws, release
}

func postQueueIfBusyRun(t *testing.T, h *PipelineHandler, user, ws, body, key string) *httptest.ResponseRecorder {
	t.Helper()
	req := withWorkspaceUser(httptest.NewRequest("POST", "/run", strings.NewReader(body)), user, ws, "OWNER")
	req.SetPathValue("slug", "serial-job")
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	rr := httptest.NewRecorder()
	h.Run(rr, req)
	return rr
}

func pendingCount(t *testing.T, db *sql.DB, ws string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pending_runs WHERE workspace_id=?`, ws).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestRunQueuesWhenConcurrencySlotIsFull(t *testing.T) {
	h, db, user, ws, release := queueIfBusyRig(t)
	defer release()

	rr := postQueueIfBusyRun(t, h, user, ws, `{"inputs":{}}`, "")
	if rr.Code != 202 {
		t.Fatalf("status %d, want 202 queued: %s", rr.Code, rr.Body)
	}
	var receipt struct {
		Status        string `json:"status"`
		PendingID     string `json:"pending_id"`
		Queued        bool   `json:"queued"`
		Reason        string `json:"reason"`
		PinnedVersion *int   `json:"pinned_version"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &receipt); err != nil {
		t.Fatal(err)
	}
	if receipt.Status != "SCHEDULED" || receipt.PendingID == "" || !receipt.Queued || receipt.Reason != "concurrency_limit" {
		t.Fatalf("receipt does not say it was queued for capacity: %s", rr.Body)
	}
	if receipt.PinnedVersion == nil || *receipt.PinnedVersion != 1 {
		t.Fatalf("queued start not pinned to the published version: %s", rr.Body)
	}
	pr, err := pipeline.NewPendingRunStore(db).Get(t.Context(), ws, receipt.PendingID)
	if err != nil || pr == nil {
		t.Fatalf("no pending row for receipt: %v", err)
	}
	if pr.Status != "pending" || pr.ExpiresAt == nil {
		t.Fatalf("queued start has no bounded wait: %+v", pr)
	}
	if wait := time.Until(*pr.ExpiresAt); wait < 55*time.Minute || wait > 61*time.Minute {
		t.Fatalf("default queue wait %v, want about one hour", wait)
	}
}

func TestRunQueueKeepsCallerTTL(t *testing.T) {
	h, db, user, ws, release := queueIfBusyRig(t)
	defer release()
	rr := postQueueIfBusyRun(t, h, user, ws, `{"inputs":{},"ttl_seconds":120}`, "")
	if rr.Code != 202 {
		t.Fatalf("status %d: %s", rr.Code, rr.Body)
	}
	var receipt struct {
		PendingID string `json:"pending_id"`
	}
	_ = json.Unmarshal(rr.Body.Bytes(), &receipt)
	pr, err := pipeline.NewPendingRunStore(db).Get(t.Context(), ws, receipt.PendingID)
	if err != nil || pr == nil || pr.ExpiresAt == nil {
		t.Fatalf("pending row: %+v %v", pr, err)
	}
	if wait := time.Until(*pr.ExpiresAt); wait > 2*time.Minute+5*time.Second || wait < time.Minute {
		t.Fatalf("caller ttl_seconds=120 not kept: %v", wait)
	}
}

// A client that retries after an uncertain response must find the start it
// already queued, not queue (or run) a second one.
func TestRunQueueRetryWithSameKeyReturnsSameStart(t *testing.T) {
	h, db, user, ws, release := queueIfBusyRig(t)
	defer release()
	first := postQueueIfBusyRun(t, h, user, ws, `{"inputs":{}}`, "click-1")
	second := postQueueIfBusyRun(t, h, user, ws, `{"inputs":{}}`, "click-1")
	if first.Code != 202 || second.Code != 202 {
		t.Fatalf("statuses %d/%d: %s / %s", first.Code, second.Code, first.Body, second.Body)
	}
	var a, b struct {
		PendingID string `json:"pending_id"`
	}
	_ = json.Unmarshal(first.Body.Bytes(), &a)
	_ = json.Unmarshal(second.Body.Bytes(), &b)
	if a.PendingID == "" || a.PendingID != b.PendingID {
		t.Fatalf("retry queued a second start: %q vs %q", a.PendingID, b.PendingID)
	}
	if n := pendingCount(t, db, ws); n != 1 {
		t.Fatalf("%d pending rows, want 1", n)
	}
	// Even once the slot frees, the same key still answers with the queued
	// start instead of executing a second run.
	release()
	third := postQueueIfBusyRun(t, h, user, ws, `{"inputs":{}}`, "click-1")
	var c struct {
		PendingID string `json:"pending_id"`
	}
	_ = json.Unmarshal(third.Body.Bytes(), &c)
	if third.Code != 202 || c.PendingID != a.PendingID {
		t.Fatalf("retry after the slot freed did not return the queued start: %d %s", third.Code, third.Body)
	}
}

func TestRunRejectIfBusyKeeps429(t *testing.T) {
	h, db, user, ws, release := queueIfBusyRig(t)
	defer release()
	rr := postQueueIfBusyRun(t, h, user, ws, `{"inputs":{},"queue_if_busy":false}`, "")
	if rr.Code != 429 || rr.Header().Get("Retry-After") == "" {
		t.Fatalf("status %d (Retry-After %q), want 429: %s", rr.Code, rr.Header().Get("Retry-After"), rr.Body)
	}
	if n := pendingCount(t, db, ws); n != 0 {
		t.Fatalf("a refused start left %d pending rows", n)
	}
}
