package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strconv"
	"testing"
)

// GET /api/v1/missions follows the same S1 contract as /api/v1/issues: bare
// array body, ?limit=&offset=, totals in X-Total-Count / X-Limit / X-Offset,
// and ?q= searched on the server. The issues board reads this list for its
// header count, which is why "15 issues" turned into "50 issues" at 1 015.

func (r *covMHRig) seedTitledMission(t *testing.T, identifier, title, status string) string {
	t.Helper()
	return r.seedTitledMissionAt(t, identifier, title, status, "2026-06-01T10:00:00Z")
}

// seedTitledMissionAt pins created_at, the column the list orders by, so a
// test can say which rows a page holds and not only how many.
func (r *covMHRig) seedTitledMissionAt(t *testing.T, identifier, title, status, createdAt string) string {
	t.Helper()
	id := generateCUID()
	if _, err := r.db.Exec(`
		INSERT INTO missions (id, workspace_id, crew_id, lead_agent_id, trace_id, title,
		    status, number, identifier, priority, sort_order, mission_type, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, 1, ?, 'medium', 0, 'issue', ?, ?)`,
		id, r.wsID, r.crewID, r.leadID, "trace-"+id, title, status, identifier, createdAt, createdAt); err != nil {
		t.Fatalf("seed mission: %v", err)
	}
	return id
}

// missionTitles: missionResponse carries no identifier, so the title is the
// handle a test pins a page's rows by.
func missionTitles(page []missionResponse) []string {
	out := make([]string, 0, len(page))
	for _, m := range page {
		out = append(out, m.Title)
	}
	return out
}

func TestMissionListAll_PagesAndPublishesTotal(t *testing.T) {
	rig := newCovMHRig(t)
	for i := 1; i <= 5; i++ {
		rig.seedTitledMissionAt(t, "ENG-"+strconv.Itoa(i), "Mission "+strconv.Itoa(i), "PLANNING",
			"2026-06-0"+strconv.Itoa(i)+"T10:00:00Z")
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
	// Newest first: Mission 5, 4 are page one; offset 2 is Mission 3, 2.
	if got := missionTitles(page); !reflect.DeepEqual(got, []string{"Mission 3", "Mission 2"}) {
		t.Fatalf("page at offset 2 = %v, want [Mission 3, Mission 2]", got)
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

// Five missions created in the same second: ORDER BY created_at alone would
// let SQLite hand two pages the same row and drop another. m.id breaks the tie.
func TestMissionListAll_SameCreatedAtPagesCleanly(t *testing.T) {
	rig := newCovMHRig(t)
	for i := 1; i <= 5; i++ {
		rig.seedTitledMission(t, "ENG-"+strconv.Itoa(i), "Mission "+strconv.Itoa(i), "PLANNING")
	}
	seen := map[string]int{}
	for offset := 0; offset < 5; offset += 2 {
		rr := httptest.NewRecorder()
		rig.h.ListAll(rr, rig.get("/api/v1/missions?limit=2&offset="+strconv.Itoa(offset), "", ""))
		if rr.Code != http.StatusOK {
			t.Fatalf("offset %d: status = %d; body=%s", offset, rr.Code, rr.Body.String())
		}
		var page []missionResponse
		if err := json.Unmarshal(rr.Body.Bytes(), &page); err != nil {
			t.Fatalf("offset %d: unmarshal: %v", offset, err)
		}
		for _, id := range missionTitles(page) {
			seen[id]++
		}
	}
	ids := make([]string, 0, len(seen))
	for id, n := range seen {
		if n != 1 {
			t.Errorf("%s appeared on %d pages", id, n)
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	if !reflect.DeepEqual(ids, []string{"Mission 1", "Mission 2", "Mission 3", "Mission 4", "Mission 5"}) {
		t.Fatalf("pages covered %v, want every mission once", ids)
	}
}

// GET /api/v1/crews/{crewId}/missions is what `crewship mission list --crew`
// calls, and it follows the same contract: `q` narrows, the total is a header.
func TestMissionList_CrewScopedSearchAndTotal(t *testing.T) {
	rig := newCovMHRig(t)
	rig.seedTitledMissionAt(t, "ENG-1", "Harborlight launch page", "IN_PROGRESS", "2026-06-01T10:00:00Z")
	rig.seedTitledMissionAt(t, "ENG-2", "Harborlight README", "PLANNING", "2026-06-02T10:00:00Z")
	rig.seedTitledMissionAt(t, "OPS-1", "Incident runbook", "IN_PROGRESS", "2026-06-03T10:00:00Z")

	rr := httptest.NewRecorder()
	rig.h.List(rr, rig.get("/x?q=Harborlight&limit=1", rig.crewID, ""))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d; body=%s", rr.Code, rr.Body.String())
	}
	var page []missionResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &page); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got := missionTitles(page); !reflect.DeepEqual(got, []string{"Harborlight README"}) {
		t.Fatalf("page = %v, want the newest Harborlight mission (limit 1)", got)
	}
	if got := headerInt(t, rr, "X-Total-Count"); got != 2 {
		t.Fatalf("X-Total-Count = %d, want 2 (q narrows the count)", got)
	}
	if got := headerInt(t, rr, "X-Limit"); got != 1 {
		t.Fatalf("X-Limit = %d, want 1", got)
	}
}
