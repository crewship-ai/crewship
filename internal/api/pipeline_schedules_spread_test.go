package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPipelineSchedules_Spread_CreateUpdateDisplay(t *testing.T) {
	h, db, userID, wsID := scheduleHandlerRig(t)
	seedPipelineRow(t, db, wsID, "pln_sp", "spread-routine")

	post := func(body string) (*httptest.ResponseRecorder, scheduleResponse) {
		req := withWorkspaceUser(httptest.NewRequest("POST", "/api/v1/workspaces/"+wsID+"/pipeline-schedules",
			strings.NewReader(body)), userID, wsID, "OWNER")
		rr := httptest.NewRecorder()
		h.CreateSchedule(rr, req)
		var out scheduleResponse
		_ = json.Unmarshal(rr.Body.Bytes(), &out)
		return rr, out
	}
	patch := func(id, body string) (*httptest.ResponseRecorder, scheduleResponse) {
		req := withWorkspaceUser(httptest.NewRequest("PATCH", "/api/v1/workspaces/"+wsID+"/pipeline-schedules/"+id,
			strings.NewReader(body)), userID, wsID, "OWNER")
		req.SetPathValue("scheduleId", id)
		rr := httptest.NewRecorder()
		h.UpdateSchedule(rr, req)
		var out scheduleResponse
		_ = json.Unmarshal(rr.Body.Bytes(), &out)
		return rr, out
	}

	// Default: no spread, nothing new in the payload.
	rr, plain := post(`{"cron_expr":"0 * * * *","target_pipeline_slug":"spread-routine"}`)
	if rr.Code != http.StatusCreated || plain.SpreadSeconds != 0 || plain.NextOccurrenceAt != nil {
		t.Fatalf("default create: %d %s", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), "spread_") {
		t.Fatalf("a schedule without a spread must not grow spread fields: %s", rr.Body.String())
	}

	// Validation at create: negative and >= interval are 400.
	for _, body := range []string{
		`{"cron_expr":"0 * * * *","target_pipeline_slug":"spread-routine","spread_seconds":-1}`,
		`{"cron_expr":"0 * * * *","target_pipeline_slug":"spread-routine","spread_seconds":3600}`,
		`{"cron_expr":"* * * * *","target_pipeline_slug":"spread-routine","spread_seconds":60}`,
	} {
		if rr, _ := post(body); rr.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400 (%s)", body, rr.Code, rr.Body.String())
		}
	}

	rr, sp := post(`{"cron_expr":"0 * * * *","target_pipeline_slug":"spread-routine","spread_seconds":600}`)
	if rr.Code != http.StatusCreated || sp.SpreadSeconds != 600 {
		t.Fatalf("create with spread: %d %s", rr.Code, rr.Body.String())
	}
	if sp.SpreadOffsetSeconds < 0 || sp.SpreadOffsetSeconds >= 600 {
		t.Fatalf("offset %d outside [0,600)", sp.SpreadOffsetSeconds)
	}
	if sp.NextRunAt == nil || sp.NextOccurrenceAt == nil ||
		!sp.NextRunAt.Equal(sp.NextOccurrenceAt.Add(time.Duration(sp.SpreadOffsetSeconds)*time.Second)) {
		t.Fatalf("displayed next_run_at must be the nominal occurrence plus the offset: %+v", sp)
	}

	// Unrelated update keeps the spread; tightening the cron under it is 400; 0 clears.
	if rr, up := patch(sp.ID, `{"name":"renamed"}`); rr.Code != http.StatusOK || up.SpreadSeconds != 600 {
		t.Fatalf("rename must keep the spread: %d %s", rr.Code, rr.Body.String())
	}
	if rr, _ := patch(sp.ID, `{"cron_expr":"*/5 * * * *"}`); rr.Code != http.StatusBadRequest {
		t.Fatalf("a new cron shorter than the stored spread must be refused: %d %s", rr.Code, rr.Body.String())
	}
	if rr, _ := patch(sp.ID, `{"spread_seconds":300}`); rr.Code != http.StatusOK {
		t.Fatalf("shrinking the spread: %d %s", rr.Code, rr.Body.String())
	}
	rr, cleared := patch(sp.ID, `{"spread_seconds":0}`)
	if rr.Code != http.StatusOK || cleared.SpreadSeconds != 0 || cleared.NextOccurrenceAt != nil {
		t.Fatalf("explicit 0 must clear: %d %s", rr.Code, rr.Body.String())
	}
}
