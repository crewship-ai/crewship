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

	type seen struct {
		mu      sync.Mutex
		deletes map[string]bool
	}
	got := &seen{deletes: map[string]bool{}}

	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		// Record every DELETE so the test can assert the cascade order.
		if r.Method == http.MethodDelete {
			got.mu.Lock()
			got.deletes[r.URL.Path] = true
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

	wantDeletes := []string{
		"/api/v1/workspaces/" + wsID + "/pipeline-webhooks/wh_test",
		"/api/v1/workspaces/" + wsID + "/pipeline-schedules/psched_test",
		"/api/v1/workspaces/" + wsID + "/pipelines/my-routine",
	}
	got.mu.Lock()
	defer got.mu.Unlock()
	for _, want := range wantDeletes {
		if !got.deletes[want] {
			var keys []string
			for k := range got.deletes {
				keys = append(keys, k)
			}
			t.Errorf("seedNuke did not DELETE %q\nactual DELETEs: %s",
				want, strings.Join(keys, "\n  "))
		}
	}
}
