package api

import (
	"context"
	"testing"

	"github.com/crewship-ai/crewship/internal/chatbridge"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/webhook"
)

// ---------------------------------------------------------------------------
// FIX R6 — webhook idempotency must reserve the SAME runID the run record
// uses. Pre-fix the reservation (LookupOrReserve) minted one CUID and
// CreateRun minted a fresh, different CUID, so the idempotency table pointed
// at a runID no run ever used. This test pins that the reserved run_id in
// pipeline_run_idempotency equals the runID handed to CreateRun.
//
// FIX R4#3 — webhook agent-run dispatch must be rate/concurrency gated per
// agent. Pre-fix every delivery (even with distinct Idempotency-Keys that
// bypass dedup) spawned an unthrottled 10-min RunAgent. This test drives the
// per-agent gate past its threshold and asserts excess deliveries are
// throttled (trigger returns an error, no run dispatched) while a single
// delivery still dispatches.
//
// Prefix: TestSecWebhookIdem* (R6) / TestSecWebhookRate* (R4#3).
// ---------------------------------------------------------------------------

// reservedIdemRunID reads the run_id the idempotency store reserved for the
// given (workspace, key) so the test can compare it against the CreateRun id.
func reservedIdemRunID(t *testing.T, h *WebhookHandler, workspaceID, key string) string {
	t.Helper()
	var runID string
	err := h.db.QueryRow(
		`SELECT run_id FROM pipeline_run_idempotency WHERE workspace_id = ? AND idempotency_key = ?`,
		workspaceID, key,
	).Scan(&runID)
	if err != nil {
		t.Fatalf("read reserved run_id for key %q: %v", key, err)
	}
	return runID
}

// TestSecWebhookIdemReservedRunIDMatchesCreatedRun fires the same
// Idempotency-Key twice and asserts: exactly ONE run is created, and the
// run_id stored in the idempotency table equals that created run's id.
//
// RED pre-fix: LookupOrReserve reserved a throwaway CUID while CreateRun
// minted a different one, so reserved != created.
func TestSecWebhookIdemReservedRunIDMatchesCreatedRun(t *testing.T) {
	body := webhook.WebhookPayload{Event: "deploy", Source: "gh"}
	resolver := &runIDRecordingResolver{}
	resolver.resolveReturnInfo = &chatbridge.ChatInfo{
		AgentID: "agent-1", AgentSlug: "ag", CrewID: "crew-1", CrewSlug: "c", WorkspaceID: "ws-idem-match",
	}
	container := &secWebhook2Container{}
	h := NewWebhookHandler(setupTestDB(t), newTestLogger(), resolver, &orchestrator.Orchestrator{}, nil, container, nil)

	ctx := context.WithValue(context.Background(), webhookIdempotencyKeyCtxKey{}, "idem-match-key")

	_ = h.trigger(ctx, "crew-1", "agent-1", body)
	_ = h.trigger(ctx, "crew-1", "agent-1", body)

	if len(resolver.createdRunIDs) != 1 {
		t.Fatalf("CreateRun called %d times, want exactly 1", len(resolver.createdRunIDs))
	}
	createdID := resolver.createdRunIDs[0]

	reserved := reservedIdemRunID(t, h, "ws-idem-match", "idem-match-key")
	if reserved != createdID {
		t.Errorf("idempotency run_id = %q, but CreateRun id = %q; the table must map the event to the run that actually exists", reserved, createdID)
	}
}

// TestSecWebhookRatePerAgentGateThrottlesBurst drives many deliveries from a
// single agent, each with a DISTINCT Idempotency-Key (so dedup never engages),
// past the per-agent rate threshold. It asserts the first delivery dispatches
// (a run is created) and that the burst is eventually throttled (some
// deliveries return an error and create no run).
//
// RED pre-fix: there was no per-agent gate at all, so every distinct-key
// delivery dispatched and createdRunIDs == number of deliveries.
func TestSecWebhookRatePerAgentGateThrottlesBurst(t *testing.T) {
	body := webhook.WebhookPayload{Event: "deploy", Source: "gh"}
	resolver := &runIDRecordingResolver{}
	resolver.resolveReturnInfo = &chatbridge.ChatInfo{
		AgentID: "agent-rate", AgentSlug: "ag", CrewID: "crew-1", CrewSlug: "c", WorkspaceID: "ws-rate",
	}
	container := &secWebhook2Container{}
	h := NewWebhookHandler(setupTestDB(t), newTestLogger(), resolver, &orchestrator.Orchestrator{}, nil, container, nil)
	// Tighten the per-agent rate so the burst trips deterministically without
	// firing hundreds of deliveries.
	h.agentRatePerMin = 3

	const deliveries = 12
	throttled := 0
	for i := 0; i < deliveries; i++ {
		// Distinct key per delivery → dedup path never short-circuits, so the
		// only thing that can stop dispatch is the rate/concurrency gate.
		ctx := context.WithValue(context.Background(), webhookIdempotencyKeyCtxKey{}, "rate-key-"+string(rune('a'+i)))
		if err := h.trigger(ctx, "crew-1", "agent-rate", body); err != nil {
			throttled++
		}
	}

	// At least one delivery must have dispatched (normal usage not broken).
	if len(resolver.createdRunIDs) == 0 {
		t.Fatal("no run dispatched at all; the gate over-throttled a legitimate first delivery")
	}
	// The burst must be throttled — runs created cannot exceed the limit, and
	// some deliveries must have been rejected.
	if len(resolver.createdRunIDs) > h.agentRatePerMin {
		t.Errorf("dispatched %d runs from one agent with limit %d/min; the per-agent gate did not engage", len(resolver.createdRunIDs), h.agentRatePerMin)
	}
	if throttled == 0 {
		t.Errorf("no deliveries throttled across %d distinct-key bursts; excess agent webhook runs are ungated", deliveries)
	}
}

// TestSecWebhookRateSingleDeliveryStillDispatches guards against
// over-throttling: a lone delivery from an agent (well under any threshold)
// must always dispatch.
func TestSecWebhookRateSingleDeliveryStillDispatches(t *testing.T) {
	body := webhook.WebhookPayload{Event: "deploy", Source: "gh"}
	resolver := &runIDRecordingResolver{}
	resolver.resolveReturnInfo = &chatbridge.ChatInfo{
		AgentID: "agent-solo", AgentSlug: "ag", CrewID: "crew-1", CrewSlug: "c", WorkspaceID: "ws-solo",
	}
	container := &secWebhook2Container{}
	h := NewWebhookHandler(setupTestDB(t), newTestLogger(), resolver, &orchestrator.Orchestrator{}, nil, container, nil)

	ctx := context.WithValue(context.Background(), webhookIdempotencyKeyCtxKey{}, "solo-key")
	if err := h.trigger(ctx, "crew-1", "agent-solo", body); err != nil {
		t.Fatalf("single delivery returned error %v; the gate must not throttle normal single use", err)
	}
	if len(resolver.createdRunIDs) != 1 {
		t.Fatalf("single delivery dispatched %d runs, want 1", len(resolver.createdRunIDs))
	}
}
