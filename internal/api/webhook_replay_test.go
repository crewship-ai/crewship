package api

import (
	"context"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/webhook"
)

// The dedup key must NOT trust the client-controlled Idempotency-Key when a
// signature is present: an attacker replaying a captured signed webhook could
// otherwise supply a fresh Idempotency-Key to bypass the dedup window and force
// a second run. Bind to the (attacker-unforgeable) signature instead. RED on
// main — the client key wins.
func TestAgentWebhookIdempotencyKey_PrefersSignature(t *testing.T) {
	pl := webhook.WebhookPayload{Event: "e", Source: "s"}
	ctx := context.WithValue(context.Background(), webhookSignatureCtxKey{}, "abc123sig")
	ctx = context.WithValue(ctx, webhookIdempotencyKeyCtxKey{}, "client-controlled-key")
	got := agentWebhookIdempotencyKey(ctx, "agent-1", pl)
	if got == "client-controlled-key" {
		t.Fatal("dedup key must not trust the client Idempotency-Key when a signature is present")
	}
	if !strings.Contains(got, "abc123sig") {
		t.Errorf("dedup key should bind to the signature, got %q", got)
	}
}

// Without a signature (legacy X-Webhook-Secret path), the client Idempotency-Key
// is still honored — unchanged behavior for un-migrated senders.
func TestAgentWebhookIdempotencyKey_FallsBackToClientKey(t *testing.T) {
	pl := webhook.WebhookPayload{Event: "e"}
	ctx := context.WithValue(context.Background(), webhookIdempotencyKeyCtxKey{}, "client-key")
	if got := agentWebhookIdempotencyKey(ctx, "agent-1", pl); got != "client-key" {
		t.Errorf("without a signature the client Idempotency-Key should be used, got %q", got)
	}
}
