package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Invalid declared form inputs must be rejected before accepting the delivery.
// Otherwise the sender receives a run_id that the executor never persists, and
// correcting and redelivering the same event is deduped onto that missing run.
func TestFireWebhook_ValidatesInputsBeforeAcceptance(t *testing.T) {
	h, db, _, wsID := webhookHandlerRig(t)
	h.SetRunner(pipelineAgentRunnerStub{})
	seedWebhookPipeline(t, db, wsID, "pln_form", "webhook-form")
	definition := `{"name":"webhook-form","agentless":true,"inputs":[{"name":"qty","type":"integer","required":true,"widget":"number","min":1}],"steps":[{"id":"echo","type":"transform","transform":{"input":"{{ inputs.qty }}","expression":"."}}]}`
	if _, err := db.Exec(`UPDATE pipelines SET definition_json=? WHERE id='pln_form'`, definition); err != nil {
		t.Fatal(err)
	}
	wh := seedWebhookRow(t, db, wsID, "pln_form", "fixture-signing-key", true)
	t.Cleanup(func() { h.webhookDispatchWG.Wait() })
	fire := func() *httptest.ResponseRecorder {
		body := `{"qty":9}`
		req := httptest.NewRequest("POST", "/api/v1/webhooks/"+wh.Token, strings.NewReader(body))
		req.SetPathValue("token", wh.Token)
		req.Header.Set("X-Crewship-Signature", covPSWSign("fixture-signing-key", body))
		req.Header.Set("Idempotency-Key", "input-gate-event")
		rr := httptest.NewRecorder()
		h.FireWebhook(rr, req)
		return rr
	}
	for _, invalid := range []string{`{}`, `{"qty":"wrong"}`, `{"qty":0}`} {
		if _, err := db.Exec(`UPDATE pipeline_webhooks SET inputs_template=? WHERE id=?`, invalid, wh.ID); err != nil {
			t.Fatal(err)
		}
		rr := fire()
		if rr.Code != http.StatusBadRequest {
			h.webhookDispatchWG.Wait()
			t.Fatalf("invalid inputs %s: status = %d, want 400", invalid, rr.Code)
		}
		if strings.Contains(rr.Body.String(), "run_id") {
			t.Fatal("rejected request must not promise a run")
		}
	}
	// Fixing the webhook inputs must allow the SAME delivery id to execute:
	// rejected requests must not consume its idempotency reservation.
	if _, err := db.Exec(`UPDATE pipeline_webhooks SET inputs_template='{"qty":9}' WHERE id=?`, wh.ID); err != nil {
		t.Fatal(err)
	}
	rr := fire()
	if rr.Code != http.StatusAccepted {
		t.Fatalf("corrected delivery = %d: %s", rr.Code, rr.Body.String())
	}
	var accepted struct {
		RunID   string `json:"run_id"`
		Deduped bool   `json:"deduped"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &accepted); err != nil || accepted.RunID == "" || accepted.Deduped {
		t.Fatalf("corrected delivery not newly accepted: %s", rr.Body.String())
	}
	h.webhookDispatchWG.Wait()
	var output, status string
	if err := db.QueryRow(`SELECT output, status FROM pipeline_runs WHERE id=?`, accepted.RunID).Scan(&output, &status); err != nil {
		t.Fatalf("accepted run was not persisted: %v", err)
	}
	if status != "completed" || output != "9" {
		t.Fatalf("corrected run = %s/%q", status, output)
	}
	if id, status := waitForWebhookFire(t, db, wh.ID, time.Second); id != accepted.RunID || status != "COMPLETED" {
		t.Fatalf("webhook record = %s/%s", id, status)
	}
}
