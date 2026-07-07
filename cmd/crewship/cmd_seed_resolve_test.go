package main

import (
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
)

// TestSeedTargetServerHonoursProfileOverEnv locks the fix for the seed
// bootstrap mis-resolution: with an explicit --profile selected AND a shell
// CREWSHIP_SERVER pointing elsewhere (the documented multi-clone convention),
// the seed flow must target the profile's server, not CREWSHIP_SERVER.
//
// Regression: seedTargetServer used cli.ResolveServer, whose precedence lets
// CREWSHIP_SERVER override the profile — so `crewship seed --profile prod` from
// a shell with CREWSHIP_SERVER=<dev3> bootstrapped against dev3 while every
// authenticated call hit prod.
func TestSeedTargetServerHonoursProfileOverEnv(t *testing.T) {
	origProfile, origServer, origCfg := flagProfile, flagServer, cliCfg
	t.Cleanup(func() { flagProfile, flagServer, cliCfg = origProfile, origServer, origCfg })

	// Shell exports a different instance (multi-clone convention).
	t.Setenv("CREWSHIP_SERVER", "https://crewship-dev3.unifylab.cz")
	t.Setenv("CREWSHIP_PROFILE", "")

	flagServer = ""
	flagProfile = "prod"
	// cliCfg is the profile-overlaid config, exactly as main.go wires it at
	// startup: cliCfg = cfg.WithActiveProfile(flagProfile).
	cliCfg = (&cli.CLIConfig{
		Current: "dev1",
		Servers: map[string]*cli.ServerProfile{
			"dev3": {Server: "https://crewship-dev3.unifylab.cz", Token: "t3"},
			"prod": {Server: "https://crewship-prod.unifylab.cz"}, // fresh: no token
		},
	}).WithActiveProfile(flagProfile)

	got := seedTargetServer()
	want := "https://crewship-prod.unifylab.cz"
	if got != want {
		t.Errorf("seedTargetServer() = %q, want %q — CREWSHIP_SERVER must not override an explicit --profile", got, want)
	}
}
