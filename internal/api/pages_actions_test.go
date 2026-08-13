package api

// Pages — action dispatch (docs/prd/pages.md §8b).
//
// The endpoint's SHAPE is the security property, so most of this file is about
// what the shape refuses rather than what it does. In particular:
//
//   - an action id that is not in the stored spec is 404, not 403, and a panel
//     the caller may not see is the same 404 — neither is enumerable;
//   - a body naming a routine changes nothing, because the wire format has no
//     field for one (§8b.2);
//   - a link is not dispatchable as a call;
//   - a replayed Idempotency-Key produces one run, and a replayed key carrying
//     DIFFERENT inputs is refused rather than silently deduped (§8b.3's open
//     question, closed in pages_actions.go);
//   - "already running" is 429 with Retry-After, not a second run.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/journal"
)

// ── Fixture ────────────────────────────────────────────────────────────────

const (
	pageActionSlug    = "fleet-201"
	pageActionPanel   = "sluzby"
	pageActionID      = "restart-api"
	pageActionRoutine = "restart-api"
	pageActionLinkID  = "open-incident"
)

// pagesActionSpecBody is the create body for a page whose panel declares one
// call, one link and one toggle — the three kinds that behave differently at
// the dispatch endpoint.
func pagesActionSpecBody(slug string) string {
	return `{
		"slug": "` + slug + `",
		"name": "Flotila .201",
		"panels": [{
			"id": "` + pageActionPanel + `",
			"schema": "status.v1",
			"owner": "crew/lookout",
			"producer": "script/watch-services.sh",
			"sla_seconds": 30,
			"span": 8,
			"actions": [
				{
					"id": "` + pageActionID + `",
					"kind": "call",
					"label": "Restart API",
					"style": "danger",
					"routine": "` + pageActionRoutine + `",
					"params": {"cluster": "prod"},
					"confirm": {"title": "Restart the API?", "body": "In-flight requests are dropped."},
					"inputs": [
						{"name": "reason", "type": "text", "required": true},
						{"name": "replicas", "type": "number", "default": "2"}
					]
				},
				{
					"id": "` + pageActionLinkID + `",
					"kind": "link",
					"label": "Open the incident",
					"ref": {"kind": "issue", "id": "ENG-15"}
				},
				{
					"id": "collapse",
					"kind": "toggle",
					"label": "Collapse",
					"target": ["` + pageActionPanel + `"]
				}
			]
		}]
	}`
}

// pagesSeedRoutineWithStatus puts a routine with a chosen governance status in the workspace for a call action to
// resolve to.
func pagesSeedRoutineWithStatus(t *testing.T, h *PageHandler, wsID, id, slug, status string) {
	t.Helper()
	if _, err := h.db.Exec(`
		INSERT INTO pipelines (id, workspace_id, slug, name, definition_json, definition_hash, status)
		VALUES (?, ?, ?, ?, '{}', 'h', ?)`, id, wsID, slug, slug, status); err != nil {
		t.Fatalf("insert routine %s: %v", slug, err)
	}
}

// newPageActionFixture is newPagesFixture plus the routine the page's call
// action names and the page itself.
func newPageActionFixture(t *testing.T) (*PageHandler, *pagesJournalSpy, string, string) {
	t.Helper()
	h, spy, _, wsID, userID := newPagesFixture(t)
	pagesSeedRoutineWithStatus(t, h, wsID, "pl-restart", pageActionRoutine, "active")

	req := pagesRequest(t, "POST", "/api/v1/pages", wsID, userID, "OWNER", pagesActionSpecBody(pageActionSlug))
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create page with actions: status = %d, want 201, body: %s", rr.Code, rr.Body.String())
	}
	return h, spy, wsID, userID
}

// pagesDispatch drives POST …/actions/{actionId}.
func pagesDispatch(t *testing.T, h *PageHandler, wsID, userID, role, actionID, body, idemKey string) *httptest.ResponseRecorder {
	t.Helper()
	target := fmt.Sprintf("/api/v1/pages/%s/panels/%s/actions/%s", pageActionSlug, pageActionPanel, actionID)
	req := pagesRequest(t, "POST", target, wsID, userID, role, body)
	req.SetPathValue("slug", pageActionSlug)
	req.SetPathValue("panelId", pageActionPanel)
	req.SetPathValue("actionId", actionID)
	if idemKey != "" {
		req.Header.Set("Idempotency-Key", idemKey)
	}
	rr := httptest.NewRecorder()
	h.DispatchAction(rr, req)
	return rr
}

func pagesPendingRows(t *testing.T, h *PageHandler) int {
	t.Helper()
	var n int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM pending_runs`).Scan(&n); err != nil {
		t.Fatalf("count pending_runs: %v", err)
	}
	return n
}

// pagesClearPending marks every queued dispatch fired, which is what
// PendingRunDispatcher does on its next tick. Tests that need a SECOND dispatch
// to be legitimate rather than "already running" call this in between.
func pagesClearPending(t *testing.T, h *PageHandler) {
	t.Helper()
	if _, err := h.db.Exec(`UPDATE pending_runs SET status = 'fired'`); err != nil {
		t.Fatalf("clear pending_runs: %v", err)
	}
}

func decodeReceipt(t *testing.T, rr *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("response is not JSON: %v — %s", err, rr.Body.String())
	}
	return out
}

// ── 1. 202, and the routine comes from the spec ────────────────────────────

// TestPageAction_DispatchIs202WithAPendingID pins the answer §8b.3 requires:
// enqueue and return, never hold the connection for the run.
func TestPageAction_DispatchIs202WithAPendingID(t *testing.T) {
	h, spy, wsID, userID := newPageActionFixture(t)

	rr := pagesDispatch(t, h, wsID, userID, "OWNER", pageActionID, `{"inputs":{"reason":"deploy wedged"}}`, "")
	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 — a page button must never hold the connection for the run; body: %s",
			rr.Code, rr.Body.String())
	}
	got := decodeReceipt(t, rr)
	if got["status"] != "SCHEDULED" {
		t.Errorf("status = %v, want SCHEDULED", got["status"])
	}
	if id, _ := got["pending_id"].(string); !strings.HasPrefix(id, "pnd_") {
		t.Errorf("pending_id = %v, want a pnd_ id", got["pending_id"])
	}
	if got["routine"] != pageActionRoutine {
		t.Errorf("receipt names routine %v, want %q — the receipt is where a caller learns what the SERVER chose",
			got["routine"], pageActionRoutine)
	}
	if n := pagesPendingRows(t, h); n != 1 {
		t.Fatalf("%d rows in pending_runs, want 1", n)
	}

	// The inputs that reached the queue are the RESOLVED ones: the declared
	// default filled in, the number coerced, the author's fixed param applied.
	var inputsJSON string
	if err := h.db.QueryRow(`SELECT inputs_json FROM pending_runs`).Scan(&inputsJSON); err != nil {
		t.Fatalf("read pending inputs: %v", err)
	}
	var inputs map[string]any
	if err := json.Unmarshal([]byte(inputsJSON), &inputs); err != nil {
		t.Fatalf("pending inputs are not JSON: %v", err)
	}
	if inputs["reason"] != "deploy wedged" {
		t.Errorf("inputs[reason] = %v", inputs["reason"])
	}
	if inputs["replicas"] != float64(2) {
		t.Errorf("inputs[replicas] = %v, want the declared default 2", inputs["replicas"])
	}
	if inputs["cluster"] != "prod" {
		t.Errorf("inputs[cluster] = %v, want the author's fixed param", inputs["cluster"])
	}

	// An action nobody can audit is not a control.
	entry := spy.firstOfType(journal.EntryPageActionDispatched)
	if entry == nil {
		t.Fatalf("no %s journal entry", journal.EntryPageActionDispatched)
	}
	if entry.Payload["routine"] != pageActionRoutine {
		t.Errorf("journal payload routine = %v, want %q", entry.Payload["routine"], pageActionRoutine)
	}
}

// TestPageAction_BodyCannotNameARoutine is §8b.2 as an assertion.
//
// The body here names a different routine every way a client could try. None of
// them lands anywhere, because dispatchRequest has one field and it is `inputs`
// — so the run that gets queued is the one the PAGE declared.
func TestPageAction_BodyCannotNameARoutine(t *testing.T) {
	h, _, wsID, userID := newPageActionFixture(t)
	pagesSeedRoutineWithStatus(t, h, wsID, "pl-evil", "delete-everything", "active")

	body := `{
		"inputs": {"reason": "ok"},
		"routine": "delete-everything",
		"pipeline": "delete-everything",
		"pipeline_slug": "delete-everything",
		"verb": "delete-everything"
	}`
	rr := pagesDispatch(t, h, wsID, userID, "OWNER", pageActionID, body, "")
	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body: %s", rr.Code, rr.Body.String())
	}
	if got := decodeReceipt(t, rr)["routine"]; got != pageActionRoutine {
		t.Fatalf("receipt names routine %v; the body named delete-everything and the spec named %q — "+
			"§8b.2 says the spec wins because there is no field for the body's answer", got, pageActionRoutine)
	}

	var queuedSlug string
	if err := h.db.QueryRow(`SELECT pipeline_slug FROM pending_runs`).Scan(&queuedSlug); err != nil {
		t.Fatalf("read pending run: %v", err)
	}
	if queuedSlug != pageActionRoutine {
		t.Fatalf("queued routine = %q, want %q — a caller named a routine at click time and it took effect",
			queuedSlug, pageActionRoutine)
	}
}

// TestPageAction_InputsAreValidatedAgainstTheDeclaration proves the other half
// of §8 rule 4: parameters are "validated server-side against that action's
// schema", not passed through.
func TestPageAction_InputsAreValidatedAgainstTheDeclaration(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"a missing required input", `{"inputs":{}}`, `requires input`},
		{"an undeclared input", `{"inputs":{"reason":"x","shell":"rm -rf /"}}`, `declares no input named`},
		{"a fixed param the caller tried to set", `{"inputs":{"reason":"x","cluster":"staging"}}`, "is not yours to set"},
		{"a number that is not one", `{"inputs":{"reason":"x","replicas":"lots"}}`, "is not a number"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, _, wsID, userID := newPageActionFixture(t)
			rr := pagesDispatch(t, h, wsID, userID, "OWNER", pageActionID, tc.body, "")
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body: %s", rr.Code, rr.Body.String())
			}
			if !strings.Contains(rr.Body.String(), tc.want) {
				t.Errorf("body %s does not mention %q", rr.Body.String(), tc.want)
			}
			if n := pagesPendingRows(t, h); n != 0 {
				t.Errorf("%d rows queued after a refused dispatch, want 0", n)
			}
		})
	}
}

// ── 2. What does not exist, and what is not dispatchable ───────────────────

// TestPageAction_UnknownActionIs404NotForbidden — an action id that is not in
// the stored spec does not exist for this panel. There is no thing to be
// forbidden from, and a 403 would say there is.
func TestPageAction_UnknownActionIs404NotForbidden(t *testing.T) {
	h, _, wsID, userID := newPageActionFixture(t)

	rr := pagesDispatch(t, h, wsID, userID, "OWNER", "delete-everything", `{"inputs":{}}`, "")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (not 403 — an undeclared action does not exist for this panel); body: %s",
			rr.Code, rr.Body.String())
	}
	if n := pagesPendingRows(t, h); n != 0 {
		t.Errorf("%d rows queued, want 0", n)
	}
}

// TestPageAction_LinkCannotBeDispatchedAsACall — a link navigates, and the
// client builds its address from the entity ref. Asking the server to run it is
// the client asking for something the vocabulary says does not happen.
func TestPageAction_LinkCannotBeDispatchedAsACall(t *testing.T) {
	h, _, wsID, userID := newPageActionFixture(t)

	for _, id := range []string{pageActionLinkID, "collapse"} {
		rr := pagesDispatch(t, h, wsID, userID, "OWNER", id, `{"inputs":{}}`, "")
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("dispatching %q: status = %d, want 400; body: %s", id, rr.Code, rr.Body.String())
		}
		if !strings.Contains(rr.Body.String(), "not a call") {
			t.Errorf("dispatching %q: body %s does not say it is not a call", id, rr.Body.String())
		}
	}
	if n := pagesPendingRows(t, h); n != 0 {
		t.Errorf("%d rows queued, want 0", n)
	}
}

// TestPageAction_RoutineThatWentAwayIs409 is §10b.4 — the ground moving under a
// stored page. The action exists; its target does not, so this is a conflict
// and not a missing action.
func TestPageAction_RoutineThatWentAwayIs409(t *testing.T) {
	h, _, wsID, userID := newPageActionFixture(t)
	if _, err := h.db.Exec(`UPDATE pipelines SET deleted_at = '2026-08-12T10:00:00Z' WHERE slug = ?`, pageActionRoutine); err != nil {
		t.Fatalf("soft-delete routine: %v", err)
	}
	rr := pagesDispatch(t, h, wsID, userID, "OWNER", pageActionID, `{"inputs":{"reason":"x"}}`, "")
	if rr.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body: %s", rr.Code, rr.Body.String())
	}
}

// TestPageAction_GovernanceGateIsNotBypassedByAButton — a proposed routine is
// awaiting approval and a disabled one is an admin's airbag. Neither becomes
// runnable by being put behind a button.
func TestPageAction_GovernanceGateIsNotBypassedByAButton(t *testing.T) {
	for _, status := range []string{"proposed", "disabled"} {
		t.Run(status, func(t *testing.T) {
			h, _, wsID, userID := newPageActionFixture(t)
			if _, err := h.db.Exec(`UPDATE pipelines SET status = ? WHERE slug = ?`, status, pageActionRoutine); err != nil {
				t.Fatalf("set routine status: %v", err)
			}
			rr := pagesDispatch(t, h, wsID, userID, "OWNER", pageActionID, `{"inputs":{"reason":"x"}}`, "")
			if rr.Code != http.StatusConflict {
				t.Fatalf("status = %d, want 409; body: %s", rr.Code, rr.Body.String())
			}
			if n := pagesPendingRows(t, h); n != 0 {
				t.Errorf("%d rows queued, want 0", n)
			}
		})
	}
}

// ── 3. Authorisation: see the panel AND hold what the routine requires ─────

// TestPageAction_AuthorisationHasTwoHalves pins both, and pins that they refuse
// DIFFERENTLY: a caller who cannot see the panel gets the same 404 as a caller
// naming an action that does not exist, so a sealed panel's action list is not
// enumerable by watching status codes (§11b decision 14's whole point).
func TestPageAction_AuthorisationHasTwoHalves(t *testing.T) {
	h, _, wsID, _ := newPageActionFixture(t)

	// A MANAGER who is not in crew/lookout: entitled to run routines, not
	// entitled to see this panel.
	pagesSeedUser(t, h, wsID, "outsider", "outsider@example.com", "MANAGER")
	rr := pagesDispatch(t, h, wsID, "outsider", "MANAGER", pageActionID, `{"inputs":{"reason":"x"}}`, "")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("a caller who cannot see the panel got %d, want 404 — a 403 would confirm the action exists; body: %s",
			rr.Code, rr.Body.String())
	}

	// A MEMBER who IS in crew/lookout: entitled to see the panel, not entitled
	// to run a routine. That one is a 403 — they already know the panel is
	// there, so there is nothing left to disclose.
	pagesSeedUser(t, h, wsID, "member", "member@example.com", "MEMBER")
	if _, err := h.db.Exec(`INSERT INTO crew_members (id, crew_id, user_id) VALUES ('cm-member', 'crew-lookout', 'member')`); err != nil {
		t.Fatalf("add crew member: %v", err)
	}
	rr = pagesDispatch(t, h, wsID, "member", "MEMBER", pageActionID, `{"inputs":{"reason":"x"}}`, "")
	if rr.Code != http.StatusForbidden {
		t.Fatalf("a crew member who may not run routines got %d, want 403 — seeing a panel is not operating it; body: %s",
			rr.Code, rr.Body.String())
	}

	// And the same member promoted to MANAGER passes both halves.
	if _, err := h.db.Exec(`UPDATE workspace_members SET role = 'MANAGER' WHERE user_id = 'member'`); err != nil {
		t.Fatalf("promote member: %v", err)
	}
	rr = pagesDispatch(t, h, wsID, "member", "MANAGER", pageActionID, `{"inputs":{"reason":"x"}}`, "")
	if rr.Code != http.StatusAccepted {
		t.Fatalf("a crew MANAGER got %d, want 202; body: %s", rr.Code, rr.Body.String())
	}
	if n := pagesPendingRows(t, h); n != 1 {
		t.Errorf("%d rows queued, want 1", n)
	}
}

// ── 4. Idempotency (§8b.3, including the question it left open) ────────────

// TestPageAction_SameIdempotencyKeyProducesOneRun is the double-click.
func TestPageAction_SameIdempotencyKeyProducesOneRun(t *testing.T) {
	h, _, wsID, userID := newPageActionFixture(t)
	const body = `{"inputs":{"reason":"deploy wedged"}}`
	const key = "click-7f3a"

	first := pagesDispatch(t, h, wsID, userID, "OWNER", pageActionID, body, key)
	if first.Code != http.StatusAccepted {
		t.Fatalf("first click: status = %d, want 202; body: %s", first.Code, first.Body.String())
	}
	// Clear the queue so the second click is NOT refused as "already running" —
	// this test is about the key, and the in-flight gate is tested separately.
	pagesClearPending(t, h)

	second := pagesDispatch(t, h, wsID, userID, "OWNER", pageActionID, body, key)
	if second.Code != http.StatusAccepted {
		t.Fatalf("second click: status = %d, want 202; body: %s", second.Code, second.Body.String())
	}
	got := decodeReceipt(t, second)
	if got["deduped"] != true {
		t.Errorf("second click deduped = %v, want true", got["deduped"])
	}
	if got["pending_id"] != decodeReceipt(t, first)["pending_id"] {
		t.Errorf("second click returned pending_id %v, want the first click's %v",
			got["pending_id"], decodeReceipt(t, first)["pending_id"])
	}
	if n := pagesPendingRows(t, h); n != 1 {
		t.Fatalf("%d rows in pending_runs after two clicks with one key, want 1", n)
	}
}

// TestPageAction_ReplayedKeyWithDifferentInputsIs409 closes §8b.3's open
// question. LookupOrReserve does not compare parameters — verified by reading
// internal/pipeline/idempotency.go, whose PK is (workspace, pipeline, key) and
// whose stored value is a single id — so a replay with different inputs would
// otherwise resolve to the FIRST dispatch and the caller would be told its
// second, different click succeeded. Stripe's rule is that this is rejected.
func TestPageAction_ReplayedKeyWithDifferentInputsIs409(t *testing.T) {
	h, _, wsID, userID := newPageActionFixture(t)
	const key = "click-7f3a"

	first := pagesDispatch(t, h, wsID, userID, "OWNER", pageActionID, `{"inputs":{"reason":"deploy wedged"}}`, key)
	if first.Code != http.StatusAccepted {
		t.Fatalf("first click: status = %d, want 202; body: %s", first.Code, first.Body.String())
	}
	pagesClearPending(t, h)

	second := pagesDispatch(t, h, wsID, userID, "OWNER", pageActionID, `{"inputs":{"reason":"something else"}}`, key)
	if second.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 — a replayed key with different parameters must be rejected, "+
			"not silently resolved to the first run; body: %s", second.Code, second.Body.String())
	}
	if n := pagesPendingRows(t, h); n != 1 {
		t.Errorf("%d rows in pending_runs, want 1 (the refused click must not queue)", n)
	}
}

// TestPageAction_ReplayIsInsensitiveToKeyOrder — "same parameters" is a
// property of the resolved values, not of how the client serialised them.
func TestPageAction_ReplayIsInsensitiveToKeyOrder(t *testing.T) {
	h, _, wsID, userID := newPageActionFixture(t)
	const key = "click-7f3a"

	if rr := pagesDispatch(t, h, wsID, userID, "OWNER", pageActionID,
		`{"inputs":{"reason":"wedged","replicas":3}}`, key); rr.Code != http.StatusAccepted {
		t.Fatalf("first click: status = %d; body: %s", rr.Code, rr.Body.String())
	}
	pagesClearPending(t, h)

	// Same values, opposite order, and the number spelled as a string — which
	// resolves to the same float. This is still one click.
	rr := pagesDispatch(t, h, wsID, userID, "OWNER", pageActionID,
		`{"inputs":{"replicas":"3","reason":"wedged"}}`, key)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202; body: %s", rr.Code, rr.Body.String())
	}
	if decodeReceipt(t, rr)["deduped"] != true {
		t.Errorf("the same parameters in a different order were treated as a different click")
	}
}

// TestPageAction_DifferentKeysAreDifferentClicks — the dedupe must not be so
// eager that a deliberate second run is impossible.
func TestPageAction_DifferentKeysAreDifferentClicks(t *testing.T) {
	h, _, wsID, userID := newPageActionFixture(t)
	const body = `{"inputs":{"reason":"wedged"}}`

	if rr := pagesDispatch(t, h, wsID, userID, "OWNER", pageActionID, body, "click-1"); rr.Code != http.StatusAccepted {
		t.Fatalf("first click: status = %d; body: %s", rr.Code, rr.Body.String())
	}
	pagesClearPending(t, h)
	rr := pagesDispatch(t, h, wsID, userID, "OWNER", pageActionID, body, "click-2")
	if rr.Code != http.StatusAccepted {
		t.Fatalf("second click: status = %d, want 202; body: %s", rr.Code, rr.Body.String())
	}
	if decodeReceipt(t, rr)["deduped"] == true {
		t.Error("a different key was deduped onto the first click")
	}
	if n := pagesPendingRows(t, h); n != 2 {
		t.Errorf("%d rows in pending_runs, want 2", n)
	}
}

// ── 5. Already running ─────────────────────────────────────────────────────

// TestPageAction_AlreadyRunningIs429WithRetryAfter is §8b.3's concurrency_key
// answer, asked of the queue because an enqueueing path never reaches the
// executor's in-process registry.
func TestPageAction_AlreadyRunningIs429WithRetryAfter(t *testing.T) {
	h, _, wsID, userID := newPageActionFixture(t)
	const body = `{"inputs":{"reason":"wedged"}}`

	if rr := pagesDispatch(t, h, wsID, userID, "OWNER", pageActionID, body, ""); rr.Code != http.StatusAccepted {
		t.Fatalf("first click: status = %d; body: %s", rr.Code, rr.Body.String())
	}
	rr := pagesDispatch(t, h, wsID, userID, "OWNER", pageActionID, body, "")
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429; body: %s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("Retry-After"); got == "" {
		t.Error("429 carries no Retry-After; a client told to back off needs to be told for how long")
	}
	if n := pagesPendingRows(t, h); n != 1 {
		t.Fatalf("%d rows in pending_runs, want 1", n)
	}

	// A 429 must not poison the idempotency key: once the queue drains, the same
	// click is legitimate again.
	pagesClearPending(t, h)
	if rr := pagesDispatch(t, h, wsID, userID, "OWNER", pageActionID, body, ""); rr.Code != http.StatusAccepted {
		t.Fatalf("after the queue drained: status = %d, want 202; body: %s", rr.Code, rr.Body.String())
	}
}

// TestPageAction_RefusedDispatchReleasesItsIdempotencyKey — a 429 that burned
// the caller's key for 24h would make a legitimate retry impossible, which is
// exactly what IdempotencyStore.Forget exists to prevent on the run path.
func TestPageAction_RefusedDispatchReleasesItsIdempotencyKey(t *testing.T) {
	h, _, wsID, userID := newPageActionFixture(t)
	const body = `{"inputs":{"reason":"wedged"}}`

	if rr := pagesDispatch(t, h, wsID, userID, "OWNER", pageActionID, body, "click-a"); rr.Code != http.StatusAccepted {
		t.Fatalf("first click: status = %d; body: %s", rr.Code, rr.Body.String())
	}
	if rr := pagesDispatch(t, h, wsID, userID, "OWNER", pageActionID, body, "click-b"); rr.Code != http.StatusTooManyRequests {
		t.Fatalf("second click: status = %d, want 429; body: %s", rr.Code, rr.Body.String())
	}
	pagesClearPending(t, h)

	// click-b was refused, so retrying it must produce a real dispatch rather
	// than a dedupe onto a run that never happened.
	rr := pagesDispatch(t, h, wsID, userID, "OWNER", pageActionID, body, "click-b")
	if rr.Code != http.StatusAccepted {
		t.Fatalf("retrying the refused key: status = %d, want 202; body: %s", rr.Code, rr.Body.String())
	}
	if decodeReceipt(t, rr)["deduped"] == true {
		t.Error("a key that was refused resolved as a replay of a dispatch that never happened")
	}
	if n := pagesPendingRows(t, h); n != 2 {
		t.Errorf("%d rows in pending_runs, want 2", n)
	}
}

// ── 6. Listing what a panel offers ─────────────────────────────────────────

func TestPageAction_ListReadsTheStoredSpec(t *testing.T) {
	h, _, wsID, userID := newPageActionFixture(t)

	target := fmt.Sprintf("/api/v1/pages/%s/panels/%s/actions", pageActionSlug, pageActionPanel)
	req := pagesRequest(t, "GET", target, wsID, userID, "OWNER", "")
	req.SetPathValue("slug", pageActionSlug)
	req.SetPathValue("panelId", pageActionPanel)
	rr := httptest.NewRecorder()
	h.ListPanelActions(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", rr.Code, rr.Body.String())
	}
	var out struct {
		Actions []struct {
			ID      string `json:"id"`
			Kind    string `json:"kind"`
			Style   string `json:"style"`
			Routine string `json:"routine"`
			Inputs  []struct {
				Name string `json:"name"`
				Type string `json:"type"`
			} `json:"inputs"`
		} `json:"actions"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("response is not JSON: %v — %s", err, rr.Body.String())
	}
	if len(out.Actions) != 3 {
		t.Fatalf("got %d actions, want the 3 the page declares", len(out.Actions))
	}
	if out.Actions[0].ID != pageActionID || out.Actions[0].Routine != pageActionRoutine {
		t.Errorf("first action = %+v", out.Actions[0])
	}
	if out.Actions[0].Style != "danger" {
		t.Errorf("style = %q, want danger", out.Actions[0].Style)
	}
	// Every input carries a type, so the client never reproduces the default.
	for _, in := range out.Actions[0].Inputs {
		if in.Type == "" {
			t.Errorf("input %q has no type on the wire", in.Name)
		}
	}
}

// TestPageAction_ListIs404ForAPanelTheCallerCannotSee — the same collapse the
// dispatch endpoint makes. An action list is a description of what somebody
// else may do, and it is not offered.
func TestPageAction_ListIs404ForAPanelTheCallerCannotSee(t *testing.T) {
	h, _, wsID, _ := newPageActionFixture(t)
	pagesSeedUser(t, h, wsID, "outsider", "outsider@example.com", "MANAGER")

	target := fmt.Sprintf("/api/v1/pages/%s/panels/%s/actions", pageActionSlug, pageActionPanel)
	req := pagesRequest(t, "GET", target, wsID, "outsider", "MANAGER", "")
	req.SetPathValue("slug", pageActionSlug)
	req.SetPathValue("panelId", pageActionPanel)
	rr := httptest.NewRecorder()
	h.ListPanelActions(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404; body: %s", rr.Code, rr.Body.String())
	}
}

// ── 7. The authoring gate's action half (§10b.1) ───────────────────────────

// TestPageAction_AuthoringRefusesARoutineThatDoesNotExist — a button that
// resolves only at click time is discovered mid-incident. §10b.1 resolves every
// declared reference at save, and an action's routine is one.
func TestPageAction_AuthoringRefusesARoutineThatDoesNotExist(t *testing.T) {
	h, _, _, wsID, userID := newPagesFixture(t)
	// Deliberately no routine seeded.

	req := pagesRequest(t, "POST", "/api/v1/pages", wsID, userID, "OWNER", pagesActionSpecBody(pageActionSlug))
	rr := httptest.NewRecorder()
	h.Create(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "no such routine exists here") {
		t.Errorf("body %s does not say the routine is missing", rr.Body.String())
	}
}

// TestPageAction_ActionsSurviveTheWriteRoundTrip — the spec the dispatcher
// reads is the spec the author sent. A pass-through that dropped `actions`
// would leave every dispatch 404-ing with a page that looks correct.
func TestPageAction_ActionsSurviveTheWriteRoundTrip(t *testing.T) {
	h, _, wsID, _ := newPageActionFixture(t)

	var pageID string
	if err := h.db.QueryRow(`SELECT id FROM pages WHERE workspace_id = ? AND slug = ?`, wsID, pageActionSlug).Scan(&pageID); err != nil {
		t.Fatalf("load page: %v", err)
	}
	spec, err := h.storedPanelSpec(t.Context(), pageID, pageActionPanel)
	if err != nil {
		t.Fatalf("read stored spec: %v", err)
	}
	if len(spec.Actions) != 3 {
		t.Fatalf("stored spec carries %d actions, want 3", len(spec.Actions))
	}
	a, ok := spec.FindAction(pageActionID)
	if !ok {
		t.Fatal("the stored spec does not carry the action the page was created with")
	}
	if a.Routine != pageActionRoutine || a.Confirm == nil || len(a.Inputs) != 2 {
		t.Errorf("stored action lost detail: %+v", a)
	}
}
