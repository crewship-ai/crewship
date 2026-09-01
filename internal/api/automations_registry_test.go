package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// A3 — triggers cannot be saved in a state where they can never fire
// (PRD-ISSUES-AND-ROUTINES-2026 §17). Before this change, event_type was
// validated by SHAPE ONLY (a regex), and matcher.payload_equals keys were
// never validated at all. Both let a well-formed, permanently-dead rule save
// successfully. These tests pin the fix: rejection at save time, naming what
// would actually have worked.
// ---------------------------------------------------------------------------

// TestAutomationHandlerRejectsUnregisteredEventType is the core A3 defect: a
// well-SHAPED event_type (passes the old regex `^[a-z][a-z0-9_]*(\.[a-z0-9_]+)+$`)
// that names no real journal entry type must still be rejected, and the
// error must name real alternatives rather than say only "invalid".
func TestAutomationHandlerRejectsUnregisteredEventType(t *testing.T) {
	db := setupTestDB(t)
	seedAutomationFixtures(t, db)
	h := NewAutomationHandler(db, newTestLogger())

	w := httptest.NewRecorder()
	h.Create(w, automationRequest(t, "POST", "/api/v1/automations", "ws_A", `{
		"name": "well-shaped nonsense", "event_type": "mission.nonexistent_thing",
		"action": {"routine_slug": "triage"}
	}`))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body %s — a well-shaped but unregistered event_type must not save", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "mission.nonexistent_thing") {
		t.Errorf("error does not echo the rejected value: %s", body)
	}
	// The error must NAME at least one real, registered alternative — a bare
	// "invalid" is not acceptable (A3 acceptance criterion).
	if !strings.Contains(body, "mission.status_change") && !strings.Contains(body, "mission.created") &&
		!strings.Contains(body, "mission.assigned") && !strings.Contains(body, "mission.comment") {
		t.Errorf("error does not name any valid mission.* alternative for a mission.* typo: %s", body)
	}
}

// TestAutomationHandlerRejectsUnregisteredEventTypeOnPatch is the same
// defect reachable through PATCH.
func TestAutomationHandlerRejectsUnregisteredEventTypeOnPatch(t *testing.T) {
	db := setupTestDB(t)
	seedAutomationFixtures(t, db)
	h := NewAutomationHandler(db, newTestLogger())

	w := httptest.NewRecorder()
	h.Create(w, automationRequest(t, "POST", "/api/v1/automations", "ws_A", `{
		"name": "real rule", "event_type": "mission.status_change",
		"action": {"routine_slug": "triage"}
	}`))
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}

	req := automationRequest(t, "PATCH", "/api/v1/automations/"+created.ID, "ws_A",
		`{"event_type": "mission.nope_not_real"}`)
	req.SetPathValue("id", created.ID)
	w = httptest.NewRecorder()
	h.Patch(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("patch status = %d, want 400; body %s", w.Code, w.Body.String())
	}
}

// TestAutomationHandlerRejectsUnknownPayloadKey is the second half of the A3
// defect: matcher.payload_equals keys were never validated at all, so a key
// no emitter writes was accepted and the rule matched nothing, forever, with
// no error anywhere.
func TestAutomationHandlerRejectsUnknownPayloadKey(t *testing.T) {
	db := setupTestDB(t)
	seedAutomationFixtures(t, db)
	h := NewAutomationHandler(db, newTestLogger())

	w := httptest.NewRecorder()
	h.Create(w, automationRequest(t, "POST", "/api/v1/automations", "ws_A", `{
		"name": "dead on arrival", "event_type": "mission.status_change",
		"matcher": {"payload_equals": {"this_key_does_not_exist": "DONE"}},
		"action": {"routine_slug": "triage"}
	}`))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body %s — a payload_equals key no emitter writes must not save", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "this_key_does_not_exist") {
		t.Errorf("error does not name the rejected key: %s", body)
	}
	// Must name the real keys mission.status_change carries.
	if !strings.Contains(body, "action") {
		t.Errorf("error does not name a valid payload key for mission.status_change: %s", body)
	}
}

// TestAutomationHandlerRejectsUnknownPayloadKeyOnPatch: a PATCH that touches
// only the matcher (not event_type) must still be checked against the
// rule's EFFECTIVE event type, not skipped because event_type was absent
// from this particular request body.
func TestAutomationHandlerRejectsUnknownPayloadKeyOnPatch(t *testing.T) {
	db := setupTestDB(t)
	seedAutomationFixtures(t, db)
	h := NewAutomationHandler(db, newTestLogger())

	w := httptest.NewRecorder()
	h.Create(w, automationRequest(t, "POST", "/api/v1/automations", "ws_A", `{
		"name": "real rule", "event_type": "mission.status_change",
		"action": {"routine_slug": "triage"}
	}`))
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}

	req := automationRequest(t, "PATCH", "/api/v1/automations/"+created.ID, "ws_A",
		`{"matcher": {"payload_equals": {"nope": "x"}}}`)
	req.SetPathValue("id", created.ID)
	w = httptest.NewRecorder()
	h.Patch(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("patch status = %d, want 400; body %s", w.Code, w.Body.String())
	}
}

// TestAutomationHandlerAcceptsRegisteredEventTypeAndKnownPayloadKey is the
// must-not-regress arm: a real event type with a real payload key still
// saves. Uses `from`/`to`, the pair the automations guide's headline example
// depends on (docs/guides/automations.mdx).
func TestAutomationHandlerAcceptsRegisteredEventTypeAndKnownPayloadKey(t *testing.T) {
	db := setupTestDB(t)
	seedAutomationFixtures(t, db)
	h := NewAutomationHandler(db, newTestLogger())

	w := httptest.NewRecorder()
	h.Create(w, automationRequest(t, "POST", "/api/v1/automations", "ws_A", `{
		"name": "on close", "event_type": "mission.status_change",
		"matcher": {"payload_equals": {"to": "DONE"}},
		"action": {"routine_slug": "triage"}
	}`))
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body %s", w.Code, w.Body.String())
	}
}

// TestAutomationHandlerUnmappedEventTypeSkipsPayloadKeyValidation pins the
// documented limitation: KnownPayloadKeys is a curated subset, not every
// registered type. A registered type absent from that map must still save —
// silently skipping payload-key validation is the STATED behaviour, not a
// bug, and must not regress into rejecting every unmapped type's rule.
func TestAutomationHandlerUnmappedEventTypeSkipsPayloadKeyValidation(t *testing.T) {
	db := setupTestDB(t)
	seedAutomationFixtures(t, db)
	h := NewAutomationHandler(db, newTestLogger())

	w := httptest.NewRecorder()
	h.Create(w, automationRequest(t, "POST", "/api/v1/automations", "ws_A", `{
		"name": "uncatalogued type", "event_type": "run.completed",
		"matcher": {"payload_equals": {"anything_at_all": "x"}},
		"action": {"routine_slug": "triage"}
	}`))
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (run.completed has no curated payload schema, so no key is rejected); body %s", w.Code, w.Body.String())
	}
}
