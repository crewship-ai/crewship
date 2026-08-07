package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The preview endpoint answers the question `automation create` warns about
// in its own help text and could not answer: would this rule ever fire? These
// pin what the pure Preview cannot — auth, the saved-rule path, and that a
// preview stays read-only.

func TestAutomationPreview_RequiresAWorkspace(t *testing.T) {
	db := setupTestDB(t)
	h := NewAutomationHandler(db, newTestLogger())
	w := httptest.NewRecorder()
	h.PreviewMatch(w, automationRequest(t, "POST", "/api/v1/automations/preview", "",
		`{"event_type":"mission.status_change"}`))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

func TestAutomationPreview_UnknownSavedRuleIs404(t *testing.T) {
	db := setupTestDB(t)
	seedAutomationFixtures(t, db)
	h := NewAutomationHandler(db, newTestLogger())
	w := httptest.NewRecorder()
	h.PreviewMatch(w, automationRequest(t, "POST", "/api/v1/automations/preview", "ws_A",
		`{"automation_id":"aut_nope"}`))
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404: %s", w.Code, w.Body.String())
	}
}

// Without an event type there is nothing to replay against, and guessing one
// would preview a rule the caller did not describe.
func TestAutomationPreview_RequiresAnEventTypeOrASavedRule(t *testing.T) {
	db := setupTestDB(t)
	seedAutomationFixtures(t, db)
	h := NewAutomationHandler(db, newTestLogger())
	w := httptest.NewRecorder()
	h.PreviewMatch(w, automationRequest(t, "POST", "/api/v1/automations/preview", "ws_A", `{}`))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	if !strings.Contains(w.Body.String(), "event_type") {
		t.Errorf("the error must name what is missing, got %s", w.Body.String())
	}
}

// A preview must never start work. That it only reads is the whole reason it
// is safe to run against a live workspace.
func TestAutomationPreview_EnqueuesNothing(t *testing.T) {
	db := setupTestDB(t)
	seedAutomationFixtures(t, db)
	h := NewAutomationHandler(db, newTestLogger())

	var before int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pending_runs`).Scan(&before); err != nil {
		t.Fatalf("count before: %v", err)
	}

	w := httptest.NewRecorder()
	h.PreviewMatch(w, automationRequest(t, "POST", "/api/v1/automations/preview", "ws_A",
		`{"event_type":"mission.status_change"}`))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}

	var after int
	if err := db.QueryRow(`SELECT COUNT(*) FROM pending_runs`).Scan(&after); err != nil {
		t.Fatalf("count after: %v", err)
	}
	if after != before {
		t.Fatalf("pending_runs went %d → %d; a preview that enqueues is not a preview", before, after)
	}
}

// A workspace only ever previews against its own history, or the diagnostic
// leaks what another tenant has been doing.
func TestAutomationPreview_JudgesOnlyThisWorkspacesHistory(t *testing.T) {
	db := setupTestDB(t)
	seedAutomationFixtures(t, db)
	h := NewAutomationHandler(db, newTestLogger())

	for _, ws := range []string{"ws_A", "ws_B"} {
		for i := 0; i < 3; i++ {
			if _, err := db.Exec(`
INSERT INTO journal_entries (id, workspace_id, ts, entry_type, severity, actor_type, summary, payload)
VALUES (?, ?, datetime('now'), 'mission.status_change', 'info', 'user', 'changed', '{"action":"status_changed"}')`,
				ws+"_j"+string(rune('a'+i)), ws); err != nil {
				t.Fatalf("seed entry: %v", err)
			}
		}
	}

	w := httptest.NewRecorder()
	h.PreviewMatch(w, automationRequest(t, "POST", "/api/v1/automations/preview", "ws_A",
		`{"event_type":"mission.status_change"}`))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"scanned":3`) {
		t.Fatalf("want 3 scanned (this workspace only), got %s", w.Body.String())
	}
}
