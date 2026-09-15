package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPipelineWebhooks_GitHubPullRequestDelivery(t *testing.T) {
	h, token, secret, _ := routineAcceptanceRig(t)
	defer h.WaitWebhookDispatches()
	if _, err := h.db.Exec(`UPDATE pipeline_webhooks SET ingress_profile='github'`); err != nil {
		t.Fatal(err)
	}
	body := `{"action":"opened","pull_request":{"number":42}}`
	fire := func(payload, id, event string, valid, legacy bool) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest("POST", "/api/v1/webhooks/"+token+"/github-pull-request", strings.NewReader(payload))
		req.SetPathValue("token", token)
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write([]byte(payload))
		signature := "sha256=" + hex.EncodeToString(mac.Sum(nil))
		if !valid {
			signature = "sha256=" + strings.Repeat("0", 64)
		}
		req.Header.Set("X-Hub-Signature-256", signature)
		req.Header.Set("X-GitHub-Delivery", id)
		req.Header.Set("X-GitHub-Event", event)
		rr := httptest.NewRecorder()
		if legacy {
			h.FireWebhook(rr, req)
		} else {
			h.FireGitHubPullRequest(rr, req)
		}
		return rr
	}
	if rr := fire(body, "bad", "pull_request", false, false); rr.Code != 401 {
		t.Fatalf("bad signature: %d", rr.Code)
	}
	if rr := fire(body, "legacy", "pull_request", true, true); rr.Code != 404 {
		t.Fatalf("legacy switched profiles: %d", rr.Code)
	}
	if rr := fire(`{"zen":"test"}`, "ping", "ping", true, false); rr.Code != 200 {
		t.Fatalf("ping: %d %s", rr.Code, rr.Body.String())
	}
	if countWebhookRows(t, h.db, `SELECT COUNT(*) FROM routine_webhook_receipts`) != 0 {
		t.Fatal("ping created work")
	}
	if rr := fire(`{"action":"opened","pull_request":{"number":314}}`, "non-pr", "issues", true, false); rr.Code != 200 || !strings.Contains(rr.Body.String(), `"status":"IGNORED"`) {
		t.Fatalf("non-PR event dispatched: %d %s", rr.Code, rr.Body.String())
	}
	if countWebhookRows(t, h.db, `SELECT COUNT(*) FROM routine_webhook_receipts`) != 0 {
		t.Fatal("ignored event created a receipt")
	}
	first := fire(body, "one", "pull_request", true, false)
	if first.Code != 202 {
		t.Fatalf("first: %d %s", first.Code, first.Body.String())
	}
	var receipt map[string]any
	if err := json.Unmarshal(first.Body.Bytes(), &receipt); err != nil {
		t.Fatal(err)
	}
	for _, event := range []string{"pull_request"} {
		rr := fire(body, "changed-unsigned-id", event, true, false)
		var duplicate map[string]any
		_ = json.Unmarshal(rr.Body.Bytes(), &duplicate)
		if rr.Code != 202 || duplicate["run_id"] != receipt["run_id"] || duplicate["deduped"] != true {
			t.Fatalf("unsigned header replay: %d %s", rr.Code, rr.Body.String())
		}
	}
	if rr := fire(`{"action":"opened","pull_request":{"number":99}}`, "one", "pull_request", true, false); rr.Code != 409 {
		t.Fatalf("same ID, changed body: %d %s", rr.Code, rr.Body.String())
	}
	if countWebhookRows(t, h.db, `SELECT COUNT(*) FROM routine_webhook_receipts WHERE profile='github'`) != 1 {
		t.Fatal("expected one GitHub receipt")
	}
}

// GitHub's delivery ID is unsigned. Concurrent requests with distinct IDs
// must collide on the body fingerprint inside the acceptance transaction.
func TestPipelineWebhooks_GitHubConcurrentIDsReserveOneRun(t *testing.T) {
	h, token, secret, _ := routineAcceptanceRig(t)
	defer h.WaitWebhookDispatches()
	if _, err := h.db.Exec(`UPDATE pipeline_webhooks SET ingress_profile='github'`); err != nil {
		t.Fatal(err)
	}
	body := `{"action":"synchronize","pull_request":{"number":42}}`
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	signature := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	const count = 8
	results := make(chan *httptest.ResponseRecorder, count)
	for i := 0; i < count; i++ {
		go func(i int) {
			req := httptest.NewRequest("POST", "/api/v1/webhooks/"+token+"/github-pull-request", strings.NewReader(body))
			req.SetPathValue("token", token)
			req.Header.Set("X-Hub-Signature-256", signature)
			req.Header.Set("X-GitHub-Event", "pull_request")
			req.Header.Set("X-GitHub-Delivery", fmt.Sprintf("delivery-%d", i))
			rr := httptest.NewRecorder()
			h.FireGitHubPullRequest(rr, req)
			results <- rr
		}(i)
	}
	var runID string
	for i := 0; i < count; i++ {
		rr := <-results
		if rr.Code != 202 {
			t.Errorf("concurrent delivery: %d %s", rr.Code, rr.Body.String())
			continue
		}
		var receipt struct {
			RunID string `json:"run_id"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &receipt); err != nil {
			t.Fatal(err)
		}
		if runID == "" {
			runID = receipt.RunID
		}
		if receipt.RunID != runID {
			t.Errorf("second run %s, first %s", receipt.RunID, runID)
		}
	}
	if countWebhookRows(t, h.db, `SELECT COUNT(*) FROM routine_webhook_receipts WHERE profile='github'`) != 1 {
		t.Fatal("concurrent IDs created multiple receipts")
	}
}
