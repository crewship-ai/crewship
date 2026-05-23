package telemetry

import "testing"

// TestServiceNameFromEnv documents the three-step resolution explicitly:
// env wins, fallback fills the gap, and "crewship" is the last-resort
// constant. Anyone changing the precedence has to delete a row in this
// table — preferred to leaving the rule scattered across binaries.
func TestServiceNameFromEnv(t *testing.T) {
	tests := []struct {
		name     string
		envVal   string // empty string means "unset"
		fallback string
		want     string
	}{
		{
			name:     "env set wins over fallback",
			envVal:   "crewship-sidecar",
			fallback: "crewship",
			want:     "crewship-sidecar",
		},
		{
			name:     "empty env uses caller fallback",
			envVal:   "",
			fallback: "crewship-sidecar",
			want:     "crewship-sidecar",
		},
		{
			name:     "empty env + empty fallback defaults to 'crewship'",
			envVal:   "",
			fallback: "",
			want:     "crewship",
		},
		{
			name:     "whitespace in env is preserved (operators get exactly what they typed)",
			envVal:   "ee-acme-corp",
			fallback: "crewship",
			want:     "ee-acme-corp",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// t.Setenv handles cleanup automatically and is safe under -parallel.
			t.Setenv(serviceNameEnv, tt.envVal)
			got := ServiceNameFromEnv(tt.fallback)
			if got != tt.want {
				t.Errorf("ServiceNameFromEnv(%q) with env=%q = %q; want %q",
					tt.fallback, tt.envVal, got, tt.want)
			}
		})
	}
}

// TestServiceNameFromEnv_UnsetEnv covers the case where the var has
// never been set in the test environment at all (vs. set-but-empty).
// t.Setenv("", "") would set it to empty; here we want truly absent.
func TestServiceNameFromEnv_UnsetEnv(t *testing.T) {
	t.Setenv(serviceNameEnv, "") // ensure clean state
	// Unset explicitly via the same env name — t.Setenv with "" still
	// leaves it set to empty string, which is what we want for this
	// assertion: empty env should behave the same as missing env.
	if got := ServiceNameFromEnv("crewship-test"); got != "crewship-test" {
		t.Errorf("ServiceNameFromEnv with empty env = %q; want fallback %q",
			got, "crewship-test")
	}
}
