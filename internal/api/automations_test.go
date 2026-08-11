package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// automationRequest builds a request already carrying a workspace context,
// which is what the auth middleware would have installed.
func automationRequest(t *testing.T, method, target, workspaceID, body string) *http.Request {
	t.Helper()
	var r *http.Request
	if body == "" {
		r = httptest.NewRequest(method, target, nil)
	} else {
		r = httptest.NewRequest(method, target, strings.NewReader(body))
	}
	return r.WithContext(context.WithValue(r.Context(), ctxWorkspaceID, workspaceID))
}

func seedAutomationFixtures(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, ws := range []string{"ws_A", "ws_B"} {
		if _, err := db.Exec(`INSERT OR IGNORE INTO workspaces (id, name, slug) VALUES (?, ?, ?)`, ws, ws, ws); err != nil {
			t.Fatalf("seed workspace: %v", err)
		}
	}
	if _, err := db.Exec(`
INSERT INTO pipelines (id, workspace_id, slug, name, definition_json, definition_hash)
VALUES ('pl_A', 'ws_A', 'triage', 'triage', '{}', 'h')`); err != nil {
		t.Fatalf("seed pipeline: %v", err)
	}
}

func TestAutomationHandlerCreateAndList(t *testing.T) {
	db := setupTestDB(t)
	seedAutomationFixtures(t, db)
	h := NewAutomationHandler(db, newTestLogger())

	refreshed := 0
	h.SetRefresh(func(context.Context) { refreshed++ })

	w := httptest.NewRecorder()
	h.Create(w, automationRequest(t, "POST", "/api/v1/automations", "ws_A", `{
		"name": "triage on close",
		"event_type": "mission.status_change",
		"matcher": {"payload_equals": {"action": "status_changed"}},
		"action": {"routine_slug": "triage", "inputs": {"issue": "{{ event.mission_id }}"}}
	}`))
	if w.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body %s", w.Code, w.Body.String())
	}
	if refreshed != 1 {
		t.Errorf("refresh hook called %d times, want 1 — a new rule would not fire for up to a minute", refreshed)
	}

	w = httptest.NewRecorder()
	h.List(w, automationRequest(t, "GET", "/api/v1/automations", "ws_A", ""))
	if w.Code != http.StatusOK {
		t.Fatalf("list status = %d", w.Code)
	}
	var listed struct {
		Automations []map[string]any `json:"automations"`
		Count       int              `json:"count"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if listed.Count != 1 {
		t.Fatalf("count = %d, want 1", listed.Count)
	}

	// The tenant fence, through the HTTP surface rather than the store.
	w = httptest.NewRecorder()
	h.List(w, automationRequest(t, "GET", "/api/v1/automations", "ws_B", ""))
	if err := json.Unmarshal(w.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if listed.Count != 0 {
		t.Fatalf("workspace B sees %d of workspace A's automations", listed.Count)
	}
}

// A rule naming a routine that does not exist is skipped by the registry —
// enqueuing against an unresolvable pipeline would park a run the dispatcher
// can never fire. Without this check the mistake surfaces hours later as
// silence, so it is refused at the moment of the typo instead.
func TestAutomationHandlerRejectsUnknownRoutine(t *testing.T) {
	db := setupTestDB(t)
	seedAutomationFixtures(t, db)
	h := NewAutomationHandler(db, newTestLogger())

	w := httptest.NewRecorder()
	h.Create(w, automationRequest(t, "POST", "/api/v1/automations", "ws_A", `{
		"name": "typo", "event_type": "mission.status_change",
		"action": {"routine_slug": "trage"}
	}`))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "trage") {
		t.Errorf("the error does not name the missing routine: %s", w.Body.String())
	}
}

// A routine in ANOTHER workspace must not satisfy the check either — slugs
// are unique per workspace, so an unscoped lookup would let one tenant point
// a rule at a routine they cannot see.
func TestAutomationHandlerRoutineLookupIsWorkspaceScoped(t *testing.T) {
	db := setupTestDB(t)
	seedAutomationFixtures(t, db)
	h := NewAutomationHandler(db, newTestLogger())

	w := httptest.NewRecorder()
	h.Create(w, automationRequest(t, "POST", "/api/v1/automations", "ws_B", `{
		"name": "borrowed", "event_type": "mission.status_change",
		"action": {"routine_slug": "triage"}
	}`))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; workspace B reached workspace A's routine", w.Code)
	}
}

func TestAutomationHandlerRejectsMalformedEventType(t *testing.T) {
	db := setupTestDB(t)
	seedAutomationFixtures(t, db)
	h := NewAutomationHandler(db, newTestLogger())

	w := httptest.NewRecorder()
	h.Create(w, automationRequest(t, "POST", "/api/v1/automations", "ws_A", `{
		"name": "bad", "event_type": "Mission Status Change",
		"action": {"routine_slug": "triage"}
	}`))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

// PATCH is sparse: `automation disable` writes one field, and must not reset
// the matcher, the routine or the burst controls to their zero values.
func TestAutomationHandlerPatchIsSparse(t *testing.T) {
	db := setupTestDB(t)
	seedAutomationFixtures(t, db)
	h := NewAutomationHandler(db, newTestLogger())

	w := httptest.NewRecorder()
	h.Create(w, automationRequest(t, "POST", "/api/v1/automations", "ws_A", `{
		"name": "triage on close", "event_type": "mission.status_change",
		"matcher": {"payload_equals": {"action": "status_changed"}},
		"action": {"routine_slug": "triage"},
		"max_per_hour": 5
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

	req := automationRequest(t, "PATCH", "/api/v1/automations/"+created.ID, "ws_A", `{"enabled": false}`)
	req.SetPathValue("id", created.ID)
	w = httptest.NewRecorder()
	h.Patch(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("patch: %d %s", w.Code, w.Body.String())
	}
	var updated struct {
		Enabled    bool `json:"enabled"`
		MaxPerHour int  `json:"max_per_hour"`
		Matcher    struct {
			PayloadEquals map[string]any `json:"payload_equals"`
		} `json:"matcher"`
		Action struct {
			RoutineSlug string `json:"routine_slug"`
		} `json:"action"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &updated); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if updated.Enabled {
		t.Error("enabled was not written")
	}
	if updated.MaxPerHour != 5 || updated.Action.RoutineSlug != "triage" ||
		updated.Matcher.PayloadEquals["action"] != "status_changed" {
		t.Errorf("a one-field patch clobbered the rest: %+v", updated)
	}
}

func TestAutomationHandlerDeleteIsIdempotentlyNotFound(t *testing.T) {
	db := setupTestDB(t)
	seedAutomationFixtures(t, db)
	h := NewAutomationHandler(db, newTestLogger())

	req := automationRequest(t, "DELETE", "/api/v1/automations/aut_missing", "ws_A", "")
	req.SetPathValue("id", "aut_missing")
	w := httptest.NewRecorder()
	h.Delete(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}
