package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// #5407 was reported from reviewing the response projection alone. The real
// schedule store already filters soft-deleted targets (#2573), including old
// schedules that escaped the routine deletion cascade. Exercise that complete
// read boundary so an absent projection is not mistaken for an exposed row.
func TestScheduleTargetAvailability_DeletedTargetExcludedFromList(t *testing.T) {
	for _, flag := range []bool{false, true} {
		name := "flag off"
		if flag {
			name = "flag on"
		}
		t.Run(name, func(t *testing.T) {
			h, db, owner, ws := scheduleHandlerRig(t)
			seedPipelineRow(t, db, ws, "pln_target", "target")
			if flag {
				mustExec(t, db, `INSERT INTO feature_flag_overrides(id,flag_id,workspace_id,enabled) VALUES('target-flag','ff_artifact_agent_access_modes',?,1)`, ws)
			}
			rr := httptest.NewRecorder()
			h.CreateSchedule(rr, asPerson(httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"cron_expr":"0 * * * *","target_pipeline_slug":"target","enabled":false}`)), owner, ws, "OWNER"))
			if rr.Code != http.StatusCreated {
				t.Fatalf("create: %d %s", rr.Code, rr.Body)
			}
			list := func() []map[string]any {
				t.Helper()
				rr := httptest.NewRecorder()
				h.ListSchedules(rr, withWorkspaceUser(httptest.NewRequest(http.MethodGet, "/", nil), owner, ws, "OWNER"))
				if rr.Code != http.StatusOK {
					t.Fatalf("list: %d %s", rr.Code, rr.Body)
				}
				var rows []map[string]any
				if err := json.Unmarshal(rr.Body.Bytes(), &rows); err != nil {
					t.Fatal(err)
				}
				return rows
			}
			live := list()
			if len(live) != 1 {
				t.Fatalf("live target schedule missing: %+v", live)
			}
			if flag && live[0]["artifact_access"] != "active" {
				t.Fatalf("live target state: %+v", live)
			}
			if !flag {
				if _, ok := live[0]["artifact_access"]; ok {
					t.Fatalf("flag off exposed artifact state: %+v", live)
				}
			}
			// Deliberately bypass the routine delete cascade to model a legacy row.
			mustExec(t, db, `UPDATE pipelines SET deleted_at=datetime('now') WHERE id='pln_target'`)
			var count int
			if err := db.QueryRow(`SELECT count(*) FROM pipeline_schedules WHERE deleted_at IS NULL AND target_pipeline_id='pln_target'`).Scan(&count); err != nil || count != 1 {
				t.Fatalf("legacy schedule fixture count=%d: %v", count, err)
			}
			if rows := list(); len(rows) != 0 {
				t.Fatalf("deleted target exposed a schedule: %+v", rows)
			}
		})
	}
}
