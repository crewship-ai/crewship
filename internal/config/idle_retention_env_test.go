package config

import "testing"

// Idle retention is opt-in: unset means off, and only an explicit value
// turns either half on. A malformed day count must not silently enable it.
func TestIdleRetentionEnv(t *testing.T) {
	for _, tc := range []struct {
		name, days, eviction string
		wantDays             int
		wantEviction         bool
	}{
		{"unset is off", "", "", 0, false},
		{"seven days", "7", "", 7, false},
		{"eviction true", "", "true", 0, true},
		{"eviction 1", "", "1", 0, true},
		{"eviction false", "", "false", 0, false},
		{"malformed days stay off", "seven", "", 0, false},
		{"negative days stay off", "-3", "", 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.days != "" {
				t.Setenv("CREWSHIP_IDLE_RUNTIME_RETENTION_DAYS", tc.days)
			}
			if tc.eviction != "" {
				t.Setenv("CREWSHIP_CACHE_EVICTION", tc.eviction)
			}
			cfg := &Config{}
			applyEnvOverrides(cfg)
			if cfg.Container.IdleRuntimeRetentionDays != tc.wantDays || cfg.Container.CacheEviction != tc.wantEviction {
				t.Fatalf("days=%d eviction=%v", cfg.Container.IdleRuntimeRetentionDays, cfg.Container.CacheEviction)
			}
		})
	}
}
