package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

// registerSlashCommands loads ~/.crewship/commands/*.md and mounts each
// one as a dynamic cobra subcommand of rootCmd.
//
// Naming-collision policy: built-in commands always win. A slash file
// that would shadow `ask`, `run`, `tui`, etc. is skipped with a warning
// to stderr — overriding ships is dangerous (think `rm`, `run` etc.) and
// surfacing the collision is much friendlier than silently masking.
//
// Failures during loading (missing dir, malformed file) never block the
// CLI from starting; they degrade to a warning. The CLI must keep
// working without user-defined commands.
func registerSlashCommands() {
	// Startup fast-path: when argv[1] is a built-in command, no slash
	// command can possibly be listed or dispatched (built-ins always win —
	// see the collision policy above), so skip the readdir + per-file parse
	// of ~/.crewship/commands entirely. `crewship version`, `crewship run …`
	// etc. stay free of filesystem work they can't use. The shadow warning
	// consequently only prints on invocations that actually load slash
	// commands (help/completion/unknown arg) — exactly the surfaces where
	// the collision is visible.
	if !shouldLoadSlashCommands(os.Args) {
		return
	}

	// Startup-time registration uses Background — the calling shell's
	// cobra context isn't bound yet at init() time. A slow commands
	// directory is bounded by os.ReadDir's own timeout characteristics
	// and the loader's per-file error swallowing.
	cmds, err := cli.LoadSlashCommands(context.Background())
	if err != nil {
		cli.PrintWarning("loading slash commands: " + err.Error())
		return
	}
	builtins := map[string]bool{}
	for _, c := range rootCmd.Commands() {
		builtins[c.Name()] = true
	}

	for _, sc := range cmds {
		if builtins[sc.Name] {
			fmt.Fprintf(rootCmd.ErrOrStderr(),
				"%s[slash]%s %s shadows built-in command — skipping\n",
				cli.Yellow, cli.Reset, sc.Name)
			continue
		}
		rootCmd.AddCommand(makeSlashCobra(sc))
	}
}

// shouldLoadSlashCommands reports whether this invocation can possibly list
// or dispatch a user-defined slash command. Help/completion surfaces, global
// flags, and unknown first args must load; a built-in first arg (name or
// alias) never can, so the scan is skipped.
func shouldLoadSlashCommands(args []string) bool {
	if len(args) < 2 {
		return true // bare `crewship` renders the full help listing
	}
	first := args[1]
	if strings.HasPrefix(first, "-") {
		return true // global flag (--help and friends enumerate commands)
	}
	switch first {
	case "help", "completion", "__complete", "__completeNoDesc", "commands":
		// These enumerate the command tree — slash commands included.
		return true
	}
	for _, c := range rootCmd.Commands() {
		if c.Name() == first {
			return false // built-in wins; a slash command can't shadow it
		}
		for _, a := range c.Aliases {
			if a == first {
				return false
			}
		}
	}
	return true // unknown — could be a slash command
}

// makeSlashCobra wraps one loaded SlashCommand into a cobra.Command that
// dispatches to ask logic with the rendered prompt body.
func makeSlashCobra(sc cli.SlashCommand) *cobra.Command {
	desc := sc.Description
	if desc == "" {
		desc = "User-defined command from " + sc.Source
	}
	c := &cobra.Command{
		Use:   sc.Name + " [args...]",
		Short: desc,
		Long: fmt.Sprintf(`%s

Loaded from: %s
Template variables: %s`,
			desc, sc.Source, strings.Join(sc.Vars, ", ")),
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			body := sc.Render(args)
			if strings.TrimSpace(body) == "" {
				return fmt.Errorf("slash command %q rendered empty body", sc.Name)
			}
			// Frontmatter values pre-fill ask flags before dispatch.
			if sc.Agent != "" {
				_ = askCmd.Flags().Set("agent", sc.Agent)
			}
			if sc.Effort != "" {
				if err := SetEffort(sc.Effort); err != nil {
					return err
				}
			}
			if sc.Plan {
				planModeRequested = true
				body = ApplyPlanFlag(body)
			}
			_ = askCmd.Flags().Set("prompt", body)
			if askCmd.RunE == nil {
				return fmt.Errorf("internal: ask command has no RunE")
			}
			return askCmd.RunE(askCmd, nil)
		},
	}
	return c
}
