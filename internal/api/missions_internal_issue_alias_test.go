package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestInternalMissionStartCannotStartIssueThroughMissionAlias(t *testing.T) {
	ih, workspace, crew, lead, _ := newInternalIssueHandler(t)
	h := NewInternalMissionHandler(ih.db, nil, nil, newTestLogger())
	issue := seedIssue(t, ih.db, workspace, crew, lead, "ENG-1", "BACKLOG")
	// PLANNING is deliberately chosen to pass the mission state gate on the
	// vulnerable alias. Type separation, not an incidental status, must deny it.
	execOrFatal(t, ih.db, `UPDATE missions SET status='PLANNING' WHERE id=?`, issue)
	for _, withCrew := range []bool{false, true} {
		t.Run(map[bool]string{false: "workspace", true: "crew"}[withCrew], func(t *testing.T) {
			target := "/api/v1/internal/missions/" + issue + "/start?workspace_id=" + workspace
			if withCrew {
				target += "&crew_id=" + crew
			}
			req := httptest.NewRequest(http.MethodPost, target, nil)
			req.SetPathValue("missionId", issue)
			rr := httptest.NewRecorder()
			h.Start(rr, req)
			if rr.Code != http.StatusNotFound {
				t.Fatalf("issue accepted by mission start: %d %s", rr.Code, rr.Body.String())
			}
			if got := missionStatus(t, h, issue); got != "PLANNING" {
				t.Fatalf("issue changed through alias: %s", got)
			}
		})
	}
}

func TestInternalMissionStartStillAcceptsActualMission(t *testing.T) {
	ih, workspace, _, lead, _ := newInternalIssueHandler(t)
	h := NewInternalMissionHandler(ih.db, nil, nil, newTestLogger())
	crew, mission := seedMissionCrewB(t, h, workspace, lead)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/missions/"+mission+"/start?workspace_id="+workspace+"&crew_id="+crew, nil)
	req.SetPathValue("missionId", mission)
	rr := httptest.NewRecorder()
	h.Start(rr, req)
	if rr.Code != http.StatusOK || missionStatus(t, h, mission) != "IN_PROGRESS" {
		t.Fatalf("real mission refused: %d %s", rr.Code, rr.Body.String())
	}
}

func TestInternalMissionStartDoesNotDispatchWhenCompareAndSetLoses(t *testing.T) {
	ih, workspace, _, lead, _ := newInternalIssueHandler(t)
	h := NewInternalMissionHandler(ih.db, nil, nil, newTestLogger())
	crew, mission := seedMissionCrewB(t, h, workspace, lead)
	// Simulate another transition winning after admission but before the write.
	// RAISE(IGNORE) makes the outer CAS report zero affected rows.
	execOrFatal(t, ih.db, `CREATE TRIGGER simulate_mission_start_race BEFORE UPDATE OF status ON missions
 WHEN OLD.id='`+mission+`' AND NEW.status='IN_PROGRESS'
 BEGIN UPDATE missions SET status='CANCELLED' WHERE id=OLD.id; SELECT RAISE(IGNORE); END`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/missions/"+mission+"/start?workspace_id="+workspace+"&crew_id="+crew, nil)
	req.SetPathValue("missionId", mission)
	rr := httptest.NewRecorder()
	h.Start(rr, req)
	if rr.Code != http.StatusConflict || missionStatus(t, h, mission) != "PLANNING" {
		t.Fatalf("lost CAS reported successful start: %d %s", rr.Code, rr.Body.String())
	}
}
