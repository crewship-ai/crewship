package config

import "testing"

func TestBoardFeatureFlag(t *testing.T) {
	if Default().BoardEnabled {
		t.Fatal("Board must default off")
	}
	for _, tc := range []struct {
		value   string
		enabled bool
	}{{"true", true}, {"1", true}, {"false", false}, {"0", false}, {"invalid", false}} {
		t.Run(tc.value, func(t *testing.T) {
			t.Setenv("CREWSHIP_BOARD_ENABLED", tc.value)
			cfg := Default()
			applyEnvOverrides(cfg)
			if cfg.BoardEnabled != tc.enabled {
				t.Fatalf("BoardEnabled=%v", cfg.BoardEnabled)
			}
		})
	}
}
