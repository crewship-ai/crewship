package main

import (
	"testing"

	"github.com/crewship-ai/crewship/internal/cli"
)

// TestStreamServerURLHonoursProfileOverEnv locks the fix for the split-brain
// streaming bug: run/ask/logs/retry/explain built their WS/SSE URL with
// cli.ResolveServer (precedence: flag > CREWSHIP_SERVER > cfg) while the
// authenticated API client — which mints the WS token — uses
// cli.EffectiveServer (flag > active profile > CREWSHIP_SERVER > cfg). With an
// explicit --profile selected AND a stale shell CREWSHIP_SERVER (the
// documented multi-clone convention), the token was minted against the
// profile server but the stream opened against the env host.
//
// Same class of bug as seedTargetServer (see
// TestSeedTargetServerHonoursProfileOverEnv); streamServerURL is the shared
// resolver all streaming call sites must use.
func TestStreamServerURLHonoursProfileOverEnv(t *testing.T) {
	origProfile, origServer, origCfg := flagProfile, flagServer, cliCfg
	t.Cleanup(func() { flagProfile, flagServer, cliCfg = origProfile, origServer, origCfg })

	// Shell exports a different instance (multi-clone convention).
	t.Setenv("CREWSHIP_SERVER", "https://crewship-dev3.example")
	t.Setenv("CREWSHIP_PROFILE", "")

	flagServer = ""
	flagProfile = "prod"
	cliCfg = (&cli.CLIConfig{
		Current: "dev1",
		Servers: map[string]*cli.ServerProfile{
			"dev3": {Server: "https://crewship-dev3.example", Token: "t3"},
			"prod": {Server: "https://crewship-prod.example"},
		},
	}).WithActiveProfile(flagProfile)

	got := streamServerURL()
	want := "https://crewship-prod.example"
	if got != want {
		t.Errorf("streamServerURL() = %q, want %q — the stream must dial the same host the WS token was minted for", got, want)
	}
}
