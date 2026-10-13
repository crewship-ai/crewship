package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

func TestManualScheduleHumanAuthority(t *testing.T) {
	for _, demote := range []bool{false, true} {
		t.Run(map[bool]string{false: "attributed-positive", true: "current-role-revoked"}[demote], func(t *testing.T) {
			h, db, user, workspace := scheduleHandlerRig(t)
			h.SetRunner(&stubRunner{output: "ok"})
			runs := pipeline.NewRunStore(db)
			h.SetRunStore(runs)
			seedVersionedPipeline(t, db, workspace, "manual-human-pipeline", "manual-human")
			saved, err := pipeline.NewScheduleStore(db).Save(t.Context(), pipeline.SaveScheduleInput{WorkspaceID: workspace, Name: "manual human", TargetPipelineID: "manual-human-pipeline", CronExpr: "0 8 * * *", Timezone: "UTC", Enabled: true})
			if err != nil {
				t.Fatal(err)
			}
			if demote {
				if _, err = db.Exec(`UPDATE workspace_members SET role='VIEWER',capabilities='[]' WHERE workspace_id=? AND user_id=?`, workspace, user); err != nil {
					t.Fatal(err)
				}
			}
			// Cached admission context cannot supply execution authority after demotion.
			req := withWorkspaceUser(httptest.NewRequest("POST", "/schedule/run", nil), user, workspace, "OWNER")
			req.SetPathValue("scheduleId", saved.ID)
			rr := httptest.NewRecorder()
			h.RunSchedule(rr, req)
			if demote {
				if rr.Code != http.StatusForbidden {
					t.Fatalf("revoked human schedule status=%d body=%s", rr.Code, rr.Body.String())
				}
				var count int
				if err := db.QueryRow(`SELECT count(*) FROM pipeline_runs WHERE pipeline_id=?`, "manual-human-pipeline").Scan(&count); err != nil {
					t.Fatal(err)
				}
				if count != 0 {
					t.Fatalf("revoked human created %d runs", count)
				}
				return
			}
			if rr.Code != http.StatusOK {
				t.Fatalf("positive=%d %s", rr.Code, rr.Body.String())
			}
			var result struct {
				RunID string `json:"run_id"`
			}
			if err := json.Unmarshal(rr.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			rec, err := runs.Get(t.Context(), result.RunID)
			if err != nil {
				t.Fatal(err)
			}
			if rec.InvokingUserID != user || rec.InvocationAuthority != pipeline.RoutineBatchAuthority {
				t.Fatalf("manual human lost actor/force-fire authority: user=%q authority=%q", rec.InvokingUserID, rec.InvocationAuthority)
			}
		})
	}
}
