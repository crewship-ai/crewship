package pipeline

import (
	"errors"
	"strings"
	"testing"
)

func TestParse_RoundTrip(t *testing.T) {
	src := `{
		"dsl_version": "1.0",
		"name": "email-fetch-summarize",
		"description": "Fetch and summarize.",
		"inputs": [
			{ "name": "since", "type": "string", "required": false, "default": "yesterday" }
		],
		"steps": [
			{
				"id": "fetch",
				"type": "agent_run",
				"complexity": "trivial",
				"agent_slug": "email-reader",
				"prompt": "Fetch emails since {{ inputs.since }}"
			},
			{
				"id": "summarize",
				"type": "agent_run",
				"complexity": "moderate",
				"agent_slug": "summarizer",
				"prompt": "Summarize: {{ steps.fetch.output }}"
			}
		]
	}`
	dsl, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if dsl.Name != "email-fetch-summarize" {
		t.Errorf("name: got %q", dsl.Name)
	}
	if len(dsl.Steps) != 2 {
		t.Fatalf("steps: got %d, want 2", len(dsl.Steps))
	}
	if dsl.Steps[0].Type != StepAgentRun {
		t.Errorf("step[0].type: got %q", dsl.Steps[0].Type)
	}
	if dsl.Steps[1].Complexity != ComplexityModerate {
		t.Errorf("step[1].complexity: got %q", dsl.Steps[1].Complexity)
	}
	// Step.Raw should be populated with the original bytes for forward compat.
	if len(dsl.Steps[0].Raw) == 0 {
		t.Error("step[0].Raw should be populated for forward-compat re-decoding")
	}
}

func TestValidate_HappyPath(t *testing.T) {
	dsl := &DSL{
		DSLVersion: "1.0",
		Name:       "demo",
		Inputs:     []InputSpec{{Name: "x", Type: "string"}},
		Steps: []Step{
			{ID: "a", Type: StepAgentRun, AgentSlug: "agent-a", Prompt: "do {{ inputs.x }}"},
			{ID: "b", Type: StepAgentRun, AgentSlug: "agent-b", Prompt: "after {{ steps.a.output }}"},
		},
	}
	agentSlugs := map[string]struct{}{"agent-a": {}, "agent-b": {}}
	if err := Validate(dsl, agentSlugs, nil); err != nil {
		t.Fatalf("expected valid, got: %v", err)
	}
}

func TestValidate_RejectsUnknownAgentSlug(t *testing.T) {
	dsl := &DSL{
		Name: "demo",
		Steps: []Step{
			{ID: "a", Type: StepAgentRun, AgentSlug: "ghost", Prompt: "x"},
		},
	}
	agentSlugs := map[string]struct{}{"agent-a": {}}
	err := Validate(dsl, agentSlugs, nil)
	if err == nil || !strings.Contains(err.Error(), "unknown agent_slug") {
		t.Errorf("expected unknown-agent error, got %v", err)
	}
}

func TestValidate_RejectsForwardTemplateRef(t *testing.T) {
	// Step "a" templates {{ steps.b.output }} but b runs AFTER a.
	dsl := &DSL{
		Name: "demo",
		Steps: []Step{
			{ID: "a", Type: StepAgentRun, AgentSlug: "agent", Prompt: "{{ steps.b.output }}"},
			{ID: "b", Type: StepAgentRun, AgentSlug: "agent", Prompt: "x"},
		},
	}
	err := Validate(dsl, map[string]struct{}{"agent": {}}, nil)
	if err == nil || !strings.Contains(err.Error(), "hasn't run yet") {
		t.Errorf("expected forward-ref error, got %v", err)
	}
}

func TestValidate_RejectsDuplicateStepID(t *testing.T) {
	dsl := &DSL{
		Name: "demo",
		Steps: []Step{
			{ID: "a", Type: StepAgentRun, AgentSlug: "agent", Prompt: "x"},
			{ID: "a", Type: StepAgentRun, AgentSlug: "agent", Prompt: "y"},
		},
	}
	err := Validate(dsl, map[string]struct{}{"agent": {}}, nil)
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("expected duplicate-id error, got %v", err)
	}
}

func TestValidate_RejectsUnsupportedStepType(t *testing.T) {
	// Truly unknown step type — http/code/wait/transform are now
	// supported, so use a synthetic name guaranteed not to clash.
	dsl := &DSL{
		Name: "demo",
		Steps: []Step{
			{ID: "a", Type: "totally_made_up_step_type"},
		},
	}
	err := Validate(dsl, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "unsupported type") {
		t.Errorf("expected unsupported-type error, got %v", err)
	}
}

func TestValidate_RejectsSelfRecursion(t *testing.T) {
	dsl := &DSL{
		Name: "demo",
		Steps: []Step{
			{ID: "a", Type: StepCallPipeline, PipelineSlug: "demo"},
		},
	}
	err := Validate(dsl, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "calls itself") {
		t.Errorf("expected self-recursion error, got %v", err)
	}
}

func TestValidate_RejectsBadComplexity(t *testing.T) {
	dsl := &DSL{
		Name: "demo",
		Steps: []Step{
			{ID: "a", Type: StepAgentRun, AgentSlug: "agent", Prompt: "x", Complexity: "genius"},
		},
	}
	err := Validate(dsl, map[string]struct{}{"agent": {}}, nil)
	if err == nil || !strings.Contains(err.Error(), "complexity") {
		t.Errorf("expected complexity error, got %v", err)
	}
}

func TestValidate_RejectsUnsupportedDSLVersion(t *testing.T) {
	dsl := &DSL{
		DSLVersion: "2.0",
		Name:       "demo",
		Steps:      []Step{{ID: "a", Type: StepAgentRun, AgentSlug: "agent", Prompt: "x"}},
	}
	err := Validate(dsl, map[string]struct{}{"agent": {}}, nil)
	if err == nil || !strings.Contains(err.Error(), "DSL version") {
		t.Errorf("expected DSL-version error, got %v", err)
	}
}

func TestCycleDetect_FlagsCycle(t *testing.T) {
	// A → B → A via call_pipeline. CycleDetect must catch it.
	a := &DSL{
		Name:  "a",
		Steps: []Step{{ID: "s1", Type: StepCallPipeline, PipelineSlug: "b"}},
	}
	b := &DSL{
		Name:  "b",
		Steps: []Step{{ID: "s1", Type: StepCallPipeline, PipelineSlug: "a"}},
	}
	resolver := func(slug string) (*DSL, error) {
		switch slug {
		case "a":
			return a, nil
		case "b":
			return b, nil
		}
		return nil, errors.New("not found")
	}
	err := CycleDetect(a, resolver)
	if err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Errorf("expected cycle error, got %v", err)
	}
}

func TestCycleDetect_NoCycle(t *testing.T) {
	// A → B, B has no call_pipeline. No cycle.
	a := &DSL{
		Name:  "a",
		Steps: []Step{{ID: "s1", Type: StepCallPipeline, PipelineSlug: "b"}},
	}
	b := &DSL{
		Name:  "b",
		Steps: []Step{{ID: "s1", Type: StepAgentRun, AgentSlug: "agent", Prompt: "x"}},
	}
	resolver := func(slug string) (*DSL, error) {
		if slug == "b" {
			return b, nil
		}
		return nil, errors.New("not found")
	}
	if err := CycleDetect(a, resolver); err != nil {
		t.Errorf("unexpected cycle: %v", err)
	}
}

func TestRender_Inputs(t *testing.T) {
	out := Render("hello {{ inputs.name }}", RenderContext{
		Inputs: map[string]any{"name": "world"},
	})
	if out != "hello world" {
		t.Errorf("got %q", out)
	}
}

func TestRender_StepOutput(t *testing.T) {
	out := Render("after {{ steps.a.output }}", RenderContext{
		StepOutputs: map[string]string{"a": "RESULT"},
	})
	if out != "after RESULT" {
		t.Errorf("got %q", out)
	}
}

func TestRender_StepOutputJSONPath(t *testing.T) {
	out := Render("count={{ steps.fetch.output.total }}", RenderContext{
		StepOutputs: map[string]string{"fetch": `{"total": 42, "name": "x"}`},
	})
	if out != "count=42" {
		t.Errorf("got %q", out)
	}
}

func TestRender_UnknownRefRendersEmpty(t *testing.T) {
	// Validator should have caught this at save time, but the
	// renderer must be robust at runtime in case data shapes drift.
	out := Render("x={{ inputs.missing }}-end", RenderContext{
		Inputs: map[string]any{"present": 1},
	})
	if out != "x=-end" {
		t.Errorf("got %q", out)
	}
}

func TestRender_NestedInputObject(t *testing.T) {
	out := Render("name={{ inputs.user.name }}", RenderContext{
		Inputs: map[string]any{"user": map[string]any{"name": "alice"}},
	})
	if out != "name=alice" {
		t.Errorf("got %q", out)
	}
}

func TestRender_EnvAllowed(t *testing.T) {
	out := Render("crew={{ env.author_crew_name }}", RenderContext{
		Env: map[string]string{"author_crew_name": "Marketing"},
	})
	if out != "crew=Marketing" {
		t.Errorf("got %q", out)
	}
}

// Validation breadth tests — verify that validateTemplatesInStep
// walks ALL template-bearing fields, not just Prompt + NestedInputs.
// The bug fixed in the routines stabilization commit was that
// templates in step.HTTP.URL/Body/Headers, step.Wait.Until,
// step.Code.Code, step.Transform.*, and step.If silently passed save
// and crashed at runtime. These tests pin the behaviour so future
// step-type additions remember to add their template fields to the
// validator.

func TestValidate_RejectsBadTemplateInHTTPUrl(t *testing.T) {
	dsl := &DSL{
		Name: "demo",
		Steps: []Step{
			{
				ID:   "fetch",
				Type: StepHTTP,
				HTTP: &HTTPStep{Method: "GET", URL: "{{ inputs.does_not_exist }}/path"},
			},
		},
	}
	if err := Validate(dsl, nil, nil); err == nil ||
		!strings.Contains(err.Error(), "does_not_exist") {
		t.Errorf("expected unknown-input error in HTTP URL template, got %v", err)
	}
}

func TestValidate_RejectsBadTemplateInHTTPBody(t *testing.T) {
	dsl := &DSL{
		Name: "demo",
		Steps: []Step{
			{ID: "a", Type: StepAgentRun, AgentSlug: "x", Prompt: "ok"},
			{
				ID:   "post",
				Type: StepHTTP,
				HTTP: &HTTPStep{
					Method: "POST",
					URL:    "https://api.example.com",
					Body:   `{"echo":"{{ steps.never.output }}"}`,
				},
			},
		},
	}
	if err := Validate(dsl, nil, nil); err == nil ||
		!strings.Contains(err.Error(), "never") {
		t.Errorf("expected unknown-step error in HTTP body template, got %v", err)
	}
}

func TestValidate_RejectsBadTemplateInHTTPHeaders(t *testing.T) {
	dsl := &DSL{
		Name: "demo",
		Steps: []Step{
			{
				ID:   "auth",
				Type: StepHTTP,
				HTTP: &HTTPStep{
					Method:  "GET",
					URL:     "https://api.example.com",
					Headers: map[string]string{"X-Token": "{{ inputs.missing }}"},
				},
			},
		},
	}
	if err := Validate(dsl, nil, nil); err == nil ||
		!strings.Contains(err.Error(), "missing") {
		t.Errorf("expected unknown-input error in HTTP header template, got %v", err)
	}
}

func TestValidate_RejectsBadTemplateInIf(t *testing.T) {
	dsl := &DSL{
		Name: "demo",
		Steps: []Step{
			{
				ID:        "guarded",
				Type:      StepAgentRun,
				If:        "{{ inputs.gate_unknown }}",
				AgentSlug: "x",
				Prompt:    "ok",
			},
		},
	}
	if err := Validate(dsl, nil, nil); err == nil ||
		!strings.Contains(err.Error(), "gate_unknown") {
		t.Errorf("expected unknown-input error in If template, got %v", err)
	}
}

func TestValidate_RejectsBadTemplateInWaitUntil(t *testing.T) {
	dsl := &DSL{
		Name: "demo",
		Steps: []Step{
			{
				ID:   "wait",
				Type: StepWait,
				Wait: &WaitStep{Kind: "datetime", Until: "{{ inputs.no_such_time }}"},
			},
		},
	}
	if err := Validate(dsl, nil, nil); err == nil ||
		!strings.Contains(err.Error(), "no_such_time") {
		t.Errorf("expected unknown-input error in Wait.Until template, got %v", err)
	}
}

func TestValidate_RejectsBadTemplateInCodeBody(t *testing.T) {
	dsl := &DSL{
		Name: "demo",
		Steps: []Step{
			{
				ID:   "run_script",
				Type: StepCode,
				Code: &CodeStep{
					Runtime: "python",
					Code:    "print('{{ inputs.absent_var }}')",
				},
			},
		},
	}
	if err := Validate(dsl, nil, nil); err == nil ||
		!strings.Contains(err.Error(), "absent_var") {
		t.Errorf("expected unknown-input error in Code body template, got %v", err)
	}
}

func TestValidate_RejectsBadTemplateInTransformExpression(t *testing.T) {
	dsl := &DSL{
		Name: "demo",
		Steps: []Step{
			{ID: "fetch", Type: StepAgentRun, AgentSlug: "x", Prompt: "ok"},
			{
				ID:   "shape",
				Type: StepTransform,
				Transform: &TransformStep{
					Input:      "{{ steps.fetch.output }}",
					Expression: ".{{ steps.unknown.output }}",
				},
			},
		},
	}
	if err := Validate(dsl, nil, nil); err == nil ||
		!strings.Contains(err.Error(), "unknown") {
		t.Errorf("expected unknown-step error in Transform template, got %v", err)
	}
}

// Positive control: known good templates across all fields pass.
func TestValidate_AcceptsValidTemplatesAcrossAllFields(t *testing.T) {
	dsl := &DSL{
		Name: "demo",
		Inputs: []InputSpec{
			{Name: "url", Type: "string"},
			{Name: "gate", Type: "boolean"},
			{Name: "until_ts", Type: "string"},
			{Name: "echo", Type: "string"},
		},
		Steps: []Step{
			{ID: "fetch", Type: StepHTTP, HTTP: &HTTPStep{
				Method: "GET", URL: "{{ inputs.url }}",
				Headers: map[string]string{"X-Echo": "{{ inputs.echo }}"},
			}},
			{ID: "guarded", Type: StepAgentRun, If: "{{ inputs.gate }}",
				AgentSlug: "x", Prompt: "see {{ steps.fetch.output }}"},
			{ID: "wait_until", Type: StepWait, Wait: &WaitStep{
				Kind: "datetime", Until: "{{ inputs.until_ts }}",
			}},
		},
	}
	if err := Validate(dsl, nil, nil); err != nil {
		t.Errorf("happy-path multi-field templates rejected: %v", err)
	}
}
