package main

import (
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// flagRootPrompt holds the value of the top-level `-p / --prompt` flag.
//
// Wired to rootCmd in init() below. Distinct from the subcommand-level
// --prompt on `ask` / `run` — this one fires only when no subcommand is
// given, dispatching to the default agent for a one-shot query.
//
//	crewship -p "what time is it?"
//	cat issue.md | crewship -p "summarise"
//	crewship -p "review my diff" --with-git-diff
//
// Pipe-friendly: stdin is auto-appended (same as `ask`), and exit code
// reflects the run's terminal state (0 done, 1 error). When --format
// ndjson is requested the run streams as line-delimited events so it
// composes with jq / downstream tooling.
var flagRootPrompt string

// subcommandSlugRe matches the shape of every registered Crewship
// subcommand: lowercase ASCII letter or digit start, then letters,
// digits, or hyphens. We deliberately exclude underscores and slashes
// because Crewship never uses them in command names.
var subcommandSlugRe = regexp.MustCompile(`^[a-z][a-z0-9-]+$`)

// looksLikeSubcommandTypo reports whether a single positional arg is
// almost certainly a misspelled subcommand rather than a one-shot
// question for the default agent.
//
// Rule: any single token whose shape matches a Crewship subcommand
// slug (lowercase ASCII, digits, hyphens, 2-40 chars, no whitespace /
// underscore / slash / leading digit / leading dash) is treated as a
// typo. By the time we get here Cobra has already resolved real
// subcommand names, so a slug-shaped input that reaches us is unknown
// to Cobra — that's the typo signal. The escape hatch for "I really
// do want to ask one word" is the explicit `-p "<word>"` flag.
//
// 40-char upper bound is generous enough for the longest plausibly-
// typed nonsense (the audit reported `definitely-not-a-real-subcommand`
// at 32 chars) without ever capturing a real free-form question, which
// would always contain whitespace or punctuation by the time it's
// 40 chars long.
func looksLikeSubcommandTypo(arg string) bool {
	// Whitespace? Definitely a sentence, not a slug.
	if strings.ContainsAny(arg, " \t\n") {
		return false
	}
	// Bounds: shorter than 2 chars is "a" / "x"; longer than 40 is
	// past any registered command and past any single-word question.
	if len(arg) < 2 || len(arg) > 40 {
		return false
	}
	return subcommandSlugRe.MatchString(arg)
}

// runHeadlessAsk dispatches a top-level `-p "..."` invocation to the
// same code-path as `crewship ask "..."`. Internally we re-execute the
// askCmd with the prompt argument synthesised; this keeps a single
// source of truth for prompt assembly + agent picking instead of
// duplicating BuildPrompt orchestration into a parallel branch.
func runHeadlessAsk(cmd *cobra.Command, prompt string) error {
	// Bail early in non-TTY mode if no prompt was given and stdin is
	// empty — friendlier than a generic "prompt required" later.
	if strings.TrimSpace(prompt) == "" {
		stdinHasData := false
		if fi, err := os.Stdin.Stat(); err == nil {
			stdinHasData = (fi.Mode() & os.ModeCharDevice) == 0
		}
		if !stdinHasData && !term.IsTerminal(int(os.Stdin.Fd())) {
			return fmt.Errorf("`-p` requires a prompt (positional or piped via stdin)")
		}
	}

	// Reuse askCmd's flag surface — copy through anything explicitly set
	// on the root command. The flags shared by both are: --agent, --quiet,
	// --no-stream, --timeout, --markdown / --no-markdown, --save,
	// --with-* context flags. Only the explicitly-set ones are forwarded
	// so defaults on `ask` itself stay authoritative.
	if cmd.Flags().Changed("agent") {
		v, _ := cmd.Flags().GetString("agent")
		_ = askCmd.Flags().Set("agent", v)
	}
	if cmd.Flags().Changed("quiet") {
		v, _ := cmd.Flags().GetBool("quiet")
		if v {
			_ = askCmd.Flags().Set("quiet", "true")
		}
	}
	// Default to quiet output in headless mode unless the user explicitly
	// asked otherwise — `crewship -p` is meant for scripting where the
	// agent's banner / meta lines are noise.
	if !cmd.Flags().Changed("quiet") {
		_ = askCmd.Flags().Set("quiet", "true")
	}

	// Synthesise the positional prompt and delegate to askCmd.RunE.
	// args=[prompt] mirrors what cobra would have parsed had the user
	// typed `crewship ask "<prompt>"`.
	args := []string{}
	if strings.TrimSpace(prompt) != "" {
		args = append(args, prompt)
	}
	if askCmd.RunE != nil {
		// Forward the root command's context so SIGINT / SIGTERM and
		// any deadline reach askCmd. Without this, askCmd.Context() in
		// BuildPrompt and HTTP calls falls back to context.Background
		// and the headless run can't be interrupted.
		askCmd.SetContext(cmd.Context())
		return askCmd.RunE(askCmd, args)
	}
	return fmt.Errorf("internal: ask command has no RunE")
}

func init() {
	// Local flag on rootCmd — NOT persistent. Persistent would collide
	// with the existing `ask -p` short flag whenever a user typed
	// `crewship ask -p "foo"` (cobra would surface our flag instead).
	rootCmd.Flags().StringVarP(&flagRootPrompt, "prompt", "p", "",
		"Quick one-shot prompt to the default agent (no subcommand required)")

	// Wire RunE on rootCmd: fires only when no subcommand is given. We
	// gate on flagRootPrompt being set so a bare `crewship` still falls
	// through to Cobra's default usage screen.
	rootCmd.RunE = func(cmd *cobra.Command, args []string) error {
		// Copy into a local so we don't mutate the package-level
		// flagRootPrompt — repeated in-process executions (REPL, tests)
		// would otherwise see a stale value from a previous run when
		// the user typed `crewship "first"` followed by `crewship -p ""`.
		prompt := flagRootPrompt

		// Typo guard (gh#554): the headless-ask fallback used to fire
		// for *any* unmatched positional, including bare subcommand
		// typos like `crewship status` or `crewship lz`. Cobra resolves
		// real subcommand names before RunE runs, so anything that
		// reaches us here is unknown to Cobra. When the user did NOT
		// opt into the ask path with -p AND the single positional looks
		// like a subcommand slug, reject with an explicit
		// "unknown command" instead of dispatching a real LLM run
		// against the typo.
		if prompt == "" && len(args) == 1 && looksLikeSubcommandTypo(args[0]) {
			return fmt.Errorf("unknown command %q for %q; run %q to see available commands",
				args[0], cmd.CommandPath(), cmd.CommandPath()+" --help")
		}

		if prompt == "" && len(args) > 0 {
			prompt = strings.Join(args, " ")
		}
		if prompt == "" {
			return cmd.Help()
		}
		return runHeadlessAsk(cmd, prompt)
	}
	// Print Help() on `crewship` with no flags/args so the new RunE
	// doesn't change the default empty-invocation behaviour.
	rootCmd.SilenceUsage = true
	// Surface a couple of common subcommand flags at the root so users
	// can write `crewship -p "..." --quiet --agent viktor` without
	// learning `ask`. These have no effect on actual subcommands because
	// they are not Persistent.
	rootCmd.Flags().String("agent", "", "Agent slug or ID (overrides default-agent)")
	rootCmd.Flags().BoolP("quiet", "q", false, "Only output agent text (no banner)")
	// Allow arbitrary positional args alongside `-p` so `crewship "say hi"`
	// (no flag) and `crewship -p "say hi"` both work. Cobra rejects
	// positional args by default unless ArbitraryArgs / MinimumNArgs is set.
	rootCmd.Args = cobra.ArbitraryArgs
}
