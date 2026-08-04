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

// inboxStateFor reads an inbox item's lifecycle state.
func inboxStateFor(t *testing.T, h *PipelineHandler, wsID, sourceID string) string {
	t.Helper()
	var state string
	err := h.db.QueryRowContext(context.Background(),
		`SELECT state FROM inbox_items WHERE workspace_id = ? AND source_id = ?`,
		wsID, sourceID).Scan(&state)
	if err != nil {
		t.Fatalf("read inbox state: %v", err)
	}
	return state
}

// addVersion advances a seeded routine by one version, the way a save
// of a changed definition would.
func addVersion(t *testing.T, h *PipelineHandler, pipelineID string, v int) {
	t.Helper()
	if _, err := h.db.Exec(`
		INSERT INTO pipeline_versions
		    (id, pipeline_id, version, definition_json, definition_hash,
		     author_type, author_id, parent_version, change_summary, created_at)
		VALUES (?, ?, ?, ?, ?, 'user', 'u1', ?, '', datetime('now'))`,
		"plnv_"+pipelineID+"_"+pcrudItoa(v), pipelineID, v,
		`{"name":"x","steps":[],"version":`+pcrudItoa(v)+`}`, "hash-"+pcrudItoa(v), v-1); err != nil {
		t.Fatalf("add version %d: %v", v, err)
	}
	if _, err := h.db.Exec(`UPDATE pipelines SET head_version = ? WHERE id = ?`, v, pipelineID); err != nil {
		t.Fatalf("bump head: %v", err)
	}
}

// The SECOND time a routine is proposed, the reviewer must still be
// told — and told about the CURRENT change.
//
// The inbox dedups on (kind, source_id), and a routine proposal's
// source id is the slug: stable by design, so a retried save doesn't
// pile up siblings. But INSERT OR IGNORE makes that dedup absolute. The
// row for this slug already exists from the last time the routine went
// for review, so the second proposal is silently dropped: the routine
// sits at 'proposed' and no reviewer is ever asked.

func TestProposeRoutineInbox_SecondProposalCarriesTheNewVersions(t *testing.T) {
	h, _, wsID := newPipelineHandlerForCRUDTest(t)
	seedPipelineWithVersions(t, h, wsID, "pln-again", "again-routine", 2)
	saved, _ := h.store.GetBySlug(context.Background(), wsID, "again-routine")
	src := routineProposalInboxSource(wsID, "again-routine")

	h.proposeRoutineInbox(context.Background(), wsID, saved, []string{"credentials_required"}, "test")

	// A change lands: v3 is authored and goes for review too.
	addVersion(t, h, saved.ID, 3)
	h.proposeRoutineInbox(context.Background(), wsID, saved, []string{"http_egress"}, "test")

	payload := inboxPayloadFor(t, h, wsID, src)
	if to, ok := payload["to_version"].(float64); !ok || int(to) != 3 {
		t.Fatalf("to_version = %v, want 3 — the item still describes the previous proposal", payload["to_version"])
	}
	if from, ok := payload["from_version"].(float64); !ok || int(from) != 2 {
		t.Fatalf("from_version = %v, want 2", payload["from_version"])
	}
	reasons, _ := payload["risk_reasons"].([]any)
	if len(reasons) != 1 || reasons[0] != "http_egress" {
		t.Fatalf("risk_reasons = %v, want the current save's reasons", payload["risk_reasons"])
	}
}

func TestProposeRoutineInbox_ReturnsToTheInboxAfterAnApproval(t *testing.T) {
	h, _, wsID := newPipelineHandlerForCRUDTest(t)
	seedPipelineWithVersions(t, h, wsID, "pln-cycle", "cycle-routine", 2)
	saved, _ := h.store.GetBySlug(context.Background(), wsID, "cycle-routine")
	src := routineProposalInboxSource(wsID, "cycle-routine")

	h.proposeRoutineInbox(context.Background(), wsID, saved, []string{"credentials_required"}, "test")
	// The reviewer approves. The row stays, resolved.
	if _, err := h.db.Exec(
		`UPDATE inbox_items SET state = 'resolved', resolved_action = 'approved' WHERE source_id = ?`, src); err != nil {
		t.Fatalf("resolve: %v", err)
	}

	// A later edit adds a NEW risk, so it goes for review again. If the
	// item is not resurrected the routine is stuck at 'proposed' with
	// nothing in anyone's inbox to approve it.
	addVersion(t, h, saved.ID, 3)
	h.proposeRoutineInbox(context.Background(), wsID, saved, []string{"http_egress"}, "test")

	if got := inboxStateFor(t, h, wsID, src); got != "unread" {
		t.Fatalf("inbox state = %q, want unread — the new proposal never surfaced", got)
	}
}

// A reviewer is told the routine "requires credentials". Which ones is
// the question they actually have, and it was never on the item — the
// payload carried the risk CATEGORY and stopped there. So the honest
// reading of the card was "something wants something", and the only
// way to learn what was to leave the inbox, find the routine and read
// its DSL.
//
// The routine declares all of it: credentials_required, integrations,
// egress targets. Putting the declarations on the item is what turns
// Approve from a reflex into a decision.

// seedPipelineWithDefinition writes a routine whose DSL is exactly what
// the caller passes, so a test can propose a routine that declares
// something.
func seedPipelineWithDefinition(t *testing.T, h *PipelineHandler, wsID, id, slug, defJSON string) {
	t.Helper()
	if _, err := h.db.Exec(`
		INSERT INTO pipelines (id, workspace_id, slug, name, definition_json, definition_hash,
		                       dsl_version, head_version, last_test_run_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, 'hash-head', '1.0', 1, datetime('now'), datetime('now'), datetime('now'))`,
		id, wsID, slug, slug, defJSON); err != nil {
		t.Fatalf("seed pipeline: %v", err)
	}
}

func TestProposeRoutineInbox_NamesWhatTheRoutineIsAskingFor(t *testing.T) {
	h, _, wsID := newPipelineHandlerForCRUDTest(t)
	seedPipelineWithDefinition(t, h, wsID, "pln-asks", "asks-routine", `{
		"name":"asks-routine","dsl_version":"1.0",
		"credentials_required":[{"type":"github","scope":"repo"},{"type":"openai"}],
		"integrations_required":["slack"],
		"egress_targets":["api.example.com"],
		"steps":[{"id":"a","type":"agent_run","agent_slug":"morgan"}]}`)
	saved, err := h.store.GetBySlug(context.Background(), wsID, "asks-routine")
	if err != nil {
		t.Fatalf("seed lookup: %v", err)
	}
	h.proposeRoutineInbox(context.Background(), wsID, saved, []string{"credentials_required"}, "test")

	payload := inboxPayloadFor(t, h, wsID, routineProposalInboxSource(wsID, "asks-routine"))

	creds := stringsFromPayload(t, payload, "credentials_required")
	// Scope matters: "github" and "github:repo" are different asks.
	if len(creds) != 2 || creds[0] != "github:repo" || creds[1] != "openai" {
		t.Fatalf("credentials_required = %v, want [github:repo openai]", creds)
	}
	if got := stringsFromPayload(t, payload, "integrations_required"); len(got) != 1 || got[0] != "slack" {
		t.Fatalf("integrations_required = %v, want [slack]", got)
	}
	if got := stringsFromPayload(t, payload, "egress_targets"); len(got) != 1 || got[0] != "api.example.com" {
		t.Fatalf("egress_targets = %v, want [api.example.com]", got)
	}
}

func TestProposeRoutineInbox_OmitsWhatTheRoutineDoesNotDeclare(t *testing.T) {
	h, _, wsID := newPipelineHandlerForCRUDTest(t)
	seedPipelineWithDefinition(t, h, wsID, "pln-plain", "plain-routine",
		`{"name":"plain-routine","dsl_version":"1.0","steps":[{"id":"a","type":"http","url":"https://x"}]}`)
	saved, _ := h.store.GetBySlug(context.Background(), wsID, "plain-routine")
	h.proposeRoutineInbox(context.Background(), wsID, saved, []string{"http_step"}, "test")

	payload := inboxPayloadFor(t, h, wsID, routineProposalInboxSource(wsID, "plain-routine"))
	// An empty list rendered as a heading with nothing under it reads as
	// "we could not find out", which is a different claim from "it asks
	// for none".
	for _, k := range []string{"credentials_required", "integrations_required", "egress_targets"} {
		if _, present := payload[k]; present {
			t.Fatalf("%s should be absent when nothing is declared, got %v", k, payload[k])
		}
	}
}

func TestProposeRoutineInbox_SurvivesAnUnparseableDefinition(t *testing.T) {
	h, _, wsID := newPipelineHandlerForCRUDTest(t)
	seedPipelineWithDefinition(t, h, wsID, "pln-bad", "bad-routine", `{not json`)
	saved, _ := h.store.GetBySlug(context.Background(), wsID, "bad-routine")
	// The proposal is the authoritative record that a human must rule on
	// this. Failing to decorate it must not stop it being raised.
	h.proposeRoutineInbox(context.Background(), wsID, saved, []string{"credentials_required"}, "test")

	payload := inboxPayloadFor(t, h, wsID, routineProposalInboxSource(wsID, "bad-routine"))
	if payload["slug"] != "bad-routine" {
		t.Fatalf("the item was not raised for an unparseable definition: %v", payload)
	}
}

// stringsFromPayload reads a JSON array of strings back out of a payload.
func stringsFromPayload(t *testing.T, payload map[string]any, key string) []string {
	t.Helper()
	raw, ok := payload[key].([]any)
	if !ok {
		t.Fatalf("payload[%q] = %v, want a list", key, payload[key])
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		s, ok := v.(string)
		if !ok {
			t.Fatalf("payload[%q] holds a non-string: %v", key, v)
		}
		out = append(out, s)
	}
	return out
}

// The banner on a proposed routine links to "the review item in Inbox"
// and landed on the inbox root, leaving the reader to find the row
// themselves. Deep-linking it needs the row's id, and the id is the
// server's to know: it is built from the (kind, source_id) pair inside
// the inbox writer. A client that reconstructed it would be a second
// copy of that rule, silently wrong the day the first one changes.

func TestInboxItemForRoutine_ReturnsTheRowToDeepLinkTo(t *testing.T) {
	h, _, wsID := newPipelineHandlerForCRUDTest(t)
	seedPipelineWithVersions(t, h, wsID, "pln-link", "link-routine", 1)
	saved, _ := h.store.GetBySlug(context.Background(), wsID, "link-routine")
	h.proposeRoutineInbox(context.Background(), wsID, saved, []string{"credentials_required"}, "test")

	id := h.inboxItemForRoutine(context.Background(), wsID, "link-routine")
	if id == "" {
		t.Fatal("no inbox item id for a routine that was just proposed")
	}
	// It has to be the row that actually exists, not a plausible string.
	var found string
	if err := h.db.QueryRowContext(context.Background(),
		`SELECT id FROM inbox_items WHERE workspace_id = ? AND source_id = ?`,
		wsID, routineProposalInboxSource(wsID, "link-routine")).Scan(&found); err != nil {
		t.Fatalf("read row: %v", err)
	}
	if id != found {
		t.Fatalf("id = %q, want the real row %q", id, found)
	}
}

func TestInboxItemForRoutine_EmptyOnceResolved(t *testing.T) {
	h, _, wsID := newPipelineHandlerForCRUDTest(t)
	seedPipelineWithVersions(t, h, wsID, "pln-done", "done-routine", 1)
	saved, _ := h.store.GetBySlug(context.Background(), wsID, "done-routine")
	h.proposeRoutineInbox(context.Background(), wsID, saved, []string{"credentials_required"}, "test")
	if _, err := h.db.Exec(`UPDATE inbox_items SET state = 'resolved' WHERE workspace_id = ?`, wsID); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	// Pointing at a decided row invites a second decision on something
	// already ruled on.
	if id := h.inboxItemForRoutine(context.Background(), wsID, "done-routine"); id != "" {
		t.Fatalf("want no id for a resolved review, got %q", id)
	}
}
