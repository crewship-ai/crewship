package main

import (
	"bufio"
	"fmt"
	"os"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

var systemCmd = &cobra.Command{
	Use:   "system",
	Short: "Show system information (runtime, license, keeper)",
}

var systemInfoCmd = &cobra.Command{
	Use:   "info",
	Short: "Show runtime and license information",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}

		client := newAPIClient()
		client.WorkspaceID = ""

		// Runtime info
		runtimeResp, err := client.Get("/api/v1/system/runtime")
		if err != nil {
			return fmt.Errorf("runtime info: %w", err)
		}
		if err := cli.CheckError(runtimeResp); err != nil {
			return err
		}

		var runtime struct {
			Available bool   `json:"available"`
			Runtime   string `json:"runtime"`
			Version   string `json:"version"`
			Socket    string `json:"socket"`
		}
		if err := cli.ReadJSON(runtimeResp, &runtime); err != nil {
			return err
		}

		fmt.Printf("%sContainer Runtime%s\n", cli.Bold, cli.Reset)
		fmt.Printf("  Available:  %v\n", runtime.Available)
		fmt.Printf("  Runtime:    %s\n", runtime.Runtime)
		fmt.Printf("  Version:    %s\n", runtime.Version)
		if runtime.Socket != "" {
			fmt.Printf("  Socket:     %s\n", runtime.Socket)
		}

		// License info
		licenseResp, err := client.Get("/api/v1/system/license")
		if err != nil {
			return fmt.Errorf("license info: %w", err)
		}
		if licenseResp.StatusCode == 200 {
			var license struct {
				Edition     string `json:"edition"`
				LicenseID   string `json:"license_id"`
				LicenseeOrg string `json:"licensee_org"`
				MaxAgents   int    `json:"max_agents_per_crew"`
				MaxCrews    int    `json:"max_crews"`
				MaxMembers  int    `json:"max_members"`
			}
			if cli.ReadJSON(licenseResp, &license) == nil {
				fmt.Printf("\n%sLicense%s\n", cli.Bold, cli.Reset)
				fmt.Printf("  Edition:          %s\n", license.Edition)
				fmt.Printf("  Max crews:        %d\n", license.MaxCrews)
				fmt.Printf("  Max agents/crew:  %d\n", license.MaxAgents)
				fmt.Printf("  Max members:      %d\n", license.MaxMembers)
				if license.LicenseeOrg != "" {
					fmt.Printf("  Licensee:         %s\n", license.LicenseeOrg)
				}
			}
		} else {
			licenseResp.Body.Close()
		}

		return nil
	},
}

var systemKeeperCmd = &cobra.Command{
	Use:   "keeper",
	Short: "Show Keeper security system status",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}

		client := newAPIClient()
		client.WorkspaceID = ""

		resp, err := client.Get("/api/v1/system/keeper")
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var keeper struct {
			Enabled      bool   `json:"enabled"`
			OllamaURL    string `json:"ollama_url"`
			Model        string `json:"model"`
			OllamaOnline bool   `json:"ollama_online"`
			SecretCount  int    `json:"secret_count"`
		}
		if err := cli.ReadJSON(resp, &keeper); err != nil {
			return err
		}

		status := cli.Red + "disabled" + cli.Reset
		if keeper.Enabled {
			status = cli.Green + "enabled" + cli.Reset
		}
		ollamaStatus := cli.Red + "offline" + cli.Reset
		if keeper.OllamaOnline {
			ollamaStatus = cli.Green + "online" + cli.Reset
		}

		fmt.Printf("%sKeeper Security%s\n", cli.Bold, cli.Reset)
		fmt.Printf("  Status:       %s\n", status)
		fmt.Printf("  Ollama URL:   %s\n", keeper.OllamaURL)
		fmt.Printf("  Model:        %s\n", keeper.Model)
		fmt.Printf("  Ollama:       %s\n", ollamaStatus)
		fmt.Printf("  Secret creds: %d\n", keeper.SecretCount)

		return nil
	},
}

var systemStatsCmd = &cobra.Command{
	Use:   "stats",
	Short: "Show admin stats (workspaces, users, agents, running)",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		client := newAPIClient()
		resp, err := client.Get("/api/v1/admin/stats")
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var stats struct {
			Workspaces int `json:"workspaces"`
			Users      int `json:"users"`
			Agents     int `json:"agents"`
			Running    int `json:"running"`
		}
		if err := cli.ReadJSON(resp, &stats); err != nil {
			return err
		}

		f := newFormatter()
		if f.Format == "json" {
			return f.JSON(stats)
		}
		fmt.Printf("%sAdmin Stats%s\n", cli.Bold, cli.Reset)
		fmt.Printf("  Workspaces: %d\n", stats.Workspaces)
		fmt.Printf("  Users:      %d\n", stats.Users)
		fmt.Printf("  Agents:     %d\n", stats.Agents)
		fmt.Printf("  Running:    %d\n", stats.Running)
		return nil
	},
}

// systemOnboardingCmd is the parent for onboarding-related subcommands.
// The bare `crewship system onboarding` invocation is preserved
// (delegates to status) so existing scripts don't break, but the
// explicit `status`/`setup`/`complete` triplet is the canonical surface.
var systemOnboardingCmd = &cobra.Command{
	Use:   "onboarding",
	Short: "Onboarding status / setup / complete",
	Long: `Inspect or drive the onboarding wizard for the current user.

Subcommands:
  status     Show whether onboarding is complete (default if no subcommand)
  setup      Run the onboarding setup wizard (crew + agent + credential)
  complete   Mark onboarding as finished without running the wizard

The bare 'crewship system onboarding' invocation delegates to 'status'
for backwards compatibility with scripts that pre-date the subcommands.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		// No subcommand → behave as `status` to preserve the pre-subcommand UX.
		return systemOnboardingStatusCmd.RunE(systemOnboardingStatusCmd, args)
	},
}

var systemOnboardingStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show onboarding status for the current user",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}

		client := newAPIClient()
		client.WorkspaceID = ""

		resp, err := client.Get("/api/v1/onboarding/status")
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var result map[string]interface{}
		if err := cli.ReadJSON(resp, &result); err != nil {
			return err
		}

		f := newFormatter()
		return f.JSON(result)
	},
}

// systemOnboardingSetupCmd POSTs to /onboarding/setup — the wizard's
// "create a crew + first agent + credential" provisioning endpoint.
// All five inputs are required by the server (crew_name, agent_name,
// plus llm_provider/credential to wire the API key) so the CLI fails
// fast if any are missing rather than letting the server return 400.
var systemOnboardingSetupCmd = &cobra.Command{
	Use:   "setup",
	Short: "Run the onboarding setup wizard (crew + agent + credential)",
	Long: `Provision a starter crew, agent, and LLM credential in one shot —
the headless equivalent of the web onboarding wizard.

Required flags:
  --crew <name>           Name of the crew to create (slugified server-side)
  --agent <name>          Name of the first agent in that crew

Optional flags:
  --cli-adapter           CLI adapter (default CLAUDE_CODE)
  --llm-provider          One of ANTHROPIC, OPENAI, GOOGLE, CURSOR, FACTORY, OLLAMA
  --llm-model             Model identifier (provider-specific)
  --credential-name           Display name for the stored API key
  --credential-value-stdin    Read the API key from stdin (preferred — keeps it out of 'ps')
  --credential-value          The API key itself (DEPRECATED — visible in 'ps' and shell history)

Examples:
  echo "$ANTHROPIC_KEY" | crewship system onboarding setup --crew "backend" --agent "viktor" \
    --llm-provider ANTHROPIC --credential-value-stdin
  crewship system onboarding setup --crew "ops" --agent "eva" \
    --llm-provider OLLAMA --llm-model llama3`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		crew, _ := cmd.Flags().GetString("crew")
		agent, _ := cmd.Flags().GetString("agent")
		if crew == "" || agent == "" {
			return fmt.Errorf("--crew and --agent are required")
		}
		body := map[string]string{
			"crew_name":  crew,
			"agent_name": agent,
		}
		if v, _ := cmd.Flags().GetString("cli-adapter"); v != "" {
			body["cli_adapter"] = v
		}
		if v, _ := cmd.Flags().GetString("llm-provider"); v != "" {
			body["llm_provider"] = v
		}
		if v, _ := cmd.Flags().GetString("llm-model"); v != "" {
			body["llm_model"] = v
		}
		if v, _ := cmd.Flags().GetString("credential-name"); v != "" {
			body["credential_name"] = v
		}
		// Sensitive-value precedence: prefer stdin (--credential-value-stdin),
		// then deprecated --credential-value flag (visible in `ps` and
		// shell history). The flag is kept as compatibility fallback
		// but warns to nudge callers off of it.
		credValue := ""
		useStdin, _ := cmd.Flags().GetBool("credential-value-stdin")
		if useStdin {
			scanner := bufio.NewScanner(os.Stdin)
			if !scanner.Scan() {
				if err := scanner.Err(); err != nil {
					return fmt.Errorf("read --credential-value-stdin: %w", err)
				}
				return fmt.Errorf("no input provided on stdin for --credential-value-stdin")
			}
			credValue = scanner.Text()
		} else if v, _ := cmd.Flags().GetString("credential-value"); v != "" {
			fmt.Fprintln(os.Stderr, "warning: --credential-value is deprecated; pipe the secret via --credential-value-stdin instead")
			credValue = v
		}
		if credValue != "" {
			body["credential_value"] = credValue
		}

		client := newAPIClient()
		client.WorkspaceID = ""
		resp, err := client.Post("/api/v1/onboarding/setup", body)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var result map[string]interface{}
		if err := cli.ReadJSON(resp, &result); err != nil {
			return err
		}
		return newFormatter().JSON(result)
	},
}

var systemOnboardingCompleteCmd = &cobra.Command{
	Use:   "complete",
	Short: "Mark onboarding as completed for the current user",
	Long: `Flip the user's onboarding_completed flag to true without going
through the setup wizard. Useful when a workspace has been provisioned
through other channels (CLI agent create, restore from backup) and the
welcome banner is still showing.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		client := newAPIClient()
		client.WorkspaceID = ""
		resp, err := client.Post("/api/v1/onboarding/complete", nil)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		resp.Body.Close()
		cli.PrintSuccess("Onboarding marked complete.")
		return nil
	},
}

// systemAuxStatusCmd renders the PR-B F3 auxiliary-model assignment
// reported by GET /api/v1/system/aux-status. Same diagnostic surface
// the future web UI badge in Settings → Models will read — exposing
// it through the CLI first means operators can confirm a YAML
// override landed before the next eval call has a chance to silently
// fall back to cfg.auxiliary.fallback.
//
// Output formats:
//   - table (default): {SLOT, PROVIDER, MODEL, TIMEOUT, SOURCE}
//   - json / yaml: pass-through of the API envelope so jq/yq pipelines work
//
// Source column values: "explicit" (slot was configured directly),
// "fallback" (slot was empty so cfg.Fallback was used), "unconfigured"
// (neither path resolved — operator misconfiguration).
var systemAuxStatusCmd = &cobra.Command{
	Use:   "aux-status",
	Short: "Show auxiliary model assignment per slot (PR-B F3)",
	Long: `Show the resolved provider, model, timeout and source for every
auxiliary-model slot (Curator, Keeper, Behavior, MemoryHealth, Negative).

The source column distinguishes how each slot was resolved:
  explicit     — cfg.auxiliary.<slot> was set directly
  fallback     — cfg.auxiliary.<slot> was empty; cfg.auxiliary.fallback used
  unconfigured — neither the slot nor fallback had a provider (operator gap)

Examples:
  crewship system aux-status
  crewship system aux-status --format json | jq '.slots[] | select(.source=="fallback")'
  crewship system aux-status --format yaml`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}

		client := newAPIClient()
		client.WorkspaceID = ""

		resp, err := client.Get("/api/v1/system/aux-status")
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var body struct {
			Slots []struct {
				Slot      string `json:"slot"`
				Provider  string `json:"provider"`
				Model     string `json:"model"`
				TimeoutMS int64  `json:"timeout_ms"`
				Source    string `json:"source"`
			} `json:"slots"`
		}
		if err := cli.ReadJSON(resp, &body); err != nil {
			return err
		}

		headers := []string{"SLOT", "PROVIDER", "MODEL", "TIMEOUT", "SOURCE"}
		rows := make([][]string, 0, len(body.Slots))
		for _, s := range body.Slots {
			timeout := "—"
			if s.TimeoutMS > 0 {
				// Render in seconds when ≥1s, else ms — keeps the
				// column human-readable across the 3s–30s span the
				// MVP defaults use without forcing operators to
				// mental-arithmetic milliseconds for the common case.
				if s.TimeoutMS >= 1000 {
					timeout = fmt.Sprintf("%.1fs", float64(s.TimeoutMS)/1000.0)
				} else {
					timeout = fmt.Sprintf("%dms", s.TimeoutMS)
				}
			}
			rows = append(rows, []string{
				s.Slot,
				dashIfEmpty(s.Provider),
				dashIfEmpty(s.Model),
				timeout,
				s.Source,
			})
		}

		return newFormatter().Auto(body, headers, rows)
	},
}

// dashIfEmpty returns "—" for empty strings so the table column
// renders cleanly when a slot is unconfigured. Trivial helper, but
// inlining at each call site obscured the intent.
func dashIfEmpty(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func init() {
	systemOnboardingSetupCmd.Flags().String("crew", "", "Crew name to create (required)")
	systemOnboardingSetupCmd.Flags().String("agent", "", "First agent name in the crew (required)")
	systemOnboardingSetupCmd.Flags().String("cli-adapter", "", "CLI adapter (default CLAUDE_CODE)")
	systemOnboardingSetupCmd.Flags().String("llm-provider", "", "LLM provider: ANTHROPIC, OPENAI, GOOGLE, CURSOR, FACTORY, OLLAMA")
	systemOnboardingSetupCmd.Flags().String("llm-model", "", "LLM model identifier")
	systemOnboardingSetupCmd.Flags().String("credential-name", "", "Display name for the stored API key")
	systemOnboardingSetupCmd.Flags().String("credential-value", "", "API key value (deprecated; visible in `ps` — use --credential-value-stdin)")
	systemOnboardingSetupCmd.Flags().Bool("credential-value-stdin", false, "Read the credential value from stdin (preferred over --credential-value)")

	systemOnboardingCmd.AddCommand(systemOnboardingStatusCmd)
	systemOnboardingCmd.AddCommand(systemOnboardingSetupCmd)
	systemOnboardingCmd.AddCommand(systemOnboardingCompleteCmd)

	systemCmd.AddCommand(systemInfoCmd)
	systemCmd.AddCommand(systemKeeperCmd)
	systemCmd.AddCommand(systemStatsCmd)
	systemCmd.AddCommand(systemOnboardingCmd)
	systemCmd.AddCommand(systemAuxStatusCmd)
}
