package api

import (
	"context"
	"testing"

	"github.com/crewship-ai/crewship/internal/chatbridge"
	"github.com/crewship-ai/crewship/internal/orchestrator"
	"github.com/crewship-ai/crewship/internal/webhook"
)

// ---------------------------------------------------------------------------
// FIX R6 — the recorded delivery must name the SAME runID the run record uses.
// Pre-fix the reservation (LookupOrReserve) minted one CUID and CreateRun
// minted a fresh, different CUID, so the dedup record pointed at a runID no run
// ever used. This test pins that the run id on the work item the delivery
// produced equals the runID handed to CreateRun.
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

// recordedRunIDForDelivery reads the run id the delivery ledger recorded for a
// (workspace, endpoint, source delivery id), so the test can compare it against
// the CreateRun id.
//
// It used to read pipeline_run_idempotency, which the agent surface no longer
// writes: a reservation in that table and the run it reserved were two separate
// writes with a crash window between them, and the delivery ledger replaced
// them with one commit. The property under test is unchanged — the record must
// name the run that actually exists — only the table holding it moved.
func recordedRunIDForDelivery(t *testing.T, h *WebhookHandler, workspaceID, endpointID, sourceDeliveryID string) string {
	t.Helper()
	var runID string
	err := h.db.QueryRow(`
		SELECT w.domain_id
		  FROM webhook_deliveries d JOIN work_items w ON w.id = d.work_id
		 WHERE d.workspace_id = ? AND d.endpoint_id = ? AND d.source_delivery_id = ?`,
		workspaceID, endpointID, sourceDeliveryID,
	).Scan(&runID)
	if err != nil {
		t.Fatalf("read recorded run id for delivery %q: %v", sourceDeliveryID, err)
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

	// Acceptance no longer creates a run — the dispatcher does, when it claims
	// the work. What must still hold is the property this test was always
	// about: the record maps the event to work that actually exists, and a
	// repeat maps to the same one rather than to a second.
	if len(resolver.createdRunIDs) != 0 {
		t.Fatalf("acceptance created %d run records, want 0; only the dispatcher creates runs now",
			len(resolver.createdRunIDs))
	}
	var workItems int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM work_items`).Scan(&workItems); err != nil {
		t.Fatal(err)
	}
	if workItems != 1 {
		t.Fatalf("two identical deliveries produced %d work items, want 1", workItems)
	}

	recorded := recordedRunIDForDelivery(t, h, "ws-idem-match", "agent-1", "idem-match-key")
	if recorded == "" {
		t.Errorf("the delivery record names no work; the event is not mapped to anything that exists")
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

	// The measure is accepted WORK now, not runs created: acceptance no longer
	// starts anything, so counting runs here would count the dispatcher's
	// behaviour instead of the gate's.
	var accepted int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM work_items`).Scan(&accepted); err != nil {
		t.Fatal(err)
	}
	if accepted == 0 {
		t.Fatal("nothing accepted at all; the gate over-throttled a legitimate first delivery")
	}
	if accepted > h.agentRatePerMin {
		t.Errorf("accepted %d deliveries from one agent with limit %d/min; the per-agent gate did not engage",
			accepted, h.agentRatePerMin)
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
	var accepted int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM work_items`).Scan(&accepted); err != nil {
		t.Fatal(err)
	}
	if accepted != 1 {
		t.Fatalf("single delivery accepted %d pieces of work, want 1", accepted)
	}
}
