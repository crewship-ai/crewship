package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

// GET /api/v1/missions follows the same S1 contract as /api/v1/issues: bare
// array body, ?limit=&offset=, totals in X-Total-Count / X-Limit / X-Offset,
// and ?q= searched on the server. The issues board reads this list for its
// header count, which is why "15 issues" turned into "50 issues" at 1 015.

func (r *covMHRig) seedTitledMission(t *testing.T, identifier, title, status string) string {
	t.Helper()
	id := generateCUID()
	if _, err := r.db.Exec(`
		INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title,
		    status, number, identifier, priority, sort_order, mission_type, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, 1, ?, 'medium', 0, 'issue', datetime('now'), datetime('now'))`,
		id, r.wsID, r.crewID, r.leadID, "trace-"+id, title, status, identifier); err != nil {
		t.Fatalf("seed mission: %v", err)
	}
	return id
}

func TestMissionListAll_PagesAndPublishesTotal(t *testing.T) {
	rig := newCovMHRig(t)
	for i := 1; i <= 5; i++ {
		rig.seedTitledMission(t, "ENG-"+strconv.Itoa(i), "Mission "+strconv.Itoa(i), "PLANNING")
	}

	rr := httptest.NewRecorder()
	rig.h.ListAll(rr, rig.get("/api/v1/missions?limit=2&offset=2", "", ""))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rr.Code, rr.Body.String())
	}
	var page []missionResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &page); err != nil {
		t.Fatalf("body is not a bare array: %v; body=%s", err, rr.Body.String())
	}
	if len(page) != 2 {
		t.Fatalf("page len = %d, want 2", len(page))
	}
	if got := headerInt(t, rr, "X-Total-Count"); got != 5 {
		t.Fatalf("X-Total-Count = %d, want 5", got)
	}
	if got := headerInt(t, rr, "X-Limit"); got != 2 {
		t.Fatalf("X-Limit = %d, want 2", got)
	}
	if got := headerInt(t, rr, "X-Offset"); got != 2 {
		t.Fatalf("X-Offset = %d, want 2", got)
	}
}

func TestMissionListAll_SearchAndStatusReachTheCount(t *testing.T) {
	rig := newCovMHRig(t)
	rig.seedTitledMission(t, "ENG-1", "Harborlight launch page", "IN_PROGRESS")
	rig.seedTitledMission(t, "ENG-2", "Harborlight README", "PLANNING")
	rig.seedTitledMission(t, "OPS-1", "Incident runbook", "IN_PROGRESS")

	cases := map[string]int{
		"?q=Harborlight":                  2,
		"?q=OPS-1":                        1,
		"?status=IN_PROGRESS":             2,
		"?status=IN_PROGRESS&q=Harborlig": 1,
	}
	for query, want := range cases {
		rr := httptest.NewRecorder()
		rig.h.ListAll(rr, rig.get("/api/v1/missions"+query, "", ""))
		if rr.Code != http.StatusOK {
			t.Fatalf("%s: status = %d; body=%s", query, rr.Code, rr.Body.String())
		}
		var page []missionResponse
		if err := json.Unmarshal(rr.Body.Bytes(), &page); err != nil {
			t.Fatalf("%s: unmarshal: %v", query, err)
		}
		if len(page) != want {
			t.Fatalf("%s: page len = %d, want %d", query, len(page), want)
		}
		if got := headerInt(t, rr, "X-Total-Count"); got != want {
			t.Fatalf("%s: X-Total-Count = %d, want %d", query, got, want)
		}
	}
}
