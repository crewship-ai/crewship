package main

import (
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
)

// Regression for the review's #1: `crewship login` with no --profile, but a
// `current` (or directory-bound) profile active, must write the token INTO that
// profile — where reads look — not the top-level slot the overlay then masks.
func TestLogin_NoFlag_WritesToActiveProfile(t *testing.T) {
	path := redirectConfigHome(t)
	t.Setenv("CREWSHIP_PROFILE", "")
	old := flagProfile
	flagProfile = ""
	t.Cleanup(func() { flagProfile = old })

	// A profile is the active default (as after `server use dev2`).
	seed := &cli.CLIConfig{
		Current: "dev2",
		Servers: map[string]*cli.ServerProfile{"dev2": {Server: "https://dev2"}},
	}
	if err := cli.SaveConfig(seed); err != nil {
		t.Fatalf("seed: %v", err)
	}

	name, err := persistCredential("https://dev2", "freshtok")
	if err != nil {
		t.Fatalf("persist: %v", err)
	}
	if name != "dev2" {
		t.Errorf("login should resolve the active profile, got %q", name)
	}
	cfg := readCfg(t, path)
	if cfg.Servers["dev2"].Token != "freshtok" {
		t.Errorf("token not written to active profile: %+v", cfg.Servers["dev2"])
	}
	if cfg.Token != "" {
		t.Errorf("token leaked to top-level (would be masked by overlay): %q", cfg.Token)
	}
}

// Regression for the review's #8: `config set workspace` under an active
// profile must persist to the profile, not the top-level field the overlay
// shadows on the next read.
func TestConfigSet_UnderProfile_WritesToProfile(t *testing.T) {
	path := redirectConfigHome(t)
	t.Setenv("CREWSHIP_PROFILE", "")
	old := flagProfile
	flagProfile = ""
	t.Cleanup(func() { flagProfile = old })

	seed := &cli.CLIConfig{
		Current: "dev1",
		Servers: map[string]*cli.ServerProfile{"dev1": {Server: "https://dev1", Token: "t1"}},
	}
	if err := cli.SaveConfig(seed); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := configSetCmd.RunE(configSetCmd, []string{"workspace", "acme-eng"}); err != nil {
		t.Fatalf("config set: %v", err)
	}
	cfg := readCfg(t, path)
	if cfg.Servers["dev1"].Workspace != "acme-eng" {
		t.Errorf("workspace not written to active profile: %+v", cfg.Servers["dev1"])
	}
	if cfg.Workspace != "" {
		t.Errorf("workspace leaked to top-level: %q", cfg.Workspace)
	}
}
