package main

import (
	"os"

	"github.com/spf13/cobra"
)

var completionCmd = &cobra.Command{
	Use:   "completion [bash|zsh|fish|powershell]",
	Short: "Generate shell completion scripts",
	Long: `Generate shell completion scripts for crewship.

To load completions:

Bash:
  $ source <(crewship completion bash)
  # To load completions for each session, execute once:
  $ crewship completion bash > /etc/bash_completion.d/crewship

Zsh:
  $ source <(crewship completion zsh)
  # To load completions for each session, execute once:
  $ crewship completion zsh > "${fpath[1]}/_crewship"

Fish:
  $ crewship completion fish | source
  # To load completions for each session, execute once:
  $ crewship completion fish > ~/.config/fish/completions/crewship.fish
`,
	Args:                  cobra.MatchAll(cobra.ExactArgs(1), cobra.OnlyValidArgs),
	ValidArgs:             []string{"bash", "zsh", "fish", "powershell"},
	DisableFlagsInUseLine: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		switch args[0] {
		case "bash":
			return rootCmd.GenBashCompletion(os.Stdout)
		case "zsh":
			return rootCmd.GenZshCompletion(os.Stdout)
		case "fish":
			return rootCmd.GenFishCompletion(os.Stdout, true)
		case "powershell":
			return rootCmd.GenPowerShellCompletionWithDesc(os.Stdout)
		}
		return nil
	},
}
