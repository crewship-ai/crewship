package api

import "testing"

func TestResolveSubAgentModel(t *testing.T) {
	env := func(v string) func(string) string {
		return func(k string) string {
			if k == subAgentModelEnv {
				return v
			}
			return ""
		}
	}

	cases := []struct {
		name         string
		target       string
		leadPlanning bool
		envVal       string
		want         string
	}{
		{"no override keeps target", "claude-opus-4-8", false, "", "claude-opus-4-8"},
		{"override downgrades worker", "claude-opus-4-8", false, "claude-haiku-4-5", "claude-haiku-4-5"},
		{"lead planner exempt from override", "claude-opus-4-8", true, "claude-haiku-4-5", "claude-opus-4-8"},
		{"blank override ignored", "claude-sonnet-4-6", false, "   ", "claude-sonnet-4-6"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveSubAgentModel(tc.target, tc.leadPlanning, env(tc.envVal))
			if got != tc.want {
				t.Fatalf("resolveSubAgentModel(%q, lead=%v, env=%q) = %q, want %q",
					tc.target, tc.leadPlanning, tc.envVal, got, tc.want)
			}
		})
	}
}
