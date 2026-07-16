package main

import (
	"strings"
	"testing"
)

// #1218: the typo guard added by gh#554 only ever looked at `len(args) ==
// 1`, so a *two-word* unknown subcommand — by far the most common shape,
// since almost every real command is `crewship <noun> <verb>` — sailed
// past it and got joined into a prompt for a live agent run. A typo in a
// script then meant "spend money and maybe hang" instead of "exit 1".
//
// These drive rootCmd.RunE directly (no server, no login) — the guard
// returns before anything network-touching, which is the whole point.
func TestRootHeadlessRejectsTypoTwoArgs(t *testing.T) {
	// Belt-and-braces: if the guard regresses, RunE would fall through to
	// the real ask path. Stub it so a regression fails the assertion
	// rather than dialing out.
	stubAskCmdRunE(t)
	flagRootPrompt = ""

	cases := []struct {
		name string
		args []string
		// want is a token the error must echo back to the user.
		want string
	}{
		// The issue's own reproductions, verbatim.
		{"nonexistent top-level command", []string{"schedule", "list"}, "schedule"},
		{"typo of a real command", []string{"routnie", "list"}, "routnie"},
		{"typo of another real command", []string{"agnet", "list"}, "agnet"},
		// NB: no `{"routine", "runz"}` case here. Only an unknown *first*
		// token reaches root's RunE — `routine` resolves, so Cobra
		// dispatches to it and never consults this guard. Asserting on it
		// would test a path the binary can't take.
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := rootCmd.RunE(rootCmd, tc.args)
			if err == nil {
				t.Fatalf("expected an error for `crewship %s`, got nil",
					strings.Join(tc.args, " "))
			}
			if !strings.Contains(err.Error(), "unknown command") {
				t.Errorf("error should say 'unknown command', got: %v", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error should echo %q, got: %v", tc.want, err)
			}
			// The escape hatch has to be discoverable, or a user who
			// really did want a two-word prompt is just stuck.
			if !strings.Contains(err.Error(), "-p") {
				t.Errorf("error should point at the -p escape hatch, got: %v", err)
			}
		})
	}
}

// TestRootHeadlessTypoErrorSuggests pins the "did you mean" hint. Cobra
// computes suggestions for free (SuggestionsFor: Levenshtein + prefix);
// ArbitraryArgs on the root threw them away along with the unknown-command
// error, so a typo lost the one thing that makes it self-correcting.
func TestRootHeadlessTypoErrorSuggests(t *testing.T) {
	stubAskCmdRunE(t)
	flagRootPrompt = ""

	err := rootCmd.RunE(rootCmd, []string{"routnie", "list"})
	if err == nil {
		t.Fatal("expected an error for `crewship routnie list`, got nil")
	}
	if !strings.Contains(err.Error(), "routine") {
		t.Errorf("error should suggest the real `routine` command, got: %v", err)
	}
}

// TestRootHeadlessAllowsBareMultiWordPrompt guards the other side of the
// line. A natural-language question is what the bare-prompt path exists
// for, and its words are individually slug-shaped ("why", "is", "this",
// "slow" all match the subcommand regex) — so the guard cannot key on
// token shape alone. The rule is shape AND brevity: <= 2 tokens.
func TestRootHeadlessAllowsBareMultiWordPrompt(t *testing.T) {
	stubAskCmdRunE(t)
	flagRootPrompt = ""

	cases := [][]string{
		{"why", "is", "this", "slow"},
		{"what", "time", "is", "it"},
		// Single arg carrying whitespace — `crewship "say hi"`.
		{"say hi"},
		// Capitalised / punctuated questions are prompts, not slugs.
		{"Why", "so", "slow?"},
	}
	for _, args := range cases {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			err := rootCmd.RunE(rootCmd, args)
			if err != nil && strings.Contains(err.Error(), "unknown command") {
				t.Errorf("`crewship %s` is a prompt, must not be flagged as a typo, got: %v",
					strings.Join(args, " "), err)
			}
		})
	}
}

// TestRootHeadlessExplicitDashPBypassesMultiArgGuard — the user who typed
// -p opted in explicitly; the guard must never second-guess that, however
// slug-shaped the value is.
func TestRootHeadlessExplicitDashPBypassesMultiArgGuard(t *testing.T) {
	stubAskCmdRunE(t)
	flagRootPrompt = "schedule list"

	err := rootCmd.RunE(rootCmd, []string{})
	if err != nil && strings.Contains(err.Error(), "unknown command") {
		t.Errorf("explicit -p must bypass the typo guard, got: %v", err)
	}
}
