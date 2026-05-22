package webhook

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func testHandler(secret string) *Handler {
	lookup := func(_ context.Context, crewID, agentID string) (string, error) {
		if crewID == "crew-1" && agentID == "agent-1" {
			return secret, nil
		}
		return "", fmt.Errorf("not found")
	}
	trigger := func(_ context.Context, crewID, agentID string, payload WebhookPayload) error {
		return nil
	}
	return NewHandler(slog.Default(), lookup, trigger)
}

func TestWebhookSuccess(t *testing.T) {
	h := testHandler("secret-123")

	body, _ := json.Marshal(map[string]string{"event": "alert", "source": "grafana"})
	req := httptest.NewRequest("POST", "/api/v1/webhooks/team-1/agent-1/trigger", bytes.NewReader(body))
	req.SetPathValue("crewId", "crew-1")
	req.SetPathValue("agentId", "agent-1")
	req.Header.Set("X-Webhook-Secret", "secret-123")
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Deprecation"); got == "" {
		t.Errorf("X-Webhook-Secret path should emit Deprecation header on success; got none")
	}
}

// TestWebhookHMAC_Accepts pins issue #537's HMAC fix: a request signed
// with X-Signature = hex(HMAC-SHA256(body, secret)) must succeed even
// when no X-Webhook-Secret header is present, and must not emit the
// deprecation header (the HMAC path is the new contract, not the
// fallback).
func TestWebhookHMAC_Accepts(t *testing.T) {
	const secret = "shared-secret-xyz"
	h := testHandler(secret)
	body := []byte(`{"event":"alert","source":"grafana"}`)

	req := httptest.NewRequest("POST", "/api/v1/webhooks/team-1/agent-1/trigger", bytes.NewReader(body))
	req.SetPathValue("crewId", "crew-1")
	req.SetPathValue("agentId", "agent-1")
	req.Header.Set("X-Signature", ComputeHMAC(body, secret))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("HMAC-signed request rejected: status=%d body=%s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Deprecation"); got != "" {
		t.Errorf("HMAC path should not emit Deprecation header; got %q", got)
	}
}

// TestWebhookHMAC_RejectsTamperedBody guarantees the X-Signature path
// blocks a request whose body was modified after signing — that's the
// whole point of moving to HMAC over plain shared-secret comparison
// (#537). A future regression that drops the body-vs-signature check
// would let this test pass through.
func TestWebhookHMAC_RejectsTamperedBody(t *testing.T) {
	const secret = "shared-secret-xyz"
	h := testHandler(secret)
	signedBody := []byte(`{"event":"alert"}`)
	sentBody := []byte(`{"event":"evil"}`)

	req := httptest.NewRequest("POST", "/api/v1/webhooks/team-1/agent-1/trigger", bytes.NewReader(sentBody))
	req.SetPathValue("crewId", "crew-1")
	req.SetPathValue("agentId", "agent-1")
	req.Header.Set("X-Signature", ComputeHMAC(signedBody, secret))
	req.Header.Set("Content-Type", "application/json")

	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("tampered body got %d, want 401", w.Code)
	}
}

func TestWebhookMissingSecret(t *testing.T) {
	h := testHandler("secret-123")

	req := httptest.NewRequest("POST", "/api/v1/webhooks/team-1/agent-1/trigger", nil)
	req.SetPathValue("crewId", "crew-1")
	req.SetPathValue("agentId", "agent-1")

	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestWebhookInvalidSecret(t *testing.T) {
	h := testHandler("secret-123")

	req := httptest.NewRequest("POST", "/api/v1/webhooks/team-1/agent-1/trigger", nil)
	req.SetPathValue("crewId", "crew-1")
	req.SetPathValue("agentId", "agent-1")
	req.Header.Set("X-Webhook-Secret", "wrong-secret")

	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", w.Code)
	}
}

func TestWebhookAgentNotFound(t *testing.T) {
	h := testHandler("secret-123")

	req := httptest.NewRequest("POST", "/api/v1/webhooks/team-2/agent-2/trigger", nil)
	req.SetPathValue("crewId", "crew-2")
	req.SetPathValue("agentId", "agent-2")
	req.Header.Set("X-Webhook-Secret", "secret-123")

	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", w.Code)
	}
}

func TestWebhookMethodNotAllowed(t *testing.T) {
	h := testHandler("secret-123")

	req := httptest.NewRequest("GET", "/api/v1/webhooks/team-1/agent-1/trigger", nil)
	req.SetPathValue("crewId", "crew-1")
	req.SetPathValue("agentId", "agent-1")

	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", w.Code)
	}
}

func TestValidateSecret(t *testing.T) {
	tests := []struct {
		name     string
		provided string
		expected string
		valid    bool
	}{
		{"matching", "abc123", "abc123", true},
		{"mismatch", "abc123", "xyz789", false},
		{"empty provided", "", "abc123", false},
		{"empty expected", "abc123", "", false},
		{"both empty", "", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ValidateSecret(tt.provided, tt.expected); got != tt.valid {
				t.Fatalf("ValidateSecret(%q, %q) = %v, want %v", tt.provided, tt.expected, got, tt.valid)
			}
		})
	}
}

func TestHMAC(t *testing.T) {
	message := []byte("hello world")
	secret := "my-secret"

	sig := ComputeHMAC(message, secret)
	if !ValidateHMAC(message, sig, secret) {
		t.Fatal("expected HMAC to validate")
	}

	if ValidateHMAC(message, "invalid-sig", secret) {
		t.Fatal("expected HMAC to fail with invalid signature")
	}

	if ValidateHMAC([]byte("other message"), sig, secret) {
		t.Fatal("expected HMAC to fail with different message")
	}
}
