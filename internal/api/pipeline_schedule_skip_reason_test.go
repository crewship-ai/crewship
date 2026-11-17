package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestScheduleSkipReasonAPIHistoricalReadAndServerOwned(t *testing.T) {
	h, db, user, ws := scheduleHandlerRig(t)
	seedPipelineRow(t, db, ws, "skip-pipeline", "skip-pipeline")
	rr := httptest.NewRecorder()
	h.CreateSchedule(rr, withWorkspaceUser(httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"target_pipeline_slug":"skip-pipeline","cron_expr":"* * * * *","enabled":false,"last_skip_reason":"capacity"}`)), user, ws, "OWNER"))
	if rr.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rr.Code, rr.Body)
	}
	var created map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	id := created["id"].(string)
	if _, ok := created["last_skip_reason"]; ok {
		t.Fatalf("caller wrote server-owned outcome: %+v", created)
	}
	for _, reason := range []string{"capacity", "catchup", "inactive", "busy", ""} {
		if _, err := db.Exec(`UPDATE pipeline_schedules SET last_status='SKIPPED',last_skip_reason=? WHERE id=?`, reason, id); err != nil {
			t.Fatal(err)
		}
		rr := httptest.NewRecorder()
		h.ListSchedules(rr, withWorkspaceUser(httptest.NewRequest(http.MethodGet, "/", nil), user, ws, "OWNER"))
		if rr.Code != http.StatusOK {
			t.Fatalf("list: %d %s", rr.Code, rr.Body)
		}
		var rows []map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &rows); err != nil || len(rows) != 1 {
			t.Fatalf("list: %v %s", err, rr.Body)
		}
		if got, _ := rows[0]["last_skip_reason"].(string); got != reason {
			t.Errorf("wire skip reason=%q want%q", got, reason)
		}
		if reason == "" {
			if _, ok := rows[0]["last_skip_reason"]; ok {
				t.Error("legacy empty reason must be omitted")
			}
		}
		rr = patchPlan(t, h, user, ws, id, `{"name":"renamed","last_skip_reason":"forged"}`)
		if rr.Code != http.StatusOK {
			t.Fatalf("update: %d %s", rr.Code, rr.Body)
		}
		var updated map[string]any
		if err := json.Unmarshal(rr.Body.Bytes(), &updated); err != nil {
			t.Fatal(err)
		}
		if got, _ := updated["last_skip_reason"].(string); got != reason {
			t.Errorf("update changed or omitted historical reason: got%q want%q", got, reason)
		}
	}
}
