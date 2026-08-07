package api

// #1712: `crew delete` freed the crew's own slug and left its agents holding
// theirs.
//
// CrewHandler.Delete cascades mission_tasks, missions, crew_members and
// crew_connections, then soft-deletes the crew — after which the crew slug is
// reusable, because every slug-uniqueness check in this package is scoped to
// `deleted_at IS NULL`. The agents were never touched, so they stayed live
// rows occupying the workspace's agent-slug namespace under a crew that no
// longer exists. Re-applying the manifest that created them answered
// `409 Agent slug already taken in this workspace` and the operator had to
// `crewship agent delete <id>` by hand before the apply would go through.
//
// The tests below drive the real handlers end to end — crew delete, then the
// crew + agent create path the re-apply uses — rather than asserting on the
// SQL the fix happens to write. The 409 is what the user reported; the 201 is
// what proves it gone.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// crewDeleteReq builds an authenticated DELETE for one crew.
func crewDeleteReq(userID, wsID, crewID string) *http.Request {
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/crews/"+crewID, nil)
	req.SetPathValue("crewId", crewID)
	ctx := withUser(req.Context(), &AuthUser{ID: userID, Email: "test@example.com"})
	return req.WithContext(withWorkspace(ctx, wsID, "OWNER"))
}

// postJSONReq builds an authenticated POST carrying body.
func postJSONReq(userID, wsID, path string, body any) *http.Request {
	b, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
	ctx := withUser(req.Context(), &AuthUser{ID: userID, Email: "test@example.com"})
	return req.WithContext(withWorkspace(ctx, wsID, "OWNER"))
}

// agentDeletedAt reports the agent row's deleted_at (empty string when live).
func agentDeletedAt(t *testing.T, h *CrewHandler, agentID string) string {
	t.Helper()
	var deletedAt *string
	if err := h.db.QueryRow(`SELECT deleted_at FROM agents WHERE id = ?`, agentID).Scan(&deletedAt); err != nil {
		t.Fatalf("read agent %s: %v", agentID, err)
	}
	if deletedAt == nil {
		return ""
	}
	return *deletedAt
}

// The reported symptom, reproduced through the two commands that produce it:
// delete the crew, then re-apply the same manifest.
func TestCrewDelete_FreesItsAgentsSlugs(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	seedCrewRow(t, db, "crew-foo", wsID, "Foo", "foo")
	seedAgentRow(t, db, "agent-pavel", wsID, "crew-foo", "Pavel", "pavel", "LEAD")

	crews := NewCrewHandler(db, newTestLogger())
	agents := NewAgentHandler(db, newTestLogger())

	rr := httptest.NewRecorder()
	crews.Delete(rr, crewDeleteReq(userID, wsID, "crew-foo"))
	if rr.Code != http.StatusOK {
		t.Fatalf("crew delete: status = %d, want 200, body: %s", rr.Code, rr.Body.String())
	}

	// Re-apply, step 1: the crew slug is free — this half already worked.
	rr = httptest.NewRecorder()
	crews.Create(rr, postJSONReq(userID, wsID, "/api/v1/crews",
		map[string]any{"name": "Foo", "slug": "foo"}))
	if rr.Code != http.StatusCreated {
		t.Fatalf("re-create crew foo: status = %d, want 201, body: %s", rr.Code, rr.Body.String())
	}
	var recreated struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &recreated); err != nil {
		t.Fatalf("decode crew create response: %v", err)
	}

	// Re-apply, step 2: the agent slug. This is what answered 409.
	rr = httptest.NewRecorder()
	agents.Create(rr, postJSONReq(userID, wsID, "/api/v1/agents", map[string]any{
		"name": "Pavel", "slug": "pavel", "crew_id": recreated.ID, "agent_role": "LEAD",
	}))
	if rr.Code != http.StatusCreated {
		t.Fatalf("re-create agent pavel: status = %d, want 201, body: %s\n"+
			"deleting a crew has to release its agents' slugs too — otherwise re-applying the "+
			"manifest that created them needs a manual `crewship agent delete` first (#1712)",
			rr.Code, rr.Body.String())
	}
}

// The mechanism, and its blast radius. The crew's own agents are soft-deleted
// — the same tombstone `agent delete` writes, so every read path already
// excludes them and the create path already knows how to rename one out of the
// way. Agents of any OTHER crew are untouched: this cascade is scoped by
// crew_id, and a bug there would silently retire live agents.
func TestCrewDelete_SoftDeletesOnlyItsOwnAgents(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	seedCrewRow(t, db, "crew-foo", wsID, "Foo", "foo")
	seedAgentRow(t, db, "agent-pavel", wsID, "crew-foo", "Pavel", "pavel", "LEAD")
	seedAgentRow(t, db, "agent-anna", wsID, "crew-foo", "Anna", "anna", "AGENT")

	seedCrewRow(t, db, "crew-bar", wsID, "Bar", "bar")
	seedAgentRow(t, db, "agent-bystander", wsID, "crew-bar", "Bystander", "bystander", "LEAD")

	h := NewCrewHandler(db, newTestLogger())
	rr := httptest.NewRecorder()
	h.Delete(rr, crewDeleteReq(userID, wsID, "crew-foo"))
	if rr.Code != http.StatusOK {
		t.Fatalf("crew delete: status = %d, want 200, body: %s", rr.Code, rr.Body.String())
	}

	for _, id := range []string{"agent-pavel", "agent-anna"} {
		if agentDeletedAt(t, h, id) == "" {
			t.Errorf("agent %s survived its crew's deletion; it still holds its slug in the "+
				"workspace agent-slug namespace, under a crew that no longer exists", id)
		}
	}
	if got := agentDeletedAt(t, h, "agent-bystander"); got != "" {
		t.Errorf("agent-bystander belongs to crew-bar and was retired anyway (deleted_at = %q); "+
			"the cascade must be scoped by crew_id", got)
	}
}
