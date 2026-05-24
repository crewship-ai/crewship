package telemetry

import (
	"os"
	"testing"
)

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
// t.Setenv("", "") sets the var to empty rather than removing it, so
// it exercises a different code path (LookupEnv returns ok=true with
// value=""). To actually test the "var absent" branch we have to
// os.Unsetenv it ourselves, then restore the prior value in a manual
// cleanup hook — t.Setenv has no "unset" mode.
func TestServiceNameFromEnv_UnsetEnv(t *testing.T) {
	prev, hadPrev := os.LookupEnv(serviceNameEnv)
	if err := os.Unsetenv(serviceNameEnv); err != nil {
		t.Fatalf("unset %s: %v", serviceNameEnv, err)
	}
	t.Cleanup(func() {
		if hadPrev {
			_ = os.Setenv(serviceNameEnv, prev)
			return
		}
		_ = os.Unsetenv(serviceNameEnv)
	})
	if got := ServiceNameFromEnv("crewship-test"); got != "crewship-test" {
		t.Errorf("ServiceNameFromEnv with unset env = %q; want fallback %q",
			got, "crewship-test")
	}
}
