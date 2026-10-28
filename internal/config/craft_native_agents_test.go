package config

import "testing"

func TestCraftNativeAgentFlagDefaultsOffAndNeverEnablesOtherProfiles(t *testing.T) {
	c := Default()
	if c.CraftNative.AgentsEnabled {
		t.Fatal("native agent mandate runtime enabled by default")
	}
	for _, input := range []struct {
		value string
		want  bool
	}{{"true", true}, {"1", true}, {"false", false}, {"0", false}, {"invalid", false}} {
		t.Setenv("CREWSHIP_CRAFT_NATIVE_AGENTS_ENABLED", input.value)
		applyEnvOverrides(c)
		if c.CraftNative.AgentsEnabled != input.want || c.CraftNative.Enabled || c.Mooring.Enabled || c.GridCraft.Enabled {
			t.Fatalf("flag %q changed independent native/legacy admission", input.value)
		}
	}
}
