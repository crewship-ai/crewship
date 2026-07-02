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
