//go:build !clionly

package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/crashreport"
	"github.com/crewship-ai/crewship/internal/database"
	"github.com/spf13/cobra"
)

// telemetryCmd manages the crash-reporting consent state stored in the
// app_settings table. The operator can flip it at any time; the running
// server picks the new value up on its next start.
//
// `crewship telemetry on`  — opt in to crash reports
// `crewship telemetry off` — opt out
// `crewship telemetry status` — show current state + install ID
var telemetryCmd = &cobra.Command{
	Use:   "telemetry",
	Short: "Manage anonymous crash reporting",
	Long: `Crewship sends anonymous crash reports to the project maintainer's Sentry
to help diagnose bugs. Manage the consent state any time with:

  crewship telemetry off
  crewship telemetry on        # re-enable
  crewship telemetry status    # show current state, endpoint, install ID

The default depends on the build (decided by version, see
internal/crashreport.DefaultOptIn):

  - prerelease builds (-beta / -rc) and dev builds: ENABLED by default,
    so the maintainer has crash signal while a release is still baking
  - stable release versions: DISABLED by default — strictly opt-in

Your explicit choice (this command, or the consent step in onboarding)
is sticky and always wins over the default. Documented in README and
docs/guides/telemetry.

Routing override:
  Set CREWSHIP_SENTRY_DSN to your own Sentry DSN to redirect events to a
  project you control instead of the maintainer's. Useful for enterprise
  self-hosters and regulated environments. Empty/unset = vendor default.

What is sent (when enabled):
  - Go stack traces and error messages
  - Crewship version, commit, OS/architecture
  - An anonymous install ID generated locally
  - Sentry "environment" derived from the version tag (beta / production)

What is NEVER sent:
  - Workspace, user, or credential data
  - HTTP request bodies
  - Authorization headers, cookies, or query-string secrets
  - Environment variables
  - Hostname (ServerName is overridden with the install ID)`,
}

var telemetryOnCmd = &cobra.Command{
	Use:   "on",
	Short: "Opt in to anonymous crash reporting",
	RunE: func(cmd *cobra.Command, args []string) error {
		return setTelemetry(cmd.Context(), true)
	},
}

var telemetryOffCmd = &cobra.Command{
	Use:   "off",
	Short: "Opt out of anonymous crash reporting",
	RunE: func(cmd *cobra.Command, args []string) error {
		return setTelemetry(cmd.Context(), false)
	},
}

var telemetryStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show current telemetry consent state",
	RunE: func(cmd *cobra.Command, args []string) error {
		// `status` reports; it does not provision. It takes the read-only
		// route for the same reason `doctor` does (B-02): a query about
		// consent that materialises a migrated database as a side effect is
		// answering a different question than the one asked. `on` and `off`
		// still create-and-init through openLocalDB — they have consent to
		// write, and somewhere to write it is the point.
		var enabled, asked bool
		var installID string
		db, err := openLocalDBReadOnly(cmd.Context())
		switch {
		case errors.Is(err, errNoLocalDB):
			// No database means no app_settings row, which is exactly the
			// "not yet configured" state printed below — zero values already
			// say so, so fall through with them.
		case err != nil:
			return err
		default:
			defer db.Close()
			enabled, asked, installID, err = crashreport.Status(cmd.Context(), db)
			if err != nil {
				return fmt.Errorf("read telemetry status: %w", err)
			}
		}
		// Resolved DSN tells the operator WHERE events would route — important
		// when CREWSHIP_SENTRY_DSN is set and we're not using the vendor
		// default. We never print the raw URL (it would still leak the public
		// key into terminal scrollback) — just the host portion.
		dsn := crashreport.ResolveDSN()
		dsnSource := "vendor default (compiled in)"
		if os.Getenv("CREWSHIP_SENTRY_DSN") != "" {
			dsnSource = "CREWSHIP_SENTRY_DSN env override"
		}

		switch {
		case !asked:
			// Prerelease/dev builds settle the default on first
			// `crewship start`, so this branch is mostly seen on stable
			// builds (default-off writes nothing) and on DBs that have
			// never booted the server.
			if crashreport.DefaultOptIn(version) {
				fmt.Println("Telemetry: not yet configured. This prerelease/dev build defaults to ENABLED on the next `crewship start` — opt out now with `crewship telemetry off`.")
			} else {
				fmt.Println("Telemetry: not yet configured. This stable build keeps telemetry DISABLED until you opt in with `crewship telemetry on`.")
			}
		case enabled:
			fmt.Println("Telemetry: ENABLED")
			if installID != "" {
				fmt.Printf("  install_id: %s\n", installID)
			}
			if dsn == "" {
				cli.PrintWarning("No DSN compiled in and CREWSHIP_SENTRY_DSN is not set — consent is recorded but no events are sent.")
			} else {
				fmt.Printf("  endpoint:   %s (%s)\n", dsnEndpointHost(dsn), dsnSource)
			}
			fmt.Println("  to disable: crewship telemetry off")
		default:
			fmt.Println("Telemetry: DISABLED")
			fmt.Println("  to enable:  crewship telemetry on")
		}
		return nil
	},
}

// dsnEndpointHost extracts the host portion of a Sentry DSN
// (https://<key>@<host>/<project_id>) so we can show the operator where
// telemetry routes without printing the full URL into terminal scrollback.
// Mirrors internal/crashreport.dsnEndpoint but kept local to avoid
// exporting a near-trivial helper.
func dsnEndpointHost(dsn string) string {
	at := strings.Index(dsn, "@")
	if at < 0 {
		return "unknown"
	}
	rest := dsn[at+1:]
	if slash := strings.Index(rest, "/"); slash >= 0 {
		return rest[:slash]
	}
	return rest
}

func init() {
	telemetryCmd.AddCommand(telemetryOnCmd)
	telemetryCmd.AddCommand(telemetryOffCmd)
	telemetryCmd.AddCommand(telemetryStatusCmd)
}

// setTelemetry is shared by `on` and `off`. It opens the local DB the same
// way `crewship start` does so the consent state lives next to the data,
// not in a separate config file the user has to keep in sync. ctx comes
// from cmd.Context() so Ctrl-C / SIGTERM during the brief migrate+UPSERT
// window actually aborts — pre-fix the helpers used context.Background()
// and would keep running past cancellation. CodeRabbit caught this.
func setTelemetry(ctx context.Context, enabled bool) error {
	db, err := openLocalDB(ctx)
	if err != nil {
		return err
	}
	defer db.Close()

	on, installID, err := crashreport.SetOptIn(ctx, db.DB, enabled)
	if err != nil {
		return fmt.Errorf("write telemetry setting: %w", err)
	}
	if on {
		cli.PrintSuccess("Telemetry enabled. Crash reports will be sent on the next server start.")
		if installID != "" {
			fmt.Printf("  install_id: %s\n", installID)
		}
		dsn := crashreport.ResolveDSN()
		if dsn == "" {
			cli.PrintWarning("No DSN compiled in and CREWSHIP_SENTRY_DSN is not set — consent recorded but no events will be sent until you install a release binary or set CREWSHIP_SENTRY_DSN.")
		} else {
			fmt.Printf("  endpoint:   %s\n", dsnEndpointHost(dsn))
		}
		return nil
	}
	cli.PrintSuccess("Telemetry disabled. No crash reports will be sent.")
	return nil
}

// openLocalDB opens the database at the same path `crewship start` uses
// when no --db override is provided, and brings the schema up to date.
//
// Calling Migrate here matters for the CI-provisioning flow:
//
//	crewship telemetry on   # set consent before bringing the service up
//	crewship start
//
// Without the Migrate call the first sub-command crashes with
// "no such table: app_settings" because the v88 migration has never run.
// On an already-migrated DB Migrate is a fast no-op (one COUNT per
// migration row), so the extra cost on the warm path is negligible.
func openLocalDB(ctx context.Context) (*database.DB, error) {
	dataDir, err := database.DefaultDataDir()
	if err != nil {
		return nil, fmt.Errorf("resolve data directory: %w", err)
	}
	db, err := database.Open(dataDir.DatabaseURL())
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	// Initialize a fresh database, but REFUSE to upgrade an existing one.
	//
	// The original code called Migrate unconditionally with a discarded
	// logger. On a warm database that is the harmless no-op the comment
	// describes. On a COLD one — an operator who swapped in a new binary and
	// ran `crewship doctor` before `crewship start`, which is the natural
	// order — it silently applied every pending migration while missing both
	// halves of what makes an upgrade safe:
	//
	//   - No SnapshotBeforeMigrate. `crewship start` takes a pre-migrate
	//     backup that `restore-snapshot` depends on; this path took none, so
	//     the upgrade had no rollback point and the operator was never told.
	//   - No secrets.LoadOrGenerate. That bootstrap is called only from
	//     cmd_start.go, so ENCRYPTION_KEY is unset here. v140 then takes its
	//     fail-open branch and leaves webhook secrets in plaintext; worse,
	//     v152 backfills the journal hash-chain via journal.ChainKeyFromEnv(),
	//     which does not error on a missing key — it derives from "". The
	//     whole historical chain gets committed under a null-seed key, and
	//     once the server later starts with the real one, every pre-migration
	//     entry fails verification permanently. The _migrations row blocks a
	//     re-run, so repair needs a new migration.
	//
	// Both hazards came with a discarded logger, so nothing was printed.
	//
	// A diagnostic command has no business performing a schema upgrade. It
	// still initializes an empty database, because that is the bootstrap the
	// original comment was actually about ("no such table: app_settings").
	from, _, pending, err := database.PendingMigrations(ctx, db.DB)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("check pending migrations: %w", err)
	}
	if pending > 0 && from > 0 {
		db.Close()
		return nil, fmt.Errorf(
			"database schema is out of date and this command will not upgrade it: %d migration(s) pending. "+
				"Run `crewship start` once — it snapshots the database before migrating and loads the encryption key "+
				"the data migrations need — then re-run this command",
			pending)
	}
	// Fresh install (from == 0): bring the schema up so the sub-command has
	// tables to read. There is no data to lose and no chain to poison.
	//
	// silentLogger: the sub-command's user surface is the success/failure
	// message we print ourselves; the per-migration INFO lines from
	// Migrate would just be noise on a no-op call.
	silent := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := database.Migrate(ctx, db.DB, silent); err != nil {
		db.Close()
		return nil, fmt.Errorf("apply migrations: %w", err)
	}
	return db, nil
}

// errNoLocalDB marks "the data directory has no database yet" so callers can
// tell that apart from a real open failure with errors.Is. The distinction is
// the whole point of the read-only route: a missing database is a normal state
// with a known answer (nothing has been configured yet), while a failed open is
// something the operator has to look at.
var errNoLocalDB = errors.New("database not found")

// openLocalDBReadOnly is the diagnostic counterpart to openLocalDB: it reads an
// existing database and REFUSES to bring one into existence.
//
// openLocalDB creates and, on a fresh install, migrates — deliberately, because
// `crewship telemetry on` has to have somewhere to write consent before the
// first `crewship start`. Handing that same helper to `crewship doctor` was
// bug B-02: doctor against an empty data dir printed
//
//	[WARN] db migration version   database file does not exist (crewshipd has never run)
//
// from checkDBMigrationVersion (which only stats) and then created a fully
// migrated 3 MB crewship.db from the telemetry probes three rows further down.
// Two costs, in ascending order of seriousness:
//
//   - The output contradicted itself, and `doctor` — the command operators run
//     precisely because they are unsure what state the box is in — mutated that
//     state as a side effect of reporting on it.
//   - The database `crewship start` then found was one it had not provisioned:
//     schema applied outside SnapshotBeforeMigrate and without
//     secrets.LoadOrGenerate, which is the same hazard the refusal above exists
//     to prevent, just reached from the empty side.
//
// Two things make this route safe where openLocalDB is not:
//
//   - We stat before opening. sql.Open is lazy and the driver always passes
//     SQLITE_OPEN_CREATE, so "open and see what happens" would create the file
//     before any query proved it was missing.
//   - mode=ro on the URI, which downgrades the connection past the driver's
//     hardcoded CREATE|READWRITE flags. It is only honoured because
//     DatabaseURL() yields a "file:" URI — SQLite parses query parameters only
//     in URI form, and modernc.org/sqlite strips the query off a bare path
//     before handing it to sqlite3_open_v2. A read-only connection also can't
//     be talked into a schema upgrade later by accident.
//
// busy_timeout matches what a reader wants against a live crewshipd: WAL lets
// us read alongside the writer, and the few seconds cover a checkpoint rather
// than surfacing SQLITE_BUSY as "doctor can't read your database".
func openLocalDBReadOnly(ctx context.Context) (*sql.DB, error) {
	// ResolveDefaultDataDir, not DefaultDataDir: the latter creates the root
	// and output/chats/logs/skills on the way to telling us where they are, so
	// this helper's refusal to create a database was undermined one call up —
	// doctor still materialised the tree on a box that had never run crewshipd.
	dataDir, err := database.ResolveDefaultDataDir()
	if err != nil {
		return nil, fmt.Errorf("resolve data directory: %w", err)
	}
	dbPath := dataDir.DatabasePath()
	if _, err := os.Stat(dbPath); err != nil {
		// Only ENOENT means "not initialised" — mirroring openAdminDB, a
		// permission error or symlink loop must surface verbatim so the
		// operator fixes access rights instead of chasing `crewship start`.
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w at %s", errNoLocalDB, dbPath)
		}
		return nil, fmt.Errorf("stat database path %s: %w", dbPath, err)
	}
	db, err := sql.Open("sqlite", dataDir.DatabaseURL()+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("open database read-only: %w", err)
	}
	// sql.Open does not connect. Ping here so a corrupt file or a lock we
	// cannot get is reported by the probe that owns the database row, rather
	// than leaking out later as a confusing "read consent" error.
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("open database read-only: %w", err)
	}
	return db, nil
}
