package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

func TestReplayOverridesInheritReplaceAndClear(t *testing.T) {
	for _, tc := range []struct {
		name, body, input, metadata string
		tags                        []string
	}{
		{"inherit", `{}`, `{"account":"old"}`, `{"source":"old"}`, []string{"old"}},
		{"replace", `{"inputs":{"account":"new"},"metadata":{"source":"new"},"tags":["new"]}`, `{"account":"new"}`, `{"source":"new"}`, []string{"new"}},
		{"clear", `{"inputs":{},"metadata":{},"tags":[]}`, `{}`, `{}`, nil},
		{"replace corrupt history", `{"inputs":{"account":"new"}}`, `{"account":"new"}`, `{"source":"old"}`, []string{"old"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, db, userID, wsID := runsHandlerRig(t)
			h.SetRunner(&stubRunner{output: "ok"})
			store := pipeline.NewRunStore(db)
			h.SetRunStore(store)
			seedVersionedPipeline(t, db, wsID, "replay_overrides", "overrides")
			seedRunRow(t, db, wsID, "replay_overrides", "overrides", "original", "completed")
			history := `{"account":"old"}`
			if tc.name == "replace corrupt history" {
				history = `{broken`
			}
			if _, err := db.Exec(`UPDATE pipeline_runs SET inputs_json=?,metadata_json=? WHERE id='original'`, history, `{"source":"old"}`); err != nil {
				t.Fatal(err)
			}
			if err := store.SetTags(t.Context(), wsID, "original", []string{"old"}); err != nil {
				t.Fatal(err)
			}
			req := withWorkspaceUser(httptest.NewRequest(http.MethodPost, "/replay", strings.NewReader(tc.body)), userID, wsID, "OWNER")
			req.SetPathValue("runId", "original")
			rr := httptest.NewRecorder()
			h.ReplayRun(rr, req)
			if rr.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
			}
			var result struct {
				RunID string `json:"run_id"`
			}
			if err := json.Unmarshal(rr.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			rec, err := store.Get(t.Context(), result.RunID)
			if err != nil {
				t.Fatal(err)
			}
			var actualInputs, expectedInputs map[string]any
			if err := json.Unmarshal([]byte(rec.InputsJSON), &actualInputs); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(tc.input), &expectedInputs); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(actualInputs, expectedInputs) || rec.MetadataJSON != tc.metadata {
				t.Fatalf("inputs=%s metadata=%s", rec.InputsJSON, rec.MetadataJSON)
			}
			tags, err := store.TagsFor(t.Context(), rec.ID)
			if err != nil || !reflect.DeepEqual(tags, tc.tags) {
				t.Fatalf("tags=%v want=%v err=%v", tags, tc.tags, err)
			}
			if !rec.IsReplay || rec.ReplayOf != "original" || rec.InvokingUserID != userID || rec.InvocationAuthority != pipeline.RoutineBatchAuthority {
				t.Fatalf("lost replay provenance or current authority: %+v", rec)
			}
		})
	}
}

func TestReplayOverrideValidationPrecedesExecution(t *testing.T) {
	for _, body := range []string{
		`{"inputs":null}`, `{"inputs":[]}`, `{"metadata":null}`, `{"metadata":[]}`,
		`{"tags":null}`, `{"tags":{}}`, `{"tags":[1]}`, `{"tags":[null]}`,
		`{"inputs":{"account":"invalid"}}`, `{"inputs":{}}`,
	} {
		t.Run(body, func(t *testing.T) {
			h, db, userID, wsID := runsHandlerRig(t)
			h.SetRunner(&stubRunner{output: "must not run"})
			h.SetRunStore(pipeline.NewRunStore(db))
			seedVersionedPipeline(t, db, wsID, "replay_validation", "validation")
			seedRunRow(t, db, wsID, "replay_validation", "validation", "original", "completed")
			definition := `{"dsl_version":"1.0","name":"validation","inputs":[{"name":"account","type":"string","widget":"select","options":["allowed"],"required":true}],"steps":[{"id":"step","type":"transform","transform":{"input":"ok","expression":"."}}]}`
			if _, err := db.Exec(`UPDATE pipelines SET definition_json=? WHERE id='replay_validation'`, definition); err != nil {
				t.Fatal(err)
			}
			req := withWorkspaceUser(httptest.NewRequest(http.MethodPost, "/replay", strings.NewReader(body)), userID, wsID, "OWNER")
			req.SetPathValue("runId", "original")
			rr := httptest.NewRecorder()
			h.ReplayRun(rr, req)
			if rr.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", rr.Code, rr.Body.String())
			}
			var count int
			if err := db.QueryRow(`SELECT COUNT(*) FROM pipeline_runs WHERE pipeline_id='replay_validation'`).Scan(&count); err != nil || count != 1 {
				t.Fatalf("invalid override created work: count=%d err=%v", count, err)
			}
		})
	}
}

func TestReplayOverridesValidateSelectedVersion(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		want       int
	}{
		{"pinned accepts inputs rejected by head", `{"pinned_version":1,"inputs":{"account":"outside-head-options"}}`, http.StatusOK},
		{"missing pin", `{"pinned_version":99,"inputs":{}}`, http.StatusConflict},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, db, userID, wsID := runsHandlerRig(t)
			store := pipeline.NewRunStore(db)
			h.SetRunStore(store)
			seedVersionedPipeline(t, db, wsID, "replay_selected", "selected")
			seedRunRow(t, db, wsID, "replay_selected", "selected", "original", "completed")
			definition := `{"dsl_version":"1.0","name":"selected","inputs":[{"name":"account","type":"string","widget":"select","options":["allowed"],"required":true}],"steps":[{"id":"step","type":"transform","transform":{"input":"head","expression":"."}}]}`
			if _, err := db.Exec(`UPDATE pipelines SET definition_json=? WHERE id='replay_selected'`, definition); err != nil {
				t.Fatal(err)
			}
			req := withWorkspaceUser(httptest.NewRequest(http.MethodPost, "/replay", strings.NewReader(tc.body)), userID, wsID, "OWNER")
			req.SetPathValue("runId", "original")
			rr := httptest.NewRecorder()
			h.ReplayRun(rr, req)
			if rr.Code != tc.want {
				t.Fatalf("status=%d want=%d body=%s", rr.Code, tc.want, rr.Body.String())
			}
			if tc.want != http.StatusOK {
				return
			}
			var result struct {
				RunID string `json:"run_id"`
			}
			if err := json.Unmarshal(rr.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			rec, err := store.Get(t.Context(), result.RunID)
			if err != nil || rec.PipelineVersion == nil || *rec.PipelineVersion != 1 {
				t.Fatalf("selected pin: record=%+v err=%v", rec, err)
			}
		})
	}
}
