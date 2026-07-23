package orchestrator

import "testing"

// TestResolveRunSemCap locks the precedence order for the agent-run
// concurrency cap: CREWSHIP_MAX_CONCURRENT_RUNS env var (when set to a valid
// positive integer) beats the config-provided default, which beats the
// built-in defaultRunSemCap (8). An invalid/non-positive env value is
// ignored and falls through exactly as if the env var were unset.
func TestResolveRunSemCap(t *testing.T) {
	tests := []struct {
		name          string
		env           string // "" means unset
		configDefault int
		want          int
	}{
		{
			name:          "env set and valid wins over config",
			env:           "3",
			configDefault: 20,
			want:          3,
		},
		{
			name:          "env unset, config value used",
			env:           "",
			configDefault: 12,
			want:          12,
		},
		{
			name:          "both unset falls back to built-in default",
			env:           "",
			configDefault: 0,
			want:          defaultRunSemCap,
		},
		{
			name:          "invalid env ignored, falls through to config",
			env:           "not-a-number",
			configDefault: 15,
			want:          15,
		},
		{
			name:          "non-positive env ignored, falls through to default",
			env:           "0",
			configDefault: 0,
			want:          defaultRunSemCap,
		},
		{
			name:          "negative env ignored, falls through to config",
			env:           "-5",
			configDefault: 7,
			want:          7,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.env != "" {
				t.Setenv("CREWSHIP_MAX_CONCURRENT_RUNS", tt.env)
			} else {
				t.Setenv("CREWSHIP_MAX_CONCURRENT_RUNS", "")
			}
			if got := resolveRunSemCap(tt.configDefault); got != tt.want {
				t.Errorf("resolveRunSemCap(%d) with env=%q = %d, want %d", tt.configDefault, tt.env, got, tt.want)
			}
		})
	}
}
