package main

import "github.com/crewship-ai/crewship/internal/cli"

// streamServerURL resolves the server the streaming call sites (run/ask/logs/
// retry/explain WS + SSE legs) dial. It uses cli.EffectiveServer — NOT
// cli.ResolveServer — so an explicit --profile / CREWSHIP_PROFILE wins over a
// stale shell CREWSHIP_SERVER, matching newAPIClient() and therefore the
// server the WS token was minted against.
//
// The bug this fixes: with an active profile AND CREWSHIP_SERVER exported
// (the documented multi-clone convention), run/ask/logs/retry/explain minted
// their WS token against the profile server but opened the stream to the env
// host — the same split-brain seedTargetServer fixed for the seed flow.
// See TestStreamServerURLHonoursProfileOverEnv.
func streamServerURL() string {
	return cli.EffectiveServer(flagServer, flagProfile, cliCfg)
}
