package api

// pipeline_webhooks_async_test.go — the async dispatch contract for
// FireWebhook (public POST /api/v1/webhooks/{token}).
//
// Real webhook senders (GitHub, Stripe) time out deliveries after
// 5–10s, while a routine with agent_run steps can run for minutes.
// The handler must therefore:
//
//   1. answer 202 + {run_id, status} as soon as the request is
//      verified (signature, rate limit, governance) — BEFORE the run
//      completes;
//   2. run the routine on a context derived from the server
//      lifecycle, NOT the request — a sender hanging up must not
//      cancel an in-flight run server-side (wasted tokens);
//   3. keep the signature gate fully synchronous — nothing is
//      dispatched for an unsigned/badly-signed delivery.
//
// The first two tests were written RED against the previous
// synchronous handler (exec.Run(r.Context(), …) inline, response
// only after completion) and pin the async contract.

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

// seedAgentRunPipeline inserts a pipelines row whose definition has a
// real agent_run step, so a fire actually exercises the AgentRunner
// (unlike seedWebhookPipeline's empty step list).
func seedAgentRunPipeline(t *testing.T, db *sql.DB, wsID, id, slug string) {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339)
	def := `{"name":"` + slug + `","steps":[{"id":"a","type":"agent_run","agent_slug":"agent_lead","prompt":"hi"}]}`
	if _, err := db.Exec(`
		INSERT INTO pipelines (id, workspace_id, slug, name, definition_json, definition_hash, created_at, updated_at, last_test_run_at)
		VALUES (?, ?, ?, ?, ?, 'hash', ?, ?, ?)`,
		id, wsID, slug, slug, def, now, now, now); err != nil {
		t.Fatalf("seed pipeline: %v", err)
	}
}

// blockingRunner is an AgentRunner that parks inside RunStep until
// release is closed, recording the ctx state it observed on the way
// out. Lets tests hold a webhook-triggered run "in flight" and probe
// what the HTTP handler does meanwhile.
type blockingRunner struct {
	started   chan struct{} // closed when RunStep is first entered
	release   chan struct{} // close to let RunStep finish
	startOnce sync.Once

	mu     sync.Mutex
	calls  int
	ctxErr error // ctx.Err() observed when RunStep returned
}

func newBlockingRunner() *blockingRunner {
	return &blockingRunner{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (b *blockingRunner) RunStep(ctx context.Context, _ pipeline.AgentStepRequest) (pipeline.AgentStepResult, error) {
	b.mu.Lock()
	b.calls++
	b.mu.Unlock()
	b.startOnce.Do(func() { close(b.started) })
	select {
	case <-b.release:
		b.mu.Lock()
		b.ctxErr = ctx.Err()
		b.mu.Unlock()
		if err := ctx.Err(); err != nil {
			return pipeline.AgentStepResult{}, err
		}
		return pipeline.AgentStepResult{Output: "done", CostUSD: 0.001, DurationMs: 1}, nil
	case <-ctx.Done():
		b.mu.Lock()
		b.ctxErr = ctx.Err()
		b.mu.Unlock()
		return pipeline.AgentStepResult{}, ctx.Err()
	}
}

func (b *blockingRunner) observedCtxErr() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.ctxErr
}

func (b *blockingRunner) callCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.calls
}

// waitForWebhookFire polls the webhook row until RecordFire has
// stamped a last_status, then returns (last_run_id, last_status).
// Fails the test after the deadline — a terminal record must always
// land, however the run ends.
func waitForWebhookFire(t *testing.T, db *sql.DB, webhookID string, deadline time.Duration) (string, string) {
	t.Helper()
	stop := time.Now().Add(deadline)
	for {
		var runID, status sql.NullString
		err := db.QueryRow(
			`SELECT last_run_id, last_status FROM pipeline_webhooks WHERE id = ?`, webhookID,
		).Scan(&runID, &status)
		if err == nil && status.String != "" {
			return runID.String, status.String
		}
		if time.Now().After(stop) {
			t.Fatalf("webhook %s never recorded a fire outcome within %s (err=%v)", webhookID, deadline, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestPipelineWebhooks_Fire_RespondsBeforeRunCompletes pins contract
// (1): the 202 must arrive while the run is still in flight. RED on
// the synchronous handler: FireWebhook only wrote its response after
// exec.Run returned, so with the runner parked the response never
// arrived and the test tripped the timeout branch.
func TestPipelineWebhooks_Fire_RespondsBeforeRunCompletes(t *testing.T) {
	runner := newBlockingRunner()
	h, db, _, wsID := webhookHandlerRig(t)
	h.SetRunner(runner)
	seedAgentRunPipeline(t, db, wsID, "pln_async", "async-target")
	wh := seedWebhookRow(t, db, wsID, "pln_async", "async-secret", true)

	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(runner.release) }) }
	// Unblock the parked run before the test DB is torn down, whatever
	// path the test exits through.
	t.Cleanup(release)

	body := `{"hello":"async"}`
	req := httptest.NewRequest("POST", "/api/v1/webhooks/"+wh.Token, strings.NewReader(body))
	req.SetPathValue("token", wh.Token)
	req.Header.Set("X-Crewship-Signature", covPSWSign("async-secret", body))

	respCh := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rr := httptest.NewRecorder()
		h.FireWebhook(rr, req)
		respCh <- rr
	}()

	select {
	case <-runner.started:
	case <-time.After(2 * time.Second):
		t.Fatal("run never started — dispatch is broken well before the async contract")
	}

	var rr *httptest.ResponseRecorder
	select {
	case rr = <-respCh:
		// Async contract: response landed while RunStep is still parked.
	case <-time.After(1500 * time.Millisecond):
		t.Fatal("FireWebhook blocked until run completion — webhook dispatch is synchronous, real senders (GitHub/Stripe) would time out")
	}

	if rr.Code != 202 {
		t.Fatalf("status = %d, want 202; body=%s", rr.Code, rr.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	respRunID, _ := resp["run_id"].(string)
	if respRunID == "" {
		t.Fatalf("202 response missing run_id: %v", resp)
	}

	// Let the run finish and confirm it completes AFTER the response,
	// under the SAME run id the sender was handed (pollable handle).
	release()
	recordedRunID, recordedStatus := waitForWebhookFire(t, db, wh.ID, 3*time.Second)
	if recordedStatus != "COMPLETED" {
		t.Errorf("recorded last_status = %q, want COMPLETED", recordedStatus)
	}
	if recordedRunID != respRunID {
		t.Errorf("recorded last_run_id = %q, but the 202 handed the sender run_id %q — the polling handle must match the executed run", recordedRunID, respRunID)
	}
}

// TestPipelineWebhooks_Fire_SenderDisconnectDoesNotCancelRun pins
// contract (2): cancelling the request context (what net/http does
// when the sender hangs up) must NOT propagate into the in-flight
// run. RED on the synchronous handler: exec.Run received r.Context(),
// so the cancel reached RunStep's ctx and killed the run mid-flight.
func TestPipelineWebhooks_Fire_SenderDisconnectDoesNotCancelRun(t *testing.T) {
	runner := newBlockingRunner()
	h, db, _, wsID := webhookHandlerRig(t)
	h.SetRunner(runner)
	seedAgentRunPipeline(t, db, wsID, "pln_hangup", "hangup-target")
	wh := seedWebhookRow(t, db, wsID, "pln_hangup", "hangup-secret", true)

	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(runner.release) }) }
	t.Cleanup(release)

	reqCtx, cancelReq := context.WithCancel(context.Background())
	defer cancelReq()

	body := `{"hello":"hangup"}`
	req := httptest.NewRequest("POST", "/api/v1/webhooks/"+wh.Token, strings.NewReader(body))
	req = req.WithContext(reqCtx)
	req.SetPathValue("token", wh.Token)
	req.Header.Set("X-Crewship-Signature", covPSWSign("hangup-secret", body))

	respCh := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rr := httptest.NewRecorder()
		h.FireWebhook(rr, req)
		respCh <- rr
	}()

	select {
	case <-runner.started:
	case <-time.After(2 * time.Second):
		t.Fatal("run never started")
	}

	// Sender gives up / closes the connection while the run is parked.
	cancelReq()
	// Give a (wrong) request-ctx propagation a moment to reach RunStep
	// before releasing, so the failure mode is deterministic.
	time.Sleep(50 * time.Millisecond)
	release()

	_, recordedStatus := waitForWebhookFire(t, db, wh.ID, 3*time.Second)

	if err := runner.observedCtxErr(); err != nil {
		t.Fatalf("request-context cancellation propagated into the run (ctx.Err()=%v) — a sender disconnect cancels in-flight runs server-side, wasting spent tokens", err)
	}
	if recordedStatus != "COMPLETED" {
		t.Errorf("recorded last_status = %q, want COMPLETED — the run must survive the sender hanging up", recordedStatus)
	}

	// Drain the handler goroutine (don't leak it into other tests).
	select {
	case <-respCh:
	case <-time.After(2 * time.Second):
		t.Fatal("FireWebhook never returned")
	}
}

// TestPipelineWebhooks_Fire_BadSignature_NothingDispatched pins
// contract (3): async dispatch must not weaken the HMAC gate. A badly
// signed delivery gets its 401 synchronously and NO background
// goroutine — the runner is never invoked, no fire is recorded.
func TestPipelineWebhooks_Fire_BadSignature_NothingDispatched(t *testing.T) {
	runner := newBlockingRunner()
	h, db, _, wsID := webhookHandlerRig(t)
	h.SetRunner(runner)
	seedAgentRunPipeline(t, db, wsID, "pln_sig", "sig-target")
	wh := seedWebhookRow(t, db, wsID, "pln_sig", "sig-secret", true)

	body := `{"hello":"forged"}`
	req := httptest.NewRequest("POST", "/api/v1/webhooks/"+wh.Token, strings.NewReader(body))
	req.SetPathValue("token", wh.Token)
	req.Header.Set("X-Crewship-Signature", "deadbeef")
	rr := httptest.NewRecorder()
	h.FireWebhook(rr, req)

	if rr.Code != 401 {
		t.Fatalf("status = %d, want 401; body=%s", rr.Code, rr.Body.String())
	}
	// Returns immediately when nothing was spawned — and proves the
	// 401 path never reaches the dispatch WaitGroup.
	h.WaitWebhookDispatches()
	if n := runner.callCount(); n != 0 {
		t.Errorf("runner invoked %d times on a rejected signature, want 0", n)
	}
	var fireCount int64
	if err := db.QueryRow(`SELECT fire_count FROM pipeline_webhooks WHERE id = ?`, wh.ID).Scan(&fireCount); err != nil {
		t.Fatalf("read fire_count: %v", err)
	}
	if fireCount != 0 {
		t.Errorf("fire_count = %d after a rejected signature, want 0", fireCount)
	}
}

// TestPipelineWebhooks_Fire_Replay_DedupedToOriginalRun pins the
// replay contract for async dispatch: the idempotency reservation
// happens synchronously, so a redelivered event answers 202 with the
// ORIGINAL run's id (status DEDUPED) instead of minting a dangling
// handle — and the routine executes exactly once.
func TestPipelineWebhooks_Fire_Replay_DedupedToOriginalRun(t *testing.T) {
	runner := &stubRunner{output: "ok"}
	h, db, _, wsID := webhookHandlerRig(t)
	h.SetRunner(runner)
	seedAgentRunPipeline(t, db, wsID, "pln_replay", "replay-target")
	wh := seedWebhookRow(t, db, wsID, "pln_replay", "replay-secret", true)

	body := `{"event":"deploy"}`
	fire := func() map[string]any {
		req := httptest.NewRequest("POST", "/api/v1/webhooks/"+wh.Token, strings.NewReader(body))
		req.SetPathValue("token", wh.Token)
		req.Header.Set("X-Crewship-Signature", covPSWSign("replay-secret", body))
		req.Header.Set("Idempotency-Key", "evt-42")
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
	if first["status"] != "PENDING" {
		t.Errorf("first fire status = %v, want PENDING", first["status"])
	}
	firstRunID, _ := first["run_id"].(string)
	if firstRunID == "" {
		t.Fatal("first fire missing run_id")
	}
	h.WaitWebhookDispatches()

	second := fire()
	if second["status"] != "DEDUPED" || second["deduped"] != true {
		t.Errorf("replay = %v, want status DEDUPED + deduped true", second)
	}
	if second["run_id"] != firstRunID {
		t.Errorf("replay run_id = %v, want the original run's id %q", second["run_id"], firstRunID)
	}
	h.WaitWebhookDispatches()
	if runner.calls != 1 {
		t.Errorf("runner invoked %d times across a delivery + its replay, want exactly 1", runner.calls)
	}
}

// TestPipelineWebhooks_Fire_ApprovalWait_ParksWaiting pins that the
// async dispatch preserves the wait:approval parking semantics: the
// background run parks promptly (status WAITING, run row 'waiting',
// pending waitpoint), it does NOT hold the dispatch goroutine for the
// approval, and the webhook records WAITING under the run id the 202
// handed the sender.
func TestPipelineWebhooks_Fire_ApprovalWait_ParksWaiting(t *testing.T) {
	h, db, _, wsID := webhookHandlerRig(t)
	h.SetRunner(&stubRunner{output: "ok"})
	h.SetRunStore(pipeline.NewRunStore(db))
	wpStore := pipeline.NewSQLWaitpointStore(db)
	t.Cleanup(wpStore.Close)
	h.SetWaitpointStore(wpStore)

	def := `{"dsl_version":"1.0","name":"appr-hook","steps":[` +
		`{"id":"gate","type":"wait","wait":{"kind":"approval","approval_prompt":"ship it?"}},` +
		`{"id":"a","type":"agent_run","agent_slug":"agent_lead","prompt":"hi"}]}`
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := db.Exec(`
		INSERT INTO pipelines (id, workspace_id, slug, name, definition_json, definition_hash, created_at, updated_at, last_test_run_at)
		VALUES ('pln_appr', ?, 'appr-hook', 'appr-hook', ?, 'hash', ?, ?, ?)`,
		wsID, def, now, now, now); err != nil {
		t.Fatalf("seed pipeline: %v", err)
	}
	wh := seedWebhookRow(t, db, wsID, "pln_appr", "appr-secret", true)

	body := `{"pr":7}`
	req := httptest.NewRequest("POST", "/api/v1/webhooks/"+wh.Token, strings.NewReader(body))
	req.SetPathValue("token", wh.Token)
	req.Header.Set("X-Crewship-Signature", covPSWSign("appr-secret", body))
	rr := httptest.NewRecorder()
	h.FireWebhook(rr, req)
	if rr.Code != 202 {
		t.Fatalf("status = %d, want 202; body=%s", rr.Code, rr.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	respRunID, _ := resp["run_id"].(string)

	// The dispatch goroutine must return promptly (parked, not blocked
	// in WaitFor until the approval timeout).
	drained := make(chan struct{})
	go func() { h.WaitWebhookDispatches(); close(drained) }()
	select {
	case <-drained:
	case <-time.After(5 * time.Second):
		t.Fatal("dispatch goroutine still running — the approval wait blocks instead of parking")
	}

	recordedRunID, recordedStatus := waitForWebhookFire(t, db, wh.ID, 2*time.Second)
	if recordedStatus != "WAITING" {
		t.Errorf("recorded last_status = %q, want WAITING", recordedStatus)
	}
	if recordedRunID != respRunID {
		t.Errorf("recorded last_run_id = %q, want the 202's run_id %q", recordedRunID, respRunID)
	}

	// Run row parked, waitpoint pending — the approval flow can pick
	// it up exactly as with a synchronous trigger.
	var runStatus string
	if err := db.QueryRow(`SELECT status FROM pipeline_runs WHERE id = ?`, respRunID).Scan(&runStatus); err != nil {
		t.Fatalf("read run row: %v", err)
	}
	if runStatus != "waiting" {
		t.Errorf("pipeline_runs.status = %q, want waiting", runStatus)
	}
	var wpStatus string
	if err := db.QueryRow(`SELECT status FROM pipeline_waitpoints WHERE pipeline_run_id = ?`, respRunID).Scan(&wpStatus); err != nil {
		t.Fatalf("read waitpoint row: %v", err)
	}
	if wpStatus != "pending" {
		t.Errorf("waitpoint status = %q, want pending", wpStatus)
	}
}

// TestPipelineWebhooks_Fire_InactiveRoutine_409Synchronous pins that
// the governance gate stays a SYNCHRONOUS sender-visible contract
// under async dispatch: a 'proposed' routine answers 409 (policy
// block), and nothing is dispatched in the background.
func TestPipelineWebhooks_Fire_InactiveRoutine_409Synchronous(t *testing.T) {
	runner := newBlockingRunner()
	h, db, _, wsID := webhookHandlerRig(t)
	h.SetRunner(runner)
	seedAgentRunPipeline(t, db, wsID, "pln_gov", "gov-target")
	if _, err := db.Exec(`UPDATE pipelines SET status = 'proposed' WHERE id = 'pln_gov'`); err != nil {
		t.Fatalf("set status: %v", err)
	}
	wh := seedWebhookRow(t, db, wsID, "pln_gov", "gov-secret", true)

	body := `{"event":"x"}`
	req := httptest.NewRequest("POST", "/api/v1/webhooks/"+wh.Token, strings.NewReader(body))
	req.SetPathValue("token", wh.Token)
	req.Header.Set("X-Crewship-Signature", covPSWSign("gov-secret", body))
	rr := httptest.NewRecorder()
	h.FireWebhook(rr, req)

	if rr.Code != 409 {
		t.Fatalf("status = %d, want 409; body=%s", rr.Code, rr.Body.String())
	}
	h.WaitWebhookDispatches()
	if n := runner.callCount(); n != 0 {
		t.Errorf("runner invoked %d times for an inactive routine, want 0", n)
	}
}
