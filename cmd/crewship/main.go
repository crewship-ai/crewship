package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

var (
	flagServer    string
	flagWorkspace string
	flagFormat    string
	flagVerbose   bool
	flagNoColor   bool

	cliCfg *cli.CLIConfig
)

var rootCmd = &cobra.Command{
	Use:   "crewship",
	Short: "Crewship — AI Agent Orchestration Platform",
	Long:  "Crewship CLI allows you to manage agents, crews, missions, skills, and credentials from the terminal.",
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		cli.InitColors(flagNoColor)

		cfg, err := cli.LoadConfig()
		if err != nil {
			cli.PrintWarning("failed to load config: " + err.Error())
			cfg = &cli.CLIConfig{}
		}
		cliCfg = cfg
	},
	SilenceUsage:  true,
	SilenceErrors: true,
}

func init() {
	rootCmd.PersistentFlags().StringVarP(&flagServer, "server", "s", "", "Server URL (default: http://localhost:8080, env: CREWSHIP_SERVER)")
	rootCmd.PersistentFlags().StringVarP(&flagWorkspace, "workspace", "w", "", "Workspace ID or slug (env: CREWSHIP_WORKSPACE)")
	rootCmd.PersistentFlags().StringVarP(&flagFormat, "format", "f", "", "Output format: table|json|yaml|quiet (default: table)")
	rootCmd.PersistentFlags().BoolVarP(&flagVerbose, "verbose", "v", false, "Verbose output")
	rootCmd.PersistentFlags().BoolVar(&flagNoColor, "no-color", false, "Disable ANSI colors")

	rootCmd.AddCommand(initCmd)
	rootCmd.AddCommand(startCmd)
	rootCmd.AddCommand(versionCmd)
	rootCmd.AddCommand(doctorCmd)
	rootCmd.AddCommand(loginCmd)
	rootCmd.AddCommand(logoutCmd)
	rootCmd.AddCommand(whoamiCmd)
	rootCmd.AddCommand(workspaceCmd)
	rootCmd.AddCommand(agentCmd)
	rootCmd.AddCommand(crewCmd)
	rootCmd.AddCommand(runCmd)
	rootCmd.AddCommand(logsCmd)
	rootCmd.AddCommand(skillCmd)
	rootCmd.AddCommand(credentialCmd)
	rootCmd.AddCommand(integrationCmd)
	rootCmd.AddCommand(missionCmd)
	rootCmd.AddCommand(activityCmd)
	rootCmd.AddCommand(journalCmd)
	rootCmd.AddCommand(paymasterCmd)
	rootCmd.AddCommand(auditCmd)
	rootCmd.AddCommand(completionCmd)
	rootCmd.AddCommand(tokenCmd)
	rootCmd.AddCommand(configCmd)
	// captainCmd is deprecated (2026-04-16) — see cmd_captain.go file header.
	// Registered for backward compat with existing user scripts.
	rootCmd.AddCommand(captainCmd)
	rootCmd.AddCommand(proposalCmd)
	rootCmd.AddCommand(escalationCmd)
	rootCmd.AddCommand(exposeCmd)
	rootCmd.AddCommand(templateCmd)
	rootCmd.AddCommand(systemCmd)
	rootCmd.AddCommand(issueCmd)
	rootCmd.AddCommand(projectCmd)
	rootCmd.AddCommand(seedCmd)
	rootCmd.AddCommand(featuresCmd)
	rootCmd.AddCommand(runtimesCmd)
	rootCmd.AddCommand(seedIssuesCmd) // deprecated: use "crewship seed" instead
	rootCmd.AddCommand(memoryCmd)
	rootCmd.AddCommand(notificationCmd)
	rootCmd.AddCommand(labelCmd)
	rootCmd.AddCommand(backupCmd)
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// newAPIClient creates an authenticated API client from resolved config.
func newAPIClient() *cli.Client {
	server := cli.ResolveServer(flagServer, cliCfg)
	workspace := cli.ResolveWorkspace(flagWorkspace, cliCfg)
	token := ""
	if cliCfg != nil {
		token = cliCfg.Token
	}
	c := cli.NewClient(server, token, workspace)
	c.Verbose = flagVerbose
	return c
}

// newFormatter creates a formatter with the resolved format.
func newFormatter() *cli.Formatter {
	format := cli.ResolveFormat(flagFormat, cliCfg)
	return cli.NewFormatter(format)
}

// requireAuth checks that a token is configured.
func requireAuth() error {
	if cliCfg == nil || cliCfg.Token == "" {
		return fmt.Errorf("not logged in. Run 'crewship login' first")
	}
	return nil
}

// confirmAction prompts for confirmation unless --yes is passed.
//
// On a TTY, it uses a styled huh confirmation prompt for better UX.
// In non-interactive mode (CI, piped input), it falls back to a plain
// fmt.Scanln read — this keeps scripts that pipe "y" or "n" working.
func confirmAction(cmd *cobra.Command, message string) error {
	yes, _ := cmd.Flags().GetBool("yes")
	if yes {
		return nil
	}

	// Non-TTY fallback: preserve the old plain-stdin behavior so scripts
	// that pipe "y\n" or "yes\n" continue to work unchanged. We gate on
	// BOTH stdin AND stdout being TTYs — if stdout is redirected to a file
	// (e.g. `crewship delete ... > out.txt`), huh would otherwise dump ANSI
	// escape sequences into the file.
	stdinTTY := term.IsTerminal(int(os.Stdin.Fd()))
	stdoutTTY := term.IsTerminal(int(os.Stdout.Fd()))
	if !stdinTTY || !stdoutTTY {
		fmt.Fprintf(os.Stderr, "%s [y/N]: ", message)
		var answer string
		_, _ = fmt.Scanln(&answer)
		answer = strings.TrimSpace(strings.ToLower(answer))
		if answer != "y" && answer != "yes" {
			return errors.New("aborted")
		}
		return nil
	}

	// Interactive mode: use huh for a pretty confirmation.
	var confirmed bool
	err := huh.NewConfirm().
		Title(message).
		Affirmative("Yes").
		Negative("No").
		Value(&confirmed).
		Run()
	if err != nil {
		// Ctrl+C or similar — treat as abort.
		return errors.New("aborted")
	}
	if !confirmed {
		return errors.New("aborted")
	}
	return nil
}

// requireWorkspace checks that a workspace is configured.
func requireWorkspace() error {
	ws := cli.ResolveWorkspace(flagWorkspace, cliCfg)
	if ws == "" {
		return fmt.Errorf("no workspace set. Use --workspace flag or run 'crewship workspace use <slug>'")
	}
	return nil
}
