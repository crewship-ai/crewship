package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// GET /api/v1/journal?mission_id= takes the issue identifier (ENG-1) as
// well as the mission id: the issue page and `crewship journal --mission`
// both have the identifier on hand, and binding "ENG-1" straight into the
// id column returned zero rows. An unknown reference stays as typed so it
// matches nothing rather than the whole workspace.
func TestJournalQuery_MissionIDAcceptsIdentifier(t *testing.T) {
	db := setupTestDB(t)
	_, wsID, crewID, leadID, _ := seedIssueFixtures(t, db)
	missionID := seedIssue(t, db, wsID, crewID, leadID, "ENG-1", "IN_PROGRESS")
	h := NewJournalHandler(db, newTestLogger(), nil)

	cases := map[string]string{
		"ENG-1":     missionID,
		missionID:   missionID,
		"ENG-999":   "ENG-999",
		"":          "",
		"  ENG-1  ": missionID,
	}
	for ref, want := range cases {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/journal", nil)
		q := req.URL.Query()
		q.Set("mission_id", ref)
		req.URL.RawQuery = q.Encode()
		got, err := h.journalQuery(req, wsID)
		if err != nil {
			t.Fatalf("%q: %v", ref, err)
		}
		if got.MissionID != want {
			t.Fatalf("mission_id=%q resolved to %q, want %q", ref, got.MissionID, want)
		}
	}
}

// Resolution is workspace-scoped: another tenant's identifier must not
// resolve to its row.
func TestJournalQuery_MissionIDIsWorkspaceScoped(t *testing.T) {
	db := setupTestDB(t)
	_, wsID, crewID, leadID, _ := seedIssueFixtures(t, db)
	seedIssue(t, db, wsID, crewID, leadID, "ENG-1", "IN_PROGRESS")
	h := NewJournalHandler(db, newTestLogger(), nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/journal?mission_id=ENG-1", nil)
	got, err := h.journalQuery(req, "ws_other")
	if err != nil {
		t.Fatal(err)
	}
	if got.MissionID != "ENG-1" {
		t.Fatalf("another workspace's identifier resolved to %q", got.MissionID)
	}
}
