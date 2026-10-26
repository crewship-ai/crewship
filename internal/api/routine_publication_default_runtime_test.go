package api

import (
	"encoding/json"
	"testing"
)

func TestPublishedRoutineGuestRunWithoutRuntimeCreatesNoWork(t *testing.T) {
	// Real authenticated publication and run routes, with no workflow runtime
	// installed. The fixture's provider value is inert synthetic test data.
	f := newServerPublicationHTTPFixture(t, true)
	execOrFatal(t, f.db, `INSERT INTO access_grants(id,member_id,resource_kind,routine_id,operation,created_by,created_at) VALUES('default-run-grant','publish-member','routine','publish-routine','run',?,'fixture')`, f.owner)
	f.call(f.publisherToken, "POST", f.path+"/publish", f.request(), 200)
	path := "/api/v1/workspaces/" + f.workspace + "/pipelines/publish-routine/run"
	for range 2 {
		response := f.call(f.publisherToken, "POST", path, map[string]any{"inputs": map[string]any{}}, 503)
		var body struct {
			Error string `json:"error"`
			Code  string `json:"code"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.Code != "restricted_runtime_unavailable" || body.Error != "private execution is not installed on this server" {
			t.Fatalf("missing actionable runtime refusal: %s", response.Body.String())
		}
		for _, table := range []string{"pipeline_runs", "chats", "access_attempts", "restricted_workflow_jobs", "work_items", "work_attempts"} {
			var count int
			if err := f.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil || count != 0 {
				t.Fatalf("refused admission created %s: count=%d err=%v", table, count, err)
			}
		}
	}
}
