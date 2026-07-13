package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// #1049: the hybrid search handler forwarded a caller-supplied crew_id into
// HybridSearch after only workspace-scoping. A workspace member could pass a
// SIBLING crew's id with scope=crew_shared and read that crew's shared
// episodic memory — breaking cross-crew isolation. The handler must verify the
// caller is a member of the requested crew.

func TestMemoryHybridSearch_CrewShared_NonMember_403(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	crewA := seedCrewRow(t, db, "crew-a", wsID, "Alpha", "alpha")
	crewB := seedCrewRow(t, db, "crew-b", wsID, "Bravo", "bravo")
	// userID is a member of A only.
	execOrFatal(t, db, `INSERT INTO crew_members (id, crew_id, user_id) VALUES ('cm-a', ?, ?)`, crewA, userID)

	h := NewMemoryHybridSearchHandler(db, newTestLogger())

	// Requesting crew B's shared memory → 403, even though B is in the same ws.
	body, _ := json.Marshal(map[string]any{"query": "x", "scope": "crew_shared", "crew_id": crewB})
	req := httptest.NewRequest("POST", "/api/v1/memory/search/hybrid", bytes.NewReader(body))
	req = withWorkspaceUser(req, userID, wsID, "MEMBER")
	rr := httptest.NewRecorder()
	h.Search(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("cross-crew crew_shared: status = %d, want 403; body=%s", rr.Code, rr.Body.String())
	}
}

func TestMemoryHybridSearch_CrewShared_Member_OK(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)
	crewA := seedCrewRow(t, db, "crew-a", wsID, "Alpha", "alpha")
	execOrFatal(t, db, `INSERT INTO crew_members (id, crew_id, user_id) VALUES ('cm-a', ?, ?)`, crewA, userID)

	h := NewMemoryHybridSearchHandler(db, newTestLogger())
	// No engine wired → empty result, but the membership gate must pass (200).
	body, _ := json.Marshal(map[string]any{"query": "x", "scope": "crew_shared", "crew_id": crewA})
	req := httptest.NewRequest("POST", "/api/v1/memory/search/hybrid", bytes.NewReader(body))
	req = withWorkspaceUser(req, userID, wsID, "MEMBER")
	rr := httptest.NewRecorder()
	h.Search(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("own-crew crew_shared: status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
}

func TestMemoryHybridSearch_CrewShared_MissingCrewID_400(t *testing.T) {
	db := setupTestDB(t)
	userID := seedTestUser(t, db)
	wsID := seedTestWorkspace(t, db, userID)

	h := NewMemoryHybridSearchHandler(db, newTestLogger())
	body, _ := json.Marshal(map[string]any{"query": "x", "scope": "crew_shared"})
	req := httptest.NewRequest("POST", "/api/v1/memory/search/hybrid", bytes.NewReader(body))
	req = withWorkspaceUser(req, userID, wsID, "MEMBER")
	rr := httptest.NewRecorder()
	h.Search(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("crew_shared without crew_id: status = %d, want 400", rr.Code)
	}
}
