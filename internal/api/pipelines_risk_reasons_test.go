package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
)

// inboxPayloadFor reads back the payload an inbox item was raised with.
func inboxPayloadFor(t *testing.T, h *PipelineHandler, wsID, sourceID string) map[string]any {
	t.Helper()
	var raw sql.NullString
	err := h.db.QueryRowContext(context.Background(),
		`SELECT payload_json FROM inbox_items WHERE workspace_id = ? AND source_id = ?`,
		wsID, sourceID).Scan(&raw)
	if err != nil {
		t.Fatalf("read inbox payload: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(raw.String), &out); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	return out
}

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

// A reviewer opening the proposal in their inbox sees a slug, a reason
// and a pipeline id — nothing about WHAT changed. The routine already
// has immutable versions and a diff endpoint; the payload just never
// carried the two numbers needed to ask for one.
//
// from_version is what was last accepted, to_version is what is being
// proposed. With both, the inbox can render the diff instead of asking
// the reviewer to go and find it.

func TestProposeRoutineInbox_CarriesTheVersionsBeingCompared(t *testing.T) {
	h, _, wsID := newPipelineHandlerForCRUDTest(t)
	seedPipelineWithVersions(t, h, wsID, "pln-ver", "versioned-routine", 3)

	saved, err := h.store.GetBySlug(context.Background(), wsID, "versioned-routine")
	if err != nil {
		t.Fatalf("seed lookup: %v", err)
	}
	head, err := h.store.HeadVersion(context.Background(), saved.ID)
	if err != nil {
		t.Fatalf("head version: %v", err)
	}
	h.proposeRoutineInbox(context.Background(), wsID, saved, []string{"credentials_required"}, "test")

	payload := inboxPayloadFor(t, h, wsID, routineProposalInboxSource(wsID, "versioned-routine"))
	to, ok := payload["to_version"].(float64)
	if !ok || int(to) != head {
		t.Fatalf("to_version = %v, want the head %d", payload["to_version"], head)
	}
	from, ok := payload["from_version"].(float64)
	if !ok || int(from) != head-1 {
		t.Fatalf("from_version = %v, want %d", payload["from_version"], head-1)
	}
}

func TestProposeRoutineInbox_FirstVersionHasNothingToCompare(t *testing.T) {
	h, _, wsID := newPipelineHandlerForCRUDTest(t)
	seedPipelineWithVersions(t, h, wsID, "pln-first", "first-version", 1)
	saved, _ := h.store.GetBySlug(context.Background(), wsID, "first-version")
	h.proposeRoutineInbox(context.Background(), wsID, saved, []string{"credentials_required"}, "test")

	payload := inboxPayloadFor(t, h, wsID, routineProposalInboxSource(wsID, "first-version"))
	// v1 has no predecessor. Emitting from_version: 0 would have the
	// inbox request a diff against a version that never existed.
	if _, present := payload["from_version"]; present {
		t.Fatalf("v1 should carry no from_version, got %v", payload["from_version"])
	}
}
