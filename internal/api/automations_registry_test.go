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

// TestAutomationHandlerAcceptsAdHocRegisteredEventType is the regression this
// fix closes: journal.AllEntryTypes's first cut only scanned
// internal/journal/types.go, so at least eleven real, genuinely-emitted
// entry types declared ad hoc elsewhere (internal/api/pages_public_tokens.go
// and internal/harbormaster/reward.go among them) were rejected as
// "unregistered" even though the events fired in production. `automation
// create --event page.public_view`, which worked before A3's first cut
// landed, must work again.
func TestAutomationHandlerAcceptsAdHocRegisteredEventType(t *testing.T) {
	db := setupTestDB(t)
	seedAutomationFixtures(t, db)
	h := NewAutomationHandler(db, newTestLogger())

	w := httptest.NewRecorder()
	h.Create(w, automationRequest(t, "POST", "/api/v1/automations", "ws_A", `{
		"name": "notify on public view", "event_type": "page.public_view",
		"action": {"routine_slug": "triage"}
	}`))
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body %s — page.public_view is declared and emitted "+
			"(internal/api/pages_public_tokens.go) even though it is not in internal/journal/types.go", w.Code, w.Body.String())
	}
}

// TestAutomationHandlerAcceptsDepthExceededWithPipelineSlugKey pins the
// automation.depth_exceeded payload-key union: internal/pipeline/journal.go's
// emitDepthExceeded is a THIRD emitter of this type (besides
// internal/automation/registry.go's two), and it writes pipeline_id,
// pipeline_slug, run_id, chain_origin and edge — none of which the other two
// emitters write. A rule matching on pipeline_slug must not be rejected as
// an unknown key.
func TestAutomationHandlerAcceptsDepthExceededWithPipelineSlugKey(t *testing.T) {
	db := setupTestDB(t)
	seedAutomationFixtures(t, db)
	h := NewAutomationHandler(db, newTestLogger())

	w := httptest.NewRecorder()
	h.Create(w, automationRequest(t, "POST", "/api/v1/automations", "ws_A", `{
		"name": "chain depth alert", "event_type": "automation.depth_exceeded",
		"matcher": {"payload_equals": {"pipeline_slug": "triage"}},
		"action": {"routine_slug": "triage"}
	}`))
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201; body %s — pipeline_slug is written by "+
			"internal/pipeline/journal.go's emitDepthExceeded", w.Code, w.Body.String())
	}
}

// TestAutomationHandlerPatchSparseSurvivesSoftDeletedRoutine is the second
// regression this fix closes. Before it, Patch always loaded the current row
// and re-validated the EFFECTIVE (merged) event_type and routine_slug even
// when the request body carried neither — so a sparse `PATCH {"enabled":
// false}` on a rule whose routine had since been soft-deleted returned 400,
// and the rule could never be disabled, renamed, or have its debounce
// tuned; the only way out was DELETE and recreate. Store.Update and
// Store.ListActive both deliberately tolerate a dangling routine reference
// ("the row stays in the table and starts working the moment the routine
// exists" — Store.ListActive's own comment); Patch must too, for whatever
// it did not touch.
func TestAutomationHandlerPatchSparseSurvivesSoftDeletedRoutine(t *testing.T) {
	db := setupTestDB(t)
	seedAutomationFixtures(t, db)
	h := NewAutomationHandler(db, newTestLogger())

	w := httptest.NewRecorder()
	h.Create(w, automationRequest(t, "POST", "/api/v1/automations", "ws_A", `{
		"name": "triage on close", "event_type": "mission.status_change",
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

	// Soft-delete the routine the rule points at, exactly as `routine
	// delete` would leave it — the automation row is untouched, only the
	// pipeline it names is gone.
	if _, err := db.Exec(`UPDATE pipelines SET deleted_at = datetime('now') WHERE workspace_id = ? AND slug = ?`,
		"ws_A", "triage"); err != nil {
		t.Fatalf("soft-delete routine: %v", err)
	}

	req := automationRequest(t, "PATCH", "/api/v1/automations/"+created.ID, "ws_A", `{"enabled": false}`)
	req.SetPathValue("id", created.ID)
	w = httptest.NewRecorder()
	h.Patch(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("patch status = %d, want 200; body %s — a sparse PATCH that does not touch "+
			"action.routine_slug must not be blocked by a routine it never mentioned", w.Code, w.Body.String())
	}
	var updated struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &updated); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if updated.Enabled {
		t.Error("enabled = true, want false")
	}
}

// TestAutomationHandlerPatchStillChecksRoutineWhenBodyChangesIt is the
// counterpart to the sparse-patch test above: when a PATCH DOES set
// action.routine_slug, that slug must still be checked, soft-deleted-routine
// tolerance is specifically about fields the body did not touch.
func TestAutomationHandlerPatchStillChecksRoutineWhenBodyChangesIt(t *testing.T) {
	db := setupTestDB(t)
	seedAutomationFixtures(t, db)
	h := NewAutomationHandler(db, newTestLogger())

	w := httptest.NewRecorder()
	h.Create(w, automationRequest(t, "POST", "/api/v1/automations", "ws_A", `{
		"name": "triage on close", "event_type": "mission.status_change",
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
		`{"action": {"routine_slug": "does-not-exist"}}`)
	req.SetPathValue("id", created.ID)
	w = httptest.NewRecorder()
	h.Patch(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("patch status = %d, want 400; body %s", w.Code, w.Body.String())
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
