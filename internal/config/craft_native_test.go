package config

import "testing"

func TestCraftNativeDefaultsOffAndExplicitEnv(t *testing.T) {
	cfg := Default()
	if cfg.CraftNative.Enabled {
		t.Fatal("native profile enabled by default")
	}
	t.Setenv("CREWSHIP_CRAFT_NATIVE_ENABLED", "true")
	applyEnvOverrides(cfg)
	if !cfg.CraftNative.Enabled || cfg.Mooring.Enabled || cfg.GridCraft.Enabled {
		t.Fatal("native flag must be independent of older profiles")
	}
	t.Setenv("CREWSHIP_CRAFT_NATIVE_ENABLED", "false")
	applyEnvOverrides(cfg)
	if cfg.CraftNative.Enabled {
		t.Fatal("explicit disable ignored")
	}
}
