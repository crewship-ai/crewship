package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/crewship-ai/crewship/internal/cli"
)

// seedKeeperDefaultModel is the judge the demo comes up on when the operator
// does not name one. A 9B-class instruct model quantised to Q4 is the smallest
// thing that reliably returns the gatekeeper's JSON verdict inside the 256-token
// budget while still fitting a 16GB laptop next to everything else — measured at
// ~5.6GB resident and ~3.4s per verdict on an M4 MacBook Air.
//
// It is a DEFAULT, not a requirement: `crewship keeper config set --model` moves
// it, and the seed only ever enables the watchdog after the judge has answered,
// so naming a model the endpoint has not pulled costs a printed note rather than
// a broken instance.
const seedKeeperDefaultModel = "qwen3.5:9b"

// seedKeeperEnv reads the judge endpoint + model out of the SEEDING operator's
// environment. runSeed has already sourced .env.local, so a clone can pin its
// own Ollama there and every seed of that clone lands on it.
//
// The names are the server's own (KEEPER_OLLAMA_URL / KEEPER_MODEL) rather than
// seed-specific ones, because they mean exactly the same thing here and a second
// spelling for one setting is how the two drift apart.
func seedKeeperEnv() (endpoint, model string) {
	endpoint = strings.TrimSpace(os.Getenv("KEEPER_OLLAMA_URL"))
	model = strings.TrimSpace(os.Getenv("KEEPER_MODEL"))
	if model == "" {
		model = seedKeeperDefaultModel
	}
	return endpoint, model
}

// seedKeeper brings the demo instance up with a WORKING Keeper watchdog, or
// with none at all — never with one that is on and cannot answer.
//
// The Keeper is fail-closed: a judge that is unreachable, has not pulled the
// model, or answers too slowly turns every credential request into a DENY that
// is indistinguishable from a considered verdict. Enabling it unconditionally
// would therefore hand a fresh demo the worst of both worlds — the friction of a
// gate, none of the judgement. So the order here is: pin the judge, ASK it for a
// verdict, and only enable the watchdog if it gave one.
//
// Errors never fail the seed. A demo without a watchdog is a demo; a seed that
// aborts halfway through leaves no fixture at all.
func seedKeeper(ctx context.Context, client *cli.Client, endpoint, model string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if endpoint == "" {
		// No local judge configured anywhere — say which variable turns this on
		// rather than silently skipping a phase the operator expected.
		fmt.Fprintln(os.Stderr, "  = Keeper: skipped (no KEEPER_OLLAMA_URL set — the watchdog stays off)")
		return nil
	}

	// Pin endpoint + model BEFORE the check, so the check measures the judge the
	// instance will actually use and not whatever it booted with.
	var cfg keeperInstanceConfig
	if err := putJSON(client, keeperConfigPath, map[string]any{
		"enabled":            true,
		"judge_endpoint_url": endpoint,
		"judge_model":        model,
	}, &cfg); err != nil {
		fmt.Fprintf(os.Stderr, "  ! Keeper: could not configure the judge (continuing): %v\n", err)
		return nil
	}

	var probe keeperJudgeTestResult
	if err := postJSON(client, "/api/v1/admin/keeper/judge/test", map[string]any{}, &probe); err != nil {
		fmt.Fprintf(os.Stderr, "  ! Keeper: judge check could not run (continuing): %v\n", err)
		fmt.Fprintf(os.Stderr, "    Watchdog left OFF. Fix, then: crewship keeper judge test && crewship keeper enable\n")
		return nil
	}
	if !probe.OK {
		fmt.Fprintf(os.Stderr, "  ! Keeper: judge at %s is not usable — watchdog left OFF\n", endpoint)
		for _, st := range probe.Stages {
			if st.OK || st.Skipped {
				continue
			}
			name := st.Label
			if name == "" {
				name = st.Name
			}
			fmt.Fprintf(os.Stderr, "    failed stage %q: %s\n", name, st.Detail)
		}
		fmt.Fprintf(os.Stderr, "    Once it answers: crewship keeper judge test && crewship keeper enable\n")
		return nil
	}

	// The judge answered. Turn the watchdog on and record which model decides —
	// one PUT, because governance is a single row and two writes could half-apply.
	var gov keeperGovernance
	if err := putJSON(client, "/api/v1/admin/keeper/governance", map[string]any{
		"enabled":            true,
		"gov_model_provider": "ollama",
		"gov_model_id":       model,
	}, &gov); err != nil {
		fmt.Fprintf(os.Stderr, "  ! Keeper: judge works but the watchdog could not be enabled (continuing): %v\n", err)
		return nil
	}

	fmt.Fprintf(os.Stderr, "  + Keeper: watchdog ON — %s judging via %s\n", model, endpoint)
	return nil
}
