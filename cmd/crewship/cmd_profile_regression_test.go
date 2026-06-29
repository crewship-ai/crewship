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

// CodeRabbit #3: login must not create a profile for a typo'd selection that
// comes from CREWSHIP_PROFILE / current / a directory binding (no --profile +
// no --server). It may only auto-create when --profile AND --server are given.
func TestPersistCredential_RejectsUnknownFromCurrent(t *testing.T) {
	redirectConfigHome(t)
	t.Setenv("CREWSHIP_PROFILE", "")
	oldP, oldS := flagProfile, flagServer
	flagProfile, flagServer = "", ""
	t.Cleanup(func() { flagProfile, flagServer = oldP, oldS })

	// `current` points at a profile that has no entry — a stale selection.
	if err := cli.SaveConfig(&cli.CLIConfig{Current: "ghost"}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := persistCredential("https://x", "tok"); err == nil {
		t.Errorf("expected error for unknown 'current' profile, got nil")
	}
}

func TestPersistCredential_RejectsFlagWithoutServer(t *testing.T) {
	redirectConfigHome(t)
	t.Setenv("CREWSHIP_PROFILE", "")
	oldP, oldS := flagProfile, flagServer
	flagProfile, flagServer = "newp", "" // named but no --server to define it
	t.Cleanup(func() { flagProfile, flagServer = oldP, oldS })

	if _, err := persistCredential("https://x", "tok"); err == nil {
		t.Errorf("expected error: unknown --profile without --server, got nil")
	}
}

func TestPersistCredential_CreatesWithFlagAndServer(t *testing.T) {
	path := redirectConfigHome(t)
	t.Setenv("CREWSHIP_PROFILE", "")
	oldP, oldS := flagProfile, flagServer
	flagProfile, flagServer = "newp", "https://newp.example"
	t.Cleanup(func() { flagProfile, flagServer = oldP, oldS })

	name, err := persistCredential("https://newp.example", "tok")
	if err != nil {
		t.Fatalf("onboarding (--profile + --server) should create: %v", err)
	}
	if name != "newp" {
		t.Errorf("name = %q, want newp", name)
	}
	cfg := readCfg(t, path)
	if cfg.Servers["newp"] == nil || cfg.Servers["newp"].Token != "tok" {
		t.Errorf("profile not created: %+v", cfg.Servers)
	}
}

func TestMaskedToken(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", "(set)"},
		{"abcdefgh", "(set)"},         // len 8 → too short to reveal
		{"abcdefghij", "abcd...ghij"}, // 8 < len 10 < 24
		{"crewship_cli_0123456789abcd", "crewship_cli_0123456...abcd"}, // len 27 ≥ 24
	}
	for _, c := range cases {
		if got := maskedToken(c.in); got != c.want {
			t.Errorf("maskedToken(%q) = %q, want %q", c.in, got, c.want)
		}
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
