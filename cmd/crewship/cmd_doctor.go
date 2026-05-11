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
	"github.com/crewship-ai/crewship/internal/database"
	"github.com/crewship-ai/crewship/internal/provider/apple"
	"github.com/crewship-ai/crewship/internal/provider/docker"
	"github.com/spf13/cobra"
)

// checkResult is one row in the doctor output. status is one of
// "PASS" / "WARN" / "FAIL"; detail is the human-readable explanation.
//
// The struct exists so each check can be a discrete function that
// returns a single value, making the doctor easy to extend (add
// another `runX()` call to the dispatch list) and test (the table
// driver in cmd_doctor_test.go can compare a slice of results without
// caring about ordering or rendering details).
type checkResult struct {
	name   string
	status string // PASS / WARN / FAIL
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

		results := make([]checkResult, 0, 7)
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
			fmt.Printf("%s%d failed, %d warned%s — fix the FAILs and re-run.\n",
				cli.Red, failed, warned, cli.Reset)
		case warned > 0:
			fmt.Printf("%s%d warned%s — review and re-run.\n", cli.Yellow, warned, cli.Reset)
		default:
			fmt.Printf("%sAll checks passed.%s\n", cli.Green, cli.Reset)
		}
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

// checkNextAuthSecret guards the "silent 404 if NEXTAUTH_SECRET is
// missing" trap documented in MEMORY (project_prod_vm_setup.md). The
// env var is only meaningful for the Next.js web UI; the CLI doesn't
// use it directly, so we WARN rather than FAIL. The check is a hard
// emptiness test — short / weak secrets are still up to the operator.
func checkNextAuthSecret() checkResult {
	v := os.Getenv("NEXTAUTH_SECRET")
	if v == "" {
		return checkResult{
			name:   "NEXTAUTH_SECRET",
			status: "WARN",
			detail: "not set — Next.js will return 404 silently",
			hint:   "export NEXTAUTH_SECRET=$(openssl rand -base64 32)  in the env that runs the web UI",
		}
	}
	if len(v) < 32 {
		return checkResult{
			name:   "NEXTAUTH_SECRET",
			status: "WARN",
			detail: fmt.Sprintf("set but short (%d chars)", len(v)),
			hint:   "regenerate with openssl rand -base64 32 (≥32 chars recommended)",
		}
	}
	return checkResult{
		name:   "NEXTAUTH_SECRET",
		status: "PASS",
		detail: fmt.Sprintf("set (%d chars)", len(v)),
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

func init() {
	doctorCmd.Flags().Bool("fix", false, "Attempt safe auto-repairs (e.g. create missing data directory)")
}
