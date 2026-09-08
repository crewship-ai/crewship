package api

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"
)

func TestRoutineCalendar_PendingLimitAppliesInsideRequestedWindow(t *testing.T) {
	h, db, user, ws := runsHandlerRig(t)
	seedRunsPipeline(t, db, ws, "calendar_window", "calendar-window")
	insert := func(id, workspace, at string) {
		t.Helper()
		if _, err := db.Exec(`INSERT INTO pending_runs (id,workspace_id,pipeline_id,pipeline_slug,fire_at) VALUES (?,?,'calendar_window','calendar-window',?)`, id, workspace, at); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 51; i++ {
		insert(fmt.Sprintf("earlier_%d", i), ws, "2027-01-01T09:00:00Z")
	}
	insert("visible", ws, "2027-02-10T09:00:00Z")
	insert("foreign", "other-workspace", "2027-02-10T09:00:00Z")
	read := func() (int, bool) {
		t.Helper()
		req := withWorkspaceUser(httptest.NewRequest("GET", "/calendar?from=2027-02-01T00:00:00Z&to=2027-03-01T00:00:00Z", nil), user, ws, "OWNER")
		rr := httptest.NewRecorder()
		h.RoutineCalendar(rr, req)
		var data struct {
			Events []struct {
				ID string `json:"id"`
			} `json:"events"`
			Truncated bool `json:"truncated"`
		}
		if rr.Code != 200 {
			t.Fatal(rr.Body.String())
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &data); err != nil {
			t.Fatal(err)
		}
		for _, e := range data.Events {
			if e.ID == "foreign" {
				t.Fatal("cross-workspace pending run leaked")
			}
		}
		return len(data.Events), data.Truncated
	}
	if n, truncated := read(); n != 1 || truncated {
		t.Fatalf("later month missing: count=%d truncated=%v", n, truncated)
	}
	for i := 0; i < 1000; i++ {
		insert(fmt.Sprintf("busy_%d", i), ws, "2027-02-11T09:00:00Z")
	}
	if n, truncated := read(); n != 1000 || !truncated {
		t.Fatalf("dense month not reported: count=%d truncated=%v", n, truncated)
	}
}
