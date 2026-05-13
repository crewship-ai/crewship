package main

import (
	"fmt"
	"os"
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
		// If the user typed positional args without a subcommand, treat
		// the joined args as the prompt. `crewship "say hi"` works.
		if flagRootPrompt == "" && len(args) > 0 {
			flagRootPrompt = strings.Join(args, " ")
		}
		if flagRootPrompt == "" {
			return cmd.Help()
		}
		return runHeadlessAsk(cmd, flagRootPrompt)
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

