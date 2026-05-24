//go:build !clionly

package main

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/crashreport"
	"github.com/crewship-ai/crewship/internal/database"
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
are deliberately left to the operator with actionable URLs in the output.`,
	Run: func(cmd *cobra.Command, args []string) {
		fixMode, _ := cmd.Flags().GetBool("fix")
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
		fmt.Println()
		fmt.Printf("  %sCrewship doctor%s — environment check\n", cli.Bold, cli.Reset)
		fmt.Printf("  %sbinary:%s  %s   %sos:%s %s/%s\n",
			cli.Dim, cli.Reset, version,
			cli.Dim, cli.Reset, runtime.GOOS, runtime.GOARCH)
		fmt.Println()

		results := make([]checkResult, 0, 12)
		runProbe := func(fn func(context.Context) checkResult) {
			ctx, cancel := withTimeout()
			defer cancel()
			results = append(results, fn(ctx))
		}
		runProbe(checkContainerRuntime)
		results = append(results, checkDataDir(fixMode))
		results = append(results, checkDataDirWritable())
		runProbe(checkDBMigrationVersion)
		results = append(results, checkSidecarBinary())
		results = append(results, checkNextAuthSecret())
		runProbe(checkServerReachable)

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

		for _, r := range results {
			r.print()
		}

		var failed, warned int
		for _, r := range results {
			switch r.status {
			case "FAIL":
				failed++
			case "WARN":
				warned++
			}
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
	},
}

// checkContainerRuntime probes both Docker-compatible runtimes and Apple
// Containers. We accept either one — they're functionally equivalent for
// Crewship's purposes, so finding any container runtime is a PASS.
func checkContainerRuntime(ctx context.Context) checkResult {
	if d, err := docker.Detect(ctx); err == nil {
		return checkResult{
			name:   "container runtime",
			status: "PASS",
			detail: fmt.Sprintf("%s %s (%s)", d.Runtime, d.Version, d.Socket),
		}
	}
	if a, err := apple.Detect(ctx); err == nil {
		return checkResult{
			name:   "container runtime",
			status: "PASS",
			detail: fmt.Sprintf("apple %s (host_ip=%s)", a.Version, a.HostIP),
		}
	}
	return checkResult{
		name:   "container runtime",
		status: "FAIL",
		detail: "no Docker-compatible runtime or Apple Containers found",
		hint:   "install one: https://docs.docker.com/get-docker/  OR  brew install container",
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
			return checkResult{
				name:   "sidecar binary",
				status: "PASS",
				detail: p,
			}
		}
	}
	return checkResult{
		name:   "sidecar binary",
		status: "WARN",
		detail: "not found on disk (may be embedded in crewshipd)",
		hint:   "searched: " + strings.Join(candidates, ", "),
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
// even when the user isn't logged in (unauthenticated /healthz endpoints
// aren't universal). A successful TCP connect is enough to distinguish
// "server is down / wrong port" from "auth issue".
func checkServerReachable(ctx context.Context) checkResult {
	server := cli.ResolveServer(flagServer, cliCfg)
	u, err := url.Parse(server)
	if err != nil {
		return checkResult{
			name:   "server reachable",
			status: "FAIL",
			detail: fmt.Sprintf("invalid server URL %q: %v", server, err),
		}
	}
	host := u.Host
	if host == "" {
		return checkResult{
			name:   "server reachable",
			status: "FAIL",
			detail: fmt.Sprintf("could not parse host from %q", server),
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
		return checkResult{
			name:   "server reachable",
			status: "FAIL",
			detail: fmt.Sprintf("dial %s: %v", host, err),
			hint:   "check that crewshipd is running and the port is open",
		}
	}
	_ = conn.Close()
	return checkResult{
		name:   "server reachable",
		status: "PASS",
		detail: "TCP " + host + " ok",
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
		return checkResult{
			name:   "telemetry status",
			status: "WARN",
			detail: "telemetry not yet configured (will default to ENABLED on next start)",
			hint:   "run 'crewship telemetry status' for details, or opt out with 'crewship telemetry off'",
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
		// Beta default is opt-in-on-first-start, so the operator WILL want
		// the DSN wired before that flip. INFO + hint is the right shape:
		// nothing is broken yet, but here's what to set before it matters.
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
}
