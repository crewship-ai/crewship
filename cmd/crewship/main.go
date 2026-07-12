package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
	"golang.org/x/term"
	"gopkg.in/yaml.v3"
)

var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

var (
	flagServer              string
	flagWorkspace           string
	flagFormat              string
	flagProfile             string
	flagVerbose             bool
	flagNoColor             bool
	flagAllowServerMismatch bool

	cliCfg *cli.CLIConfig
)

var rootCmd = &cobra.Command{
	Use:   "crewship",
	Short: "Crewship — AI Agent Orchestration Platform",
	Long:  "Crewship CLI allows you to manage agents, crews, missions, skills, and credentials from the terminal.",
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		cli.InitColors(flagNoColor)
		// Feed the ldflags-injected version to the client's version-skew
		// detector (X-Crewship-Server-Version comparison, once per process).
		cli.SetClientVersion(version)

		// Inject the working directory so internal profile resolution can do
		// directory_profiles matching without reaching into the filesystem
		// itself (provider pattern). Best-effort: an error just disables
		// directory-based selection.
		if wd, werr := os.Getwd(); werr == nil {
			cli.SetWorkingDir(wd)
		}

		cfg, err := cli.LoadConfig()
		if err != nil {
			cli.PrintWarning("failed to load config: " + err.Error())
			cfg = &cli.CLIConfig{}
		}
		// Overlay the active server profile (--profile / CREWSHIP_PROFILE /
		// directory match / `current`) onto the top-level Server/Token/
		// Workspace so every downstream read path — ResolveServer,
		// newAPIClient's token-host binding, requireAuth — sees the selected
		// target with zero per-command changes. Profile-writing commands
		// (login, server, config set) re-load the raw config themselves, so
		// this read-side overlay never corrupts what gets saved.
		cliCfg = cfg.WithActiveProfile(flagProfile)
	},
	SilenceUsage:  true,
	SilenceErrors: true,
}

func init() {
	rootCmd.PersistentFlags().StringVarP(&flagServer, "server", "s", "", "Server URL (default: http://localhost:8080, env: CREWSHIP_SERVER)")
	rootCmd.PersistentFlags().StringVarP(&flagWorkspace, "workspace", "w", "", "Workspace ID or slug (env: CREWSHIP_WORKSPACE)")
	rootCmd.PersistentFlags().StringVarP(&flagFormat, "format", "f", "", "Output format: table|json|yaml|ndjson|quiet (default: table)")
	rootCmd.PersistentFlags().StringVar(&flagProfile, "profile", "", "Server profile to target (env: CREWSHIP_PROFILE; manage with 'crewship server')")
	rootCmd.PersistentFlags().BoolVarP(&flagVerbose, "verbose", "v", false, "Verbose output")
	rootCmd.PersistentFlags().BoolVar(&flagNoColor, "no-color", false, "Disable ANSI colors")
	rootCmd.PersistentFlags().BoolVar(&flagAllowServerMismatch, "server-allow-mismatch", false,
		"Allow sending the stored auth token to a --server host that differs from the one it was issued for (env: CREWSHIP_ALLOW_SERVER_MISMATCH)")

	rootCmd.AddCommand(initCmd)
	rootCmd.AddCommand(versionCmd)
	rootCmd.AddCommand(selfUpdateCmd)
	rootCmd.AddCommand(loginCmd)
	rootCmd.AddCommand(logoutCmd)
	rootCmd.AddCommand(whoamiCmd)
	rootCmd.AddCommand(authCmd)
	rootCmd.AddCommand(workspaceCmd)
	rootCmd.AddCommand(agentCmd)
	rootCmd.AddCommand(crewCmd)
	rootCmd.AddCommand(runCmd)
	rootCmd.AddCommand(askCmd)
	rootCmd.AddCommand(retryCmd)
	rootCmd.AddCommand(explainCmd)
	rootCmd.AddCommand(copyPromptCmd)
	rootCmd.AddCommand(inspectCmd)
	rootCmd.AddCommand(promptCmd)
	rootCmd.AddCommand(openCmd)
	rootCmd.AddCommand(exportCmd)
	rootCmd.AddCommand(chatCmd)
	rootCmd.AddCommand(triageCmd)
	rootCmd.AddCommand(recurringCmd)
	rootCmd.AddCommand(savedViewCmd)
	rootCmd.AddCommand(workflowCmd)    // SPEC-2: workflow templates
	rootCmd.AddCommand(featureFlagCmd) // SPEC-2: feature flags
	rootCmd.AddCommand(instanceCmd)    // SPEC-2: instance settings
	rootCmd.AddCommand(mcpCallsCmd)
	rootCmd.AddCommand(metricsCmd)
	rootCmd.AddCommand(lintCmd)
	rootCmd.AddCommand(logsCmd)
	rootCmd.AddCommand(skillCmd)
	rootCmd.AddCommand(credentialCmd)
	rootCmd.AddCommand(integrationCmd)
	rootCmd.AddCommand(missionCmd)
	rootCmd.AddCommand(activityCmd)
	rootCmd.AddCommand(conversationCmd)
	rootCmd.AddCommand(historyCmd)
	rootCmd.AddCommand(journalCmd)
	rootCmd.AddCommand(recallCmd)
	rootCmd.AddCommand(watchCmd)
	rootCmd.AddCommand(paymasterCmd)
	rootCmd.AddCommand(costCmd)
	rootCmd.AddCommand(modelCmd)
	rootCmd.AddCommand(approvalsCmd)
	rootCmd.AddCommand(inboxCmd)
	rootCmd.AddCommand(pipelineCmd)
	rootCmd.AddCommand(checkpointCmd)
	rootCmd.AddCommand(notifyChannelCmd)
	rootCmd.AddCommand(consolidateCmd)
	rootCmd.AddCommand(evalCmd)
	rootCmd.AddCommand(hooksCmd)
	rootCmd.AddCommand(presenceCmd)
	rootCmd.AddCommand(auditCmd)
	rootCmd.AddCommand(completionCmd)
	rootCmd.AddCommand(tokenCmd)
	rootCmd.AddCommand(sessionCmd)
	rootCmd.AddCommand(configCmd)
	rootCmd.AddCommand(serverCmd)
	rootCmd.AddCommand(escalationCmd)
	rootCmd.AddCommand(exposeCmd)
	rootCmd.AddCommand(templateCmd)
	rootCmd.AddCommand(systemCmd)
	rootCmd.AddCommand(policyCmd)
	rootCmd.AddCommand(issueCmd)
	rootCmd.AddCommand(projectCmd)
	rootCmd.AddCommand(seedCmd)
	rootCmd.AddCommand(nukeCmd)
	rootCmd.AddCommand(featuresCmd)
	rootCmd.AddCommand(runtimesCmd)
	rootCmd.AddCommand(memoryCmd)
	rootCmd.AddCommand(personaCmd)
	rootCmd.AddCommand(notificationCmd)
	rootCmd.AddCommand(labelCmd)
	rootCmd.AddCommand(backupCmd)
	rootCmd.AddCommand(preferencesCmd)
	rootCmd.AddCommand(privacyCmd)
}

func main() {
	// Mount user-defined slash commands AFTER built-ins so the
	// collision-detection in registerSlashCommands sees the full
	// built-in set and can warn-and-skip on name clashes.
	registerSlashCommands()

	// ExecuteC (not Execute) so the error path can see the resolved
	// command's flags — the legacy per-command --json bool must select
	// the structured error envelope exactly like it selects success JSON.
	cmd, err := rootCmd.ExecuteC()
	if err != nil {
		exitWithError(cmd, err)
	}
}

// exitWithError prints err and exits with its mapped exit code.
//
// Human formats (table/quiet) keep the historical plain-text line. When the
// operator asked for a machine format (json/ndjson/yaml), the error is
// emitted on stderr as a structured envelope in that same format, so an
// agent driving the CLI parses failures the same way it parses successes —
// stdout stays reserved for success output either way.
func exitWithError(cmd *cobra.Command, err error) {
	format := cli.ResolveFormat(flagFormat, cliCfg)
	if cmd != nil {
		format = resolvedFormat(cmd)
	}
	switch format {
	case "json", "ndjson":
		enc := json.NewEncoder(os.Stderr)
		if format == "json" {
			enc.SetIndent("", "  ")
		}
		if encErr := enc.Encode(cli.NewErrorEnvelope(err)); encErr != nil {
			fmt.Fprintln(os.Stderr, err)
		}
	case "yaml":
		if data, mErr := yaml.Marshal(cli.NewErrorEnvelope(err)); mErr == nil {
			_, _ = os.Stderr.Write(data)
		} else {
			fmt.Fprintln(os.Stderr, err)
		}
	default:
		fmt.Fprintln(os.Stderr, err)
	}
	os.Exit(cli.ExitCodeFor(err))
}

// newAPIClient creates an authenticated API client from resolved config.
//
// The token is bound to the host of the *configured* server (cliCfg.Server)
// via c.TokenHost. When --server / CREWSHIP_SERVER points the client at a
// different host, the client refuses to attach the bearer token unless the
// operator opts in with --server-allow-mismatch — this is the guard against
// token exfiltration to an attacker-controlled host (issue #571). In the
// common case (no --server override) the resolved host equals the configured
// host, so there is zero friction.
func newAPIClient() *cli.Client {
	// EffectiveServer (not ResolveServer) so an active profile's server wins
	// over a stale CREWSHIP_SERVER env left over from the #544 stopgap; the
	// token is host-bound to that same profile server below, so the two agree.
	server := cli.EffectiveServer(flagServer, flagProfile, cliCfg)
	workspace := cli.ResolveWorkspace(flagWorkspace, cliCfg)
	token := ""
	tokenHost := ""
	if envTok := cli.EnvToken(); envTok != "" {
		// CREWSHIP_TOKEN: explicit per-shell credential (CI, agent
		// containers). Wins over the stored token and is deliberately
		// NOT host-bound — the operator scoped it to this environment;
		// the #571 guard protects the *persisted* token, not this one.
		token = envTok
	} else if cliCfg != nil {
		token = cliCfg.Token
		tokenHost = serverHost(cliCfg.Server)
	}
	c := cli.NewClient(server, token, workspace)
	c.Verbose = flagVerbose
	c.TokenHost = tokenHost
	c.AllowHostMismatch = flagAllowServerMismatch || envAllowServerMismatch()
	return c
}

// serverHost extracts the lowercased hostname from a server URL, or "" if
// the URL is empty/unparseable (in which case token-host binding is skipped).
func serverHost(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

// envAllowServerMismatch reports whether CREWSHIP_ALLOW_SERVER_MISMATCH is
// set to a truthy value, the env-var twin of --server-allow-mismatch.
func envAllowServerMismatch() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CREWSHIP_ALLOW_SERVER_MISMATCH"))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// newFormatter creates a formatter with the resolved format.
func newFormatter() *cli.Formatter {
	format := cli.ResolveFormat(flagFormat, cliCfg)
	return cli.NewFormatter(format)
}

// requireAuth checks that a token is configured for the active target. When a
// profile is selected but undefined (typo'd --profile/CREWSHIP_PROFILE or a
// `current` pointing at a removed profile), the overlay blanks the token, so
// point the operator at the profile rather than a generic "run login".
func requireAuth() error {
	// CREWSHIP_TOKEN short-circuits everything: the env credential is a
	// complete, self-contained auth source (see newAPIClient), so neither
	// a missing config nor an unconfigured profile should block it.
	if cli.EnvToken() != "" {
		return nil
	}
	// Handle a selected profile first: a profile can carry a token but an empty
	// server (hand-edited / half-written config), and accepting that token
	// before the target is proven configured would let later fallbacks dial the
	// wrong host. Require a non-empty profile server before the token counts.
	if name := cli.ActiveProfileName(flagProfile, cliCfg); name != "" {
		if cliCfg == nil {
			return cli.WithExitCode(fmt.Errorf("profile %q is not configured — run 'crewship server add %s --server <url>' then 'crewship login --profile %s'", name, name, name), cli.ExitAuth)
		}
		p := cliCfg.Servers[name]
		if p == nil || strings.TrimSpace(p.Server) == "" {
			return cli.WithExitCode(fmt.Errorf("profile %q is not configured — run 'crewship server add %s --server <url>' then 'crewship login --profile %s'", name, name, name), cli.ExitAuth)
		}
		if strings.TrimSpace(p.Token) != "" {
			return nil
		}
		return cli.WithExitCode(fmt.Errorf("not logged into profile %q. Run 'crewship login --profile %s'", name, name), cli.ExitAuth)
	}
	if cliCfg != nil && cliCfg.Token != "" {
		return nil
	}
	return cli.WithExitCode(fmt.Errorf("not logged in. Run 'crewship login' first"), cli.ExitAuth)
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

// requireWorkspace checks that a workspace is configured. The failure is
// typed ExitValidation — a precondition the caller fixes by passing
// --workspace — mirroring requireAuth's typed ExitAuth.
func requireWorkspace() error {
	ws := cli.ResolveWorkspace(flagWorkspace, cliCfg)
	if ws == "" {
		return cli.WithExitCode(
			fmt.Errorf("no workspace set. Use --workspace flag or run 'crewship workspace use <slug>'"),
			cli.ExitValidation)
	}
	return nil
}
