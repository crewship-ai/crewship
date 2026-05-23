package main

import (
	"strings"
	"testing"
)

// TestLooksLikeSubcommandTypo pins down the heuristic that distinguishes
// "user typoed a subcommand name" from "user piped a single-word question
// into the headless ask path".
//
// The rule: a single positional whose shape matches a Cobra subcommand
// slug (lowercase ASCII, hyphens, 2-30 chars) is almost certainly a typo.
// A real question that the user wants delivered to the default agent
// contains punctuation or a capital letter, or is multi-word — none of
// which the heuristic rejects.
func TestLooksLikeSubcommandTypo(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want bool
		why  string
	}{
		// Real typos — want=true (block + suggest). Anything that
		// matches the shape of a registered subcommand but isn't one
		// Cobra resolved (because Cobra runs first, only unknown names
		// reach our RunE) is a typo by definition. The escape hatch
		// for "I really do want to ask a one-word question" is -p.
		{"status", true, "exact case observed in dev2 audit"},
		{"ls", true, "shell habit"},
		{"start", true, "common typo for `run`"},
		{"version", true, "typo for `--version` flag"},
		{"definitely-not-a-real-subcommand", true, "long slug shape from audit issue"},
		{"a-b", true, "slug w/ hyphen"},
		{"why", true, "single-word lowercase — use -p \"why\" to actually ask"},
		{"hi", true, "two-char word — same rule"},

		// Genuine prompts — want=false (don't block)
		{"hello world", false, "multi-word — clearly a prompt"},
		{"what time is it", false, "multi-word question"},
		{"Hello?", false, "capital + punctuation"},
		{"WHY", false, "uppercase — not slug shape"},
		{"summarize today's PRs", false, "apostrophe + multi-word"},
		{"a", false, "single char — too short to be a real command name"},
		{"x" + strings.Repeat("y", 60), false, "longer than any real subcommand"},
		{"123", false, "starts with digit — not slug shape"},
		{"-flag", false, "starts with dash — looks like a flag"},
		{"foo/bar", false, "contains slash — not a slug"},
		{"foo_bar", false, "contains underscore — not Crewship slug style"},
	}
	for _, tc := range cases {
		got := looksLikeSubcommandTypo(tc.in)
		if got != tc.want {
			t.Errorf("looksLikeSubcommandTypo(%q) = %v, want %v (%s)", tc.in, got, tc.want, tc.why)
		}
	}
}

// TestRootHeadlessRejectsTypoSingleArg confirms the integration: typing
// `crewship status` (one slug-shaped positional, no -p flag) returns a
// non-nil error that names the unknown command, instead of falling
// through to ask's "no default agent" message.
//
// We invoke rootCmd.RunE directly with a synthesized args slice rather
// than going through Execute(), so the test doesn't need a real server
// or login state. The early-bail in the typo branch happens before
// anything network-touching.
func TestRootHeadlessRejectsTypoSingleArg(t *testing.T) {
	// flagRootPrompt is set by the -p flag binding; tests must clear it
	// because Go test binaries reuse package globals across cases.
	flagRootPrompt = ""

	err := rootCmd.RunE(rootCmd, []string{"status"})
	if err == nil {
		t.Fatal("expected an error for `crewship status`, got nil")
	}
	if !strings.Contains(err.Error(), "unknown command") {
		t.Errorf("error should name the unknown command, got: %v", err)
	}
	if !strings.Contains(err.Error(), "status") {
		t.Errorf("error should echo the typo'd token, got: %v", err)
	}
}

// TestRootHeadlessAllowsRealPrompt confirms that the typo guard does NOT
// fire when the user clearly meant the headless-ask path — multi-word,
// punctuation, or capitals. We just check that we don't get the typo
// error; the downstream ask path will fail on its own (no default agent,
// no auth), and that's the existing behaviour we're preserving.
func TestRootHeadlessAllowsRealPrompt(t *testing.T) {
	flagRootPrompt = ""

	err := rootCmd.RunE(rootCmd, []string{"what time is it"})
	if err != nil && strings.Contains(err.Error(), "unknown command") {
		t.Errorf("multi-word prompt must not be flagged as typo, got: %v", err)
	}
}

// TestRootHeadlessAllowsExplicitDashPSingleWord confirms that the user's
// explicit `-p <word>` is honored even when the value matches the slug
// heuristic — they typed -p, they meant a one-shot ask.
func TestRootHeadlessAllowsExplicitDashPSingleWord(t *testing.T) {
	flagRootPrompt = "status"
	defer func() { flagRootPrompt = "" }()

	err := rootCmd.RunE(rootCmd, []string{})
	if err != nil && strings.Contains(err.Error(), "unknown command") {
		t.Errorf("explicit -p must bypass the typo guard, got: %v", err)
	}
}
