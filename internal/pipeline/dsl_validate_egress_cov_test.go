package pipeline

import (
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// dsl_validate_egress.go — validateStepEgress.
//
// Table-driven walk over every step-shape branch: http (method/url/
// size limits), code (runtime/code/size), wait (approval/datetime/
// event kinds), transform (input/expression), and the unsupported-type
// default. Each invalid case asserts on the distinguishing fragment of
// the error so a future re-wording that drops the actionable detail
// fails loudly.
// ---------------------------------------------------------------------------

func TestValidateStepEgress_Branches(t *testing.T) {
	t.Parallel()

	bigCode := strings.Repeat("x", 1_000_001)

	cases := []struct {
		name    string
		step    Step
		wantErr string // "" = expect nil error
	}{
		// slug-typed bodies are validated elsewhere — pass through.
		{"agent_run passthrough", Step{ID: "s", Type: StepAgentRun}, ""},
		{"call_pipeline passthrough", Step{ID: "s", Type: StepCallPipeline}, ""},

		// --- http ---
		{"http missing body", Step{ID: "h", Type: StepHTTP}, "missing http body"},
		{"http missing method", Step{ID: "h", Type: StepHTTP, HTTP: &HTTPStep{URL: "https://x"}}, "missing method"},
		{"http invalid method", Step{ID: "h", Type: StepHTTP, HTTP: &HTTPStep{Method: "TRACE", URL: "https://x"}}, `method "TRACE" invalid`},
		{"http lowercase method ok", Step{ID: "h", Type: StepHTTP, HTTP: &HTTPStep{Method: "post", URL: "https://x"}}, ""},
		{"http missing url", Step{ID: "h", Type: StepHTTP, HTTP: &HTTPStep{Method: "GET"}}, "missing url"},
		{"http negative max bytes", Step{ID: "h", Type: StepHTTP, HTTP: &HTTPStep{Method: "GET", URL: "https://x", MaxResponseBytes: -1}}, "cannot be negative"},
		{"http max bytes too high", Step{ID: "h", Type: StepHTTP, HTTP: &HTTPStep{Method: "GET", URL: "https://x", MaxResponseBytes: 50_000_001}}, "too high"},
		{"http valid", Step{ID: "h", Type: StepHTTP, HTTP: &HTTPStep{Method: "DELETE", URL: "https://x", MaxResponseBytes: 1024}}, ""},

		// --- code ---
		{"code missing body", Step{ID: "c", Type: StepCode}, "missing code body"},
		{"code invalid runtime", Step{ID: "c", Type: StepCode, Code: &CodeStep{Runtime: "ruby", Code: "puts 1"}}, `runtime "ruby" invalid`},
		{"code missing code", Step{ID: "c", Type: StepCode, Code: &CodeStep{Runtime: "python"}}, "missing code"},
		{"code script too big", Step{ID: "c", Type: StepCode, Code: &CodeStep{Runtime: "bash", Code: bigCode}}, ">1MB"},
		{"code valid go", Step{ID: "c", Type: StepCode, Code: &CodeStep{Runtime: "go", Code: "package main"}}, ""},

		// --- wait ---
		{"wait missing body", Step{ID: "w", Type: StepWait}, "missing wait body"},
		{"wait approval missing prompt", Step{ID: "w", Type: StepWait, Wait: &WaitStep{Kind: "approval"}}, "missing approval_prompt"},
		{"wait approval valid", Step{ID: "w", Type: StepWait, Wait: &WaitStep{Kind: "approval", ApprovalPrompt: "ok?"}}, ""},
		{"wait datetime missing until", Step{ID: "w", Type: StepWait, Wait: &WaitStep{Kind: "datetime"}}, "missing until"},
		{"wait datetime valid", Step{ID: "w", Type: StepWait, Wait: &WaitStep{Kind: "datetime", Until: "2030-01-01T00:00:00Z"}}, ""},
		{"wait event missing type", Step{ID: "w", Type: StepWait, Wait: &WaitStep{Kind: "event"}}, "missing event_type"},
		{"wait event valid", Step{ID: "w", Type: StepWait, Wait: &WaitStep{Kind: "event", EventType: "deploy.done"}}, ""},
		{"wait invalid kind", Step{ID: "w", Type: StepWait, Wait: &WaitStep{Kind: "nap"}}, `kind "nap" invalid`},

		// --- transform ---
		{"transform missing body", Step{ID: "t", Type: StepTransform}, "missing transform body"},
		{"transform missing input", Step{ID: "t", Type: StepTransform, Transform: &TransformStep{Expression: ".x"}}, "missing input"},
		{"transform missing expression", Step{ID: "t", Type: StepTransform, Transform: &TransformStep{Input: "{{ steps.a.output }}"}}, "missing expression"},
		{"transform valid", Step{ID: "t", Type: StepTransform, Transform: &TransformStep{Input: "{{ steps.a.output }}", Expression: ".x"}}, ""},

		// --- default ---
		{"unsupported type", Step{ID: "z", Type: StepType("teleport")}, `unsupported type "teleport"`},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := validateStepEgress(tc.step)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("expected nil error, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.wantErr)
			}
			// Every error must name the step id so authors can find it.
			if !strings.Contains(err.Error(), tc.step.ID) {
				t.Errorf("error %q does not name step id %q", err.Error(), tc.step.ID)
			}
		})
	}
}
