package api

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

func TestPlanPresetVisibility(t *testing.T) {
	h, db, user, ws := scheduleHandlerRig(t)
	seedPipelineRow(t, db, ws, "preset_pipeline", "preset-pipeline")
	at := time.Now().UTC().Add(48 * time.Hour).Truncate(time.Hour)
	for _, tc := range []struct{ id, inputs string }{{"with_inputs", `{"message":"captured","count":0}`}, {"empty_inputs", `{}`}, {"sensitive_inputs", `{"api_key":"private","file":{"name":"invoice.pdf","content":"private bytes"},"nested":{"secret":"private"},"message":"ghp_` + strings.Repeat("x", 36) + `"}`}} {
		if _, err := db.ExecContext(t.Context(), `INSERT INTO pending_runs (id,workspace_id,pipeline_id,pipeline_slug,fire_at,inputs_json) VALUES (?,?,'preset_pipeline','preset-pipeline',?,?)`, tc.id, ws, at.Format(time.RFC3339), tc.inputs); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pipeline.NewScheduleStore(db).Save(t.Context(), pipeline.SaveScheduleInput{WorkspaceID: ws, Name: "Daily", TargetPipelineID: "preset_pipeline", CronExpr: "0 * * * *", Timezone: "UTC", Enabled: true, Inputs: map[string]any{"message": "recurring", "api_key": "private", "nested": map[string]any{"secret": "private"}, "file": map[string]any{"content": "private bytes"}}}); err != nil {
		t.Fatal(err)
	}
	for _, calendar := range []bool{false, true} {
		t.Run(map[bool]string{false: "pending", true: "calendar"}[calendar], func(t *testing.T) {
			url := "/pipelines/pending"
			if calendar {
				url = "/calendar?from=" + at.Add(-time.Hour).Format(time.RFC3339) + "&to=" + at.Add(time.Hour).Format(time.RFC3339)
			}
			req := withWorkspaceUser(httptest.NewRequest("GET", url, nil), user, ws, "VIEWER")
			rr := httptest.NewRecorder()
			if calendar {
				h.RoutineCalendar(rr, req)
			} else {
				h.ListPendingRuns(rr, req)
			}
			if rr.Code != 200 {
				t.Fatal(rr.Code, rr.Body)
			}
			if strings.Contains(rr.Body.String(), "private") || strings.Contains(rr.Body.String(), "ghp_") {
				t.Fatal("sensitive content exposed on the wire")
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
			foundPlanned := false
			for _, row := range rows {
				inputs, ok := row["inputs"].(map[string]any)
				if !ok {
					t.Fatalf("inputs must be an object, got %v", row)
				}
				id := row["id"].(string)
				found[id] = true
				if id == "sensitive_inputs" || row["kind"] == "planned" {
					keys := []string{"api_key"}
					if id == "sensitive_inputs" {
						keys = append(keys, "message")
					}
					for _, key := range keys {
						if v, ok := inputs[key].(map[string]any); !ok || v["type"] != "redacted" {
							t.Fatalf("unredacted %s", key)
						}
					}
					if v, ok := inputs["file"].(map[string]any); !ok || v["type"] != "file" || len(v) != 1 {
						t.Fatal("file content exposed")
					}
					if v, ok := inputs["nested"].(map[string]any); !ok || len(v) != 0 {
						t.Fatal("nested content exposed")
					}
				}
				if id == "with_inputs" && (inputs["message"] != "captured" || inputs["count"] != float64(0)) {
					t.Fatalf("lost accepted inputs: %v", inputs)
				}
				if id == "empty_inputs" && len(inputs) != 0 {
					t.Fatalf("empty preset: %v", inputs)
				}
				if row["kind"] == "planned" {
					foundPlanned = true
				}
				if row["kind"] == "planned" && inputs["message"] != "recurring" {
					t.Fatalf("wrong recurring preset: %v", inputs)
				}
			}
			if !found["with_inputs"] || !found["empty_inputs"] || !found["sensitive_inputs"] {
				t.Fatalf("missing one-time plans: %v", found)
			}
			if calendar && !foundPlanned {
				t.Fatal("missing recurring occurrence")
			}
		})
	}
}

func TestPlanPresetFilePrefixNormalization(t *testing.T) {
	for _, prefix := range []string{"data:", "file:", "blob:"} {
		t.Run(prefix, func(t *testing.T) {
			raw, err := json.Marshal(map[string]any{"message": " \t\u200b" + prefix[:2] + "\u200b" + prefix[2:] + "private-content"})
			if err != nil {
				t.Fatal(err)
			}
			inputs, err := planPresetInputs(string(raw))
			if err != nil {
				t.Fatal(err)
			}
			marker, ok := inputs["message"].(map[string]any)
			if !ok || len(marker) != 1 || marker["type"] != "file" {
				t.Fatal("file payload was not replaced with a marker")
			}
		})
	}
}
