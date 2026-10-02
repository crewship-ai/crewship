package config

import (
	"os"
	"reflect"
	"testing"
)

// The egress fence pilot (#1368) is off unless crews are named explicitly.
func TestEgressFenceCrewsEnv(t *testing.T) {
	for _, tc := range []struct {
		name string
		set  bool
		val  string
		want []string
	}{
		{"unset is off", false, "", nil},
		{"one crew", true, "lab", []string{"lab"}},
		{"list with blanks", true, " lab, ,cm123 ", []string{"lab", "cm123"}},
		{"empty clears", true, "", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Setenv registers the restore; the unset case then removes the
			// variable so an outer environment cannot leak into it.
			t.Setenv("CREWSHIP_EGRESS_FENCE_CREWS", tc.val)
			if !tc.set {
				if err := os.Unsetenv("CREWSHIP_EGRESS_FENCE_CREWS"); err != nil {
					t.Fatal(err)
				}
			}
			cfg := &Config{}
			applyEnvOverrides(cfg)
			if !reflect.DeepEqual(cfg.Container.EgressFenceCrews, tc.want) {
				t.Fatalf("EgressFenceCrews = %#v, want %#v", cfg.Container.EgressFenceCrews, tc.want)
			}
		})
	}
}
