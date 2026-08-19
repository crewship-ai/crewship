package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

// shellCmd opens an interactive REPL session that dispatches typed
// lines to the default agent and exposes slash-commands for the
// frequent CLI actions.
//
// Slash commands supported v1:
//
//	/help               list commands
//	/agent <slug>       change active agent
//	/cd <slug>          switch workspace (alias for /workspace)
//	/workspace <slug>   switch workspace
//	/plan               toggle plan-mode for subsequent prompts
//	/effort <level>     set --effort
//	/think              toggle --show-thinking
//	/clear              clear terminal
//	/history            (no-op stub — readline history is v2)
//	/quit | /exit       leave
//
// Bare text is passed through ExpandAtFiles (so `summarise @notes.md`
// inlines the file) and dispatched to ask. The agent latch tracks the
// "currently active" agent across turns so `/agent viktor` then plain
// "what's up" both target viktor without re-typing.
var shellCmd = &cobra.Command{
	Use:   "shell",
	Short: "Interactive REPL session",
	Long: `Start an interactive shell where each line is a prompt to the default
agent. Slash-commands (/help, /agent, /plan, /effort, /quit) control
session state. @-prefixed tokens inline file content.

Examples (typed at the prompt):
  what time is it?
  /agent viktor
  summarise @notes.md
  /plan
  rewrite the auth flow
  /quit`,
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		_ = client

		// Shell session state. Held in closure scope so handlers
		// mutate consistently. Agent latches to whatever the user
		// last set with /agent (or default-agent at startup).
		var (
			activeAgent = cli.ResolveDefaultAgent("", cliCfg)
			planSticky  bool
			effortVal   string
			thinkOn     bool
		)

		repl := cli.NewREPL()
		repl.Prompt = shellPromptString(activeAgent)

		// Filled after the built-ins, by the server-catalog load below.
		// /help closes over it so the list it prints is the list the
		// shell actually has — a hardcoded help that omits half the
		// commands is how a user concludes the feature is missing.
		var serverActionNames []string

		repl.Register("help", func(_ context.Context, _ []string) (bool, error) {
			fmt.Fprintln(os.Stdout, `Commands:
  /agent <slug>       switch active agent
  /workspace <slug>   switch workspace (alias: /cd)
  /plan               toggle plan-mode for subsequent prompts
  /effort <level>     set reasoning effort (minimal|low|medium|high|xhigh)
  /think              toggle --show-thinking
  /clear              clear the screen
  /quit, /exit        leave`)
			if len(serverActionNames) > 0 {
				fmt.Fprintf(os.Stdout, "\nWorkspace actions (from this workspace, filtered to your grants):\n  /%s\n",
					strings.Join(serverActionNames, "\n  /"))
			}
			return true, nil
		})
		repl.Register("quit", func(_ context.Context, _ []string) (bool, error) { return false, nil })
		repl.Register("exit", func(_ context.Context, _ []string) (bool, error) { return false, nil })
		repl.Register("clear", func(_ context.Context, _ []string) (bool, error) {
			fmt.Print("\033[2J\033[H")
			return true, nil
		})
		repl.Register("agent", func(_ context.Context, args []string) (bool, error) {
			if len(args) == 0 {
				fmt.Fprintf(os.Stdout, "active agent: %s\n", activeAgent)
				return true, nil
			}
			activeAgent = args[0]
			repl.Prompt = shellPromptString(activeAgent)
			fmt.Fprintf(os.Stdout, "agent → %s\n", activeAgent)
			return true, nil
		})
		repl.Register("workspace", func(_ context.Context, args []string) (bool, error) {
			if len(args) == 0 {
				return true, fmt.Errorf("usage: /workspace <slug>")
			}
			flagWorkspace = args[0]
			fmt.Fprintf(os.Stdout, "workspace → %s\n", args[0])
			return true, nil
		})
		repl.Register("cd", func(ctx context.Context, args []string) (bool, error) {
			// /cd is an alias for /workspace — pick whichever feels native.
			return repl.Slash["workspace"](ctx, args)
		})
		repl.Register("plan", func(_ context.Context, _ []string) (bool, error) {
			planSticky = !planSticky
			fmt.Fprintf(os.Stdout, "plan-mode: %v\n", planSticky)
			return true, nil
		})
		repl.Register("effort", func(_ context.Context, args []string) (bool, error) {
			if len(args) == 0 {
				fmt.Fprintf(os.Stdout, "effort: %s\n", effortVal)
				return true, nil
			}
			if err := SetEffort(args[0]); err != nil {
				return true, err
			}
			effortVal = args[0]
			fmt.Fprintf(os.Stdout, "effort → %s\n", effortVal)
			return true, nil
		})
		repl.Register("think", func(_ context.Context, _ []string) (bool, error) {
			thinkOn = !thinkOn
			SetShowThinking(thinkOn)
			fmt.Fprintf(os.Stdout, "show-thinking: %v\n", thinkOn)
			return true, nil
		})
		repl.Register("history", func(_ context.Context, _ []string) (bool, error) {
			fmt.Fprintln(os.Stdout, "(readline history is a v2 feature — use shell-level history for now)")
			return true, nil
		})

		// Server-driven actions, loaded AFTER the built-ins above so a
		// routine can never take /exit or /help — LoadServerSlashCommands
		// skips a name already registered and warns. This is what makes
		// `/msn-etn-podklady obdobi=2026-07` work at this prompt: the
		// capability-filtered catalog arrives from
		// GET /api/v1/slash-commands and each entry becomes a command.
		//
		// Non-fatal by construction: a network blip returns 0 and logs,
		// and the shell opens as it always did.
		serverActionNames = cli.LoadServerSlashCommands(cmd.Context(), repl, client)

		repl.BareHandler = func(_ context.Context, line string) error {
			if activeAgent == "" {
				return fmt.Errorf("no active agent. Set one with /agent <slug>")
			}
			// Apply sticky state. effortVal was already validated by
			// the /effort handler at set time, so SetEffort here cannot
			// fail — but defensively check so a refactor of validation
			// doesn't silently break the REPL.
			_ = askCmd.Flags().Set("agent", activeAgent)
			_ = askCmd.Flags().Set("quiet", "true")
			if effortVal != "" {
				if err := SetEffort(effortVal); err != nil {
					return fmt.Errorf("re-apply effort %q: %w", effortVal, err)
				}
			}
			if planSticky {
				planModeRequested = true
				line = cli.ApplyPlanShellPrefix(line)
			}
			_ = askCmd.Flags().Set("prompt", line)
			if askCmd.RunE == nil {
				return fmt.Errorf("internal: ask command has no RunE")
			}
			return askCmd.RunE(askCmd, nil)
		}

		fmt.Println(strings.TrimSpace(`
crewship shell — type /help for commands, Ctrl-D to exit.
`))
		if n := len(serverActionNames); n > 0 {
			// Worth a line: these commands are workspace-specific and
			// capability-filtered, so they are in no static help text and
			// the difference is not otherwise discoverable. It points at
			// /help rather than at a bare "/", which dispatches to
			// nothing and prints nothing.
			fmt.Printf("%d workspace action(s) loaded — /help lists them.\n", n)
		}
		return repl.Run(cmd.Context())
	},
}

// shellPromptString builds the prompt line. Agent slug in the prompt is
// a small but high-information signal — at a glance the user knows
// which agent will get the next message.
func shellPromptString(agent string) string {
	if agent == "" {
		agent = "?"
	}
	return fmt.Sprintf("%screwship%s [%s] › ", cli.Bold, cli.Reset, agent)
}

func init() {
	rootCmd.AddCommand(shellCmd)
}
