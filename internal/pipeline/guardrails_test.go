package pipeline

import "testing"

// TestResolveInputGuardAction is the pinned mapping from DSL config to
// the AgentStepRequest.InputGuardAction string. Any drift in the
// vocabulary (sanitize → mask, log → audit, etc.) breaks the contract
// the runner_llm.go side relies on, so the test enumerates the four
// real states explicitly: nil DSL, no guardrails, no input, no
// prompt-injection — all yield empty so callers know to keep the
// platform default — and the three valid actions round-trip verbatim.
func TestResolveInputGuardAction(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   *DSL
		want string
	}{
		{name: "nil dsl", in: nil, want: ""},
		{name: "no guardrails", in: &DSL{}, want: ""},
		{name: "guardrails empty", in: &DSL{Guardrails: &GuardrailsConfig{}}, want: ""},
		{name: "input empty", in: &DSL{Guardrails: &GuardrailsConfig{Input: &InputGuardrailsConfig{}}}, want: ""},
		{
			name: "prompt_injection empty action",
			in: &DSL{Guardrails: &GuardrailsConfig{
				Input: &InputGuardrailsConfig{PromptInjection: &PromptInjectionConfig{}},
			}},
			want: "",
		},
		{
			name: "block",
			in: &DSL{Guardrails: &GuardrailsConfig{
				Input: &InputGuardrailsConfig{PromptInjection: &PromptInjectionConfig{Action: "block"}},
			}},
			want: "block",
		},
		{
			name: "sanitize",
			in: &DSL{Guardrails: &GuardrailsConfig{
				Input: &InputGuardrailsConfig{PromptInjection: &PromptInjectionConfig{Action: "sanitize"}},
			}},
			want: "sanitize",
		},
		{
			name: "log",
			in: &DSL{Guardrails: &GuardrailsConfig{
				Input: &InputGuardrailsConfig{PromptInjection: &PromptInjectionConfig{Action: "log"}},
			}},
			want: "log",
		},
		{
			name: "unknown action collapses to empty (fail-closed)",
			in: &DSL{Guardrails: &GuardrailsConfig{
				Input: &InputGuardrailsConfig{PromptInjection: &PromptInjectionConfig{Action: "explode"}},
			}},
			want: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveInputGuardAction(tc.in)
			if got != tc.want {
				t.Errorf("resolveInputGuardAction = %q, want %q", got, tc.want)
			}
		})
	}
}
