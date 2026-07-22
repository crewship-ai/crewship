//go:build !clionly

package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/config"
	"github.com/crewship-ai/crewship/internal/crashreport"
	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/preflight"
	"github.com/crewship-ai/crewship/internal/provider/apple"
	"github.com/crewship-ai/crewship/internal/provider/docker"
	"github.com/crewship-ai/crewship/internal/secrets"
	"github.com/crewship-ai/crewship/internal/update"
	"github.com/spf13/cobra"
)

// checkResult is one row in the doctor output. status is one of
// "PASS" / "WARN" / "FAIL" / "INFO"; detail is the human-readable
// explanation. INFO is treated like PASS for exit-code purposes but
// rendered in a neutral colour — it surfaces purely informational
// findings (e.g. "update check failed, harmless") that should not be
// confused with a real warning the operator must act on.
//
// The struct exists so each check can be a discrete function that
// returns a single value, making the doctor easy to extend (add
// another `runX()` call to the dispatch list) and test (the table
// driver in cmd_doctor_test.go can compare a slice of results without
// caring about ordering or rendering details).
type checkResult struct {
	name   string
	status string // PASS / WARN / FAIL / INFO
	detail string
	hint   string
}

func (r checkResult) print() {
	var color string
	switch r.status {
	case "PASS":
		color = cli.Green
	case "WARN":
		color = cli.Yellow
	case "FAIL":
		color = cli.Red
	case "INFO":
		color = cli.Dim
	default:
		color = cli.Gray
	}
	fmt.Printf("  %s[%-4s]%s  %-32s  %s\n", color, r.status, cli.Reset, r.name, r.detail)
	if r.hint != "" {
		fmt.Printf("           %s%s%s\n", cli.Dim, r.hint, cli.Reset)
	}
}

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Check system requirements and health (--fix to attempt safe auto-repair)",
	Long: `Run a battery of system checks: container runtime, data directory,
database schema version, server reachability, sidecar binary presence,
write-perm on the data dir, and the NEXTAUTH_SECRET trap.

Each check is PASS / WARN / FAIL with a hint for failures. --fix attempts
only safe, reversible repairs (creating the missing data directory).
Unsafe fixes (installing Docker, repairing networks, setting env vars)
are deliberately left to the operator with actionable URLs in the output.

With --json, emits a structured object instead of the colored table:

  {
    "checks": [
      {"name": "container runtime", "status": "PASS", "detail": "...", "hint": ""},
      ...
    ],
    "failed":  0,
    "warned":  0,
    "version": "<crewship version>",
    "os":      "<runtime.GOOS>",
    "arch":    "<runtime.GOARCH>"
  }

The status enum is PASS / WARN / FAIL / INFO, identical to the human
table. CI gates can branch on the top-level "failed" / "warned"
counters or filter the per-check array.

Exit code: 0 when every check is PASS / WARN / INFO, 1 when any check
FAILs — in both output modes. In --json mode the full payload is still
written to stdout before the non-zero exit, so a gate can read the
counters AND branch on $?.

The port-binding check probes the address 'crewship start' would bind:
CREWSHIP_HOST (default 0.0.0.0) on CREWSHIP_PORT (default 8080).`,
	RunE: func(cmd *cobra.Command, args []string) error {
		fixMode, _ := cmd.Flags().GetBool("fix")
		// Doctor's machine output is a bespoke JSON document (checks +
		// counters), not a formatter-rendered list — fail fast on formats
		// it can't honor rather than silently degrading to the color table.
		doctorFormat := resolvedFormat(cmd)
		if doctorFormat != "json" && doctorFormat != "table" {
			return fmt.Errorf("doctor supports --format table|json (got %q)", doctorFormat)
		}
		jsonOut := doctorFormat == "json"
		parentCtx := cmd.Context()
		if parentCtx == nil {
			parentCtx = context.Background()
		}

		// withTimeout derives a fresh per-probe context so one slow probe
		// can't consume the whole budget and parent cancellation
		// (SIGINT etc.) still flows.
		withTimeout := func() (context.Context, context.CancelFunc) {
			return context.WithTimeout(parentCtx, 10*time.Second)
		}

		// Header banner — gives the operator a moment to read what we're
		// running before the result table scrolls past. Match width to the
		// widest expected detail string (~64 chars) so terminals narrower
		// than that wrap gracefully and wider ones don't waste real estate.
		//
		// Suppressed in --json mode: stdout is reserved for the JSON
		// payload there. The header survives if --json was forgotten;
		// keeping it human-side only is a stronger contract.
		if !jsonOut {
			fmt.Println()
			fmt.Printf("  %sCrewship doctor%s — environment check\n", cli.Bold, cli.Reset)
			fmt.Printf("  %sbinary:%s  %s   %sos:%s %s/%s\n",
				cli.Dim, cli.Reset, version,
				cli.Dim, cli.Reset, runtime.GOOS, runtime.GOARCH)
			fmt.Println()
		}

		results := make([]checkResult, 0, 12)
		runProbe := func(fn func(context.Context) checkResult) {
			ctx, cancel := withTimeout()
			defer cancel()
			results = append(results, fn(ctx))
		}
		runProbe(checkContainerRuntime)
		// Apple Containers is a first-class provider on macOS, but the
		// runtime row above only ever names the ONE provider `start` would
		// pick. Report Apple's availability separately there so an operator
		// running Docker still learns the alternative exists (and that
		// CREWSHIP_CONTAINER_PROVIDER=apple would work). Never FAIL/WARN —
		// not having it installed is not a health problem.
		if runtime.GOOS == "darwin" {
			runProbe(checkAppleContainers)
		}
		runProbe(runCheckPortBinding)
		results = append(results, checkDataDir(fixMode))
		results = append(results, checkDataDirWritable())
		runProbe(checkDBMigrationVersion)
		results = append(results, checkSidecarBinary())
		results = append(results, checkNextAuthSecret())
		runProbe(checkServerReachable)
		// CLI ↔ server build parity. A CLI several releases behind the
		// daemon it drives is the single most common source of "the API
		// returned something weird" reports, and nothing else in the CLI
		// surfaces the comparison.
		runProbe(runCheckServerVersionMatch)
		// Episodic recall mode (W2): reads the `episodic` field off the
		// server's /healthz. WARN when recall runs sparse-only (no
		// embedder configured), INFO when the server is down or older
		// (the reachable check above already covers a dead daemon).
		runProbe(runCheckEpisodicRecallMode)

		// Legacy C1 crew resources: reads `legacy_resources` off /healthz.
		// WARN when orphaned pre-C1 slug-only volumes/containers survive (they
		// block agent container start); INFO when unknown/older server.
		runProbe(runCheckLegacyResources)

		// New checks (CRE-XXX): telemetry visibility, DSN reachability,
		// data-dir perm drift, and CLI staleness. Each one is implemented
		// as a thin wrapper around a testable helper that accepts state via
		// parameters; doctor wires in the production state here.
		runProbe(runCheckTelemetryStatus)
		runProbe(runCheckSentryDSNWiring)
		runProbe(runCheckDsnReachability)
		results = append(results, runCheckDataDirPerms())
		runProbe(runCheckUpdateAvailable)

		// CLI-side security audit: server URL scheme (plaintext token
		// over non-loopback) and cli-config.yaml perms (token at-rest).
		// Both are pure local-config checks — no network, fast — so we
		// run them unconditionally on every doctor invocation.
		results = append(results, runCheckCLIConfigServerScheme())
		results = append(results, checkCLIConfigPerms(fixMode))

		// Local-model endpoint reachability: if the workspace has an
		// ENDPOINT_URL credential (Ollama / OpenAI-compatible), ask the server
		// to probe it so a down/misconfigured endpoint surfaces here instead of
		// as an opaque mid-run failure. Advisory (WARN at worst) and skipped
		// cleanly when unauthenticated or no endpoint is configured.
		runProbe(runCheckLocalModelEndpoint)

		return finishDoctor(cmd.OutOrStdout(), results, jsonOut)
	},
}

// countDoctorStatuses tallies the two counters the exit contract and the
// JSON payload are built on. INFO deliberately counts as neither: it exists
// precisely for findings that must not colour the summary line or the exit
// code (skipped probes, "not applicable on this OS", …).
func countDoctorStatuses(results []checkResult) (failed, warned int) {
	for _, r := range results {
		switch r.status {
		case "FAIL":
			failed++
		case "WARN":
			warned++
		}
	}
	return failed, warned
}

// doctorExitErr turns the FAIL count into the documented exit-code contract
// (docs/cli/doctor.mdx: 0 when everything is PASS/WARN/INFO, 1 when anything
// FAILs — "treat the non-zero exit as a hard gate").
//
// It returns a plain coded error rather than calling os.Exit so the decision
// stays testable and the normal cobra unwinding still runs. rootCmd sets
// SilenceUsage + SilenceErrors (main.go), so this error produces neither a
// usage dump nor cobra's "Error:" prefix — main.exitWithError prints the one
// summary line and exits with cli.ExitCodeFor. The message is deliberately
// short: the table (or the JSON payload) printed above is the real output,
// and this line only exists so a human who piped stdout elsewhere still sees
// why the command exited non-zero.
func doctorExitErr(failed int) error {
	if failed == 0 {
		return nil
	}
	return cli.WithExitCode(
		fmt.Errorf("%d check(s) FAILED — see the doctor output above", failed),
		cli.ExitGeneric,
	)
}

// finishDoctor renders the collected results in the requested format and
// returns the exit-contract error. Split out of RunE so both output paths
// are unit-testable without standing up the full check battery (which
// touches Docker, the data dir, the network, and the DB).
//
// Ordering matters in JSON mode: the payload is written in FULL before the
// gate error is returned, so `crewship doctor --json | jq` still receives a
// complete document on exactly the runs a CI gate most needs to inspect.
func finishDoctor(out io.Writer, results []checkResult, jsonOut bool) error {
	failed, warned := countDoctorStatuses(results)

	if jsonOut {
		// JSON mode: emit a single object on stdout, no human
		// header/footer/per-check lines. The structure matches
		// the contract documented in the command's Long.
		if err := emitDoctorJSON(out, results, failed, warned); err != nil {
			return err
		}
		return doctorExitErr(failed)
	}

	printDoctorChecks(results, failed, warned)
	return doctorExitErr(failed)
}

// printDoctorChecks writes the human table plus the summary/next-steps
// footer. Kept on fmt.Printf (os.Stdout) rather than cmd.OutOrStdout so the
// colour helpers and the per-row printer keep their single output target.
func printDoctorChecks(results []checkResult, failed, warned int) {
	for _, r := range results {
		r.print()
	}
	fmt.Println()
	switch {
	case failed > 0:
		fmt.Printf("  %s%d failed, %d warned%s — fix the FAILs and re-run.\n",
			cli.Red, failed, warned, cli.Reset)
		fmt.Println()
		fmt.Printf("  %sNeed help?%s  https://docs.crewship.ai/troubleshooting\n", cli.Dim, cli.Reset)
	case warned > 0:
		fmt.Printf("  %s%d warned%s — review and re-run.\n", cli.Yellow, warned, cli.Reset)
		fmt.Println()
		fmt.Printf("  %sNext steps:%s\n", cli.Bold, cli.Reset)
		fmt.Printf("    1. crewship start\n")
		fmt.Printf("    2. open http://localhost:8080\n")
		fmt.Printf("    3. follow the onboarding wizard (workspace → crew → agent → credentials)\n")
	default:
		fmt.Printf("  %sAll checks passed.%s\n", cli.Green, cli.Reset)
		fmt.Println()
		fmt.Printf("  %sNext steps:%s\n", cli.Bold, cli.Reset)
		fmt.Printf("    1. crewship start\n")
		fmt.Printf("    2. open http://localhost:8080\n")
		fmt.Printf("    3. follow the onboarding wizard (workspace → crew → agent → credentials)\n")
		fmt.Println()
		fmt.Printf("  %sCLI walkthrough:%s  https://docs.crewship.ai/guides/onboarding\n", cli.Dim, cli.Reset)
	}
	fmt.Println()
}

// preflightInstalled is the indirection that lets tests stub the
// installed-runtime scan — it reads real host state (PATH, /Applications),
// which a unit test can't control via env vars alone.
var preflightInstalled = preflight.Installed

// containerProviderSetting resolves the container provider `crewship start`
// would use: CREWSHIP_CONTAINER_PROVIDER when set, else the built-in default
// from internal/config.Default() ("docker").
//
// Caveat worth knowing when reading doctor's output: `crewship start
// --config <file>` can also set container.provider from YAML, and doctor has
// no --config flag to point at that file. Env + default is what doctor can
// honestly resolve, and it covers every deployment that doesn't hand-write a
// config file. The detail string always names the provider it assumed so the
// discrepancy is visible rather than silent.
func containerProviderSetting() string {
	if v := strings.ToLower(strings.TrimSpace(os.Getenv("CREWSHIP_CONTAINER_PROVIDER"))); v != "" {
		return v
	}
	return config.Default().Container.Provider
}

// checkContainerRuntime reports the runtime `crewship start` would ACTUALLY
// use, honouring the configured provider. Doctor used to hand-roll a
// docker-then-apple probe and PASS on whichever answered — which disagreed
// with initProviders (cmd_start.go), where "auto" tries APPLE first and an
// explicit "docker"/"apple" never falls back at all. Reporting a provider
// start would never pick is worse than reporting nothing: it sends operators
// debugging the wrong runtime.
func checkContainerRuntime(ctx context.Context) checkResult {
	var dockerDesc, appleDesc string
	if d, err := docker.Detect(ctx); err == nil {
		dockerDesc = fmt.Sprintf("%s %s (%s)", d.Runtime, d.Version, d.Socket)
	}
	if a, err := apple.Detect(ctx); err == nil {
		appleDesc = fmt.Sprintf("apple %s (host_ip=%s)", a.Version, a.HostIP)
	}
	// Distinguish "installed but not running" (start it) from "not
	// installed" (install one) — the fixes are opposites and a wrong hint
	// sends the user reinstalling software they already have.
	var installedNames []string
	var startHint string
	if installed := preflightInstalled(); len(installed) > 0 {
		for _, rt := range installed {
			installedNames = append(installedNames, rt.Name)
		}
		startHint = installed[0].StartHint
	}
	return containerRuntimeVerdict(containerProviderSetting(), dockerDesc, appleDesc, installedNames, startHint)
}

// containerRuntimeVerdict is the pure decision table behind
// checkContainerRuntime. dockerDesc / appleDesc are the rendered detail for
// each runtime, empty when that runtime did not answer; installed /
// startHint come from the preflight scan. Keeping it parameterised is what
// makes the start-parity contract testable without a container daemon.
func containerRuntimeVerdict(provider, dockerDesc, appleDesc string, installed []string, startHint string) checkResult {
	const name = "container runtime"
	pass := func(desc string) checkResult {
		detail := fmt.Sprintf("%s  [provider=%s]", desc, provider)
		r := checkResult{name: name, status: "PASS", detail: detail}
		// Docker won under an explicit/auto selection while Apple is also
		// up: mention the road not taken once, quietly, rather than leaving
		// `doctor` silent about a provider the host fully supports.
		if provider != "apple" && dockerDesc != "" && appleDesc != "" && desc == dockerDesc {
			r.hint = "Apple Containers is also available here — set CREWSHIP_CONTAINER_PROVIDER=apple (or container.provider) to use it"
		}
		return r
	}
	fail := func(detail, hint string) checkResult {
		return checkResult{
			name:   name,
			status: "FAIL",
			detail: fmt.Sprintf("%s  [provider=%s]", detail, provider),
			hint:   hint,
		}
	}
	// Shared "nothing answered" tail: both the explicit-provider and the
	// auto paths end here, and the remediation only depends on whether a
	// runtime is installed at all.
	notRunning := func() checkResult {
		if len(installed) > 0 {
			return fail(fmt.Sprintf("%s installed but not running", strings.Join(installed, ", ")),
				fmt.Sprintf("start it: %s", startHint))
		}
		return fail("no container runtime installed (Docker, Podman, Colima, OrbStack, Rancher Desktop, Apple Containers)",
			installHintForOS(runtime.GOOS))
	}

	switch provider {
	case "docker":
		if dockerDesc != "" {
			return pass(dockerDesc)
		}
		if appleDesc != "" {
			// Apple is up but the provider is pinned to docker: `start`
			// would run WITHOUT containers, so this is a real FAIL even
			// though a runtime exists on the host.
			return fail("docker not available (Apple Containers is up, but container.provider is pinned to docker)",
				"start Docker, or set CREWSHIP_CONTAINER_PROVIDER=apple to use the runtime you already have")
		}
		return notRunning()
	case "apple":
		if appleDesc != "" {
			return pass(appleDesc)
		}
		if dockerDesc != "" {
			return fail("Apple Containers not available (Docker is up, but container.provider is pinned to apple)",
				"start Apple Containers ('container system start'), or set CREWSHIP_CONTAINER_PROVIDER=docker")
		}
		return notRunning()
	case "auto":
		// Mirror initProviders: Apple first (native, lighter on macOS),
		// Docker second.
		if appleDesc != "" {
			return pass(appleDesc)
		}
		if dockerDesc != "" {
			return pass(dockerDesc)
		}
		return notRunning()
	default:
		// config.Validate rejects anything outside docker|apple|auto, so
		// this is a config error the server would refuse to boot on.
		return fail(fmt.Sprintf("unknown container provider %q", provider),
			"set container.provider (or CREWSHIP_CONTAINER_PROVIDER) to docker, apple, or auto")
	}
}

// checkAppleContainers reports Apple Containers availability as its own row
// on macOS. Purely informational (PASS/INFO, never WARN/FAIL): the container
// runtime row above owns the health verdict, this one only answers "is the
// native macOS provider an option on this host?" — a question doctor could
// not answer at all whenever Docker replied first.
func checkAppleContainers(ctx context.Context) checkResult {
	desc := ""
	if a, err := apple.Detect(ctx); err == nil {
		desc = fmt.Sprintf("apple %s (host_ip=%s)", a.Version, a.HostIP)
	}
	return appleContainersVerdict(desc)
}

func appleContainersVerdict(desc string) checkResult {
	const name = "apple containers"
	if desc == "" {
		return checkResult{
			name:   name,
			status: "INFO",
			detail: "not available (optional native macOS provider)",
			hint:   "install from https://github.com/apple/container and run 'container system start' to use CREWSHIP_CONTAINER_PROVIDER=apple",
		}
	}
	return checkResult{name: name, status: "PASS", detail: desc}
}

// installHintForOS picks the one-line install pointer doctor prints when no
// runtime is installed at all. The full multi-line guidance lives in
// internal/preflight (printed by `crewship start`); doctor's hint column is
// single-line, so this is the condensed OS-specific version.
func installHintForOS(goos string) string {
	switch goos {
	case "darwin":
		return "install one: https://docs.docker.com/desktop/setup/install/mac-install/  OR  brew install --cask orbstack  OR  brew install colima docker"
	case "linux":
		return "install Docker Engine: curl -fsSL https://get.docker.com | sh && sudo systemctl enable --now docker"
	case "windows":
		return "install Docker Desktop (WSL 2 backend): https://docs.docker.com/desktop/setup/install/windows-install/"
	default:
		return "install one: https://docs.docker.com/get-docker/"
	}
}

// ─── port binding ─────────────────────────────────────────────────────
//
// docs/cli/doctor.mdx has documented this check (and shown it in the sample
// output) since the page was written, but it never existed in code. It does
// now: bind the address `crewship start` would bind, release it immediately,
// and report whether the port is available.

// doctorBindTarget resolves the host:port `crewship start` binds, using the
// same inputs internal/config does: CREWSHIP_HOST / CREWSHIP_PORT over the
// defaults from config.Default() (0.0.0.0:8080). An unparseable or
// out-of-range CREWSHIP_PORT falls back to the default exactly like
// applyEnvOverrides + Validate would (the server refuses to boot on a port
// outside 1-65535, so probing it would be diagnosing the wrong problem —
// the config error surfaces at `start`).
func doctorBindTarget() (string, int) {
	def := config.Default().Server
	host, port := def.Host, def.Port
	if v := strings.TrimSpace(os.Getenv("CREWSHIP_HOST")); v != "" {
		host = v
	}
	if v := strings.TrimSpace(os.Getenv("CREWSHIP_PORT")); v != "" {
		if p, err := strconv.Atoi(v); err == nil && p >= 1 && p <= 65535 {
			port = p
		}
	}
	return host, port
}

// runCheckPortBinding wires the production state into checkPortBinding: the
// effective server URL (to decide whether the port is even ours to bind) and
// a probe that identifies a live crewshipd on the port we're testing.
func runCheckPortBinding(ctx context.Context) checkResult {
	host, port := doctorBindTarget()
	return checkPortBinding(
		cli.EffectiveServer(flagServer, flagProfile, cliCfg),
		host, port,
		func(p int) bool { return crewshipdOnPort(ctx, p) },
	)
}

// checkPortBinding attempts to bind host:port and releases it immediately.
//
//   - remote server configured → INFO. The port lives on the other host; a
//     local bind test would be answering a question nobody asked (and would
//     FAIL on any machine that happens to run something on 8080).
//   - bind succeeds → PASS.
//   - bind fails, but the squatter answers as crewshipd → PASS. `crewship
//     doctor` on a machine where `crewship start` is ALREADY running is the
//     single most common invocation; reporting the daemon's own listener as
//     a failure would make the check a permanent false alarm.
//   - bind fails to anything else → FAIL with the documented lsof
//     remediation.
func checkPortBinding(serverURL, host string, port int, ownsPort func(int) bool) checkResult {
	const name = "port binding"

	// Only meaningful for a local target. A remote/LAN server means the
	// listener is on that host, not this one.
	if u, err := url.Parse(strings.TrimSpace(serverURL)); err == nil {
		if h := u.Hostname(); h != "" && !isLoopbackHost(h) {
			return checkResult{
				name:   name,
				status: "INFO",
				detail: fmt.Sprintf("skipped — server %s is remote, the port is bound there", h),
			}
		}
	}

	addr := net.JoinHostPort(host, strconv.Itoa(port))
	ln, err := net.Listen("tcp", addr)
	if err == nil {
		_ = ln.Close()
		return checkResult{
			name:   name,
			status: "PASS",
			detail: fmt.Sprintf("%s is bindable", addr),
		}
	}
	if ownsPort != nil && ownsPort(port) {
		return checkResult{
			name:   name,
			status: "PASS",
			detail: fmt.Sprintf("%s in use by the running crewshipd (expected)", addr),
		}
	}
	return checkResult{
		name:   name,
		status: "FAIL",
		detail: fmt.Sprintf("%s is in use by another process: %v", addr, err),
		hint:   fmt.Sprintf("find the squatter with 'lsof -i :%d' (Linux: 'ss -ltnp sport = :%d'), or pick another port with CREWSHIP_PORT", port, port),
	}
}

// crewshipdOnPort reports whether the process holding the port is our own
// daemon, by asking http://127.0.0.1:<port>/healthz for the service name.
// Anything else — no answer, wrong shape, a different service — is false, so
// an unrelated squatter still trips the FAIL branch.
func crewshipdOnPort(ctx context.Context, port int) bool {
	url := fmt.Sprintf("http://127.0.0.1:%d/healthz", port)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false
	}
	var body struct {
		Service string `json:"service"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return false
	}
	return body.Service == "crewshipd"
}

// ─── CLI ↔ server version parity ──────────────────────────────────────

// runCheckServerVersionMatch wires the production client into the testable
// helper. GET /api/v1/system/version is the endpoint that reports the
// daemon's own build (internal/api/system.go); /healthz deliberately does
// not carry it.
func runCheckServerVersionMatch(ctx context.Context) checkResult {
	return checkServerVersionMatch(ctx, newAPIClient(), version)
}

// checkServerVersionMatch compares this binary's version against the running
// server's. Every "couldn't ask" condition (down, unauthenticated, older
// server without the endpoint, unparseable body) is INFO, never FAIL: the
// "server reachable" row already owns a dead daemon and doctor must not
// double-report one root cause as two failures.
func checkServerVersionMatch(ctx context.Context, client *cli.Client, cliVersion string) checkResult {
	const name = "cli/server version"

	if cliVersion == "" || cliVersion == "dev" {
		return checkResult{name: name, status: "INFO", detail: "skipped (development build)"}
	}
	resp, err := client.WithContext(ctx).Get("/api/v1/system/version")
	if err != nil {
		return checkResult{name: name, status: "INFO", detail: "skipped (server not reachable — see 'server reachable' check)"}
	}
	if err := cli.CheckError(resp); err != nil {
		return checkResult{name: name, status: "INFO", detail: "skipped (not authenticated — run `crewship login`)"}
	}
	var body struct {
		Current string `json:"current"`
	}
	if err := cli.ReadJSON(resp, &body); err != nil {
		return checkResult{name: name, status: "INFO", detail: "skipped (could not parse the server version response)"}
	}
	return serverVersionVerdict(cliVersion, body.Current)
}

// serverVersionVerdict is the pure comparison. The `v` prefix is normalised
// away on both sides: the CLI is built with a `v`-prefixed git tag while the
// server may report either spelling depending on how it was built, and a
// prefix-only difference is not a version skew.
func serverVersionVerdict(cliVersion, serverVersion string) checkResult {
	const name = "cli/server version"
	if cliVersion == "" || cliVersion == "dev" {
		return checkResult{name: name, status: "INFO", detail: "skipped (development build)"}
	}
	if strings.TrimSpace(serverVersion) == "" {
		return checkResult{name: name, status: "INFO", detail: "skipped (server did not report a version)"}
	}
	norm := func(v string) string { return strings.TrimPrefix(strings.TrimSpace(v), "v") }
	if norm(cliVersion) == norm(serverVersion) {
		return checkResult{
			name:   name,
			status: "PASS",
			detail: fmt.Sprintf("both on %s", strings.TrimSpace(serverVersion)),
		}
	}
	return checkResult{
		name:   name,
		status: "WARN",
		detail: fmt.Sprintf("CLI %s, server %s — mismatched builds", cliVersion, strings.TrimSpace(serverVersion)),
		hint:   "run `crewship self-update` (a stale CLI against a newer server is the most common source of confusing API errors)",
	}
}

// checkDataDir verifies the data directory exists. With fixMode, it
// creates the directory if missing — the only auto-fix doctor performs.
func checkDataDir(fixMode bool) checkResult {
	dataDir, err := database.DefaultDataDir()
	if err != nil {
		return checkResult{
			name:   "data directory",
			status: "FAIL",
			detail: err.Error(),
		}
	}
	if _, err := os.Stat(dataDir.Root); err == nil {
		return checkResult{
			name:   "data directory",
			status: "PASS",
			detail: dataDir.Root,
		}
	} else if os.IsNotExist(err) {
		if fixMode {
			if mkErr := os.MkdirAll(dataDir.Root, 0o700); mkErr != nil {
				return checkResult{
					name:   "data directory",
					status: "FAIL",
					detail: fmt.Sprintf("missing, mkdir failed: %v", mkErr),
				}
			}
			return checkResult{
				name:   "data directory",
				status: "PASS",
				detail: dataDir.Root + " (created via --fix)",
			}
		}
		return checkResult{
			name:   "data directory",
			status: "WARN",
			detail: dataDir.Root + " does not exist",
			hint:   "re-run with --fix to create, or 'mkdir -p " + dataDir.Root + "'",
		}
	} else {
		return checkResult{
			name:   "data directory",
			status: "FAIL",
			detail: err.Error(),
		}
	}
}

// checkDataDirWritable does a touch-test on the data directory by
// creating a temp file. Catches the "directory exists but root mounted
// it read-only" footgun before crewshipd hits it at runtime.
func checkDataDirWritable() checkResult {
	dataDir, err := database.DefaultDataDir()
	if err != nil {
		return checkResult{name: "data dir writable", status: "FAIL", detail: err.Error()}
	}
	if _, err := os.Stat(dataDir.Root); os.IsNotExist(err) {
		return checkResult{
			name:   "data dir writable",
			status: "WARN",
			detail: "data dir does not exist (skipped)",
		}
	}
	f, err := os.CreateTemp(dataDir.Root, ".doctor-write-*.tmp")
	if err != nil {
		return checkResult{
			name:   "data dir writable",
			status: "FAIL",
			detail: fmt.Sprintf("create test file failed: %v", err),
			hint:   "fix permissions on " + dataDir.Root + " (chmod u+rwX)",
		}
	}
	path := f.Name()
	_ = f.Close()
	_ = os.Remove(path)
	return checkResult{
		name:   "data dir writable",
		status: "PASS",
		detail: "touch test ok",
	}
}

// checkDBMigrationVersion opens the local SQLite DB (if present) and
// reads the latest applied migration version from the _migrations
// table. We don't open the DB through the package's Migrate helper
// because we explicitly do NOT want to apply migrations — doctor is
// diagnostic-only. A missing _migrations table means crewshipd has
// never run; that's a WARN, not a FAIL.
//
// The "expected" version is hard-coded here at the latest known
// migration so an outdated CLI talking to a freshly-migrated DB
// surfaces visibly. Bump this constant whenever a new migration lands.
func checkDBMigrationVersion(ctx context.Context) checkResult {
	const expectedLatest = 85 // matches internal/database/migrate.go highest version
	dataDir, err := database.DefaultDataDir()
	if err != nil {
		return checkResult{name: "db migration version", status: "FAIL", detail: err.Error()}
	}
	dbPath := dataDir.DatabasePath()
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return checkResult{
			name:   "db migration version",
			status: "WARN",
			detail: "database file does not exist (crewshipd has never run)",
			hint:   "run 'crewship start' to initialise the database",
		}
	}
	// Read-only open keeps doctor diagnostic-only: no WAL pragma (which
	// would mutate state) and no risk of fighting crewshipd for an
	// exclusive lock.
	db, err := sql.Open("sqlite", dbPath+"?mode=ro")
	if err != nil {
		return checkResult{
			name:   "db migration version",
			status: "WARN",
			detail: fmt.Sprintf("could not open DB: %v", err),
			hint:   "ensure crewshipd is not holding an exclusive lock",
		}
	}
	defer db.Close()
	var v int
	row := db.QueryRowContext(ctx, "SELECT MAX(version) FROM _migrations")
	if err := row.Scan(&v); err != nil {
		// _migrations missing → DB was just created or is the wrong file.
		return checkResult{
			name:   "db migration version",
			status: "WARN",
			detail: fmt.Sprintf("could not read _migrations: %v", err),
			hint:   "crewshipd may not have run against this DB yet",
		}
	}
	switch {
	case v == expectedLatest:
		return checkResult{
			name:   "db migration version",
			status: "PASS",
			detail: fmt.Sprintf("v%d (latest)", v),
		}
	case v > expectedLatest:
		return checkResult{
			name:   "db migration version",
			status: "WARN",
			detail: fmt.Sprintf("v%d (newer than CLI knows about — expected ≤ v%d)", v, expectedLatest),
			hint:   "upgrade the CLI binary to match the server",
		}
	default:
		return checkResult{
			name:   "db migration version",
			status: "WARN",
			detail: fmt.Sprintf("v%d (CLI expects v%d)", v, expectedLatest),
			hint:   "start crewshipd to apply pending migrations",
		}
	}
}

// checkSidecarBinary looks for the sidecar in the conventional paths.
// We don't fail hard if it's missing because crewshipd can embed the
// sidecar at build time — a separate file isn't required. WARN with
// the search paths is the honest signal.
func checkSidecarBinary() checkResult {
	// names captures both Unix and Windows (.exe) filenames so the probe
	// works cross-platform. On non-Windows hosts the .exe variants are
	// harmless extras that just won't exist.
	names := []string{"crewship-sidecar", "sidecar"}
	if runtime.GOOS == "windows" {
		names = append(names, "crewship-sidecar.exe", "sidecar.exe")
	}
	candidates := []string{}
	if home, err := os.UserHomeDir(); err == nil {
		for _, name := range names {
			candidates = append(candidates, filepath.Join(home, ".crewship", "bin", name))
		}
	}
	// Also check next to the CLI binary itself — useful in tarball installs.
	if exe, err := os.Executable(); err == nil {
		for _, name := range names {
			candidates = append(candidates, filepath.Join(filepath.Dir(exe), name))
		}
	}
	for _, p := range candidates {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			// Presence alone is a weak signal: the classic bad deploy
			// updates the server binary but not the sidecar next to it, and
			// the stale file passes a stat check forever (#1160). Compare
			// the file against the hash baked into THIS binary at link time
			// and surface the divergence here, where an operator is already
			// looking, instead of at the first failed agent start.
			return sidecarHashVerdict(p, docker.SidecarFileHash(p), docker.ExpectedSidecarHashFromBuild())
		}
	}
	return checkResult{
		name:   "sidecar binary",
		status: "WARN",
		detail: "not found on disk (may be embedded in crewshipd)",
		hint:   "searched: " + strings.Join(candidates, ", "),
	}
}

// sidecarHashVerdict compares the on-disk sidecar against the build-time
// expected hash. Fail-open by design, matching the provider-side contract in
// internal/provider/docker:
//
//   - expected == ""  → no hash was injected (dev `go build`, `go test`,
//     container image builds). Presence-only PASS, exactly as before.
//   - onDisk  == ""   → unreadable file. "Unknown", never a mismatch — a
//     permissions hiccup must not raise a fleet-wide false alarm.
//   - equal           → PASS with the hash, so the operator can eyeball it
//     against the server logs.
//   - different       → WARN. Not FAIL: agents still start, they just run a
//     sidecar the server wasn't built against. The remediation is a rebuild
//     and recopy, NOT a restart (restarting remounts the same stale file).
func sidecarHashVerdict(path, onDisk, expected string) checkResult {
	const name = "sidecar binary"
	if expected == "" || onDisk == "" {
		return checkResult{name: name, status: "PASS", detail: path}
	}
	if onDisk == expected {
		return checkResult{
			name:   name,
			status: "PASS",
			detail: fmt.Sprintf("%s (hash %s, matches this build)", path, onDisk),
		}
	}
	return checkResult{
		name:   name,
		status: "WARN",
		detail: fmt.Sprintf("%s is stale: hash %s, this binary was built against %s", path, onDisk, expected),
		hint: "rebuild and recopy the sidecar ('make build:sidecar'), then " +
			"'crewship crew restart-agents <crew>' — restarting alone would remount the same stale file",
	}
}

// checkNextAuthSecret used to FAIL/WARN when NEXTAUTH_SECRET was unset
// because the old startup path took the process down on a missing
// secret. As of the zero-friction-install change, `crewship start`
// auto-generates the secret on first boot and persists it under
// <dataDir>/secrets.env, so an empty env is no longer a problem
// post-start — doctor's job is now to surface where the value lives
// (env vs persisted file) and confirm it's at least minimum strength.
func checkNextAuthSecret() checkResult {
	if v := os.Getenv("NEXTAUTH_SECRET"); v != "" {
		if len(v) < 32 {
			return checkResult{
				name:   "NEXTAUTH_SECRET",
				status: "WARN",
				detail: fmt.Sprintf("env-provided value is short (%d chars)", len(v)),
				hint:   "regenerate with openssl rand -hex 32 (≥32 chars recommended)",
			}
		}
		return checkResult{
			name:   "NEXTAUTH_SECRET",
			status: "PASS",
			detail: fmt.Sprintf("env-provided (%d chars)", len(v)),
		}
	}

	// Not in env — check the persisted secrets file the auto-bootstrap
	// would write. Missing == "crewship start hasn't been run yet on
	// this data dir", which is fine; the next `crewship start` will
	// generate the value.
	dataDir, err := database.DefaultDataDir()
	if err != nil {
		return checkResult{
			name:   "NEXTAUTH_SECRET",
			status: "WARN",
			detail: "not in env; could not locate data dir to check persisted file",
			hint:   fmt.Sprintf("resolve data dir error: %v", err),
		}
	}
	path := secrets.SecretsFilePath(dataDir.Root)
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return checkResult{
				name:   "NEXTAUTH_SECRET",
				status: "INFO",
				detail: "not yet bootstrapped",
				hint:   "first `crewship start` will generate and persist this to " + path,
			}
		}
		// File exists but we can't stat it — most commonly a
		// permission problem on the data dir. Surface the real
		// error so the operator can fix it, not "not yet bootstrapped"
		// which would lie about a healthy state.
		return checkResult{
			name:   "NEXTAUTH_SECRET",
			status: "WARN",
			detail: fmt.Sprintf("cannot inspect persisted secret file: %v", err),
			hint:   "check permissions / ownership on " + path,
		}
	}

	// File exists — inspect its contents and mirror the env-branch
	// validation. A stale or hand-edited secrets.env with the key
	// missing or too short would otherwise pass this check while
	// silently bricking the next server start.
	persisted, err := secrets.ReadPersisted(path)
	if err != nil {
		return checkResult{
			name:   "NEXTAUTH_SECRET",
			status: "WARN",
			detail: fmt.Sprintf("cannot parse persisted secret file: %v", err),
			hint:   "inspect " + path + " for malformed lines",
		}
	}
	v, ok := persisted[secrets.NextAuthSecretKey]
	if !ok || v == "" {
		return checkResult{
			name:   "NEXTAUTH_SECRET",
			status: "WARN",
			detail: "persisted secret file exists but " + secrets.NextAuthSecretKey + " entry is missing",
			hint:   "delete " + path + " so the next `crewship start` regenerates it (any encrypted credentials encrypted under the missing key are unrecoverable)",
		}
	}
	if err := secrets.ValidateNextAuthSecret(v); err != nil {
		return checkResult{
			name:   "NEXTAUTH_SECRET",
			status: "WARN",
			detail: fmt.Sprintf("persisted value invalid: %v", err),
			hint:   "regenerate with openssl rand -hex 32 and replace the entry in " + path,
		}
	}
	return checkResult{
		name:   "NEXTAUTH_SECRET",
		status: "PASS",
		detail: fmt.Sprintf("auto-managed in %s (%d chars)", path, len(v)),
	}
}

// checkServerReachable does a TCP dial against the configured server's
// host:port. We can't do a full HTTP probe because doctor must work
// runCheckLocalModelEndpoint probes the workspace's configured local-model
// endpoint (an ENDPOINT_URL credential — Ollama / OpenAI-compatible) by asking
// the server to test it. This turns "my agent run failed opaquely" into an
// up-front "endpoint X unreachable". It is advisory: INFO (skip) when the CLI
// isn't authenticated, the server is unreachable, or no endpoint is configured;
// PASS when reachable; WARN when configured-but-unreachable.
func runCheckLocalModelEndpoint(ctx context.Context) checkResult {
	const name = "local-model endpoint"
	client := newAPIClient()

	resp, err := client.Get("/api/v1/credentials")
	if err != nil {
		return checkResult{name: name, status: "INFO", detail: "skipped (could not reach the server to list credentials)"}
	}
	if err := cli.CheckError(resp); err != nil {
		return checkResult{name: name, status: "INFO", detail: "skipped (not authenticated — run `crewship login`)"}
	}
	var creds []struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Type   string `json:"type"`
		Status string `json:"status"`
	}
	if err := cli.ReadJSON(resp, &creds); err != nil {
		return checkResult{name: name, status: "INFO", detail: "skipped (could not parse credential list)"}
	}

	var endpointID, endpointName string
	for _, c := range creds {
		if c.Type == "ENDPOINT_URL" && (c.Status == "" || c.Status == "ACTIVE") {
			endpointID, endpointName = c.ID, c.Name
			break
		}
	}
	if endpointID == "" {
		return checkResult{name: name, status: "INFO", detail: "no local-model endpoint configured (skip)"}
	}

	tResp, err := client.Post("/api/v1/credentials/"+endpointID+"/test", nil)
	if err != nil {
		return checkResult{name: name, status: "INFO", detail: fmt.Sprintf("skipped (could not test %q)", endpointName)}
	}
	if err := cli.CheckError(tResp); err != nil {
		return checkResult{name: name, status: "INFO", detail: fmt.Sprintf("skipped (test call failed for %q)", endpointName)}
	}
	var res struct {
		Valid bool   `json:"valid"`
		Error string `json:"error"`
	}
	if err := cli.ReadJSON(tResp, &res); err != nil {
		return checkResult{name: name, status: "INFO", detail: "skipped (could not parse test result)"}
	}
	if res.Valid {
		detail := endpointName + " reachable"
		if res.Error != "" {
			detail = endpointName + ": " + res.Error
		}
		return checkResult{name: name, status: "PASS", detail: detail}
	}
	msg := res.Error
	if msg == "" {
		msg = "unreachable"
	}
	return checkResult{
		name:   name,
		status: "WARN",
		detail: fmt.Sprintf("%s: %s", endpointName, msg),
		hint:   "start the endpoint (e.g. `ollama serve`) or fix the URL with `crewship credential update`",
	}
}

// even when the user isn't logged in (unauthenticated /healthz endpoints
// aren't universal). A successful TCP connect is enough to distinguish
// "server is down / wrong port" from "auth issue".
// unreachableServerVerdict grades a failed dial by whether the target is local.
//
// doctor is a PRE-flight tool: `crewship doctor` before `crewship start` is the
// primary way it gets run, and a local daemon that hasn't been started yet is
// the expected state there, not a fault. Grading that FAIL would make the
// documented exit-1 gate fire on a healthy machine every time — which trains
// operators to ignore the exit code and defeats the point of having a gate.
//
// A remote target is the opposite: nobody configures a remote server by
// accident, so if it does not answer, something is genuinely wrong and the
// gate should fire.
func unreachableServerVerdict(host, via string, dialErr error) checkResult {
	name := "server reachable"
	dialHost, _, splitErr := net.SplitHostPort(host)
	if splitErr != nil {
		dialHost = host
	}
	if isLoopbackHost(dialHost) {
		return checkResult{
			name:   name,
			status: "WARN",
			detail: fmt.Sprintf("dial %s (from %s): %v", host, via, dialErr),
			hint:   "expected if you have not run `crewship start` yet — start it, then re-run doctor",
		}
	}
	return checkResult{
		name:   name,
		status: "FAIL",
		detail: fmt.Sprintf("dial %s (from %s): %v", host, via, dialErr),
		hint:   "check that crewshipd is running on that host and the port is open",
	}
}

func checkServerReachable(ctx context.Context) checkResult {
	// EffectiveServer (flag > profile > env > cfg) — probe the host commands
	// actually dial, not env>cfg, so the reachability check reflects the active
	// profile. (#1003)
	//
	// The WithSource variant additionally reports WHICH layer won. "TCP
	// localhost:8080 ok" was true but useless when the operator's real
	// question is "why is it talking to THAT host?" — a forgotten
	// CREWSHIP_SERVER in one shell and a `server:` line in cli-config.yaml
	// produce byte-identical output otherwise.
	server, source := cli.EffectiveServerWithSource(flagServer, flagProfile, cliCfg)
	via := serverSourceLabel(source, cli.ActiveProfileName(flagProfile, cliCfg))
	u, err := url.Parse(server)
	if err != nil {
		return checkResult{
			name:   "server reachable",
			status: "FAIL",
			detail: fmt.Sprintf("invalid server URL %q (from %s): %v", server, via, err),
		}
	}
	host := u.Host
	if host == "" {
		return checkResult{
			name:   "server reachable",
			status: "FAIL",
			detail: fmt.Sprintf("could not parse host from %q (from %s)", server, via),
		}
	}
	// Default ports for http/https when the URL omits them — net.Dial
	// would otherwise fail on a bare "host" string.
	if _, _, splitErr := net.SplitHostPort(host); splitErr != nil {
		switch u.Scheme {
		case "https":
			host += ":443"
		case "http", "":
			host += ":80"
		}
	}
	dialer := &net.Dialer{Timeout: 3 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", host)
	if err != nil {
		return unreachableServerVerdict(host, via, err)
	}
	_ = conn.Close()
	return checkResult{
		name:   "server reachable",
		status: "PASS",
		detail: fmt.Sprintf("TCP %s ok (from %s)", host, via),
	}
}

// serverSourceLabel renders a cli.ServerSource as the phrase doctor prints
// in the detail column. The profile case names the profile: "profile" alone
// is not actionable on a config holding dev1/dev2/dev3.
func serverSourceLabel(source cli.ServerSource, profileName string) string {
	switch source {
	case cli.ServerSourceFlag:
		return "--server flag"
	case cli.ServerSourceProfile:
		if profileName != "" {
			return fmt.Sprintf("profile %q", profileName)
		}
		return "active profile"
	case cli.ServerSourceEnv:
		return "CREWSHIP_SERVER env"
	case cli.ServerSourceConfig:
		return "config file (server:)"
	case cli.ServerSourceDefault:
		return "built-in default"
	default:
		return string(source)
	}
}

// runCheckTelemetryStatus surfaces the crashreport consent state from the
// local DB. The check is split into a thin wrapper that resolves the
// production DB + DSN and an inner helper (checkTelemetryStatus) that
// accepts both as parameters so unit tests can drive every branch with a
// seeded temp DB.
//
// The "not asked" path is a WARN rather than PASS because operators reading
// `crewship doctor` output after install should see *something* about
// telemetry — the first `crewship start` will flip it to default-on per
// the beta opt-out policy, and we want that visible before it happens.
func runCheckTelemetryStatus(ctx context.Context) checkResult {
	db, err := openLocalDB(ctx)
	if err != nil {
		return checkResult{
			name:   "telemetry status",
			status: "WARN",
			detail: fmt.Sprintf("could not open local DB: %v", err),
			hint:   "run 'crewship start' once to initialise the database",
		}
	}
	defer db.Close()
	return checkTelemetryStatus(ctx, db.DB, crashreport.ResolveDSN())
}

// checkTelemetryStatus is the testable inner form. dsn is the resolved DSN
// (vendor default OR CREWSHIP_SENTRY_DSN override) — passed in rather than
// re-resolved internally so tests can simulate "no DSN compiled" and
// "DSN set" without touching env vars.
func checkTelemetryStatus(ctx context.Context, db *sql.DB, dsn string) checkResult {
	enabled, asked, _, err := crashreport.Status(ctx, db)
	if err != nil {
		return checkResult{
			name:   "telemetry status",
			status: "WARN",
			detail: fmt.Sprintf("read consent: %v", err),
		}
	}
	if !asked {
		// Default-by-version (crashreport.DefaultOptIn): prerelease/dev
		// builds flip to ENABLED on next start, stable builds stay off
		// until the operator opts in. Surface the one that applies.
		detail := "telemetry not yet configured (stable build: stays DISABLED until you opt in)"
		hint := "opt in with 'crewship telemetry on'"
		if crashreport.DefaultOptIn(version) {
			detail = "telemetry not yet configured (prerelease/dev build: will default to ENABLED on next start)"
			hint = "run 'crewship telemetry status' for details, or opt out with 'crewship telemetry off'"
		}
		return checkResult{
			name:   "telemetry status",
			status: "WARN",
			detail: detail,
			hint:   hint,
		}
	}
	if !enabled {
		return checkResult{
			name:   "telemetry status",
			status: "PASS",
			detail: "disabled by operator",
		}
	}
	if dsn == "" {
		return checkResult{
			name:   "telemetry status",
			status: "WARN",
			detail: "enabled but no DSN compiled in or set",
			hint:   "set CREWSHIP_SENTRY_DSN, or rebuild with -X crashreport.DSN=...",
		}
	}
	return checkResult{
		name:   "telemetry status",
		status: "PASS",
		detail: fmt.Sprintf("enabled, endpoint %s", dsnEndpointHost(dsn)),
	}
}

// runCheckSentryDSNWiring closes the gap that crashreport.Init logs once at
// boot and then silently swallows: "enabled by consent but no DSN compiled in
// or set". In production that line scrolls past systemd-journal noise and
// the operator never learns that crashes are routing to /dev/null. `crewship
// doctor` is the surface where that trap should surface explicitly.
//
// This check is intentionally narrower than the broader "telemetry status"
// row above — that one reports the consent state from the operator's POV
// (have they been asked, did they say yes/no). This one asks the orthogonal
// question: regardless of consent, is the DSN actually wired? Both rows can
// fire in the same run and that's by design — they describe different
// failure modes and a fix for one (set CREWSHIP_SENTRY_DSN) is irrelevant
// to the other (operator hasn't opted in yet).
//
// Splitting the wrapper from the inner helper mirrors checkTelemetryStatus:
// the inner form takes db + dsn as parameters so tests can drive every
// branch without touching env vars or rebuilding with ldflags.
func runCheckSentryDSNWiring(ctx context.Context) checkResult {
	db, err := openLocalDB(ctx)
	if err != nil {
		return checkResult{
			name:   "sentry DSN wiring",
			status: "INFO",
			detail: fmt.Sprintf("skipped (local DB unavailable: %v)", err),
		}
	}
	defer db.Close()
	return checkSentryDSNWiring(ctx, db.DB, crashreport.ResolveDSN())
}

// dsnWiringHint is the single source of truth for the "how do I fix this"
// line. Both the WARN (enabled-but-empty) and the INFO (not-yet-configured)
// paths reuse it so operators see the same remediation regardless of which
// state surfaced the missing DSN.
const dsnWiringHint = "set CREWSHIP_SENTRY_DSN in /etc/crewship/<env-file> + systemctl restart, " +
	"or rebuild with -X github.com/crewship-ai/crewship/internal/crashreport.DSN=https://..."

func checkSentryDSNWiring(ctx context.Context, db *sql.DB, dsn string) checkResult {
	enabled, asked, _, err := crashreport.Status(ctx, db)
	if err != nil {
		// Don't escalate a transient DB read failure into a WARN here — the
		// telemetry-status row above already surfaces that, and duplicating
		// the warning would just train operators to ignore both.
		return checkResult{
			name:   "sentry DSN wiring",
			status: "INFO",
			detail: fmt.Sprintf("skipped (could not read consent: %v)", err),
		}
	}
	if !asked {
		// Prerelease/dev builds flip consent on at first start, and a
		// stable-build operator may opt in any time — either way the DSN
		// should be wired before it matters. INFO + hint is the right
		// shape: nothing is broken yet, but here's what to set.
		return checkResult{
			name:   "sentry DSN wiring",
			status: "INFO",
			detail: "telemetry not yet configured; DSN wiring will matter on first start",
			hint:   dsnWiringHint,
		}
	}
	if !enabled {
		// Operator deliberately opted out. No DSN required, no hint —
		// surfacing one here would second-guess their consent decision.
		return checkResult{
			name:   "sentry DSN wiring",
			status: "INFO",
			detail: "telemetry disabled by operator (DSN not required)",
		}
	}
	if dsn == "" {
		return checkResult{
			name:   "sentry DSN wiring",
			status: "WARN",
			detail: "telemetry enabled but no Sentry DSN compiled in or set — crashes will not be reported",
			hint:   dsnWiringHint,
		}
	}
	return checkResult{
		name:   "sentry DSN wiring",
		status: "PASS",
		detail: fmt.Sprintf("DSN resolved (%s)", dsnEndpointHost(dsn)),
	}
}

// runCheckDsnReachability does a best-effort TCP probe to the configured
// Sentry-style DSN host:443. Skipped (no row in output) when telemetry is
// disabled or no DSN is configured — there's no useful signal to emit and
// adding a noisy "skipped" line every run trains operators to ignore the
// section.
//
// Result is intentionally never FAIL: an unreachable Sentry endpoint is a
// soft problem (events buffer or drop locally) that should not gate
// `crewship doctor` for the rest of the system.
func runCheckDsnReachability(ctx context.Context) checkResult {
	db, err := openLocalDB(ctx)
	if err != nil {
		return checkResult{
			name:   "telemetry endpoint",
			status: "INFO",
			detail: "skipped (local DB unavailable)",
		}
	}
	defer db.Close()
	return checkDsnReachability(ctx, db.DB, crashreport.ResolveDSN())
}

func checkDsnReachability(ctx context.Context, db *sql.DB, dsn string) checkResult {
	enabled, asked, _, err := crashreport.Status(ctx, db)
	if err != nil || !asked || !enabled {
		return checkResult{
			name:   "telemetry endpoint",
			status: "INFO",
			detail: "skipped (telemetry off)",
		}
	}
	if dsn == "" {
		return checkResult{
			name:   "telemetry endpoint",
			status: "INFO",
			detail: "skipped (no DSN configured)",
		}
	}
	host := dsnEndpointHost(dsn)
	if host == "" || host == "unknown" {
		return checkResult{
			name:   "telemetry endpoint",
			status: "WARN",
			detail: "could not parse DSN host",
			hint:   "verify CREWSHIP_SENTRY_DSN follows https://<key>@<host>/<project>",
		}
	}
	target := host + ":443"
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	conn, err := dialer.DialContext(ctx, "tcp", target)
	if err != nil {
		return checkResult{
			name:   "telemetry endpoint",
			status: "WARN",
			detail: fmt.Sprintf("dial %s: %v", target, err),
			hint:   "Sentry being unreachable will silently drop crash events; check egress firewall",
		}
	}
	_ = conn.Close()
	return checkResult{
		name:   "telemetry endpoint",
		status: "PASS",
		detail: "TCP " + target + " ok",
	}
}

// runCheckDataDirPerms surfaces drift in the strict permissions
// internal/database.Open applies on every boot: 0700 on the directory and
// 0600 on the .db file. TestOpenChmodsDBFile asserts the *initial* set;
// this check catches the operator footgun where a backup restore, a chmod
// fat-finger, or an rsync without --perms re-loosens the file underneath
// a running install.
//
// Posix-only semantics: we skip the perm bits entirely on Windows where
// the SQLite file inherits ACLs we can't usefully assert on with os.FileMode.
func runCheckDataDirPerms() checkResult {
	dataDir, err := database.DefaultDataDir()
	if err != nil {
		return checkResult{name: "data dir perms", status: "FAIL", detail: err.Error()}
	}
	return checkDataDirPerms(dataDir.Root, dataDir.DatabasePath())
}

func checkDataDirPerms(root, dbPath string) checkResult {
	if runtime.GOOS == "windows" {
		return checkResult{
			name:   "data dir perms",
			status: "INFO",
			detail: "skipped (POSIX perm bits don't apply on Windows)",
		}
	}
	dirInfo, err := os.Stat(root)
	if os.IsNotExist(err) {
		return checkResult{
			name:   "data dir perms",
			status: "WARN",
			detail: "data dir does not exist (skipped)",
		}
	}
	if err != nil {
		return checkResult{name: "data dir perms", status: "FAIL", detail: err.Error()}
	}
	dirMode := dirInfo.Mode().Perm()
	if dirMode != 0o700 {
		return checkResult{
			name:   "data dir perms",
			status: "WARN",
			detail: fmt.Sprintf("%s mode = %#o (want 0700)", root, dirMode),
			hint:   "chmod 0700 " + root,
		}
	}
	fileInfo, err := os.Stat(dbPath)
	if os.IsNotExist(err) {
		// Directory is correct but the DB hasn't been created yet — the
		// next crewshipd boot will chmod it. PASS on the dir alone.
		return checkResult{
			name:   "data dir perms",
			status: "PASS",
			detail: fmt.Sprintf("%s 0700 (db file not yet created)", root),
		}
	}
	if err != nil {
		return checkResult{name: "data dir perms", status: "FAIL", detail: err.Error()}
	}
	fileMode := fileInfo.Mode().Perm()
	if fileMode != 0o600 {
		return checkResult{
			name:   "data dir perms",
			status: "WARN",
			detail: fmt.Sprintf("%s mode = %#o (want 0600)", dbPath, fileMode),
			hint:   "chmod 0600 " + dbPath,
		}
	}
	return checkResult{
		name:   "data dir perms",
		status: "PASS",
		detail: fmt.Sprintf("dir 0700, db file 0600 (%s)", root),
	}
}

// runCheckUpdateAvailable hits the GitHub Releases API (cached on disk for
// 24h by internal/update). The version-check semantics:
//
//   - version == "dev"               → skipped silently (developer build)
//   - network failure                → INFO, not WARN — being offline isn't a
//     health signal
//   - newer release exists           → WARN with "vX available, you're on vY"
//   - equal or newer-than-latest     → PASS
//
// Network calls in doctor are bounded by the 10s probe timeout from
// run(); internal/update further caps at 5s for the HTTP request.
func runCheckUpdateAvailable(ctx context.Context) checkResult {
	return checkUpdateAvailable(ctx, version, update.Check)
}

// checkFunc is the indirection that lets tests stub update.Check without
// invoking the real GitHub Releases API.
type checkFunc func(ctx context.Context, currentVersion string) (*update.Result, error)

func checkUpdateAvailable(ctx context.Context, currentVersion string, fn checkFunc) checkResult {
	if currentVersion == "" || currentVersion == "dev" {
		return checkResult{
			name:   "update check",
			status: "INFO",
			detail: "skipped (development build)",
		}
	}
	result, err := fn(ctx, currentVersion)
	if err != nil {
		// Network/parse failure is not a health signal — version checks
		// have no operational consequence when they fail.
		return checkResult{
			name:   "update check",
			status: "INFO",
			detail: fmt.Sprintf("could not check for updates: %v", err),
		}
	}
	if result == nil {
		// update.Check returns (nil, nil) when the binary is "dev" or
		// CREWSHIP_SKIP_UPDATE_CHECK is set. Mirror as a skipped INFO.
		return checkResult{
			name:   "update check",
			status: "INFO",
			detail: "skipped",
		}
	}
	if result.Newer {
		return checkResult{
			name:   "update check",
			status: "WARN",
			detail: fmt.Sprintf("%s available, you're on %s", result.Latest, result.Current),
			hint:   "brew upgrade crewship  OR  docker pull ghcr.io/crewship-ai/crewship:latest",
		}
	}
	return checkResult{
		name:   "update check",
		status: "PASS",
		detail: fmt.Sprintf("%s is current", result.Current),
	}
}

func init() {
	doctorCmd.Flags().Bool("fix", false, "Attempt safe auto-repairs (e.g. create missing data directory)")
	doctorCmd.Flags().Bool("json", false, "Deprecated alias for --format json")
}

// doctorCheckJSON is the per-check shape in the --json payload.
// Mirrors checkResult but with JSON tags so the field names land in
// snake_case (idiomatic for the CLI's JSON outputs everywhere else).
// hint is omitempty so consumers can branch on its presence rather
// than its emptiness.
type doctorCheckJSON struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail"`
	Hint   string `json:"hint,omitempty"`
}

// doctorJSON is the top-level --json output shape. failed + warned
// are the canonical CI gate inputs; the per-check array is for
// callers that need richer filtering.
type doctorJSON struct {
	Checks  []doctorCheckJSON `json:"checks"`
	Failed  int               `json:"failed"`
	Warned  int               `json:"warned"`
	Version string            `json:"version"`
	OS      string            `json:"os"`
	Arch    string            `json:"arch"`
}

// emitDoctorJSON marshals the doctor result into the documented
// --json shape and writes it to out. Pulled out as a helper so a
// future test can drive it with hand-built results without standing
// up the full check battery (which touches Docker, the data dir,
// the network, and the DB).
//
// Returns the encode error so the cobra RunE handler can surface a
// non-zero exit if stdout is closed mid-write (CI piping into a
// killed reader) or json.Marshal balks on a non-marshalable field.
// Silent failure here would let automation receive empty/truncated
// JSON, miss the missing top-level fields, and miscount failures.
func emitDoctorJSON(out io.Writer, results []checkResult, failed, warned int) error {
	checks := make([]doctorCheckJSON, 0, len(results))
	for _, r := range results {
		checks = append(checks, doctorCheckJSON{
			Name:   r.name,
			Status: r.status,
			Detail: r.detail,
			Hint:   r.hint,
		})
	}
	payload := doctorJSON{
		Checks:  checks,
		Failed:  failed,
		Warned:  warned,
		Version: version,
		OS:      runtime.GOOS,
		Arch:    runtime.GOARCH,
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	if err := enc.Encode(payload); err != nil {
		return fmt.Errorf("emit doctor JSON: %w", err)
	}
	return nil
}
