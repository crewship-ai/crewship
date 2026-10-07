package webhook

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func signedAcceptanceRequest(body []byte) *http.Request {
	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader(body))
	req.SetPathValue("crewId", "crew-1")
	req.SetPathValue("agentId", "agent-1")
	req.Header.Set("X-Signature", ComputeHMAC(body, "fixture-shared-value"))
	req.Header.Set("X-Delivery-ID", "source-delivery")
	return req
}

func TestAcceptanceCommitsBeforeReceiptAndSupersedesLegacyTrigger(t *testing.T) {
	body := []byte("{\n \"event\": \"alert\", \"source\":\"monitor\", \"data\": {\"count\": 2}}")
	for _, tc := range []struct {
		name       string
		acceptance Acceptance
		status     int
		receipt    map[string]any
	}{
		{"new", Acceptance{DeliveryID: "delivery", WorkID: "work"}, 202, map[string]any{"delivery_id": "delivery", "work_id": "work", "status": "queued", "duplicate": false}},
		{"duplicate completed", Acceptance{DeliveryID: "delivery", WorkID: "work", State: "succeeded", Duplicate: true}, 202, map[string]any{"delivery_id": "delivery", "work_id": "work", "status": "succeeded", "duplicate": true}},
		{"filtered", Acceptance{DeliveryID: "delivery", Ignored: true, Reason: "event_filtered"}, 200, map[string]any{"delivery_id": "delivery", "status": "ignored", "reason": "event_filtered"}},
		{"ping", Acceptance{DeliveryID: "delivery", Ignored: true}, 200, map[string]any{"delivery_id": "delivery", "status": "ignored"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := testHandler("fixture-shared-value")
			h.trigger = func(context.Context, string, string, WebhookPayload) error {
				t.Fatal("legacy trigger ran before receipt")
				return nil
			}
			w := httptest.NewRecorder()
			called := false
			h.SetAcceptFunc(func(ctx context.Context, crew, agent string, in Inbound) (Acceptance, error) {
				called = true
				if w.Flushed || w.Body.Len() != 0 || w.Header().Get("Content-Type") != "" {
					t.Fatal("response published before acceptance returned")
				}
				if crew != "crew-1" || agent != "agent-1" || !bytes.Equal(in.RawBody, body) || in.Header.Get("X-Delivery-ID") != "source-delivery" || in.Payload.Event != "alert" || in.Payload.Source != "monitor" || in.Payload.RecvAt.IsZero() {
					t.Fatalf("delivery changed at boundary: %s %s %+v", crew, agent, in)
				}
				if ctx.Err() != nil {
					t.Fatal(ctx.Err())
				}
				return tc.acceptance, nil
			})
			h.ServeHTTP(w, signedAcceptanceRequest(body))
			if !called || w.Code != tc.status || w.Header().Get("Content-Type") != "application/json" {
				t.Fatalf("receipt: %d %s", w.Code, w.Body.String())
			}
			var receipt map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &receipt); err != nil {
				t.Fatal(err)
			}
			got, _ := json.Marshal(receipt)
			want, _ := json.Marshal(tc.receipt)
			if string(got) != string(want) {
				t.Fatalf("receipt=%s want=%s", got, want)
			}
		})
	}
}

func TestAcceptanceAndLegacyErrorsHaveSafeRetrySemantics(t *testing.T) {
	for _, durable := range []bool{false, true} {
		for _, tc := range []struct {
			err    error
			status int
			body   string
			retry  bool
		}{
			{ErrIngressFull, 429, "ingress capacity full", true},
			{ErrDeliveryConflict, 409, "delivery_conflict", false},
			{ErrUnavailable, 503, "acceptance unavailable", true},
			{ErrEndpointUnknown, 404, "not found", false},
			{ErrMalformed, 400, "bad request", false},
			{errors.New("unexpected"), 500, "internal error", false},
		} {
			t.Run(fmt.Sprintf("durable=%v/%d", durable, tc.status), func(t *testing.T) {
				h := testHandler("fixture-shared-value")
				failure := fmt.Errorf("private database detail: %w", tc.err)
				if durable {
					h.SetAcceptFunc(func(context.Context, string, string, Inbound) (Acceptance, error) { return Acceptance{}, failure })
				} else {
					h.trigger = func(context.Context, string, string, WebhookPayload) error { return failure }
				}
				w := httptest.NewRecorder()
				h.ServeHTTP(w, signedAcceptanceRequest([]byte(`{"event":"alert"}`)))
				if w.Code != tc.status || strings.TrimSpace(w.Body.String()) != tc.body {
					t.Fatalf("failure response=%d %s", w.Code, w.Body.String())
				}
				wantRetry := ""
				if tc.retry {
					wantRetry = IngressRetryAfterSeconds
				}
				if got := w.Header().Get("Retry-After"); got != wantRetry {
					t.Fatalf("retry=%q want=%q", got, wantRetry)
				}
			})
		}
	}
}

type brokenWebhookBody struct{}

func (brokenWebhookBody) Read([]byte) (int, error) { return 0, errors.New("broken transport") }
func (brokenWebhookBody) Close() error             { return nil }

func TestRejectedBodiesNeverReachAcceptance(t *testing.T) {
	for _, tc := range []struct {
		name   string
		body   io.ReadCloser
		status int
	}{
		{"oversized", io.NopCloser(strings.NewReader(strings.Repeat("x", MaxBodyBytes+1))), 413},
		{"read failure", brokenWebhookBody{}, 400},
		{"malformed", io.NopCloser(strings.NewReader(`{`)), 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := testHandler("fixture-shared-value").SetAcceptFunc(func(context.Context, string, string, Inbound) (Acceptance, error) {
				t.Fatal("rejected body reached acceptance")
				return Acceptance{}, nil
			})
			req := signedAcceptanceRequest(nil)
			req.Body = tc.body
			req.ContentLength = -1
			req.Header.Del("X-Signature")
			req.Header.Set("X-Webhook-Secret", "fixture-shared-value")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != tc.status {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
		})
	}
}
