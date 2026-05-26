package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
)

// TestSeedNuke_CascadesPipelines verifies seedNuke deletes pipeline
// webhooks, pipeline schedules, and routine rows alongside the rest
// of the workspace inventory.
//
// Regression: before this cascade, `crewship seed --nuke` cleared
// crews/agents/issues but left orphan rows in `pipelines`,
// `pipeline_schedules`, and `pipeline_webhooks`. After the re-seed,
// `crewship routine list` then showed routines whose `author_crew_id`
// pointed at a crew that no longer existed.
func TestSeedNuke_CascadesPipelines(t *testing.T) {
	saveCLIState(t)

	const wsID = "cabcdefghijklmnopqrs"

	// Slice (not map) so the test can assert relative ordering. The
	// production cascade documents "webhooks → schedules → pipelines"
	// because schedules and webhooks FK back to pipelines; a regression
	// that reorders the calls would corrupt the cascade silently if we
	// only checked existence.
	type seen struct {
		mu      sync.Mutex
		deletes []string
	}
	got := &seen{}

	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if r.Method == http.MethodDelete {
			got.mu.Lock()
			got.deletes = append(got.deletes, r.URL.Path)
			got.mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
			return
		}

		// One row per pipeline-shaped collection so the cascade has
		// something to delete. Every other GET returns [] so the
		// non-pipeline phases of seedNuke walk past quickly.
		switch r.URL.Path {
		case "/api/v1/workspaces/" + wsID + "/pipeline-webhooks":
			_, _ = w.Write([]byte(`[{"id":"wh_test"}]`))
		case "/api/v1/workspaces/" + wsID + "/pipeline-schedules":
			_, _ = w.Write([]byte(`[{"id":"psched_test"}]`))
		case "/api/v1/workspaces/" + wsID + "/pipelines":
			_, _ = w.Write([]byte(`[{"id":"pln_test","slug":"my-routine"}]`))
		default:
			_, _ = w.Write([]byte(`[]`))
		}
	}))
	defer mock.Close()

	cliCfg = &cli.CLIConfig{
		Token:     "fake-token",
		Workspace: wsID,
		Server:    mock.URL,
	}
	client := cli.NewClient(mock.URL, "fake-token", wsID)

	if err := seedNuke(context.Background(), client); err != nil {
		t.Fatalf("seedNuke: %v", err)
	}

	got.mu.Lock()
	actual := append([]string(nil), got.deletes...)
	got.mu.Unlock()

	// Existence pass: every expected DELETE must be present.
	wantDeletes := []string{
		"/api/v1/workspaces/" + wsID + "/pipeline-webhooks/wh_test",
		"/api/v1/workspaces/" + wsID + "/pipeline-schedules/psched_test",
		"/api/v1/workspaces/" + wsID + "/pipelines/my-routine",
	}
	idx := map[string]int{}
	for i, p := range actual {
		idx[p] = i
	}
	for _, want := range wantDeletes {
		if _, ok := idx[want]; !ok {
			t.Errorf("seedNuke did not DELETE %q\nactual DELETEs:\n  %s",
				want, strings.Join(actual, "\n  "))
		}
	}

	// Order pass: webhooks before schedules before the pipeline row
	// itself. FK relations make the reverse order impossible to
	// execute against a real Postgres without ON DELETE CASCADE.
	if t.Failed() {
		return
	}
	whIdx := idx["/api/v1/workspaces/"+wsID+"/pipeline-webhooks/wh_test"]
	schIdx := idx["/api/v1/workspaces/"+wsID+"/pipeline-schedules/psched_test"]
	plnIdx := idx["/api/v1/workspaces/"+wsID+"/pipelines/my-routine"]
	if !(whIdx < plnIdx && schIdx < plnIdx) {
		t.Errorf("cascade order broken: webhooks(%d), schedules(%d), pipelines(%d) — both triggers must precede the pipeline row\nactual DELETEs:\n  %s",
			whIdx, schIdx, plnIdx, strings.Join(actual, "\n  "))
	}
}
