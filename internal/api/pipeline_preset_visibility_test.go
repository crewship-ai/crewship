package api

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

func TestPlanPresetVisibility(t *testing.T) {
	h, db, user, ws := scheduleHandlerRig(t)
	seedPipelineRow(t, db, ws, "preset_pipeline", "preset-pipeline")
	at := time.Now().UTC().Add(48 * time.Hour).Truncate(time.Hour)
	for _, tc := range []struct{ id, inputs string }{{"with_inputs", `{"message":"captured","count":0}`}, {"empty_inputs", `{}`}} {
		if _, err := db.Exec(`INSERT INTO pending_runs (id,workspace_id,pipeline_id,pipeline_slug,fire_at,inputs_json) VALUES (?,?,'preset_pipeline','preset-pipeline',?,?)`, tc.id, ws, at.Format(time.RFC3339), tc.inputs); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pipeline.NewScheduleStore(db).Save(t.Context(), pipeline.SaveScheduleInput{WorkspaceID: ws, Name: "Daily", TargetPipelineID: "preset_pipeline", CronExpr: "0 * * * *", Timezone: "UTC", Enabled: true, Inputs: map[string]any{"message": "recurring"}}); err != nil {
		t.Fatal(err)
	}
	for _, calendar := range []bool{false, true} {
		t.Run(map[bool]string{false: "pending", true: "calendar"}[calendar], func(t *testing.T) {
			url := "/pipelines/pending"
			if calendar {
				url = "/calendar?from=" + at.Add(-time.Hour).Format(time.RFC3339) + "&to=" + at.Add(time.Hour).Format(time.RFC3339)
			}
			req := withWorkspaceUser(httptest.NewRequest("GET", url, nil), user, ws, "OWNER")
			rr := httptest.NewRecorder()
			if calendar {
				h.RoutineCalendar(rr, req)
			} else {
				h.ListPendingRuns(rr, req)
			}
			if rr.Code != 200 {
				t.Fatal(rr.Code, rr.Body)
			}
			var rows []map[string]any
			if calendar {
				var body struct {
					Events []map[string]any `json:"events"`
				}
				if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				rows = body.Events
			} else if err := json.Unmarshal(rr.Body.Bytes(), &rows); err != nil {
				t.Fatal(err)
			}
			found := map[string]bool{}
			for _, row := range rows {
				inputs, ok := row["inputs"].(map[string]any)
				if !ok {
					t.Fatalf("inputs must be an object, got %v", row)
				}
				id := row["id"].(string)
				found[id] = true
				if id == "with_inputs" && (inputs["message"] != "captured" || inputs["count"] != float64(0)) {
					t.Fatalf("lost accepted inputs: %v", inputs)
				}
				if id == "empty_inputs" && len(inputs) != 0 {
					t.Fatalf("empty preset: %v", inputs)
				}
				if row["kind"] == "planned" && inputs["message"] != "recurring" {
					t.Fatalf("wrong recurring preset: %v", inputs)
				}
			}
			if !found["with_inputs"] || !found["empty_inputs"] {
				t.Fatalf("missing one-time plans: %v", found)
			}
			if calendar && len(rows) < 3 {
				t.Fatal("missing recurring occurrence")
			}
		})
	}
}
