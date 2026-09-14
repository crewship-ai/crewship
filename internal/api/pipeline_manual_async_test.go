package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/crewship-ai/crewship/internal/pipeline"
)

func TestManualRunAsync_ReturnsDurableIdentityBeforeWorkCompletes(t *testing.T) {
	h, db, user, ws := runsHandlerRig(t)
	runner := newBlockingRunner()
	h.SetRunner(runner)
	h.SetRunStore(pipeline.NewRunStore(db))
	h.SetRunRegistry(pipeline.NewRunRegistry())
	lifecycle, cancel := context.WithCancel(context.Background())
	defer cancel()
	h.SetLifecycleContext(lifecycle)
	seedAgentRunPipeline(t, db, ws, "manual_async_pipeline", "manual-async")
	req := withWorkspaceUser(httptest.NewRequest("POST", "/run", strings.NewReader(`{"inputs":{}}`)), user, ws, "OWNER")
	req.SetPathValue("slug", "manual-async")
	requestContext, disconnect := context.WithCancel(req.Context())
	defer disconnect()
	req = req.WithContext(requestContext)
	req.Header.Set("Prefer", "respond-async")
	rr := httptest.NewRecorder()
	done := make(chan struct{})
	go func() { defer close(done); h.Run(rr, req) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatal("response waited for the worker")
	}
	if rr.Code != 202 {
		t.Fatalf("status %d: %s", rr.Code, rr.Body.String())
	}
	var response struct {
		RunID string `json:"run_id"`
	}
	json.Unmarshal(rr.Body.Bytes(), &response)
	var definition string
	if err := db.QueryRow(`SELECT executed_definition_json FROM pipeline_runs WHERE id=? AND workspace_id=?`, response.RunID, ws).Scan(&definition); err != nil || definition == "" {
		t.Fatalf("acknowledged before durable snapshot: %q %v", definition, err)
	}
	select {
	case <-runner.started:
	case <-time.After(3 * time.Second):
		t.Fatal("worker never started")
	}
	// N2: closing the browser must not cancel an already accepted run.
	disconnect()
	close(runner.release)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		var status string
		db.QueryRow(`SELECT status FROM pipeline_runs WHERE id=?`, response.RunID).Scan(&status)
		if status == "completed" {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("run did not complete")
}
