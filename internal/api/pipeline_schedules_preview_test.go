package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// B9 (#2362): the next-five-fire-times preview is a stateless computation
// endpoint — the UI and the CLI both need to answer "what would this cron
// fire" for a cron expression that has not been saved yet (still being
// typed into the editor), so this takes cron_expr/timezone/count as query
// params rather than a saved schedule id.

func TestPreviewSchedule_MissingCronExpr_Returns400(t *testing.T) {
	h, _, userID, wsID := scheduleHandlerRig(t)
	req := withWorkspaceUser(httptest.NewRequest("GET",
		"/api/v1/workspaces/"+wsID+"/pipeline-schedules/preview?timezone=UTC", nil),
		userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.PreviewSchedule(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body=%s", rr.Code, rr.Body.String())
	}
}

func TestPreviewSchedule_InvalidCron_Returns400(t *testing.T) {
	h, _, userID, wsID := scheduleHandlerRig(t)
	req := withWorkspaceUser(httptest.NewRequest("GET",
		"/api/v1/workspaces/"+wsID+"/pipeline-schedules/preview?cron_expr=not+a+cron", nil),
		userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.PreviewSchedule(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body=%s", rr.Code, rr.Body.String())
	}
}

func TestPreviewSchedule_InvalidTimezone_Returns400(t *testing.T) {
	h, _, userID, wsID := scheduleHandlerRig(t)
	req := withWorkspaceUser(httptest.NewRequest("GET",
		"/api/v1/workspaces/"+wsID+"/pipeline-schedules/preview?cron_expr=0+9+*+*+*&timezone=Not/AZone", nil),
		userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.PreviewSchedule(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body=%s", rr.Code, rr.Body.String())
	}
}

func TestPreviewSchedule_DefaultsToFiveOccurrences(t *testing.T) {
	h, _, userID, wsID := scheduleHandlerRig(t)
	req := withWorkspaceUser(httptest.NewRequest("GET",
		"/api/v1/workspaces/"+wsID+"/pipeline-schedules/preview?cron_expr=0+9+*+*+*&timezone=Europe/Prague", nil),
		userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.PreviewSchedule(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rr.Code, rr.Body.String())
	}
	var out struct {
		CronExpr    string   `json:"cron_expr"`
		Timezone    string   `json:"timezone"`
		Occurrences []string `json:"occurrences"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v, body=%s", err, rr.Body.String())
	}
	if len(out.Occurrences) != 5 {
		t.Fatalf("got %d occurrences, want 5 (default): %v", len(out.Occurrences), out.Occurrences)
	}
	if out.Timezone != "Europe/Prague" {
		t.Fatalf("timezone = %q, want Europe/Prague", out.Timezone)
	}
}

func TestPreviewSchedule_CountCappedAt20(t *testing.T) {
	h, _, userID, wsID := scheduleHandlerRig(t)
	req := withWorkspaceUser(httptest.NewRequest("GET",
		"/api/v1/workspaces/"+wsID+"/pipeline-schedules/preview?cron_expr=*+*+*+*+*&count=500", nil),
		userID, wsID, "OWNER")
	rr := httptest.NewRecorder()
	h.PreviewSchedule(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rr.Code, rr.Body.String())
	}
	var out struct {
		Occurrences []string `json:"occurrences"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out.Occurrences) != 20 {
		t.Fatalf("got %d occurrences, want capped at 20", len(out.Occurrences))
	}
}
