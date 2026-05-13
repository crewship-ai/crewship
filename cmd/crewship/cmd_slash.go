package main

import (
	"context"
	"fmt"
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
