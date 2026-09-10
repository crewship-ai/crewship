package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

func TestReplayPreservesPinWithoutContentLength(t *testing.T) {
	h, db, userID, wsID := runsHandlerRig(t)
	h.SetRunner(&stubRunner{output: "ok"})
	store := pipeline.NewRunStore(db)
	h.SetRunStore(store)
	seedVersionedPipeline(t, db, wsID, "replay_stream_pin", "stream-pin")
	seedRunRow(t, db, wsID, "replay_stream_pin", "stream-pin", "run_stream_original", "completed")
	req := withWorkspaceUser(httptest.NewRequest(http.MethodPost, "/replay", strings.NewReader(`{"pinned_version":1}`)), userID, wsID, "OWNER")
	req.ContentLength = -1
	req.SetPathValue("runId", "run_stream_original")
	rr := httptest.NewRecorder()
	h.ReplayRun(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
	}
	var result struct {
		RunID string `json:"run_id"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	record, err := store.Get(t.Context(), result.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if record.PipelineVersion == nil || *record.PipelineVersion != 1 {
		t.Fatalf("lost archive pin: %+v", record.PipelineVersion)
	}
}

func TestReplayRejectsCorruptInputsAndAmbiguousRequestBeforeDispatch(t *testing.T) {
	for _, tc := range []struct {
		name, history, body string
		want                int
	}{
		{"corrupt history", `{broken`, `{"pinned_version":1}`, http.StatusConflict},
		{"array history", `[1,2]`, `{}`, http.StatusConflict},
		{"trailing object", `{}`, `{"pinned_version":1}{"pinned_version":2}`, http.StatusBadRequest},
		{"trailing garbage", `{}`, `{"pinned_version":1}garbage`, http.StatusBadRequest},
		{"invalid stream", `{}`, `{broken`, http.StatusBadRequest},
		{"negative pin", `{}`, `{"pinned_version":-1}`, http.StatusBadRequest},
		{"zero pin", `{}`, `{"pinned_version":0}`, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, db, userID, wsID := runsHandlerRig(t)
			h.SetRunner(&stubRunner{output: "must not run"})
			h.SetRunStore(pipeline.NewRunStore(db))
			seedVersionedPipeline(t, db, wsID, "replay_integrity", "replay-integrity")
			seedRunRow(t, db, wsID, "replay_integrity", "replay-integrity", "run_integrity_original", "completed")
			if _, err := db.Exec(`UPDATE pipeline_runs SET inputs_json = ? WHERE id = ?`, tc.history, "run_integrity_original"); err != nil {
				t.Fatal(err)
			}
			req := withWorkspaceUser(httptest.NewRequest(http.MethodPost, "/replay", strings.NewReader(tc.body)), userID, wsID, "OWNER")
			req.ContentLength = -1
			req.SetPathValue("runId", "run_integrity_original")
			rr := httptest.NewRecorder()
			h.ReplayRun(rr, req)
			if rr.Code != tc.want {
				t.Fatalf("status %d, want %d: %s", rr.Code, tc.want, rr.Body.String())
			}
			var count int
			if err := db.QueryRow(`SELECT COUNT(*) FROM pipeline_runs WHERE pipeline_id = ?`, "replay_integrity").Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 1 {
				t.Fatalf("replay dispatched despite invalid evidence: %d records", count)
			}
		})
	}
}
