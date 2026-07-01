package pipeline

import (
	"reflect"
	"sort"
	"testing"
)

func TestStaticRiskReasons(t *testing.T) {
	cases := []struct {
		name string
		dsl  *DSL
		want []string
	}{
		{
			name: "nil",
			dsl:  nil,
			want: nil,
		},
		{
			name: "safe agent_run + transform only",
			dsl: &DSL{
				Steps: []Step{
					{ID: "a", Type: StepAgentRun, AgentSlug: "eva", Prompt: "hi"},
					{ID: "b", Type: StepTransform, Transform: &TransformStep{Input: "{{ steps.a.output }}", Expression: "."}},
				},
			},
			want: nil,
		},
		{
			name: "http step is risky",
			dsl: &DSL{
				Steps: []Step{
					{ID: "h", Type: StepHTTP, HTTP: &HTTPStep{Method: "GET", URL: "https://x"}},
				},
			},
			want: []string{RiskHTTPStep},
		},
		{
			name: "code step is risky",
			dsl: &DSL{
				Steps: []Step{
					{ID: "c", Type: StepCode, Code: &CodeStep{Runtime: "expr", Code: "1+1"}},
				},
			},
			want: []string{RiskCodeStep},
		},
		{
			name: "egress_targets is risky",
			dsl: &DSL{
				EgressTargets: []string{"api.example.com"},
				Steps: []Step{
					{ID: "a", Type: StepAgentRun, AgentSlug: "eva", Prompt: "hi"},
				},
			},
			want: []string{RiskEgressTargets},
		},
		{
			name: "credentials_required is risky",
			dsl: &DSL{
				CredsRequired: []CredReq{{Type: "stripe"}},
				Steps: []Step{
					{ID: "a", Type: StepAgentRun, AgentSlug: "eva", Prompt: "hi"},
				},
			},
			want: []string{RiskCredentialsRequired},
		},
		{
			name: "http hook on an otherwise-safe step is risky",
			dsl: &DSL{
				Steps: []Step{
					{
						ID:        "a",
						Type:      StepAgentRun,
						AgentSlug: "eva", Prompt: "hi",
						Hooks: &StepHooks{
							After: &Step{ID: "a-after", Type: StepHTTP, HTTP: &HTTPStep{Method: "POST", URL: "https://x"}},
						},
					},
				},
			},
			want: []string{RiskHTTPStep},
		},
		{
			name: "routine on_failure code hook is risky",
			dsl: &DSL{
				Steps: []Step{
					{ID: "a", Type: StepAgentRun, AgentSlug: "eva", Prompt: "hi"},
				},
				Hooks: &RoutineHooks{
					OnFailure: &Step{ID: "cleanup", Type: StepCode, Code: &CodeStep{Runtime: "expr", Code: "1"}},
				},
			},
			want: []string{RiskCodeStep},
		},
		{
			name: "multiple factors",
			dsl: &DSL{
				EgressTargets: []string{"api.example.com"},
				CredsRequired: []CredReq{{Type: "stripe"}},
				Steps: []Step{
					{ID: "h", Type: StepHTTP, HTTP: &HTTPStep{Method: "GET", URL: "https://x"}},
					{ID: "c", Type: StepCode, Code: &CodeStep{Runtime: "expr", Code: "1"}},
				},
			},
			want: []string{RiskEgressTargets, RiskHTTPStep, RiskCodeStep, RiskCredentialsRequired},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.dsl.StaticRiskReasons()
			gs, ws := append([]string(nil), got...), append([]string(nil), tc.want...)
			sort.Strings(gs)
			sort.Strings(ws)
			if len(gs) == 0 && len(ws) == 0 {
				return
			}
			if !reflect.DeepEqual(gs, ws) {
				t.Errorf("StaticRiskReasons() = %v, want %v", got, tc.want)
			}
		})
	}
}
