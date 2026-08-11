package pipeline

import (
	"reflect"
	"sort"
	"testing"
	"time"
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

// TestStaticRiskReasons_ForeachBody is the governance-bypass regression.
//
// The property under test is an EQUIVALENCE, not a list of strings: wrapping a
// step in a foreach must not change the routine's risk classification. It is
// asserted that way (wrapped vs the byte-identical unwrapped step) because the
// bug was not "the wrong reason" — it was "no reason at all", and a routine
// with no reason saves `active`, live and unreviewed, on a door
// (InternalSave) that has no role gate and no self-approve behind it.
//
// Foreach became saveable in this branch (dsl_validate_egress.go grew a
// StepForeach case; before that every foreach routine was refused at save),
// which is what turned a latent asymmetry into a live way past review.
func TestStaticRiskReasons_ForeachBody(t *testing.T) {
	httpStep := Step{ID: "call", Type: StepHTTP, HTTP: &HTTPStep{Method: "POST", URL: "https://exfil.example.com/x"}}
	codeStep := Step{ID: "run", Type: StepCode, Code: &CodeStep{Runtime: "expr", Code: "1+1"}}

	cases := []struct {
		name string
		dsl  *DSL
		want []string
	}{
		{
			// The base case the whole test exists for: one http step, one
			// foreach around it, and the classifier must not care.
			name: "http inside foreach is as risky as http at top level",
			dsl: &DSL{
				Steps: []Step{{
					ID: "fan", Type: StepForeach,
					Foreach: &ForeachStep{Items: "{{ inputs.urls }}", Steps: []Step{httpStep}},
				}},
			},
			want: []string{RiskHTTPStep},
		},
		{
			name: "code inside foreach is as risky as code at top level",
			dsl: &DSL{
				Steps: []Step{{
					ID: "fan", Type: StepForeach,
					Foreach: &ForeachStep{Items: "{{ inputs.rows }}", Steps: []Step{codeStep}},
				}},
			},
			want: []string{RiskCodeStep},
		},
		{
			// A hook on a BODY step. Hook scanning already existed; this
			// pins that it survives one level down rather than only applying
			// to steps the top-level loop happens to visit.
			name: "http hook on a foreach body step is risky",
			dsl: &DSL{
				Steps: []Step{{
					ID: "fan", Type: StepForeach,
					Foreach: &ForeachStep{Items: "{{ inputs.rows }}", Steps: []Step{{
						ID: "inner", Type: StepAgentRun, AgentSlug: "eva", Prompt: "hi",
						Hooks: &StepHooks{After: &Step{ID: "inner-after", Type: StepHTTP, HTTP: &HTTPStep{Method: "GET", URL: "https://x"}}},
					}}},
				}},
			},
			want: []string{RiskHTTPStep},
		},
		{
			// The other direction: a foreach parked inside a routine-level
			// hook. Hooks are only scanned from three named fields, so the
			// foreach recursion has to be reachable from there too.
			name: "foreach inside a routine on_failure hook is risky",
			dsl: &DSL{
				Steps: []Step{{ID: "a", Type: StepAgentRun, AgentSlug: "eva", Prompt: "hi"}},
				Hooks: &RoutineHooks{OnFailure: &Step{
					ID: "cleanup", Type: StepForeach,
					Foreach: &ForeachStep{Items: "{{ steps.a.output }}", Steps: []Step{httpStep}},
				}},
			},
			want: []string{RiskHTTPStep},
		},
		{
			// validateForeachStep refuses a nested foreach, so this DSL
			// cannot be saved through the normal door today. The classifier
			// must still see through it: it runs on DSLs from the import
			// path and from rows already in the database, and a governance
			// decision that is only correct when some *other* function ran
			// first is not a guarantee.
			name: "arbitrarily nested foreach still reaches the code step",
			dsl: &DSL{
				Steps: []Step{nestForeach(codeStep, 3)},
			},
			want: []string{RiskCodeStep},
		},
		{
			// A foreach step whose block is nil is malformed, not a panic.
			name: "foreach with nil body does not panic",
			dsl: &DSL{
				Steps: []Step{{ID: "fan", Type: StepForeach}},
			},
			want: nil,
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

// nestForeach wraps a step in `levels` foreach layers, innermost first.
func nestForeach(inner Step, levels int) Step {
	st := inner
	for i := 0; i < levels; i++ {
		st = Step{
			ID: "fan", Type: StepForeach,
			Foreach: &ForeachStep{Items: "{{ inputs.x }}", Steps: []Step{st}},
		}
	}
	return st
}

// TestStaticRiskReasons_CyclicDSLTerminates — a Step graph that points at
// itself must not hang the save path.
//
// JSON can't encode a cycle, so no author reaches this; Go code can, because
// Foreach and Hooks are pointers. The classifier is called from the save
// handlers, and a hang there is an unavailable API rather than a rejected
// routine — cheaper to bound the walk than to argue that every present and
// future caller decoded its DSL from bytes.
//
// The test would hang rather than fail without the bound, so it runs the walk
// on a goroutine and fails on a timeout.
func TestStaticRiskReasons_CyclicDSLTerminates(t *testing.T) {
	fe := &ForeachStep{Items: "{{ inputs.x }}", Steps: []Step{
		{ID: "inner", Type: StepHTTP, HTTP: &HTTPStep{Method: "GET", URL: "https://x"}},
		{ID: "loop", Type: StepForeach},
	}}
	fe.Steps[1].Foreach = fe // self-reference
	d := &DSL{Steps: []Step{{ID: "fan", Type: StepForeach, Foreach: fe}}}

	done := make(chan []string, 1)
	go func() { done <- d.StaticRiskReasons() }()
	select {
	case got := <-done:
		if len(got) != 1 || got[0] != RiskHTTPStep {
			t.Errorf("StaticRiskReasons() = %v, want [%s]", got, RiskHTTPStep)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("StaticRiskReasons did not terminate on a cyclic DSL")
	}
}
