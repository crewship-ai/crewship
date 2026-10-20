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

// ODCH-RT-15: "When, in words". The preview takes a phrase= parameter,
// derives the cron through pipeline.ParseNaturalCron and answers with the
// cron it derived plus the next starts. cron_expr stays the explicit path.
func TestPreviewSchedule_Phrase(t *testing.T) {
	cases := []struct {
		name     string
		query    string
		wantCode int
		wantCron string
		wantN    int
	}{
		{"every day at 9", "phrase=every+day+at+9am&timezone=Europe/Prague&count=3", http.StatusOK, "0 9 * * *", 3},
		{"weekdays", "phrase=every+weekday+at+8:30", http.StatusOK, "30 8 * * 1-5", 5},
		{"unknown phrase", "phrase=whenever+the+moon+is+full", http.StatusBadRequest, "", 0},
		{"phrase and cron together", "phrase=every+day+at+9am&cron_expr=0+9+*+*+*", http.StatusBadRequest, "", 0},
		{"blank phrase and no cron", "phrase=+", http.StatusBadRequest, "", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, _, userID, wsID := scheduleHandlerRig(t)
			req := withWorkspaceUser(httptest.NewRequest("GET",
				"/api/v1/workspaces/"+wsID+"/pipeline-schedules/preview?"+tc.query, nil),
				userID, wsID, "OWNER")
			rr := httptest.NewRecorder()
			h.PreviewSchedule(rr, req)
			if rr.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d, body=%s", rr.Code, tc.wantCode, rr.Body.String())
			}
			if tc.wantCode != http.StatusOK {
				return
			}
			var out struct {
				CronExpr    string   `json:"cron_expr"`
				Phrase      string   `json:"phrase"`
				Occurrences []string `json:"occurrences"`
			}
			if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if out.CronExpr != tc.wantCron || len(out.Occurrences) != tc.wantN || out.Phrase == "" {
				t.Fatalf("got cron=%q phrase=%q n=%d, want cron=%q n=%d", out.CronExpr, out.Phrase, len(out.Occurrences), tc.wantCron, tc.wantN)
			}
		})
	}
}
