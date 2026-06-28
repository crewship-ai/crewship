package pipeline

import (
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// dsl_validate_agentless.go — the `agentless: true` token-zero guarantee.
//
// An agentless routine may only contain step kinds that never invoke an
// LLM (http, code, wait, transform). agent_run is the obvious reject;
// call_pipeline is rejected too because its target resolves by slug at
// RUNTIME — the referenced routine could gain an agent step after this
// one is saved, silently breaking the guarantee. eval.online with a
// non-zero sample_rate is rejected because online grading runs a grader
// AGENT against this routine's completed runs.
// ---------------------------------------------------------------------------

func agentlessProbeDSL() *DSL {
	return &DSL{
		DSLVersion: "1.0",
		Name:       "cost-probe",
		Agentless:  true,
		Inputs:     []InputSpec{{Name: "threshold", Type: "string"}},
		Steps: []Step{
			{ID: "fetch", Type: StepHTTP, HTTP: &HTTPStep{Method: "GET", URL: "https://billing.example.com/today"}},
			{ID: "judge", Type: StepCode, Code: &CodeStep{Runtime: "cel", Code: "inputs.threshold == \"hi\""}},
			{ID: "shape", Type: StepTransform, Transform: &TransformStep{Input: "{{ steps.judge.output }}", Expression: "."}},
		},
	}
}

func TestValidate_Agentless_AllowsNonLLMSteps(t *testing.T) {
	if err := Validate(agentlessProbeDSL(), nil, nil); err != nil {
		t.Fatalf("expected agentless http/code/transform routine to validate, got: %v", err)
	}
}

func TestValidate_Agentless_RejectsAgentRun(t *testing.T) {
	dsl := agentlessProbeDSL()
	dsl.Steps = append(dsl.Steps, Step{ID: "think", Type: StepAgentRun, AgentSlug: "analyst", Prompt: "summarize"})
	err := Validate(dsl, map[string]struct{}{"analyst": {}}, nil)
	if err == nil {
		t.Fatal("expected agent_run step inside agentless routine to be rejected")
	}
	if !strings.Contains(err.Error(), "agentless") {
		t.Errorf("error should name the agentless guarantee, got: %v", err)
	}
}

func TestValidate_Agentless_RejectsCallPipeline(t *testing.T) {
	dsl := agentlessProbeDSL()
	dsl.Steps = append(dsl.Steps, Step{ID: "nest", Type: StepCallPipeline, PipelineSlug: "other-routine"})
	err := Validate(dsl, nil, nil)
	if err == nil {
		t.Fatal("expected call_pipeline step inside agentless routine to be rejected")
	}
	if !strings.Contains(err.Error(), "agentless") {
		t.Errorf("error should name the agentless guarantee, got: %v", err)
	}
}

func TestValidate_Agentless_RejectsOnlineEvalSampling(t *testing.T) {
	dsl := agentlessProbeDSL()
	dsl.Eval = &EvalConfig{Online: &OnlineEvalConfig{SampleRate: 0.05, GraderAgentSlug: "grader"}}
	err := Validate(dsl, nil, nil)
	if err == nil {
		t.Fatal("expected eval.online sample_rate>0 inside agentless routine to be rejected")
	}
	if !strings.Contains(err.Error(), "agentless") {
		t.Errorf("error should name the agentless guarantee, got: %v", err)
	}
}

func TestValidate_Agentless_AllowsZeroSampleRateEval(t *testing.T) {
	dsl := agentlessProbeDSL()
	dsl.Eval = &EvalConfig{Online: &OnlineEvalConfig{SampleRate: 0}}
	if err := Validate(dsl, nil, nil); err != nil {
		t.Fatalf("sample_rate=0 disables grading — should be allowed, got: %v", err)
	}
}

func TestValidate_NonAgentless_KeepsAgentRunWorking(t *testing.T) {
	dsl := &DSL{
		Name: "demo",
		Steps: []Step{
			{ID: "a", Type: StepAgentRun, AgentSlug: "agent-a", Prompt: "x"},
		},
	}
	if err := Validate(dsl, map[string]struct{}{"agent-a": {}}, nil); err != nil {
		t.Fatalf("agentless=false must not change existing behaviour, got: %v", err)
	}
}

func TestParse_Agentless_RoundTrip(t *testing.T) {
	src := `{
		"dsl_version": "1.0",
		"name": "probe",
		"agentless": true,
		"steps": [
			{ "id": "t", "type": "transform", "transform": { "input": "true", "expression": "." } }
		]
	}`
	dsl, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !dsl.Agentless {
		t.Fatal("agentless flag should survive Parse")
	}
}
