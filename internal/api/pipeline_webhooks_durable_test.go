package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

// Construct the executor after acceptance, as production does, so tests may
// configure their runner and registry after creating the shared handler rig.
type webhookQueuedTestExecutor struct{ handler *PipelineHandler }

func (e webhookQueuedTestExecutor) Run(ctx context.Context, in pipeline.RunInput) (*pipeline.RunResult, error) {
	if barrier := e.handler.webhookDispatchBarrier; barrier != nil {
		select {
		case <-barrier:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return e.handler.newExecutor().Run(ctx, in)
}

func startWebhookTestDispatcher(t *testing.T, h *PipelineHandler) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	dispatcher := pipeline.NewPendingRunDispatcher(pipeline.NewPendingRunStore(h.db), webhookQueuedTestExecutor{h}, h.logger)
	dispatcher.Start(ctx)
	t.Cleanup(func() { cancel(); dispatcher.Stop() })
}

// Wait for durable work and the dispatcher's webhook bookkeeping, including
// parked approval runs. Handler goroutines no longer own webhook execution.
func waitPendingWebhookDispatches(t *testing.T, h *PipelineHandler) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for {
		var active int
		err := h.db.QueryRow(`SELECT COUNT(*) FROM pending_runs p LEFT JOIN pipeline_runs r ON r.id=p.fired_run_id
   WHERE p.triggered_via='webhook' AND (p.status='pending' OR (p.status='fired' AND (COALESCE(p.fired_run_id,'')='' OR r.status IN ('queued','running'))))`).Scan(&active)
		var missing int
		if err == nil {
			err = h.db.QueryRow(`SELECT COUNT(*) FROM pipeline_webhooks w WHERE w.fire_count < (SELECT COUNT(*) FROM pending_runs p WHERE p.triggered_via='webhook' AND p.triggered_by_id=w.id AND p.status='fired')`).Scan(&missing)
		}
		if err != nil {
			t.Fatal(err)
		}
		if active == 0 && missing == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("webhook queue did not drain: active=%d missing_outcomes=%d", active, missing)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestPipelineWebhooks_DurableReceiptAndQueueCommitBefore202(t *testing.T) {
	ensureEncryptionKey(t)
	h, db, _, wsID := webhookHandlerRigWithDispatcher(t, false)
	h.SetRunner(&stubRunner{output: "ok"})
	seedWebhookPipeline(t, db, wsID, "durable-target", "durable-target")
	wh := seedWebhookRow(t, db, wsID, "durable-target", "durable-secret", true)
	pinnedDefinition := `{"name":"durable-target","steps":[{"id":"pinned","type":"transform","transform":{"input":"pinned-version","expression":"."}}]}`
	if _, err := db.Exec(`INSERT INTO pipeline_versions(id,pipeline_id,version,definition_json,definition_hash,author_type,author_id) VALUES('durable-version',?,1,?,'pinned-hash','system','fixture')`, wh.TargetPipelineID, pinnedDefinition); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE pipeline_webhooks SET target_pipeline_version=1 WHERE id=?`, wh.ID); err != nil {
		t.Fatal(err)
	}

	rr := fireRoutineWebhook(t, h, wh.Token, "durable-secret", `{"event":"accepted"}`, "durable-delivery")
	if rr.Code != 202 {
		t.Fatalf("accept: %d %s", rr.Code, rr.Body.String())
	}
	var response struct {
		RunID      string `json:"run_id"`
		DeliveryID string `json:"delivery_id"`
		PendingID  string `json:"pending_id"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	var planned, receiptRun, origin, source, status string
	var pin int
	var fired sql.NullString
	err := db.QueryRow(`SELECT p.run_id,d.run_id,p.triggered_via,p.triggered_by_id,p.status,p.fired_run_id,p.pinned_version FROM routine_webhook_receipts d JOIN pending_runs p ON p.run_id=d.run_id WHERE d.id=?`, response.DeliveryID).Scan(&planned, &receiptRun, &origin, &source, &status, &fired, &pin)
	if err != nil {
		t.Fatal(err)
	}
	if response.PendingID != response.DeliveryID || response.PendingID == "" || response.RunID == "" || planned != response.RunID || receiptRun != planned || origin != "webhook" || source != wh.ID || status != "pending" || fired.String != "" || pin != 1 {
		t.Fatalf("invalid durable acceptance: response=%+v planned=%s receipt=%s origin=%s source=%s status=%s fired=%v", response, planned, receiptRun, origin, source, status, fired)
	}
	var runs int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pipeline_runs WHERE id=?`, planned).Scan(&runs); err != nil || runs != 0 {
		t.Fatalf("run started without dispatcher: count=%d err=%v", runs, err)
	}
	duplicate := fireRoutineWebhook(t, h, wh.Token, "durable-secret", `{"event":"accepted"}`, "durable-delivery")
	var repeated map[string]any
	_ = json.Unmarshal(duplicate.Body.Bytes(), &repeated)
	if duplicate.Code != 202 || repeated["run_id"] != planned || repeated["pending_id"] != response.PendingID || repeated["deduped"] != true {
		t.Fatalf("queued redelivery: %d %v", duplicate.Code, repeated)
	}
	startWebhookTestDispatcher(t, h)
	waitPendingWebhookDispatches(t, h)
	var actualOrigin, actualSource, output string
	var actualPin int
	if err := db.QueryRow(`SELECT triggered_via,triggered_by_id,pipeline_version,output FROM pipeline_runs WHERE id=?`, planned).Scan(&actualOrigin, &actualSource, &actualPin, &output); err != nil {
		t.Fatal(err)
	}
	if actualOrigin != "webhook" || actualSource != wh.ID || actualPin != 1 || output != "pinned-version" {
		t.Fatalf("lost webhook authority: %s/%s", actualOrigin, actualSource)
	}
}

func TestPipelineWebhooks_DurableQueueFailureRollsBackReceipt(t *testing.T) {
	ensureEncryptionKey(t)
	h, db, _, wsID := webhookHandlerRigWithDispatcher(t, false)
	h.SetRunner(&stubRunner{output: "ok"})
	seedWebhookPipeline(t, db, wsID, "durable-fault", "durable-fault")
	wh := seedWebhookRow(t, db, wsID, "durable-fault", "durable-secret", true)
	if _, err := db.Exec(`CREATE TRIGGER refuse_webhook_queue BEFORE INSERT ON pending_runs BEGIN SELECT RAISE(ABORT,'queue unavailable'); END`); err != nil {
		t.Fatal(err)
	}
	rr := fireRoutineWebhook(t, h, wh.Token, "durable-secret", `{"event":"accepted"}`, "retry-after-fault")
	if rr.Code != 503 {
		t.Fatalf("failed durable accept: %d %s", rr.Code, rr.Body.String())
	}
	for _, table := range []string{"routine_webhook_receipts", "pending_runs", "pipeline_runs", "pipeline_run_idempotency"} {
		var n int
		if err := db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil || n != 0 {
			t.Fatalf("acceptance left %s rows=%d err=%v", table, n, err)
		}
	}
	if _, err := db.Exec(`DROP TRIGGER refuse_webhook_queue`); err != nil {
		t.Fatal(err)
	}
	retry := fireRoutineWebhook(t, h, wh.Token, "durable-secret", `{"event":"accepted"}`, "retry-after-fault")
	if retry.Code != 202 {
		t.Fatalf("same delivery retry: %d %s", retry.Code, retry.Body.String())
	}
}

func TestPipelineWebhooks_DurableConcurrentDeliveriesAtCapacityOne(t *testing.T) {
	h, db, _, wsID := webhookHandlerRigWithDispatcher(t, false)
	runner := newBlockingRunner()
	close(runner.release)
	h.SetRunner(runner)
	h.SetRunRegistry(pipeline.NewRunRegistry())
	seedConcurrencyGatedPipeline(t, db, wsID, "durable-concurrent", "durable-concurrent")
	wh := seedWebhookRow(t, db, wsID, "durable-concurrent", "durable-secret", true)
	responses := make(chan struct {
		code int
		body []byte
	}, 2)
	start := make(chan struct{})
	for _, body := range []string{`{"n":1}`, `{"n":2}`} {
		go func(body string) {
			<-start
			rr := fireRoutineWebhook(t, h, wh.Token, "durable-secret", body, body)
			responses <- struct {
				code int
				body []byte
			}{rr.Code, rr.Body.Bytes()}
		}(body)
	}
	close(start)
	planned := make(map[string]bool)
	for range 2 {
		response := <-responses
		if response.code != 202 {
			t.Fatalf("concurrent acceptance: %d %s", response.code, response.body)
		}
		var accepted map[string]any
		if err := json.Unmarshal(response.body, &accepted); err != nil {
			t.Fatal(err)
		}
		id, _ := accepted["run_id"].(string)
		if id == "" || planned[id] {
			t.Fatalf("distinct deliveries lack distinct identity: %v", accepted)
		}
		planned[id] = true
	}
	var durable int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pending_runs p JOIN routine_webhook_receipts d ON p.run_id=d.run_id WHERE p.pipeline_id=? AND p.status='pending'`, wh.TargetPipelineID).Scan(&durable); err != nil || durable != 2 {
		t.Fatalf("accepted deliveries not durable: %d %v", durable, err)
	}
	startWebhookTestDispatcher(t, h)
	waitPendingWebhookDispatches(t, h)
	for runID := range planned {
		var status, linked string
		if err := db.QueryRow(`SELECT r.status,p.fired_run_id FROM pipeline_runs r JOIN pending_runs p ON p.run_id=r.id WHERE r.id=?`, runID).Scan(&status, &linked); err != nil || status != "completed" || linked != runID {
			t.Fatalf("lost accepted start %s: status=%s link=%s err=%v", runID, status, linked, err)
		}
	}
	if calls := runner.callCount(); calls != 2 {
		t.Fatalf("concurrent acceptance executed %d times, want2", calls)
	}
}

func TestPipelineWebhooks_DurablePlannedRunPollingIsWorkspaceScoped(t *testing.T) {
	h, db, userID, wsID := webhookHandlerRigWithDispatcher(t, false)
	h.SetRunner(&stubRunner{output: "ok"})
	seedWebhookPipeline(t, db, wsID, "poll-queued", "poll-queued")
	wh := seedWebhookRow(t, db, wsID, "poll-queued", "durable-secret", true)
	accepted := fireRoutineWebhook(t, h, wh.Token, "durable-secret", `{"n":1}`, "poll-delivery")
	if accepted.Code != 202 {
		t.Fatalf("accept: %d %s", accepted.Code, accepted.Body.String())
	}
	var identity map[string]any
	if err := json.Unmarshal(accepted.Body.Bytes(), &identity); err != nil {
		t.Fatal(err)
	}
	runID, _ := identity["run_id"].(string)
	get := func(workspace, run string) *httptest.ResponseRecorder {
		request := withWorkspaceUser(httptest.NewRequest("GET", "/api/v1/workspaces/"+workspace+"/pipeline-runs/"+run, nil), userID, workspace, "MEMBER")
		request.SetPathValue("runId", run)
		response := httptest.NewRecorder()
		h.GetRun(response, request)
		return response
	}
	queued := get(wsID, runID)
	var detail map[string]any
	if err := json.Unmarshal(queued.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if queued.Code != 200 || detail["id"] != runID || detail["status"] != "QUEUED" || detail["pending_id"] == nil || detail["pending_id"] != identity["pending_id"] || detail["reason_code"] != "scheduled" {
		t.Fatalf("planned run polling: %d %v", queued.Code, detail)
	}
	if _, invented := detail["started_at"]; invented {
		t.Fatal("queued projection invented a confirmed start timestamp")
	}
	foreign := get("foreign-workspace", runID)
	unknown := get("foreign-workspace", "unknown-run")
	if foreign.Code != 404 || unknown.Code != 404 || foreign.Body.String() != unknown.Body.String() {
		t.Fatalf("planned identity leaks across workspaces: foreign=%d/%s unknown=%d/%s", foreign.Code, foreign.Body.String(), unknown.Code, unknown.Body.String())
	}
	startWebhookTestDispatcher(t, h)
	waitPendingWebhookDispatches(t, h)
	completed := get(wsID, runID)
	if err := json.Unmarshal(completed.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if completed.Code != 200 || detail["id"] != runID || detail["status"] != "completed" {
		t.Fatalf("polling did not follow same identity to run: %d %v", completed.Code, detail)
	}
}
