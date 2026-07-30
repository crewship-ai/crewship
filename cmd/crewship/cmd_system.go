package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// keeperPermissionHint turns the server's bare "403 Forbidden" for
// `system keeper` into an actionable message. The route is ADMIN+ in the
// caller's workspace (#893); a MEMBER should learn *why* they're blocked
// rather than see a raw RFC-7807 "Forbidden". Non-403 errors pass through
// unchanged so the underlying status/detail is preserved.
func keeperPermissionHint(err error) error {
	var apiErr *cli.APIError
	if errors.As(err, &apiErr) && apiErr.Status == http.StatusForbidden {
		ws := cli.ResolveWorkspace(flagWorkspace, cliCfg)
		return cli.WithExitCode(fmt.Errorf(
			"API error (403): keeper status requires ADMIN or OWNER role in workspace %q — ask a workspace admin or switch workspaces with 'crewship workspace use <slug>'",
			ws), cli.ExitAuth)
	}
	return err
}

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
		// Keep the workspace on the request. /system/runtime redacts host
		// detail — the runtime name, its version, its socket — for anyone it
		// cannot resolve as ADMIN+ (#865), and it resolves the role FROM the
		// workspace. Clearing it here made the command print an empty Runtime
		// and Version for everyone, which reads as "nothing detected" rather
		// than "you did not ask as someone allowed to know".

		// Runtime info
		runtimeResp, err := client.Get("/api/v1/system/runtime")
		if err != nil {
			return fmt.Errorf("runtime info: %w", err)
		}
		if err := cli.CheckError(runtimeResp); err != nil {
			return err
		}

		var runtime systemRuntimeInfo
		if err := cli.ReadJSON(runtimeResp, &runtime); err != nil {
			return err
		}

		// License info — optional: a non-200 (endpoint absent, older server)
		// omits the section/key rather than failing the command.
		var license *systemLicenseInfo
		licenseResp, err := client.Get("/api/v1/system/license")
		if err != nil {
			return fmt.Errorf("license info: %w", err)
		}
		if licenseResp.StatusCode == 200 {
			var lic systemLicenseInfo
			if cli.ReadJSON(licenseResp, &lic) == nil {
				license = &lic
			}
		} else {
			licenseResp.Body.Close()
		}

		payload := systemInfoPayload{Runtime: runtime, License: license}
		return newFormatter().AutoHuman(payload, func() {
			fmt.Printf("%sContainer Runtime%s\n", cli.Bold, cli.Reset)
			fmt.Printf("  Available:  %v\n", runtime.Available)
			fmt.Printf("  Runtime:    %s\n", runtime.Runtime)
			fmt.Printf("  Version:    %s\n", runtime.Version)
			if runtime.Socket != "" {
				fmt.Printf("  Socket:     %s\n", runtime.Socket)
			}
			// Everything else that answered a socket probe. The first entry
			// is the one in use; the rest are what you could switch to.
			if len(runtime.Runtimes) > 1 {
				fmt.Printf("  Also found: ")
				for i, rt := range runtime.Runtimes[1:] {
					if i > 0 {
						fmt.Printf(", ")
					}
					fmt.Printf("%s %s", rt.Runtime, rt.Version)
				}
				fmt.Println()
			}
			if !runtime.Available {
				fmt.Printf("  %sNo container runtime answered. Crewship probes Docker, Colima,\n", cli.Dim)
				fmt.Printf("  OrbStack, Rancher Desktop, Podman (rootless/root/machine),\n")
				fmt.Printf("  containerd/nerdctl and Apple Containers.%s\n", cli.Reset)
			}
			if license != nil {
				fmt.Printf("\n%sLicense%s\n", cli.Bold, cli.Reset)
				fmt.Printf("  Edition:          %s\n", license.Edition)
				fmt.Printf("  Max crews:        %d\n", license.MaxCrews)
				fmt.Printf("  Max agents/crew:  %d\n", license.MaxAgents)
				fmt.Printf("  Max members:      %d\n", license.MaxMembers)
				if license.LicenseeOrg != "" {
					fmt.Printf("  Licensee:         %s\n", license.LicenseeOrg)
				}
			}
		})
	},
}

// systemRuntimeInfo / systemLicenseInfo / systemInfoPayload give `system info`
// a stable machine shape for --format json/yaml/ndjson (the command used to
// print ANSI human text regardless of format). License is a pointer so an
// unavailable license endpoint omits the key instead of emitting zero values.
type systemRuntimeInfo struct {
	Available bool   `json:"available"`
	Runtime   string `json:"runtime"`
	Version   string `json:"version"`
	Socket    string `json:"socket,omitempty"`
	// Runtimes is every runtime detected, not just the one in use. Docker
	// Desktop and Podman on one laptop is the normal case for anyone testing
	// both, and without this list switching between them is invisible.
	Runtimes []systemRuntimeEntry `json:"runtimes,omitempty"`
}

type systemRuntimeEntry struct {
	Runtime string `json:"runtime"`
	Version string `json:"version"`
	Socket  string `json:"socket,omitempty"`
}

type systemLicenseInfo struct {
	Edition     string `json:"edition"`
	LicenseID   string `json:"license_id,omitempty"`
	LicenseeOrg string `json:"licensee_org,omitempty"`
	MaxAgents   int    `json:"max_agents_per_crew"`
	MaxCrews    int    `json:"max_crews"`
	MaxMembers  int    `json:"max_members"`
}

type systemInfoPayload struct {
	Runtime systemRuntimeInfo  `json:"runtime"`
	License *systemLicenseInfo `json:"license,omitempty"`
}

var systemKeeperCmd = &cobra.Command{
	Use:   "keeper",
	Short: "Show Keeper security system status",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		// #893 moved GET /api/v1/system/keeper behind authedAdmin
		// (RequireWorkspace → roleManage), so the route is now
		// workspace-scoped and ADMIN+. Sending the active workspace id
		// (the default the client already resolves) is mandatory —
		// clearing it, as this command used to for the old instance-wide
		// route, makes RequireWorkspace hard-400 for everyone (#896).
		if err := requireWorkspace(); err != nil {
			return err
		}

		client := newAPIClient()

		resp, err := client.Get("/api/v1/system/keeper")
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return keeperPermissionHint(err)
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

		return newFormatter().AutoHuman(keeper, func() {
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
		})
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

		return newFormatter().AutoHuman(stats, func() {
			fmt.Printf("%sAdmin Stats%s\n", cli.Bold, cli.Reset)
			fmt.Printf("  Workspaces: %d\n", stats.Workspaces)
			fmt.Printf("  Users:      %d\n", stats.Users)
			fmt.Printf("  Agents:     %d\n", stats.Agents)
			fmt.Printf("  Running:    %d\n", stats.Running)
		})
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

		// The canonical output of onboarding status IS the raw API payload —
		// default stays JSON, but --format yaml/ndjson is honored.
		return newFormatter().Machine(result)
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
		// MarkFlagRequired (below, in init()) only checks Flag.Changed — it
		// catches --crew omitted entirely, but `--crew ""` (e.g. an unset
		// shell variable interpolated into a script) sets Changed=true with
		// an empty value and sails through. Keep the explicit emptiness
		// check so a script can't silently provision a crew/agent with a
		// blank name against the server. (#966)
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
		return newFormatter().Machine(result)
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

// auxSubsystem is one row of GET /api/v1/system/aux-status as the server
// has shaped it since #1506, which replaced the flat `slots` list with
// `subsystems` and added the two verdicts this command exists to relay:
//
//   - Healthy — the slot resolved to a provider that BUILDS. Construction
//     only; it proves configuration, not liveness.
//   - Reachable — the model server ANSWERED just now. A nil pointer is an
//     honest third state ("not probed"): only self-hosted providers are
//     dialled, because rendering a status table must not bill an operator
//     for a paid API call.
//
// Keeping them apart is the whole point. llm.NewOllama never dials, so a
// box with no Ollama running used to report a perfectly healthy judge.
type auxSubsystem struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Provider    string `json:"provider"`
	Model       string `json:"model"`
	TimeoutMS   int64  `json:"timeout_ms,omitempty"`
	Source      string `json:"source"`
	Healthy     bool   `json:"healthy"`
	Detail      string `json:"detail,omitempty"`
	Reachable   *bool  `json:"reachable,omitempty"`
	ReachDetail string `json:"reach_detail,omitempty"`
}

// auxStatusPayload is what --format json/yaml emits: the current server
// envelope, verbatim, so `jq '.subsystems[]'` works against CLI output and
// against curl alike.
type auxStatusPayload struct {
	Subsystems []auxSubsystem `json:"subsystems"`
}

// auxStatusWire adds the pre-#1506 `slots` key purely so a stale server can
// be NAMED rather than silently rendered as an empty table. It never reaches
// the formatter — see auxStatusPayload.
type auxStatusWire struct {
	Subsystems []auxSubsystem    `json:"subsystems"`
	Slots      []json.RawMessage `json:"slots"`
}

// auxVerdict collapses healthy + reachable into the single word an operator
// scans the STATUS column for. The four states are distinct on purpose:
// "unreachable" (configured, silent) must not read like "ok", and
// "ok (unprobed)" must not read like a confirmed green.
func auxVerdict(s auxSubsystem) string {
	switch {
	case !s.Healthy:
		return "unhealthy"
	case s.Reachable == nil:
		return "ok (unprobed)"
	case !*s.Reachable:
		return "unreachable"
	default:
		return "ok"
	}
}

// auxUsable mirrors the admin console's isUsable: a judge works only if it
// is both buildable and — where we can check — answering.
func auxUsable(s auxSubsystem) bool {
	return s.Healthy && (s.Reachable == nil || *s.Reachable)
}

// auxTimeout renders in seconds when >=1s, else ms — keeps the column
// human-readable across the 3s–30s span the MVP defaults use without
// forcing operators to mental-arithmetic milliseconds for the common case.
func auxTimeout(ms int64) string {
	if ms <= 0 {
		return "—"
	}
	if ms >= 1000 {
		return fmt.Sprintf("%.1fs", float64(ms)/1000.0)
	}
	return fmt.Sprintf("%dms", ms)
}

// systemAuxStatusCmd renders the auxiliary-evaluator status reported by
// GET /api/v1/system/aux-status — the same surface the admin console's
// "Judge models" card reads. It answers two questions per subsystem: which
// model judges it, and can that model actually run right now.
//
// Output formats:
//   - table (default): {SUBSYSTEM, ROLE, PROVIDER, MODEL, TIMEOUT, SOURCE,
//     STATUS}, followed by the server's own reason for every subsystem that
//     cannot run — a red dot with no reason is not actionable
//   - json / yaml: pass-through of the API envelope so jq/yq pipelines work
//
// Source column values: "explicit" (slot was configured directly),
// "fallback" (slot was empty so cfg.Fallback was used), "unconfigured"
// (neither path resolved — operator misconfiguration), "keeper_config"
// (the credential-access judge, built from cfg.Keeper on its own path).
var systemAuxStatusCmd = &cobra.Command{
	Use:   "aux-status",
	Short: "Show auxiliary model assignment and health per subsystem",
	Long: `Show which model judges each auxiliary subsystem (credential access,
skill review, behaviour monitoring, memory health, negative lessons, run
summaries) — and whether that model can actually run.

The source column distinguishes how each subsystem was resolved:
  explicit      — cfg.auxiliary.<slot> was set directly
  fallback      — cfg.auxiliary.<slot> was empty; cfg.auxiliary.fallback used
  unconfigured  — neither the slot nor fallback had a provider (operator gap)
  keeper_config — the credential-access judge, built from cfg.keeper

The status column answers a different question from source — not "is this
configured" but "can it run":
  ok             — buildable, and the model server answered
  ok (unprobed)  — buildable; a paid API is not dialled to render a status page
  unreachable    — buildable, but nothing answered at the model server
  unhealthy      — not configured, or its provider cannot be built

Requires ADMIN or OWNER in the active workspace.

Examples:
  crewship system aux-status
  crewship system aux-status --format json | jq '.subsystems[] | select(.reachable == false)'
  crewship system aux-status --format yaml`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		// The route sits behind authedAdmin (#868) — RequireWorkspace →
		// ADMIN+ — so it is workspace-scoped like `system keeper`. Clearing
		// the workspace here, as this command used to for the old
		// instance-wide route, hard-400s every call (#896).
		if err := requireWorkspace(); err != nil {
			return err
		}

		client := newAPIClient()

		resp, err := client.Get("/api/v1/system/aux-status")
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var wire auxStatusWire
		if err := cli.ReadJSON(resp, &wire); err != nil {
			return err
		}
		// A missing `subsystems` key is a server we do not understand — most
		// likely one predating the #1506 reshape, which still answers 200
		// with the old `slots` list. Rendering "(no results)" off it would
		// read as "nothing configured", which is the opposite of the truth
		// and exactly the failure this command was reported for. Decoding
		// distinguishes an absent key (nil) from an empty list (non-nil).
		if wire.Subsystems == nil {
			if wire.Slots != nil {
				return fmt.Errorf(
					"server returned the old aux-status shape ({\"slots\": …}); this CLI reads {\"subsystems\": …}. " +
						"Upgrade the server, or run a CLI from the same release — refusing to print an empty table")
			}
			return fmt.Errorf("server response carried no \"subsystems\" list — cannot report auxiliary subsystem status")
		}

		payload := auxStatusPayload{Subsystems: wire.Subsystems}

		headers := []string{"SUBSYSTEM", "ROLE", "PROVIDER", "MODEL", "TIMEOUT", "SOURCE", "STATUS"}
		rows := make([][]string, 0, len(payload.Subsystems))
		for _, s := range payload.Subsystems {
			rows = append(rows, []string{
				s.ID,
				dashIfEmpty(s.Label),
				dashIfEmpty(s.Provider),
				dashIfEmpty(s.Model),
				auxTimeout(s.TimeoutMS),
				dashIfEmpty(s.Source),
				auxVerdict(s),
			})
		}

		f := newFormatter()
		if err := f.Auto(payload, headers, rows); err != nil {
			return err
		}
		// Machine and quiet formats are consumed by scripts; the annotation
		// below is for a human reading the table.
		switch f.Format {
		case "json", "yaml", "ndjson", "quiet":
			return nil
		}

		broken := 0
		for _, s := range payload.Subsystems {
			if !auxUsable(s) {
				broken++
			}
		}
		if broken > 0 {
			fmt.Printf("\n%s%d of %d subsystems cannot run right now — evaluations that need them fail closed.%s\n",
				cli.Red, broken, len(payload.Subsystems), cli.Reset)
		}
		// Verbatim server reasons, for the rows that have a problem. The
		// "not probed" note on a healthy paid-API row is already carried by
		// its status word, so it is not repeated here.
		for _, s := range payload.Subsystems {
			if auxUsable(s) {
				continue
			}
			for _, reason := range []string{s.Detail, s.ReachDetail} {
				if reason != "" {
					fmt.Printf("  %s%s%s: %s\n", cli.Bold, s.ID, cli.Reset, reason)
				}
			}
		}
		return nil
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
	// Enforce via cobra's required-flag machinery (usage error), consistent
	// with the other MarkFlagRequired sites, instead of a hand-rolled in-RunE
	// check that returned a bare error. (#966)
	_ = systemOnboardingSetupCmd.MarkFlagRequired("crew")
	_ = systemOnboardingSetupCmd.MarkFlagRequired("agent")
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
