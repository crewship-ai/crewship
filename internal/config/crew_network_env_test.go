package config

import (
	"os"
	"reflect"
	"testing"
)

// Per-crew networks (#2240) are off unless crews are named explicitly.
func TestCrewNetworkEnv(t *testing.T) {
	for _, tc := range []struct {
		name     string
		set      bool
		crews    string
		pool     string
		want     []string
		wantPool string
	}{
		{"unset is off", false, "", "", nil, ""},
		{"list and pool", true, " ops, cm-mkt ,", "10.231.0.0/16", []string{"ops", "cm-mkt"}, "10.231.0.0/16"},
		{"empty clears", true, "", "", nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CREWSHIP_CREW_NETWORK_CREWS", tc.crews)
			t.Setenv("CREWSHIP_CREW_NETWORK_POOL", tc.pool)
			if !tc.set {
				if err := os.Unsetenv("CREWSHIP_CREW_NETWORK_CREWS"); err != nil {
					t.Fatal(err)
				}
			}
			cfg := &Config{}
			applyEnvOverrides(cfg)
			if !reflect.DeepEqual(cfg.Container.CrewNetworkCrews, tc.want) || cfg.Container.CrewNetworkPool != tc.wantPool {
				t.Fatalf("crews=%#v pool=%q", cfg.Container.CrewNetworkCrews, cfg.Container.CrewNetworkPool)
			}
		})
	}
}
