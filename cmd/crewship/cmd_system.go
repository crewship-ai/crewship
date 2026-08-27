package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// keeperPermissionHint turns the server's bare "403 Forbidden" for
// `system keeper` into an actionable message. The route is ADMIN+ in the
// caller's workspace (#893); a MEMBER should learn *why* they're blocked
// rather than see a raw RFC-7807 "Forbidden". Non-403 errors pass through
// unchanged so the underlying status/detail is preserved.
//
// It substitutes ONLY when the server offered nothing. Every keeper command
// routes 403s through here, and not all of them are role problems: `keeper
// resolve` answers the four-eyes rule with "this escalation was raised by an
// agent you own, so somebody else must confirm it". Replacing that with a role
// hint sent an OWNER off to ask an admin to fix a permission that was never
// wrong, when the actual answer was "get a second person". A message the server
// took the trouble to write beats one the client guessed.
func keeperPermissionHint(err error) error {
	var apiErr *cli.APIError
	if errors.As(err, &apiErr) && apiErr.Status == http.StatusForbidden && isBareForbidden(apiErr.Detail) {
		ws := cli.ResolveWorkspace(flagWorkspace, cliCfg)
		return cli.WithExitCode(fmt.Errorf(
			"API error (403): keeper status requires ADMIN or OWNER role in workspace %q — ask a workspace admin or switch workspaces with 'crewship workspace use <slug>'",
			ws), cli.ExitAuth)
	}
	return err
}

// isBareForbidden reports whether a 403 body said anything beyond "no". The
// RFC-7807 default title is the only value the substitution is for.
func isBareForbidden(detail string) bool {
	switch strings.ToLower(strings.TrimSpace(detail)) {
	case "", "forbidden":
		return true
	}
	return false
}

var systemCmd = &cobra.Command{
	Use:   "system",
	Short: "Show system information (runtime, license, keeper)",
}

// probedRuntimeNames is the one list of container runtimes the CLI is allowed
// to name, in display form. `system info`'s "nothing answered" hint and
// `doctor`'s "none installed" detail both render from it, so the two cannot
// drift apart — or drift away from what the detector actually probes, which is
// what runtime_vocabulary_test.go pins them to (docker.RuntimeLabels() plus
// Apple Containers).
//
// containerd/nerdctl was here and is not a runtime Crewship can use: containerd
// serves its own gRPC API over HTTP/2, the moby client speaks the Docker REST
// API over HTTP/1.1, and no version of either bridges that (#1687). Naming it
// sent operators on a containerd host off to start a daemon that was already
// running (#1689).
var probedRuntimeNames = []string{
	"Docker",
	"Colima",
	"OrbStack",
	"Rancher Desktop",
	"Podman",
	"Apple Containers",
}

// noRuntimeHint is what `system info` prints when the server reports no
// runtime available — the list of what it looked for, so the reader can tell
// "I have none of these" from "the one I have is stopped".
func noRuntimeHint() string {
	return "No container runtime answered. Crewship probes " +
		strings.Join(probedRuntimeNames, ", ") + "."
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
			switch {
			case !runtime.Available:
				// Nothing answered: an empty `Runtime:` line adds nothing the
				// availability flag has not already said. Say what was looked
				// for, and where to get one.
				fmt.Printf("  %s%s%s\n", cli.Dim, noRuntimeHint(), cli.Reset)
				printInstallLinks(runtime.InstallLinks)
			case runtime.redacted():
				printRedactedRuntimeHint()
			default:
				printRuntimeDetail(runtime)
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
	// InstallLinks is the server's "how do I get one" map, keyed by runtime
	// label. It is sent alongside an available runtime too, not only when none
	// was found (#1690) — an operator with one runtime installed still needs to
	// be told what the others are. The CLI used to drop it on the floor (#1707).
	InstallLinks map[string]string `json:"install_links,omitempty"`
}

type systemRuntimeEntry struct {
	Runtime string `json:"runtime"`
	Version string `json:"version"`
	Socket  string `json:"socket,omitempty"`
	// InUse marks the single runtime the server is actually driving. Not
	// omitempty: `false` is the answer for every other entry and dropping it
	// would make "not in use" indistinguishable from "this server is too old
	// to say". The whole reason /system/runtime carries the flag is that
	// installed and in-use are different facts (#1696); the CLI dropped it and
	// so could not answer the one question the endpoint exists for (#1707).
	InUse bool `json:"in_use"`
	// Gaps are the crew hardening controls this runtime is measured not to
	// deliver. The server sets them on the `in_use` entry only, so this is
	// populated on at most one element (#1672).
	//
	// A local twin of docker.Gap rather than the type itself: cmd_system.go
	// carries no build tag and must keep linking in the `clionly` build, which
	// deliberately excludes the container provider.
	Gaps []systemRuntimeGap `json:"gaps,omitempty"`
}

// systemRuntimeGap is one control the runtime in use will not honour —
// `control` names it, `detail` says what breaks. Both come from the server
// verbatim; the CLI never composes the wording, so a gap added server-side
// reaches an already-installed CLI unchanged.
type systemRuntimeGap struct {
	Control string `json:"control"`
	Detail  string `json:"detail"`
}

// redacted reports whether the server answered with the availability-only
// shape it gives a caller it cannot resolve as ADMIN+ in a workspace (#865):
// `{"available": true}` and nothing else. Printing that as `Runtime:` and
// `Version:` with empty values reads as "no runtime detected" — the opposite of
// what the server said (#1707).
//
// An unavailable runtime is not redaction (the server volunteers `available:
// false` to everyone), and neither is a server that lists runtimes while
// driving none — `runtimes` is populated there.
func (r systemRuntimeInfo) redacted() bool {
	return r.Available && r.Runtime == "" && r.Version == "" && r.Socket == "" && len(r.Runtimes) == 0
}

func printRedactedRuntimeHint() {
	fmt.Printf("  %sRuntime, version and socket are host detail: the server sends them only\n", cli.Dim)
	fmt.Printf("  to an ADMIN or OWNER of the workspace on the request. Select one you\n")
	fmt.Printf("  administer — 'crewship workspace use <slug>' — or pass --workspace.%s\n", cli.Reset)
}

// printRuntimeDetail renders the runtime being driven and, when more than one
// answered, the whole inventory with the driven one marked.
//
// The list used to be printed as "Also found: …" over runtimes[1:], on the
// assumption that entry 0 is the one in use, and captioned "what you could
// switch to". Both were untrue: the server marks the driven entry with `in_use`
// and it need not be first (a DOCKER_HOST engine is appended last), and there is
// no switch to make — `container.provider` accepts only docker | apple | auto,
// so no value names orbstack, colima, rancher or podman (#1689).
func printRuntimeDetail(runtime systemRuntimeInfo) {
	if runtime.Runtime == "" && len(runtime.Runtimes) > 0 {
		// The server sends runtime/version/socket as null when runtimes are
		// installed but none is in use — it booted without a container provider
		// (`--no-docker`). Naming one of them there would report a runtime that
		// is running nothing, so say so instead of printing a blank.
		fmt.Printf("  Runtime:    %s(none in use — installed, but this server drives none)%s\n", cli.Dim, cli.Reset)
	} else {
		fmt.Printf("  Runtime:    %s\n", runtime.Runtime)
		fmt.Printf("  Version:    %s\n", runtime.Version)
		if runtime.Socket != "" {
			fmt.Printf("  Socket:     %s\n", runtime.Socket)
		}
	}
	printRuntimeGaps(inUseRuntimeGaps(runtime))
	// One runtime that is also the one in use is already fully described by the
	// three lines above; listing it again is noise. Everything else — several
	// installed, or none in use — needs the inventory.
	if len(runtime.Runtimes) == 0 || (len(runtime.Runtimes) == 1 && runtime.Runtime != "") {
		return
	}
	fmt.Printf("  Detected:\n")
	for _, rt := range runtime.Runtimes {
		line := fmt.Sprintf("    %-10s %-10s %s", rt.Runtime, dashIfEmpty(rt.Version), rt.Socket)
		line = strings.TrimRight(line, " ")
		if rt.InUse {
			line += fmt.Sprintf("  %s(in use)%s", cli.Green, cli.Reset)
		}
		fmt.Println(line)
	}
	if len(runtime.Runtimes) < 2 {
		return
	}
	fmt.Printf("  %sNot selectable by name — Crewship drives the first socket that answers.\n", cli.Dim)
	fmt.Printf("  Point DOCKER_HOST at another one, or stop the daemon that wins.%s\n", cli.Reset)
}

// inUseRuntimeGaps returns the gaps hung on the entry the server marked
// `in_use`. The flag is the selector rather than position: the driven entry
// need not be first, and a server that drives nothing marks none.
func inUseRuntimeGaps(runtime systemRuntimeInfo) []systemRuntimeGap {
	for _, rt := range runtime.Runtimes {
		if rt.InUse {
			return rt.Gaps
		}
	}
	return nil
}

// printRuntimeGaps names the crew hardening controls the runtime in use does
// not deliver (#1672).
//
// This is the first surface an operator can reach on demand. The same facts
// were already emitted as a startup WARN, which is no help at all to somebody
// debugging hours later — and the failure it describes does not look like a
// runtime problem from the outside: an agent that cannot hold gid 1002 reads
// nothing from the crew's shared memory and presents as one that forgot things.
//
// Nothing is printed when there are no gaps. An empty heading reads as a
// finding, and a surface that cries wolf on a clean host is one people learn to
// skip past on the host where it matters.
func printRuntimeGaps(gaps []systemRuntimeGap) {
	if len(gaps) == 0 {
		return
	}
	fmt.Printf("  %sKnown gaps:%s\n", cli.Yellow, cli.Reset)
	for _, g := range gaps {
		fmt.Printf("    %s%s%s — %s\n", cli.Yellow, g.Control, cli.Reset, g.Detail)
	}
}

// printInstallLinks renders the server's install_links map. Sorted, so two runs
// against the same server produce the same bytes.
func printInstallLinks(links map[string]string) {
	if len(links) == 0 {
		return
	}
	names := make([]string, 0, len(links))
	for name := range links {
		names = append(names, name)
	}
	sort.Strings(names)
	fmt.Printf("  Install one:\n")
	for _, name := range names {
		fmt.Printf("    %-10s %s\n", name, links[name])
	}
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
		// Verbatim server reasons, for the rows that have a problem.
		//
		// reach_detail only counts as a reason when a probe actually ran.
		// The server stamps the standing policy note ("not probed — …") into
		// reach_detail on EVERY non-self-hosted row, healthy or not, so a
		// paid-API slot that simply failed to build — the everyday missing
		// ANTHROPIC_API_KEY case — would otherwise print that note directly
		// beneath its real error, reading as a second fault and sending the
		// operator after a probe that was never the problem. A nil
		// `reachable` is "not probed", and that state is already carried by
		// the status word.
		for _, s := range payload.Subsystems {
			if auxUsable(s) {
				continue
			}
			reasons := []string{s.Detail}
			if s.Reachable != nil {
				reasons = append(reasons, s.ReachDetail)
			}
			for _, reason := range reasons {
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
