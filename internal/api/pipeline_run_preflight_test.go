package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// callPipelineParentDef is a one-step routine whose only step calls another
// routine by slug.
func callPipelineParentDef(name, targetSlug string) string {
	return `{"dsl_version":"1.0","name":"` + name + `","steps":[` +
		`{"id":"nested","type":"call_pipeline","pipeline_slug":"` + targetSlug + `"}]}`
}

// The live bypass, end to end. `run_routine` (the agent door) goes over HTTP
// to InternalRun and is refused when the target's declared credential is not
// in the vault. `call_pipeline` (the author door) ran the same routine
// in-process, straight to runDSL, with no gate at all — so a routine that
// could not be run directly could be run by being CALLED.
//
// The child here declares credentials_required: [stripe], which the vault
// cannot satisfy. Calling it must fail the parent run with the gate's own
// sentence, and the child's agent step must never execute.
func TestCallPipeline_RunsThroughTheSameCredentialGateAsInternalRun(t *testing.T) {
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	runner := &stubRunner{output: "ok"}
	h.SetRunner(runner)
	crewID := seedCrewRow(t, h.db, "crew_pf", wsID, "Payments", "payments")
	_ = seedAgentRow(t, h.db, "ag_pf", wsID, crewID, "Eva", "eva", "LEAD")

	// Child: unsatisfiable credential requirement. Directly runnable? No —
	// TestCredentialGate_NonAnthropicTypeStillBlocks pins that it 422s.
	seedPipelineWithAuthorCrew(t, h.db, wsID, "pipe_pf_child", "pf-child",
		credGateDef("stripe"), crewID)
	// Parent: declares nothing, just calls the child.
	seedPipelineWithAuthorCrew(t, h.db, wsID, "pipe_pf_parent", "pf-parent",
		callPipelineParentDef("pf-parent", "pf-child"), crewID)

	rr := httptest.NewRecorder()
	h.Run(rr, covPE2Req(t, "POST", "/x", `{"inputs":{}}`, userID, wsID, "pf-parent"))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (the parent dispatches; the STEP fails); body=%s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, "FAILED") {
		t.Fatalf("parent run did not fail on the gated nested call; body=%s", body)
	}
	if !strings.Contains(body, "stripe") {
		t.Errorf("the gate's reason must reach the run error so the author can act on it; body=%s", body)
	}
	if runner.calls != 0 {
		t.Errorf("runner invoked %d times — a gated nested routine must not execute", runner.calls)
	}
}

// The gate must bound dispatch, not forbid it: a nested routine whose
// preconditions are met still runs. Without this, "refuse every nested call"
// would pass the test above.
func TestCallPipeline_UngatedNestedRoutineStillRuns(t *testing.T) {
	h, userID, wsID := newPipelineHandlerForCRUDTest(t)
	runner := &stubRunner{output: "ok"}
	h.SetRunner(runner)
	crewID := seedCrewRow(t, h.db, "crew_pf2", wsID, "Payments", "payments")
	_ = seedAgentRow(t, h.db, "ag_pf2", wsID, crewID, "Eva", "eva", "LEAD")

	seedPipelineWithAuthorCrew(t, h.db, wsID, "pipe_pf2_child", "pf2-child",
		`{"dsl_version":"1.0","name":"pf2-child","steps":[`+
			`{"id":"a","type":"agent_run","agent_slug":"eva","prompt":"hi"}]}`, crewID)
	seedPipelineWithAuthorCrew(t, h.db, wsID, "pipe_pf2_parent", "pf2-parent",
		callPipelineParentDef("pf2-parent", "pf2-child"), crewID)

	rr := httptest.NewRecorder()
	h.Run(rr, covPE2Req(t, "POST", "/x", `{"inputs":{}}`, userID, wsID, "pf2-parent"))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), "FAILED") {
		t.Fatalf("an ungated nested routine must still run; body=%s", rr.Body.String())
	}
	if runner.calls == 0 {
		t.Error("nested routine never executed")
	}
}
