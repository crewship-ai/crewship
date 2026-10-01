package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The Phase-2 reviews (skill review, behavior, memory health, negative
// learning) write keeper_requests rows about an agent, not a credential, so
// credential_id is NULL. Scanning that NULL into a string failed and the loop
// skipped the row: Admin › Keeper reviews showed "0" while the table held
// the reviews.
func TestKeeperLogList_KeepsReviewsWithoutACredential(t *testing.T) {
	db := setupTestDB(t)
	wsID, crewID, agentID, credID := seedKeeperFixture(t, db)
	h := NewKeeperLogHandler(db, newTestLogger())
	execOrFatal(t, db, `INSERT INTO keeper_requests (id, requesting_agent_id, requesting_crew_id, credential_id, intent, request_type, decision)
		VALUES ('kr-cred', ?, ?, ?, 'read the db', 'access', 'ALLOW')`, agentID, crewID, credID)
	execOrFatal(t, db, `INSERT INTO keeper_requests (id, requesting_agent_id, requesting_crew_id, credential_id, intent, request_type, decision)
		VALUES ('kr-review', ?, ?, NULL, 'sampled tool call', 'behavior', 'ALLOW')`, agentID, crewID)

	req := httptest.NewRequest("GET", "/?limit=50", nil)
	req = req.WithContext(withWorkspace(withUser(req.Context(), &AuthUser{ID: "test-user-id"}), wsID, "ADMIN"))
	rr := httptest.NewRecorder()
	h.List(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var entries []keeperLogEntry
	if err := json.Unmarshal(rr.Body.Bytes(), &entries); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	types := map[string]bool{}
	for _, e := range entries {
		types[e.RequestType] = true
	}
	if len(entries) != 2 || !types["behavior"] {
		t.Fatalf("entries = %+v, want the credential request AND the behavior review", entries)
	}
}
