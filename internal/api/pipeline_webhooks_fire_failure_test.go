package api

import (
	"context"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/journal"
)

// PRD-ISSUES-AND-ROUTINES-2026.md §17 A4 ("Trigger failure is visible for
// all three trigger kinds") / F20: a webhook fire that never becomes a run
// used to write a DB row (RecordFire "FAILED") and nothing else — no
// journal entry, no inbox item, matching neither the schedule pattern nor
// condition #5 of the 1.0 quality bar. These tests pin the fixed contract:
//
//   - EVERY such failure emits journal.EntryPipelineWebhookFireFailed
//     (durable, queryable "should have run, didn't").
//   - Only once the SAME webhook's consecutive-failure streak reaches
//     webhookFireFailureAlertThreshold (3) does exactly ONE MANAGER inbox
//     card get raised — further failures past the threshold do not pile
//     up more cards (A4 point d: no inbox noise from a repeat failure).

// webhookFireFailureCaptureEmitter captures every journal entry Emit
// receives, for assertions without a database-backed journal writer.
type webhookFireFailureCaptureEmitter struct {
	mu      sync.Mutex
	entries []journal.Entry
}

func (e *webhookFireFailureCaptureEmitter) Emit(_ context.Context, entry journal.Entry) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.entries = append(e.entries, entry)
	return "j_test", nil
}

func (e *webhookFireFailureCaptureEmitter) count(t journal.EntryType) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	n := 0
	for _, entry := range e.entries {
		if entry.Type == t {
			n++
		}
	}
	return n
}

// TestFireWebhook_GovernanceLoadFailure_JournalsEveryFailure_AlertsOnRepetition
// is the RED-on-main proof for A4's webhook half: on current main,
// FireWebhook's governance pre-check calls `_ = h.webhooks.RecordFire(...)`
// and nothing else — no journal.Emit call exists in that code path, and
// journal.EntryPipelineWebhookFireFailed / inbox.KindWebhookFireFailed do
// not exist on main at all, so this test does not even COMPILE against
// pre-change code. Against the fix, it must pass.
func TestFireWebhook_GovernanceLoadFailure_JournalsEveryFailure_AlertsOnRepetition(t *testing.T) {
	h, db, _, wsID := webhookHandlerRig(t)
	emitter := &webhookFireFailureCaptureEmitter{}
	h.emitter = emitter
	h.SetRunner(pipelineAgentRunnerStub{})

	// Seed the target pipeline (pipeline_webhooks FKs to it), then
	// soft-delete it: GetByID filters deleted_at IS NULL, so every fire
	// takes the "could not load the target routine" branch of
	// alertWebhookFireFailure BEFORE any run is dispatched — mirrors
	// TestScheduler_CircuitBreaker_TripsWhenTargetRoutineDeleted's fixture
	// (internal/pipeline/schedules_circuit_breaker_test.go).
	seedWebhookPipeline(t, db, wsID, "pln_gone", "will-be-deleted")
	wh := seedWebhookRow(t, db, wsID, "pln_gone", "gov-secret", true)
	if _, err := db.Exec(`UPDATE pipelines SET deleted_at = ? WHERE id = ?`, "2026-01-01T00:00:00Z", "pln_gone"); err != nil {
		t.Fatalf("soft-delete target pipeline: %v", err)
	}

	fire := func() int {
		body := `{"event":"deploy"}`
		req := httptest.NewRequest("POST", "/api/v1/webhooks/"+wh.Token, strings.NewReader(body))
		req.SetPathValue("token", wh.Token)
		req.Header.Set("X-Crewship-Signature", covPSWSign("gov-secret", body))
		rr := httptest.NewRecorder()
		h.FireWebhook(rr, req)
		return rr.Code
	}

	countInboxItems := func() int {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM inbox_items WHERE kind = 'webhook_fire_failed' AND source_id = ?`, wh.ID).Scan(&n); err != nil {
			t.Fatalf("count inbox items: %v", err)
		}
		return n
	}

	// Fire 1: journaled, no alert yet (streak 1 < threshold 3).
	if code := fire(); code != 500 {
		t.Fatalf("fire 1: status = %d, want 500", code)
	}
	if n := emitter.count(journal.EntryPipelineWebhookFireFailed); n != 1 {
		t.Fatalf("after fire 1: journal entries = %d, want 1", n)
	}
	if n := countInboxItems(); n != 0 {
		t.Fatalf("after fire 1: inbox items = %d, want 0 (single failure must not page anyone)", n)
	}

	// Fire 2: journaled, still no alert (streak 2 < threshold 3).
	fire()
	if n := emitter.count(journal.EntryPipelineWebhookFireFailed); n != 2 {
		t.Fatalf("after fire 2: journal entries = %d, want 2", n)
	}
	if n := countInboxItems(); n != 0 {
		t.Fatalf("after fire 2: inbox items = %d, want 0", n)
	}

	// Fire 3: crosses the threshold — exactly one inbox card raised.
	fire()
	if n := emitter.count(journal.EntryPipelineWebhookFireFailed); n != 3 {
		t.Fatalf("after fire 3: journal entries = %d, want 3", n)
	}
	if n := countInboxItems(); n != 1 {
		t.Fatalf("after fire 3 (crossed threshold): inbox items = %d, want exactly 1", n)
	}

	// Fire 4 and 5: journal keeps growing (durable per-attempt record),
	// but the inbox is NOT re-alerted — this is the "no noise" contract.
	fire()
	fire()
	if n := emitter.count(journal.EntryPipelineWebhookFireFailed); n != 5 {
		t.Fatalf("after fire 5: journal entries = %d, want 5", n)
	}
	if n := countInboxItems(); n != 1 {
		t.Fatalf("after fire 5: inbox items = %d, want exactly 1 (repetition past threshold must not pile up cards)", n)
	}

	// The inbox row itself: MANAGER-targeted, high priority, sourced to
	// this webhook.
	var targetRole, priority string
	if err := db.QueryRow(`SELECT target_role, priority FROM inbox_items WHERE kind = 'webhook_fire_failed' AND source_id = ?`, wh.ID).
		Scan(&targetRole, &priority); err != nil {
		t.Fatalf("read inbox row: %v", err)
	}
	if targetRole != "MANAGER" {
		t.Errorf("target_role = %q, want MANAGER", targetRole)
	}
	if priority != "high" {
		t.Errorf("priority = %q, want high", priority)
	}
}
