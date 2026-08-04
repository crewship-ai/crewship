package api

import (
	"context"
	"testing"
)

// A routine that lands as `proposed` shows the reviewer a banner saying
// "awaiting approval" and nothing else — no indication of WHY, or of
// what they are being asked to judge. The reasons exist: the risk
// classifier produces them at save time and they are written into the
// inbox item's payload. They were simply never read back.
//
// Reading them from the inbox rather than storing them a second time on
// the routine keeps one source of truth: a reason shown on the routine
// and a reason shown in the inbox cannot then disagree.

func TestRiskReasonsForRoutine_ReadsThemBackFromTheInboxItem(t *testing.T) {
	h, _, wsID := newPipelineHandlerForCRUDTest(t)
	seedPipelineWithVersions(t, h, wsID, "pln-risky", "risky-routine", 1)

	saved, err := h.store.GetBySlug(context.Background(), wsID, "risky-routine")
	if err != nil {
		t.Fatalf("seed lookup: %v", err)
	}
	h.proposeRoutineInbox(context.Background(), wsID, saved,
		[]string{"declares http egress", "requires credentials"}, "test")

	got := h.riskReasonsForRoutine(context.Background(), wsID, "risky-routine")
	if len(got) != 2 {
		t.Fatalf("want 2 reasons, got %d (%v)", len(got), got)
	}
	if got[0] != "declares http egress" || got[1] != "requires credentials" {
		t.Fatalf("reasons came back wrong: %v", got)
	}
}

func TestRiskReasonsForRoutine_NoneWhenNothingProposed(t *testing.T) {
	h, _, wsID := newPipelineHandlerForCRUDTest(t)
	seedPipelineWithVersions(t, h, wsID, "pln-calm", "calm-routine", 1)

	if got := h.riskReasonsForRoutine(context.Background(), wsID, "calm-routine"); len(got) != 0 {
		t.Fatalf("want no reasons for a routine with no proposal, got %v", got)
	}
}

func TestRiskReasonsForRoutine_ScopedToWorkspace(t *testing.T) {
	h, _, wsID := newPipelineHandlerForCRUDTest(t)
	seedPipelineWithVersions(t, h, wsID, "pln-tenant", "tenant-routine", 1)
	saved, _ := h.store.GetBySlug(context.Background(), wsID, "tenant-routine")
	h.proposeRoutineInbox(context.Background(), wsID, saved, []string{"secret reason"}, "test")

	// The source id is workspace-qualified, so another tenant asking for
	// the same slug must come back empty rather than reading our reasons.
	if got := h.riskReasonsForRoutine(context.Background(), "ws_other", "tenant-routine"); len(got) != 0 {
		t.Fatalf("another workspace read our risk reasons: %v", got)
	}
}
