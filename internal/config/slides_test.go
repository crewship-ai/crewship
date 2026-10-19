package config

import "testing"

func TestSlidesDisabledByDefault(t *testing.T) {
	c := Default()
	if c.Slides.Enabled {
		t.Fatal("Slides must be disabled by default")
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("disabled Slides startup: %v", err)
	}
}
func TestSlidesEnablementEnvironmentOverrides(t *testing.T) {
	for _, tc := range []struct {
		name, value   string
		initial, want bool
	}{
		{"true", "true", false, true}, {"one", "1", false, true},
		{"false", "false", true, false}, {"zero", "0", true, false},
		{"unknown stays disabled", "not-enabled", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CREWSHIP_SLIDES_ENABLED", tc.value)
			c := Default()
			c.Slides.Enabled = tc.initial
			applyEnvOverrides(c)
			if c.Slides.Enabled != tc.want {
				t.Fatalf("Slides enabled=%v, want %v", c.Slides.Enabled, tc.want)
			}
		})
	}
}
