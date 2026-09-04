package api

// Golden-scenario test for the recurring-work engine's webhook trigger
// (PRD-ISSUES-AND-ROUTINES-2026.md §18, scenario 13). A query of the live
// dev clone on 2026-09-01 found pipeline_webhooks at 0 rows — this
// dispatch/idempotency machinery is well-built and, as far as anyone can
// prove, has never fired outside a unit test. The pre-existing suite in
// pipeline_webhooks_test.go already pins webhookIdempotencyKey's cascade
// as a pure function, and pins the single-fire happy path — but nothing
// there actually POSTs the same delivery twice through FireWebhook and
// checks the real pipeline_runs table. That gap is what this file closes:
// every case here counts actual rows in pipeline_runs, not just the HTTP
// status code, so a regression that answered "202 DEDUPED" correctly
// while still dispatching a second run underneath would be caught.
//
// webhookHandlerRig (pipeline_webhooks_test.go) already wires against
// setupTestDB, i.e. testutil.MigratedSQLDB — the real migrated schema —
// so these tests reuse it rather than building a parallel fixture.

import (
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

// goldenWebhookRig wraps webhookHandlerRig and additionally wires a
// RunStore — pipeline_runs persistence is an OPT-IN dependency
// (PipelineHandler.runStore, pipelines.go:35, "optional; nil → ...
// no persistence") that webhookHandlerRig does NOT wire, and neither
// does any pre-existing FireWebhook test. That means the entire
// existing webhook suite has only ever checked the HTTP response body,
// never the actual pipeline_runs table a duplicate-delivery claim is
// really about. Wire it here so these golden tests check the same
// ground truth production's /run-records endpoint would.
func goldenWebhookRig(t *testing.T) (*PipelineHandler, *sql.DB, string, string) {
	t.Helper()
	h, db, userID, wsID := webhookHandlerRig(t)
	h.SetRunStore(pipeline.NewRunStore(db))
	return h, db, userID, wsID
}

// goldenWebhookRunCount counts real pipeline_runs rows for a pipeline —
// the ground truth a duplicate-delivery claim must be checked against,
// not just the HTTP response body.
func goldenWebhookRunCount(t *testing.T, db *sql.DB, pipelineID string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pipeline_runs WHERE pipeline_id = ?`, pipelineID).Scan(&n); err != nil {
		t.Fatalf("count pipeline_runs: %v", err)
	}
	return n
}

func goldenSignedRequest(t *testing.T, token, secret, body string, extraHeaders map[string]string) *http.Request {
	t.Helper()
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	sig := hex.EncodeToString(mac.Sum(nil))

	req := httptest.NewRequest("POST", "/api/v1/webhooks/"+token, strings.NewReader(body))
	req.SetPathValue("token", token)
	req.Header.Set("X-Crewship-Signature", sig)
	for k, v := range extraHeaders {
		req.Header.Set(k, v)
	}
	return req
}

// TestGolden13_DuplicateDelivery_ProducesExactlyOneRun covers all three
// idempotency-key sources in the cascade (pipeline_webhooks.go:686-703):
// an explicit Idempotency-Key header, the X-Crewship-Event-ID fallback,
// and the synthetic sha256(token|sig|body) fallback used when the
// sender sets neither. In every case: two POSTs of the SAME delivery
// must leave exactly one pipeline_runs row, and the second response
// must hand back the FIRST run's id with deduped:true — never mint a
// second run silently.
func TestGolden13_DuplicateDelivery_ProducesExactlyOneRun(t *testing.T) {
	cases := []struct {
		name    string
		headers map[string]string
	}{
		{"Idempotency-Key header", map[string]string{"Idempotency-Key": "evt-golden-1"}},
		{"X-Crewship-Event-ID header", map[string]string{"X-Crewship-Event-ID": "evt-golden-2"}},
		{"synthetic sha256(token|sig|body) fallback (no header)", nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, db, _, wsID := goldenWebhookRig(t)
			h.SetRunner(&stubRunner{output: "ok"})
			seedWebhookPipeline(t, db, wsID, "pln_golden13", "golden13")
			wh := seedWebhookRow(t, db, wsID, "pln_golden13", "real-secret", true)
			body := `{"hello":"golden-13"}`

			// First delivery: must dispatch a real run.
			req1 := goldenSignedRequest(t, wh.Token, "real-secret", body, tc.headers)
			rr1 := httptest.NewRecorder()
			h.FireWebhook(rr1, req1)
			h.WaitWebhookDispatches()
			if rr1.Code != http.StatusAccepted {
				t.Fatalf("first delivery: status = %d, want 202; body=%s", rr1.Code, rr1.Body.String())
			}
			var resp1 map[string]any
			if err := json.Unmarshal(rr1.Body.Bytes(), &resp1); err != nil {
				t.Fatalf("decode first response: %v", err)
			}
			runID1, _ := resp1["run_id"].(string)
			if runID1 == "" {
				t.Fatalf("first response missing run_id: %v", resp1)
			}
			if deduped, _ := resp1["deduped"].(bool); deduped {
				t.Fatalf("first delivery must NOT be reported deduped: %v", resp1)
			}
			if n := goldenWebhookRunCount(t, db, "pln_golden13"); n != 1 {
				t.Fatalf("after 1st delivery: pipeline_runs count = %d, want 1", n)
			}

			// Second delivery: identical key material (same headers, same
			// body, same signature since the body is unchanged) must
			// dedupe onto the SAME run, not create a second one.
			req2 := goldenSignedRequest(t, wh.Token, "real-secret", body, tc.headers)
			rr2 := httptest.NewRecorder()
			h.FireWebhook(rr2, req2)
			h.WaitWebhookDispatches()
			if rr2.Code != http.StatusAccepted {
				t.Fatalf("second delivery: status = %d, want 202; body=%s", rr2.Code, rr2.Body.String())
			}
			var resp2 map[string]any
			if err := json.Unmarshal(rr2.Body.Bytes(), &resp2); err != nil {
				t.Fatalf("decode second response: %v", err)
			}
			if runID2, _ := resp2["run_id"].(string); runID2 != runID1 {
				t.Errorf("second delivery run_id = %q, want the FIRST run's id %q", runID2, runID1)
			}
			if deduped, _ := resp2["deduped"].(bool); !deduped {
				t.Errorf("second delivery must be reported deduped:true, got %v", resp2)
			}
			if status, _ := resp2["status"].(string); status != "DEDUPED" {
				t.Errorf("second delivery status = %q, want DEDUPED", status)
			}
			if n := goldenWebhookRunCount(t, db, "pln_golden13"); n != 1 {
				t.Fatalf("after 2nd (duplicate) delivery: pipeline_runs count = %d, want still 1 — a duplicate delivery created a second run", n)
			}
		})
	}
}

// TestGolden13_DistinctDeliveries_EachProduceARun is the control case for
// the synthetic-key path: two GENUINELY different bodies (no explicit
// idempotency header) must NOT collide — each is its own run. Without
// this, a bug that hashed only the token (ignoring the body) would still
// pass the duplicate-delivery test above by accident.
func TestGolden13_DistinctDeliveries_EachProduceARun(t *testing.T) {
	h, db, _, wsID := goldenWebhookRig(t)
	h.SetRunner(&stubRunner{output: "ok"})
	seedWebhookPipeline(t, db, wsID, "pln_golden13b", "golden13b")
	wh := seedWebhookRow(t, db, wsID, "pln_golden13b", "real-secret", true)

	for i, body := range []string{`{"n":1}`, `{"n":2}`} {
		req := goldenSignedRequest(t, wh.Token, "real-secret", body, nil)
		rr := httptest.NewRecorder()
		h.FireWebhook(rr, req)
		h.WaitWebhookDispatches()
		if rr.Code != http.StatusAccepted {
			t.Fatalf("delivery %d: status = %d, want 202; body=%s", i, rr.Code, rr.Body.String())
		}
	}
	if n := goldenWebhookRunCount(t, db, "pln_golden13b"); n != 2 {
		t.Fatalf("two distinct bodies: pipeline_runs count = %d, want 2 (they must NOT dedupe onto each other)", n)
	}
}

// TestGolden13_HMACSignatureMismatch_Rejected_NoRunCreated proves the
// signature check is a real gate, not just a status-code formality: a
// wrong signature must leave pipeline_runs untouched.
func TestGolden13_HMACSignatureMismatch_Rejected_NoRunCreated(t *testing.T) {
	h, db, _, wsID := goldenWebhookRig(t)
	h.SetRunner(&stubRunner{output: "ok"})
	seedWebhookPipeline(t, db, wsID, "pln_golden13c", "golden13c")
	wh := seedWebhookRow(t, db, wsID, "pln_golden13c", "real-secret", true)

	req := httptest.NewRequest("POST", "/api/v1/webhooks/"+wh.Token, strings.NewReader(`{"hello":"world"}`))
	req.SetPathValue("token", wh.Token)
	req.Header.Set("X-Crewship-Signature", "0000000000000000000000000000000000000000000000000000000000000000")
	rr := httptest.NewRecorder()
	h.FireWebhook(rr, req)
	h.WaitWebhookDispatches()

	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body=%s", rr.Code, rr.Body.String())
	}
	if n := goldenWebhookRunCount(t, db, "pln_golden13c"); n != 0 {
		t.Fatalf("bad signature: pipeline_runs count = %d, want 0", n)
	}
}

// TestGolden13_TimestampTolerance covers the 5-minute (#1416 item 2)
// freshness window on the opt-in ts.body signature scheme
// (internal/pipeline/webhooks.go:633-705, DefaultWebhookTimestampTolerance):
// a signature computed over a timestamp inside the window is accepted and
// dispatches a run; the identical scheme with a timestamp just outside the
// window is rejected and creates none.
func TestGolden13_TimestampTolerance(t *testing.T) {
	sign := func(secret, ts, body string) string {
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write([]byte(ts + "."))
		mac.Write([]byte(body))
		return hex.EncodeToString(mac.Sum(nil))
	}

	t.Run("within tolerance is accepted and dispatches", func(t *testing.T) {
		h, db, _, wsID := goldenWebhookRig(t)
		h.SetRunner(&stubRunner{output: "ok"})
		seedWebhookPipeline(t, db, wsID, "pln_golden13d", "golden13d")
		wh := seedWebhookRow(t, db, wsID, "pln_golden13d", "real-secret", true)

		body := `{"hello":"fresh"}`
		ts := strconv.FormatInt(time.Now().Add(-4*time.Minute).Unix(), 10) // inside the 5-minute window
		req := httptest.NewRequest("POST", "/api/v1/webhooks/"+wh.Token, strings.NewReader(body))
		req.SetPathValue("token", wh.Token)
		req.Header.Set("X-Crewship-Signature", sign("real-secret", ts, body))
		req.Header.Set("X-Crewship-Timestamp", ts)
		rr := httptest.NewRecorder()
		h.FireWebhook(rr, req)
		h.WaitWebhookDispatches()

		if rr.Code != http.StatusAccepted {
			t.Fatalf("status = %d, want 202; body=%s", rr.Code, rr.Body.String())
		}
		if n := goldenWebhookRunCount(t, db, "pln_golden13d"); n != 1 {
			t.Fatalf("pipeline_runs count = %d, want 1", n)
		}
	})

	t.Run("outside tolerance is rejected and dispatches nothing", func(t *testing.T) {
		h, db, _, wsID := goldenWebhookRig(t)
		h.SetRunner(&stubRunner{output: "ok"})
		seedWebhookPipeline(t, db, wsID, "pln_golden13e", "golden13e")
		wh := seedWebhookRow(t, db, wsID, "pln_golden13e", "real-secret", true)

		body := `{"hello":"stale"}`
		ts := strconv.FormatInt(time.Now().Add(-6*time.Minute).Unix(), 10) // outside the 5-minute window
		req := httptest.NewRequest("POST", "/api/v1/webhooks/"+wh.Token, strings.NewReader(body))
		req.SetPathValue("token", wh.Token)
		req.Header.Set("X-Crewship-Signature", sign("real-secret", ts, body))
		req.Header.Set("X-Crewship-Timestamp", ts)
		rr := httptest.NewRecorder()
		h.FireWebhook(rr, req)
		h.WaitWebhookDispatches()

		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401; body=%s", rr.Code, rr.Body.String())
		}
		if n := goldenWebhookRunCount(t, db, "pln_golden13e"); n != 0 {
			t.Fatalf("stale timestamp: pipeline_runs count = %d, want 0", n)
		}
	})
}
