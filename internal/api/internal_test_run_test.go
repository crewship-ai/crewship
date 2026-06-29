package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// InternalTestRun is the X-Internal-Token test_run the sidecar agent-authoring
// save loop forwards to (the public test_run is JWT-authed and rejected the
// sidecar's internal token). It reads workspace_id from the body (no wsCtx)
// and applies the same integration gate as the public test_run.

func TestInternalTestRun_RequiresWorkspaceID(t *testing.T) {
	h, _, _ := newPipelineHandlerForCRUDTest(t)
	h.SetRunner(&stubRunner{output: "ok"})

	// workspace_id omitted — internal routes can't read it from context.
	body := `{"definition":` + gateDef("slack") + `,"author_crew_id":"c1","sample_inputs":{}}`
	req := httptest.NewRequest("POST", "/api/v1/internal/pipelines/test_run", strings.NewReader(body))
	req.ContentLength = int64(len(body))
	rr := httptest.NewRecorder()
	h.InternalTestRun(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400; body=%s", rr.Code, rr.Body.String())
	}
}

func TestInternalTestRunGate_BlocksWhenIntegrationMissing(t *testing.T) {
	h, _, wsID := newPipelineHandlerForCRUDTest(t)
	runner := &stubRunner{output: "ok"}
	h.SetRunner(runner)
	crewID := seedCrewRow(t, h.db, "crew_itr", wsID, "Marketing", "marketing")
	_ = seedAgentRow(t, h.db, "ag_itr", wsID, crewID, "Eva", "eva", "LEAD")

	body := `{"workspace_id":"` + wsID + `","definition":` + gateDef("slack") +
		`,"author_crew_id":"` + crewID + `","sample_inputs":{}}`
	req := httptest.NewRequest("POST", "/api/v1/internal/pipelines/test_run", strings.NewReader(body))
	req.ContentLength = int64(len(body))
	rr := httptest.NewRecorder()
	h.InternalTestRun(rr, req)

	if rr.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status=%d want 422; body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "missing_integrations") {
		t.Errorf("body missing machine-readable missing_integrations: %s", rr.Body.String())
	}
	if runner.calls != 0 {
		t.Errorf("runner invoked %d times; test_run must not execute when blocked", runner.calls)
	}
}
