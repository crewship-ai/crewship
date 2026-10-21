package config

import (
	"os"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestSlidesHumanFeatureSwitch(t *testing.T) {
	for _, tc := range []struct {
		name, input, env string
		want             bool
	}{
		{"default off", "", "", false},
		{"yaml on", "slides:\n  enabled: true\n", "", true},
		{"environment on", "", "true", true},
		{"environment numeric on", "", "1", true},
		{"environment overrides yaml", "slides:\n  enabled: true\n", "false", false},
		{"invalid environment fails closed", "slides:\n  enabled: true\n", "misspelled", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := Default()
			if err := yaml.Unmarshal([]byte(tc.input), cfg); err != nil {
				t.Fatal(err)
			}
			// Register cleanup for a host override before testing an absent
			// variable; an empty value would itself be an explicit override.
			t.Setenv("CREWSHIP_SLIDES_ENABLED", tc.env)
			if tc.env == "" {
				if err := os.Unsetenv("CREWSHIP_SLIDES_ENABLED"); err != nil {
					t.Fatal(err)
				}
			}
			applyEnvOverrides(cfg)
			raw, err := yaml.Marshal(cfg)
			if err != nil {
				t.Fatal(err)
			}
			var got struct {
				Slides struct {
					Enabled bool `yaml:"enabled"`
				} `yaml:"slides"`
			}
			if err := yaml.Unmarshal(raw, &got); err != nil {
				t.Fatal(err)
			}
			if got.Slides.Enabled != tc.want {
				t.Fatalf("Slides enabled = %v, want %v", got.Slides.Enabled, tc.want)
			}
		})
	}
}
