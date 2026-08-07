package api

// Coverage tests for saved_view_handler.go and standup_handler.go. Covers
// auth failures, invalid JSON (400), not-found (404), forbidden (403), and
// happy paths asserting DB state.
//
// The notification_handler.go half of this file went with the handler in
// #1751 — it covered five endpoints reading a table nothing wrote to.
//
// Skipped (not testable without an LLM / network, or require DB-fault
// injection for the 500 branches): none of the standup handler needs an
// LLM (it formats SQL data only), so no LLM branches were skipped. The
// DB-error 500 branches in both handlers are skipped because they
// require a closed/faulted *sql.DB.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ── shared helpers (prefixed covNSV) ────────────────────────────────────────

func covNSVSavedViewHandler(t *testing.T) (*SavedViewHandler, string, string) {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	return NewSavedViewHandler(db, newTestLogger()), userID, wsID
}

func covNSVSeedSavedView(t *testing.T, h *SavedViewHandler, id, wsID, userID, name string, shared bool) {
	t.Helper()
	sharedVal := 0
	if shared {
		sharedVal = 1
	}
	_, err := h.db.Exec(`
		INSERT INTO saved_views (id, workspace_id, user_id, name, filters_json,
		    sort_json, view_type, is_default, shared, created_at, updated_at)
		VALUES (?, ?, ?, ?, '{"a":1}', NULL, 'list', 0, ?, ?, ?)`,
		id, wsID, userID, name, sharedVal,
		time.Now().UTC().Format(time.RFC3339), time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		t.Fatalf("seed saved view %s: %v", id, err)
	}
}

func covNSVQueryHandler(t *testing.T) (*QueryHandler, string, string) {
	t.Helper()
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	return NewQueryHandler(db, nil, nil, "tok", newTestLogger()), userID, wsID
}

// covNSVSeedPeerConv inserts a peer_conversations row between two seeded agents.
func covNSVSeedPeerConv(t *testing.T, q *QueryHandler, id, wsID, crewID, fromID, toID, question, response, status string, escalated int) {
	t.Helper()
	var resp interface{}
	if response != "" {
		resp = response
	}
	_, err := q.db.Exec(`
		INSERT INTO peer_conversations (id, workspace_id, crew_id, chat_id,
		    from_agent_id, to_agent_id, question, response, status, escalated, created_at)
		VALUES (?, ?, ?, 'chat1', ?, ?, ?, ?, ?, ?, ?)`,
		id, wsID, crewID, fromID, toID, question, resp, status, escalated,
		time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		t.Fatalf("seed peer conv %s: %v", id, err)
	}
}

func covNSVSeedEscalation(t *testing.T, q *QueryHandler, id, wsID, crewID, fromID, reason, status string) {
	t.Helper()
	_, err := q.db.Exec(`
		INSERT INTO escalations (id, workspace_id, crew_id, chat_id, from_agent_id,
		    reason, status, created_at)
		VALUES (?, ?, ?, 'chat1', ?, ?, ?, ?)`,
		id, wsID, crewID, fromID, reason, status, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		t.Fatalf("seed escalation %s: %v", id, err)
	}
}

// ── saved_view_handler.go ────────────────────────────────────────────────────

func TestCovNSVSavedViewListUnauthorized(t *testing.T) {
	h, _, _ := covNSVSavedViewHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/saved-views", nil)
	rec := httptest.NewRecorder()
	h.List(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", rec.Code)
	}
}

func TestCovNSVSavedViewListHappyAndEmpty(t *testing.T) {
	h, userID, wsID := covNSVSavedViewHandler(t)

	req := withWorkspaceUser(httptest.NewRequest(http.MethodGet, "/api/v1/saved-views", nil), userID, wsID, "OWNER")
	rec := httptest.NewRecorder()
	h.List(rec, req)
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatalf("empty want 200 [], got %d %q", rec.Code, rec.Body.String())
	}

	covNSVSeedSavedView(t, h, "v1", wsID, userID, "Mine", false)
	covNSVSeedSavedView(t, h, "v2", wsID, "other-user", "Shared", true)   // shared → visible
	covNSVSeedSavedView(t, h, "v3", wsID, "other-user", "Private", false) // private to other → hidden

	req = withWorkspaceUser(httptest.NewRequest(http.MethodGet, "/api/v1/saved-views", nil), userID, wsID, "OWNER")
	rec = httptest.NewRecorder()
	h.List(rec, req)
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, "v1") || !strings.Contains(body, "v2") || strings.Contains(body, "v3") {
		t.Fatalf("list want v1+v2 not v3, got %d %q", rec.Code, body)
	}
}

func TestCovNSVSavedViewCreateInvalidJSON(t *testing.T) {
	h, userID, wsID := covNSVSavedViewHandler(t)
	req := withWorkspaceUser(httptest.NewRequest(http.MethodPost, "/api/v1/saved-views", strings.NewReader("{not json")), userID, wsID, "OWNER")
	rec := httptest.NewRecorder()
	h.Create(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
}

func TestCovNSVSavedViewCreateValidation(t *testing.T) {
	h, userID, wsID := covNSVSavedViewHandler(t)

	// Missing name.
	req := withWorkspaceUser(httptest.NewRequest(http.MethodPost, "/api/v1/saved-views", strings.NewReader(`{"filters_json":"{}"}`)), userID, wsID, "OWNER")
	rec := httptest.NewRecorder()
	h.Create(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing name want 400, got %d", rec.Code)
	}

	// Missing filters_json.
	req = withWorkspaceUser(httptest.NewRequest(http.MethodPost, "/api/v1/saved-views", strings.NewReader(`{"name":"X"}`)), userID, wsID, "OWNER")
	rec = httptest.NewRecorder()
	h.Create(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing filters want 400, got %d", rec.Code)
	}
}

func TestCovNSVSavedViewCreateHappy(t *testing.T) {
	h, userID, wsID := covNSVSavedViewHandler(t)
	// view_type omitted → defaults to "list".
	req := withWorkspaceUser(httptest.NewRequest(http.MethodPost, "/api/v1/saved-views",
		strings.NewReader(`{"name":"My View","filters_json":"{\"x\":1}","shared":true}`)), userID, wsID, "OWNER")
	rec := httptest.NewRecorder()
	h.Create(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("want 201, got %d %q", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"view_type":"list"`) {
		t.Fatalf("expected default view_type list, got %q", rec.Body.String())
	}
	var name, viewType string
	var shared int
	if err := h.db.QueryRow(`SELECT name, view_type, shared FROM saved_views WHERE workspace_id=? AND user_id=?`, wsID, userID).Scan(&name, &viewType, &shared); err != nil {
		t.Fatalf("query: %v", err)
	}
	if name != "My View" || viewType != "list" || shared != 1 {
		t.Fatalf("DB state mismatch: %q %q %d", name, viewType, shared)
	}
}

func TestCovNSVSavedViewUpdateNotFound(t *testing.T) {
	h, userID, wsID := covNSVSavedViewHandler(t)
	req := withWorkspaceUser(httptest.NewRequest(http.MethodPatch, "/api/v1/saved-views/nope", strings.NewReader(`{"name":"Y"}`)), userID, wsID, "OWNER")
	req.SetPathValue("viewId", "nope")
	rec := httptest.NewRecorder()
	h.Update(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", rec.Code)
	}
}

func TestCovNSVSavedViewUpdateForbidden(t *testing.T) {
	h, userID, wsID := covNSVSavedViewHandler(t)
	covNSVSeedSavedView(t, h, "v1", wsID, "other-user", "Theirs", false)
	req := withWorkspaceUser(httptest.NewRequest(http.MethodPatch, "/api/v1/saved-views/v1", strings.NewReader(`{"name":"Y"}`)), userID, wsID, "OWNER")
	req.SetPathValue("viewId", "v1")
	rec := httptest.NewRecorder()
	h.Update(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d", rec.Code)
	}
}

func TestCovNSVSavedViewUpdateInvalidJSON(t *testing.T) {
	h, userID, wsID := covNSVSavedViewHandler(t)
	covNSVSeedSavedView(t, h, "v1", wsID, userID, "Mine", false)
	req := withWorkspaceUser(httptest.NewRequest(http.MethodPatch, "/api/v1/saved-views/v1", strings.NewReader("{bad")), userID, wsID, "OWNER")
	req.SetPathValue("viewId", "v1")
	rec := httptest.NewRecorder()
	h.Update(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
}

func TestCovNSVSavedViewUpdateNoFields(t *testing.T) {
	h, userID, wsID := covNSVSavedViewHandler(t)
	covNSVSeedSavedView(t, h, "v1", wsID, userID, "Mine", false)
	req := withWorkspaceUser(httptest.NewRequest(http.MethodPatch, "/api/v1/saved-views/v1", strings.NewReader(`{}`)), userID, wsID, "OWNER")
	req.SetPathValue("viewId", "v1")
	rec := httptest.NewRecorder()
	h.Update(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("no fields want 400, got %d", rec.Code)
	}
}

func TestCovNSVSavedViewUpdateHappy(t *testing.T) {
	h, userID, wsID := covNSVSavedViewHandler(t)
	covNSVSeedSavedView(t, h, "v1", wsID, userID, "Mine", false)
	body := `{"name":"Renamed","filters_json":"{\"y\":2}","sort_json":"[]","view_type":"board","is_default":true,"shared":true}`
	req := withWorkspaceUser(httptest.NewRequest(http.MethodPatch, "/api/v1/saved-views/v1", strings.NewReader(body)), userID, wsID, "OWNER")
	req.SetPathValue("viewId", "v1")
	rec := httptest.NewRecorder()
	h.Update(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d %q", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"name":"Renamed"`) {
		t.Fatalf("response should reflect update, got %q", rec.Body.String())
	}
	var name, viewType string
	var isDefault, shared int
	if err := h.db.QueryRow(`SELECT name, view_type, is_default, shared FROM saved_views WHERE id='v1'`).Scan(&name, &viewType, &isDefault, &shared); err != nil {
		t.Fatalf("query: %v", err)
	}
	if name != "Renamed" || viewType != "board" || isDefault != 1 || shared != 1 {
		t.Fatalf("DB state mismatch: %q %q %d %d", name, viewType, isDefault, shared)
	}
}

func TestCovNSVSavedViewDelete(t *testing.T) {
	h, userID, wsID := covNSVSavedViewHandler(t)

	// Unauthorized.
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/saved-views/v1", nil)
	req.SetPathValue("viewId", "v1")
	rec := httptest.NewRecorder()
	h.Delete(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", rec.Code)
	}

	// Not found.
	req = withWorkspaceUser(httptest.NewRequest(http.MethodDelete, "/api/v1/saved-views/nope", nil), userID, wsID, "OWNER")
	req.SetPathValue("viewId", "nope")
	rec = httptest.NewRecorder()
	h.Delete(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("not found want 404, got %d", rec.Code)
	}

	// Forbidden (owned by someone else).
	covNSVSeedSavedView(t, h, "v2", wsID, "other-user", "Theirs", false)
	req = withWorkspaceUser(httptest.NewRequest(http.MethodDelete, "/api/v1/saved-views/v2", nil), userID, wsID, "OWNER")
	req.SetPathValue("viewId", "v2")
	rec = httptest.NewRecorder()
	h.Delete(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("forbidden want 403, got %d", rec.Code)
	}

	// Happy.
	covNSVSeedSavedView(t, h, "v1", wsID, userID, "Mine", false)
	req = withWorkspaceUser(httptest.NewRequest(http.MethodDelete, "/api/v1/saved-views/v1", nil), userID, wsID, "OWNER")
	req.SetPathValue("viewId", "v1")
	rec = httptest.NewRecorder()
	h.Delete(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("want 204, got %d", rec.Code)
	}
	var cnt int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM saved_views WHERE id='v1'`).Scan(&cnt); err != nil {
		t.Fatalf("query: %v", err)
	}
	if cnt != 0 {
		t.Fatalf("row should be deleted, count=%d", cnt)
	}
}

// ── standup_handler.go ───────────────────────────────────────────────────────

func TestCovNSVStandupMissingCrewID(t *testing.T) {
	q, _, _ := covNSVQueryHandler(t)
	// Internal route (no path value, no query param) → 400.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/internal/standup", nil)
	rec := httptest.NewRecorder()
	q.Standup(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
}

func TestCovNSVStandupInvalidSince(t *testing.T) {
	q, _, _ := covNSVQueryHandler(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/internal/standup?crew_id=c1&since=not-a-time", nil)
	rec := httptest.NewRecorder()
	q.Standup(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
}

func TestCovNSVStandupCrewNotInWorkspace(t *testing.T) {
	q, userID, wsID := covNSVQueryHandler(t)
	// Public route with workspace context but crew not in workspace → 404.
	req := withWorkspaceUser(httptest.NewRequest(http.MethodGet, "/api/v1/crews/missing/standup", nil), userID, wsID, "OWNER")
	req.SetPathValue("crewId", "missing")
	rec := httptest.NewRecorder()
	q.Standup(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d", rec.Code)
	}
}

func TestCovNSVStandupInternalEmpty(t *testing.T) {
	q, _, wsID := covNSVQueryHandler(t)
	crewID := seedCrewRow(t, q.db, "c1", wsID, "Crew", "crew")
	// Internal route (no workspace context) skips the crew-in-ws check.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/internal/standup?crew_id="+crewID, nil)
	rec := httptest.NewRecorder()
	q.Standup(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d %q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "No peer interactions") || !strings.Contains(body, "[CREW STANDUP]") {
		t.Fatalf("empty standup body unexpected: %q", body)
	}
}

func TestCovNSVStandupPublicHappyWithData(t *testing.T) {
	q, userID, wsID := covNSVQueryHandler(t)
	crewID := seedCrewRow(t, q.db, "c1", wsID, "Crew", "crew")
	a1 := seedAgentRow(t, q.db, "a1", wsID, crewID, "Alice", "alice", "LEAD")
	a2 := seedAgentRow(t, q.db, "a2", wsID, crewID, "Bob", "bob", "AGENT")

	covNSVSeedPeerConv(t, q, "pc1", wsID, crewID, a1, a2, "Status?", "All good", "RESOLVED", 1)
	covNSVSeedPeerConv(t, q, "pc2", wsID, crewID, a2, a1, "Need help", "", "PENDING", 0)
	covNSVSeedEscalation(t, q, "e1", wsID, crewID, a1, "blocked on infra", "PENDING")
	covNSVSeedEscalation(t, q, "e2", wsID, crewID, a2, "design unclear", "RESOLVED")

	req := withWorkspaceUser(httptest.NewRequest(http.MethodGet, "/api/v1/crews/c1/standup", nil), userID, wsID, "OWNER")
	req.SetPathValue("crewId", "c1")
	rec := httptest.NewRecorder()
	q.Standup(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d %q", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		"Peer interactions (2)", "Alice", "Bob", "ESCALATED",
		"Escalations (1 pending, 1 resolved)", "blocked on infra",
		"2 queries", "2 escalations",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("standup body missing %q in %q", want, body)
		}
	}
}

func TestCovNSVStandupValidSinceClamps(t *testing.T) {
	q, _, wsID := covNSVQueryHandler(t)
	crewID := seedCrewRow(t, q.db, "c1", wsID, "Crew", "crew")
	since := time.Now().Add(-1 * time.Hour).UTC().Format(time.RFC3339)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/internal/standup?crew_id="+crewID+"&since="+since, nil)
	rec := httptest.NewRecorder()
	q.Standup(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", rec.Code)
	}
}
