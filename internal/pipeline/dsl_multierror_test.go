package pipeline

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// #1423 item 1: Validate now accumulates every static check failure
// instead of stopping at the first, and attaches a JSON-pointer path to
// each so editor/LSP tooling can jump straight to the offending field.
// These tests pin that contract; the pre-existing dsl_test.go /
// dsl_cov_test.go single-error-substring tests keep passing unchanged
// because ValidationErrors.Error() degrades to exactly the old message
// when there's only one failure.

func TestValidate_MultiError_AccumulatesAcrossChecks(t *testing.T) {
	t.Parallel()
	// Two independent, unrelated failures: a malformed name AND an
	// unknown agent_slug on a step. A first-error-wins validator would
	// only ever report the name problem; the caller would fix it, save
	// again, and only then discover the agent_slug typo.
	dsl := &DSL{
		Name: "BAD NAME",
		Steps: []Step{
			{ID: "a", Type: StepAgentRun, AgentSlug: "gohst-writer", Prompt: "hi"},
		},
	}
	agentSlugs := map[string]struct{}{"ghost-writer": {}}

	err := Validate(dsl, agentSlugs, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	var verrs ValidationErrors
	if !errors.As(err, &verrs) {
		t.Fatalf("expected ValidationErrors, got %T: %v", err, err)
	}
	if len(verrs) != 2 {
		t.Fatalf("expected 2 accumulated errors, got %d: %v", len(verrs), verrs)
	}
	var sawName, sawAgentSlug bool
	for _, e := range verrs {
		switch e.Path {
		case "/name":
			sawName = true
			if !strings.Contains(e.Message, "kebab-case") {
				t.Errorf("/name error message unexpected: %q", e.Message)
			}
		case "/steps/0/agent_slug":
			sawAgentSlug = true
			if !strings.Contains(e.Message, "unknown agent_slug") {
				t.Errorf("agent_slug error message unexpected: %q", e.Message)
			}
		}
	}
	if !sawName || !sawAgentSlug {
		t.Errorf("expected both /name and /steps/0/agent_slug entries, got %v", verrs)
	}

	// The joined Error() string must still contain both fragments, so
	// any caller that only does strings.Contains(err.Error(), "...")
	// (the pre-#1423 idiom used throughout this package's own tests)
	// keeps working against a multi-error result too.
	if !strings.Contains(err.Error(), "kebab-case") || !strings.Contains(err.Error(), "unknown agent_slug") {
		t.Errorf("joined error text missing a fragment: %v", err)
	}
}

func TestValidate_SingleError_MatchesPreMultiErrorMessage(t *testing.T) {
	t.Parallel()
	// Exactly one failure: ValidationErrors.Error() must degrade to
	// that single entry's own message, not a "1 validation errors:"
	// wrapper — this is what keeps every pre-existing
	// strings.Contains(err.Error(), "...") test in this package valid.
	dsl := &DSL{
		Name:  "demo",
		Steps: []Step{{ID: "a", Type: StepAgentRun, AgentSlug: "x", Prompt: "hi"}},
	}
	err := Validate(dsl, map[string]struct{}{"y": {}}, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), "validation errors:") {
		t.Errorf("single failure should not use the multi-error wrapper format: %q", err.Error())
	}
	if !strings.HasPrefix(err.Error(), "/steps/0/agent_slug: ") {
		t.Errorf("expected path-prefixed message, got %q", err.Error())
	}
}

func TestValidate_MaxPipelineSteps_StaysSingleFirstClassError(t *testing.T) {
	t.Parallel()
	// #1423 item 1 explicitly pins this: the MaxPipelineSteps bound
	// (#1416) must NOT be folded into multi-error accumulation — it
	// stays an early, single, un-pathed return.
	steps := make([]Step, MaxPipelineSteps+1)
	for i := range steps {
		steps[i] = Step{ID: fmt.Sprintf("s%d", i), Type: StepTransform, Transform: &TransformStep{Input: "x", Expression: "."}}
	}
	dsl := &DSL{Name: "too-many-steps", Steps: steps}
	err := Validate(dsl, nil, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	var verrs ValidationErrors
	if errors.As(err, &verrs) {
		t.Errorf("MaxPipelineSteps error must not be a ValidationErrors, got %v", verrs)
	}
	want := fmt.Sprintf("pipeline: %d steps exceeds the %d step limit", len(steps), MaxPipelineSteps)
	if err.Error() != want {
		t.Errorf("message/behavior of the step-limit error must be unchanged: got %q, want %q", err.Error(), want)
	}
}

func TestValidate_JSONPointerPaths(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		dsl  *DSL
		want string
	}{
		{
			name: "missing name",
			dsl:  &DSL{Steps: []Step{{ID: "a", Type: StepTransform, Transform: &TransformStep{Input: "x", Expression: "."}}}},
			want: "/name",
		},
		{
			name: "bad parallelism",
			dsl: &DSL{
				Name:        "demo",
				Parallelism: "bogus",
				Steps:       []Step{{ID: "a", Type: StepTransform, Transform: &TransformStep{Input: "x", Expression: "."}}},
			},
			want: "/parallelism",
		},
		{
			name: "http step missing url",
			dsl: &DSL{
				Name:  "demo",
				Steps: []Step{{ID: "a", Type: StepHTTP, HTTP: &HTTPStep{Method: "GET"}}},
			},
			want: "/steps/0/http",
		},
		{
			name: "http step bad template in url",
			dsl: &DSL{
				Name:  "demo",
				Steps: []Step{{ID: "a", Type: StepHTTP, HTTP: &HTTPStep{Method: "GET", URL: "https://x/{{ inputs.missing }}"}}},
			},
			want: "/steps/0/http/url",
		},
		{
			name: "bad template in prompt",
			dsl: &DSL{
				Name:  "demo",
				Steps: []Step{{ID: "a", Type: StepAgentRun, AgentSlug: "x", Prompt: "{{ inputs.missing }}"}},
			},
			want: "/steps/0/prompt",
		},
		{
			name: "concurrency key can render empty",
			dsl: &DSL{
				Name:           "demo",
				ConcurrencyKey: "{{ inputs.maybe }}",
				Inputs:         []InputSpec{{Name: "maybe", Type: "string"}},
				Steps:          []Step{{ID: "a", Type: StepTransform, Transform: &TransformStep{Input: "x", Expression: "."}}},
			},
			want: "/concurrency_key",
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := Validate(tc.dsl, nil, nil)
			if err == nil {
				t.Fatal("expected error")
			}
			var verrs ValidationErrors
			var path string
			if errors.As(err, &verrs) {
				if len(verrs) == 0 {
					t.Fatal("ValidationErrors empty")
				}
				path = verrs[0].Path
			} else {
				var ve *ValidationError
				if !errors.As(err, &ve) {
					t.Fatalf("expected *ValidationError or ValidationErrors, got %T", err)
				}
				path = ve.Path
			}
			if path != tc.want {
				t.Errorf("path: got %q, want %q (err=%v)", path, tc.want, err)
			}
		})
	}
}

func TestValidate_DidYouMean_AgentSlug(t *testing.T) {
	t.Parallel()
	dsl := &DSL{
		Name:  "demo",
		Steps: []Step{{ID: "a", Type: StepAgentRun, AgentSlug: "triaeg", Prompt: "hi"}},
	}
	err := Validate(dsl, map[string]struct{}{"triage": {}, "writer": {}}, nil)
	if err == nil || !strings.Contains(err.Error(), "did you mean: triage") {
		t.Errorf("expected did-you-mean hint for agent_slug typo, got %v", err)
	}
}

func TestValidate_DidYouMean_InputName(t *testing.T) {
	t.Parallel()
	dsl := &DSL{
		Name:   "demo",
		Inputs: []InputSpec{{Name: "since", Type: "string"}},
		Steps:  []Step{{ID: "a", Type: StepAgentRun, AgentSlug: "x", Prompt: "fetch {{ inputs.sinse }}"}},
	}
	err := Validate(dsl, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "did you mean: since") {
		t.Errorf("expected did-you-mean hint for input-name typo, got %v", err)
	}
}

func TestValidate_DidYouMean_GraderAgentSlug(t *testing.T) {
	t.Parallel()
	dsl := &DSL{
		Name: "demo",
		Steps: []Step{{
			ID: "a", Type: StepAgentRun, AgentSlug: "x", Prompt: "hi",
			Outcomes: &Outcomes{
				GraderAgentSlug: "grade-bot",
				Criteria:        []OutcomeCriterion{{Name: "c1", Rule: "true"}},
			},
		}},
	}
	agentSlugs := map[string]struct{}{"x": {}, "grader-bot": {}}
	err := Validate(dsl, agentSlugs, nil)
	if err == nil || !strings.Contains(err.Error(), "did you mean: grader-bot") {
		t.Errorf("expected did-you-mean hint for grader_agent_slug typo, got %v", err)
	}
}

func TestValidate_DidYouMean_NoCloseCandidate_OmitsHint(t *testing.T) {
	t.Parallel()
	dsl := &DSL{
		Name:  "demo",
		Steps: []Step{{ID: "a", Type: StepAgentRun, AgentSlug: "zzzzzzzzzz", Prompt: "hi"}},
	}
	err := Validate(dsl, map[string]struct{}{"triage": {}}, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), "did you mean") {
		t.Errorf("no close candidate should omit the hint: %v", err)
	}
}

func TestValidationErrors_ErrorFormatting(t *testing.T) {
	t.Parallel()
	if got := (ValidationErrors{}).Error(); got != "" {
		t.Errorf("empty: got %q, want empty string", got)
	}
	one := ValidationErrors{{Path: "/name", Message: "pipeline: name required"}}
	if got, want := one.Error(), "/name: pipeline: name required"; got != want {
		t.Errorf("single: got %q, want %q", got, want)
	}
	two := ValidationErrors{
		{Path: "/name", Message: "pipeline: name required"},
		{Path: "/steps/0/agent_slug", Message: "pipeline: step \"a\" references unknown agent_slug \"x\""},
	}
	got := two.Error()
	if !strings.HasPrefix(got, "2 validation errors:") {
		t.Errorf("multi: expected count prefix, got %q", got)
	}
	if !strings.Contains(got, "/name: pipeline: name required") || !strings.Contains(got, "/steps/0/agent_slug:") {
		t.Errorf("multi: missing an entry, got %q", got)
	}
}

func TestValidationErrors_Unwrap(t *testing.T) {
	t.Parallel()
	target := &ValidationError{Path: "/name", Message: "pipeline: name required"}
	verrs := ValidationErrors{target, {Path: "/steps/0/agent_slug", Message: "boom"}}
	var err error = verrs
	var got *ValidationError
	if !errors.As(err, &got) {
		t.Fatal("errors.As should find a *ValidationError inside ValidationErrors")
	}
	if got.Path != "/name" {
		t.Errorf("errors.As found the wrong entry: %+v", got)
	}
}
