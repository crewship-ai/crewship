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
	const sig = "deadbeefcafe" // valid hex
	ctx := context.WithValue(context.Background(), webhookSignatureCtxKey{}, sig)
	ctx = context.WithValue(ctx, webhookIdempotencyKeyCtxKey{}, "client-controlled-key")
	got := agentWebhookIdempotencyKey(ctx, "agent-1", pl)
	if got == "client-controlled-key" {
		t.Fatal("dedup key must not trust the client Idempotency-Key when a signature is present")
	}
	if !strings.Contains(got, sig) {
		t.Errorf("dedup key should bind to the signature, got %q", got)
	}
	if !strings.Contains(got, "agent-1") {
		t.Errorf("dedup key should be scoped by agent id, got %q", got)
	}
}

// Equivalent hex spellings of the same signature must produce the SAME dedup
// key (canonicalized), and different agents must produce DIFFERENT keys — so a
// replay can't grind header casing to dodge dedup, and same-workspace agents
// can't collide on identical signature material.
func TestAgentWebhookIdempotencyKey_CanonicalizesAndScopes(t *testing.T) {
	pl := webhook.WebhookPayload{Event: "e"}
	key := func(agent, sig string) string {
		ctx := context.WithValue(context.Background(), webhookSignatureCtxKey{}, sig)
		return agentWebhookIdempotencyKey(ctx, agent, pl)
	}
	if lower, upper := key("a", "deadbeef"), key("a", "DEADBEEF"); lower != upper {
		t.Errorf("hex case must canonicalize to one key: %q vs %q", lower, upper)
	}
	if a, b := key("agent-a", "deadbeef"), key("agent-b", "deadbeef"); a == b {
		t.Errorf("distinct agents must not collide on the same signature: both %q", a)
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
