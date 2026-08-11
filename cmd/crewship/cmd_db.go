//go:build !clionly

package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/database"
	"github.com/spf13/cobra"
	sqlite "modernc.org/sqlite"
)

var (
	restoreSnapshotList bool
	restoreSnapshotYes  bool
)

// warnDBLocalOnly prints a stderr notice when the CLI's effective server
// target is a non-local host: `crewship db` never touches that remote.
func warnDBLocalOnly(dbPath string) {
	srv := cli.EffectiveServer(flagServer, flagProfile, cliCfg)
	if srv == "" || isLoopbackServerURL(srv) {
		return
	}
	fmt.Fprintf(os.Stderr,
		"note: your CLI targets %s, but 'crewship db' only touches the LOCAL database at %s\n",
		srv, dbPath)
}

// isLoopbackServerURL reports whether the server URL points at this host.
func isLoopbackServerURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host := u.Hostname()
	if host == "localhost" {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

var dbCmd = &cobra.Command{
	Use:   "db",
	Short: "Local database maintenance (snapshots, restore)",
	Long:  "Host-side maintenance for the local Crewship SQLite database in the data directory.",
}

var restoreSnapshotCmd = &cobra.Command{
	Use:   "restore-snapshot [snapshot]",
	Short: "Restore the database from a pre-migration snapshot",
	Long: `Restore the local database from a pre-migration snapshot.

Crewship writes a snapshot ("<db>.pre-migrate-*.bak") automatically before
applying pending migrations. Restoring one is the database half of a
downgrade: pair it with reinstalling the older binary (see the upgrades
guide). Forward-only migrations mean a newer schema won't boot under an older
binary until the snapshot is restored.

  crewship db restore-snapshot --list        # show available snapshots
  crewship db restore-snapshot               # restore the most recent one
  crewship db restore-snapshot <file>.bak    # restore a specific snapshot

Stop crewshipd before restoring — a running server holds the database open.
The current database is copied aside to "<db>.before-restore-<ts>" first, so
the restore is itself reversible.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		dd, err := database.DefaultDataDir()
		if err != nil {
			return fmt.Errorf("resolve data dir: %w", err)
		}
		dbPath := dd.DatabasePath()

		// `db` is host-side maintenance on the LOCAL SQLite file. When the
		// CLI is pointed at a remote server (profile / CREWSHIP_SERVER /
		// --server), say so explicitly — silently doing local-disk work
		// while the operator believes they are targeting a remote instance
		// is worse than a noisy line. (Contrast: `nuke` acts through the
		// remote API and is the model for remote-destructive commands.)
		warnDBLocalOnly(dbPath)

		snaps, err := database.ListSnapshots(dbPath)
		if err != nil {
			return fmt.Errorf("list snapshots: %w", err)
		}

		if restoreSnapshotList {
			if len(snaps) == 0 {
				fmt.Printf("No pre-migration snapshots found next to %s\n", dbPath)
				return nil
			}
			fmt.Printf("Pre-migration snapshots for %s (newest first):\n\n", dbPath)
			for _, s := range snaps {
				fmt.Printf("  %s\n    v%d → v%d   %s   %.1f MB\n",
					s.Name, s.FromVersion, s.ToVersion,
					s.TakenAt.Format(time.RFC3339), float64(s.Size)/(1<<20))
			}
			return nil
		}

		// Refuse if the file we are about to overwrite is open right now. The
		// question that matters is "is anyone using THIS database", and the
		// only thing that can answer it is the database file itself — see
		// databaseInUse for why the old health-endpoint probe could not.
		//
		// A probe failure is not a pass: if we cannot even open the file we
		// have no idea whether a server holds it, and guessing "free" is how
		// you tear a live DB. Fail closed, but say what actually happened
		// instead of claiming a server is running.
		if inUse, err := databaseInUse(dbPath); err != nil {
			return fmt.Errorf("cannot determine whether %s is in use, so not restoring: %w", dbPath, err)
		} else if inUse {
			return fmt.Errorf("%s is open by another process (most likely a running crewshipd) — stop it before restoring; a live server holds the database open and would see a torn file", dbPath)
		}

		// Pick the snapshot: explicit arg, else the most recent.
		var target string
		switch {
		case len(args) == 1:
			target = args[0]
			if filepath.Base(target) == target {
				// Bare name → resolve beside the DB.
				target = filepath.Join(filepath.Dir(dbPath), target)
			}
		case len(snaps) == 0:
			return fmt.Errorf("no pre-migration snapshots found next to %s (nothing to restore)", dbPath)
		default:
			target = snaps[0].Path
		}

		fmt.Printf("Restore %s\n     ← %s\n", dbPath, target)
		fmt.Println("The current database will be copied aside to a .before-restore-* file first.")
		if !restoreSnapshotYes && !confirmInteractive("Proceed?") {
			return fmt.Errorf("aborted (pass --yes to skip confirmation)")
		}

		if err := database.RestoreSnapshot(dbPath, target); err != nil {
			// RestoreSnapshot does all fallible prep before the atomic swap,
			// so the live DB file is left in place on error — but a
			// .before-restore-* copy may already have been written, so point
			// there rather than promising nothing changed.
			return fmt.Errorf("restore failed — database file left in place; a .before-restore-* copy may exist beside it: %w", err)
		}
		fmt.Printf("Restored %s from %s\n", dbPath, filepath.Base(target))
		fmt.Println("Start the matching (older) crewship binary now; it will boot against the restored schema.")
		return nil
	},
}

func init() {
	restoreSnapshotCmd.Flags().BoolVar(&restoreSnapshotList, "list", false, "list available snapshots and exit")
	restoreSnapshotCmd.Flags().BoolVar(&restoreSnapshotYes, "yes", false, "skip the confirmation prompt")
	dbCmd.AddCommand(restoreSnapshotCmd)
	rootCmd.AddCommand(dbCmd)
}

// SQLite primary result codes we care about. Spelled out rather than
// imported from modernc.org/sqlite/lib: two integers are not worth pulling
// the generated C-translation package into the CLI's import graph.
const (
	sqliteBusy   = 5 // SQLITE_BUSY   — another connection holds a conflicting lock
	sqliteLocked = 6 // SQLITE_LOCKED — same, within the same process/shared cache
)

// dbProbeBusyTimeout bounds how long the lock probe waits for a conflicting
// lock to clear. Set explicitly and kept short on purpose: inheriting a
// default would be a lottery (internal/database.Open uses 30s, sized for a
// server riding out a background purge), and this is an interactive CLI —
// "is the DB busy right now" must answer in a blink, not stall the operator
// for half a minute before printing an error either way.
const dbProbeBusyTimeout = 250 * time.Millisecond

// dbProbeTimeout is the hard ceiling on the whole probe, so a pathological
// filesystem (stuck NFS, unresponsive fuse mount) cannot hang the CLI even
// if the busy timeout above never comes into play.
const dbProbeTimeout = 5 * time.Second

// databaseInUse reports whether another process currently has the SQLite
// database at dbPath open — the actual question `restore-snapshot` needs
// answered before it overwrites that file.
//
// It replaces a health-endpoint probe (localServerRunning, below) that had no
// connection to dbPath whatsoever and was wrong in both directions: it
// refused to restore a sandbox database because an unrelated production
// instance answered on :8080, and — the dangerous half — it happily restored
// a database that a crewshipd on some other port was holding open, tearing
// the file under a live server. Widening /api/health to publish its database
// path is not the fix: that endpoint is unauthenticated, and the on-disk
// location of the credential store is not something to hand out pre-auth.
//
// The probe: open the file with locking_mode=EXCLUSIVE and try to start a
// write transaction on the short busy timeout above. If it succeeds we roll
// back and close immediately — the lock is a question, not a reservation; it
// is deliberately NOT held across the restore, since RestoreSnapshot works by
// renaming a staged file into place and wants no handle of ours on the DB.
//
// What this detects, precisely:
//
//   - Any other connection that has the database open in WAL mode, even a
//     completely idle one. That is the live-crewshipd case: a WAL connection
//     holds a shared lock on the dead-man-switch byte for its whole lifetime,
//     and locking_mode=EXCLUSIVE needs that byte exclusively, so the probe
//     gets SQLITE_BUSY. Plain `BEGIN EXCLUSIVE` without the pragma is NOT
//     enough here — measured: against a held-open idle WAL connection it
//     succeeds in ~250µs, because in WAL mode it only contends with an
//     in-flight writer. That distinction is the whole reason for the pragma.
//   - Another writer mid-transaction, in any journal mode.
//
// What it does NOT detect: an idle connection to a database in rollback-journal
// mode, which holds no OS-level lock between transactions and is therefore
// invisible to any lock-based check. Crewship's own Open always sets WAL, so
// every crewshipd is covered; a hand-rolled non-WAL connection from some other
// tool is the residual gap, and there the "stop the server first" instruction
// in the command help is still the operator's job.
//
// Closing the probe connection on a WAL database checkpoints and removes the
// -wal/-shm sidecars. That is harmless-to-helpful here: it folds committed
// frames into the main file before RestoreSnapshot copies it aside as
// .before-restore-*, so that rollback copy is complete rather than a stale
// pre-WAL image.
func databaseInUse(dbPath string) (bool, error) {
	// A file that does not exist cannot be held open, and sql.Open would
	// CREATE it — turning a read-only question into a side effect, and
	// leaving an empty DB behind on a fresh box. Answer from the stat.
	if _, err := os.Stat(dbPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		// Permissions, a directory in the way, a broken mount: a real
		// error, and emphatically not "a server is running".
		return false, fmt.Errorf("stat database file: %w", err)
	}

	// Same naive path+"?"+pragmas concatenation internal/database.Open uses,
	// deliberately: if a data dir path containing "?" ever broke this DSN it
	// would already have broken every server connection, so the two must at
	// least agree on which file they mean.
	dsn := dbPath +
		"?_pragma=busy_timeout(" + strconv.FormatInt(dbProbeBusyTimeout.Milliseconds(), 10) + ")" +
		"&_pragma=locking_mode(EXCLUSIVE)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return false, fmt.Errorf("open database file: %w", err)
	}
	// In EXCLUSIVE locking mode SQLite keeps the file lock until the
	// connection is CLOSED — ROLLBACK alone does not give it back. So this
	// Close is what releases the database for the restore that follows, and
	// it must happen before we return, not at the end of the command.
	defer func() { _ = db.Close() }()
	// One connection: the probe is a single statement, and a pool would let
	// database/sql open a second handle that contends with our own lock.
	db.SetMaxOpenConns(1)

	ctx, cancel := context.WithTimeout(context.Background(), dbProbeTimeout)
	defer cancel()

	// sql.Open is lazy; Conn is where the file is actually opened and the
	// pragmas above run. A busy result can surface here as well as on the
	// BEGIN, so both sites classify it the same way.
	conn, err := db.Conn(ctx)
	if err != nil {
		if isSQLiteBusy(err) {
			return true, nil
		}
		return false, fmt.Errorf("connect to database file: %w", err)
	}
	defer func() { _ = conn.Close() }()

	if _, err := conn.ExecContext(ctx, "BEGIN EXCLUSIVE"); err != nil {
		if isSQLiteBusy(err) {
			return true, nil
		}
		// Corrupt file, not-a-database, disk I/O error: report it as what
		// it is. Reporting these as "a server is running" would send the
		// operator hunting for a process that does not exist.
		return false, fmt.Errorf("acquire exclusive lock: %w", err)
	}
	// Nothing else holds the database. Release at once — see above, the lock
	// was only ever the question.
	if _, err := conn.ExecContext(ctx, "ROLLBACK"); err != nil {
		return false, fmt.Errorf("release probe lock on %s: %w", dbPath, err)
	}
	return false, nil
}

// isSQLiteBusy reports whether err is SQLite refusing on lock contention, as
// opposed to any other failure. modernc.org/sqlite returns *sqlite.Error with
// the raw result code; extended codes pack the primary code in the low byte
// (e.g. SQLITE_BUSY_SNAPSHOT = 5 | 2<<8), so mask before comparing or a
// perfectly ordinary busy result reads as an unknown error.
func isSQLiteBusy(err error) bool {
	var serr *sqlite.Error
	if !errors.As(err, &serr) {
		return false
	}
	switch serr.Code() & 0xff {
	case sqliteBusy, sqliteLocked:
		return true
	}
	return false
}

// localServerRunning best-effort detects a local crewshipd by probing the
// health endpoint on the configured/default port ($CREWSHIP_PORT, else 8080).
//
// Know what it is before gating anything destructive on it: it answers "is
// something answering HTTP on one port", and nothing more. It has no idea
// which database the responder uses — so it fires for an unrelated instance
// that shares no state with this data dir, and stays silent for a crewshipd on
// any other port. restore-snapshot used to gate on it and got burned both
// ways; it now locks the target file itself (databaseInUse, above).
//
// The remaining caller is `db repair-ledger`, where it is a nudge rather than
// a guard: that command rewrites the ledger inside a single transaction on a
// connection it opens itself, so the actual serialization comes from SQLite,
// not from this function.
func localServerRunning() (bool, string) {
	port := os.Getenv("CREWSHIP_PORT")
	if port == "" {
		port = "8080"
	}
	url := "http://localhost:" + port + "/api/health"
	client := &http.Client{Timeout: 800 * time.Millisecond}
	resp, err := client.Get(url)
	if err != nil {
		return false, ""
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		return true, url
	}
	return false, ""
}
