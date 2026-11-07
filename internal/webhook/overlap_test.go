package webhook

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func overlapHandler(current, previous string, prevErr error, calls *[]string) *Handler {
	lookup := func(context.Context, string, string) (string, error) { return current, nil }
	trigger := func(context.Context, string, string, WebhookPayload) error { return nil }
	h := NewHandler(slog.Default(), lookup, trigger)
	h.SetPreviousSecretLookup(func(context.Context, string, string) (string, error) { return previous, prevErr },
		func(_ context.Context, crewID, agentID, scheme string) {
			*calls = append(*calls, crewID+"/"+agentID+"/"+scheme)
		})
	return h
}

func TestWebhookOverlap_PreviousSecret(t *testing.T) {
	body := []byte(`{"event":"x"}`)
	tests := []struct {
		name      string
		previous  string
		prevErr   error
		header    string
		value     string
		wantCode  int
		wantAudit []string
	}{
		{"current hmac, no audit", "old", nil, "X-Signature", ComputeHMAC(body, "new"), http.StatusAccepted, nil},
		{"previous hmac accepted and audited", "old", nil, "X-Signature", ComputeHMAC(body, "old"), http.StatusAccepted, []string{"crew-1/agent-1/hmac"}},
		{"previous plaintext accepted and audited", "old", nil, "X-Webhook-Secret", "old", http.StatusAccepted, []string{"crew-1/agent-1/secret"}},
		{"no overlap open", "", nil, "X-Signature", ComputeHMAC(body, "old"), http.StatusUnauthorized, nil},
		{"lookup error fails closed", "old", fmt.Errorf("boom"), "X-Signature", ComputeHMAC(body, "old"), http.StatusUnauthorized, nil},
		{"third secret rejected", "old", nil, "X-Signature", ComputeHMAC(body, "third"), http.StatusUnauthorized, nil},
		{"empty-key signature rejected with empty previous", "", nil, "X-Signature", ComputeHMAC(body, ""), http.StatusUnauthorized, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var calls []string
			h := overlapHandler("new", tc.previous, tc.prevErr, &calls)
			req := httptest.NewRequest("POST", "/x", bytes.NewReader(body))
			req.SetPathValue("crewId", "crew-1")
			req.SetPathValue("agentId", "agent-1")
			req.Header.Set(tc.header, tc.value)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d: %s", w.Code, tc.wantCode, w.Body.String())
			}
			if fmt.Sprint(calls) != fmt.Sprint(tc.wantAudit) {
				t.Fatalf("overlap observer calls = %v, want %v", calls, tc.wantAudit)
			}
		})
	}
}
