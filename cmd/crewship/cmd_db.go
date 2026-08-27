//go:build !clionly

package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/crewship-ai/crewship/internal/database"
	"github.com/spf13/cobra"
	sqlite "modernc.org/sqlite"
)

var (
	restoreSnapshotList  bool
	restoreSnapshotYes   bool
	restoreSnapshotForce bool
)

// dbConfirm is confirmInteractive, indirected so the tests can occupy the
// window the prompt opens. That window is the whole point of the re-probe
// below: the prompt blocks for as long as the operator takes to read it, and
// what a test needs to do is take the database *during* it. Nothing in
// production reassigns this.
var dbConfirm = confirmInteractive

// dbWriteGuard answers "may this command rewrite the database at path right
// now?" for the two commands that do it in place.
//
// Three outcomes, deliberately not collapsed into two:
//
//   - free — proceed.
//   - definitely in use — hard refusal, which --force does NOT lift. A live
//     crewshipd holding the file is a knowable fact, and writing under it is
//     the torn-database outcome the guard exists for.
//   - indeterminate, i.e. the probe itself failed (corrupt header, unreadable
//     file) — refusal by default, liftable with --force. Refusing outright
//     locked the operator out of the recovery path: a corrupt database cannot
//     answer the probe, and a corrupt database is exactly what
//     restore-snapshot is for. With "everything goes through the CLI, never a
//     DB shell" there was then nothing legitimate left to do. Fail-open
//     ("could not probe, assume free") is not the alternative — that is the
//     original defect this guard replaced — so the override is explicit,
//     per-invocation and loud.
type dbWriteGuard struct {
	path  string // the database file about to be rewritten
	verb  string // present participle for the refusals: "restoring", "repairing"
	risk  string // what a live server suffers if we write anyway
	force bool
}

// check probes the database and reports why the command must not continue.
//
// announce covers the --force warning only. The guard runs twice per command —
// once before the confirmation prompt, once immediately before the write — and
// printing the same paragraph twice would read like two separate problems.
func (g dbWriteGuard) check(announce bool) error {
	inUse, err := databaseInUse(g.path)
	switch {
	case err != nil && !g.force:
		// Fail closed, but honestly: "we could not open the file" is not "a
		// server is running", and sending the operator to hunt for a process
		// that does not exist is how the old message wasted time. Name the
		// probe's own error, and name the one flag that gets past it.
		return fmt.Errorf("cannot determine whether %s is in use, so not %s: %w — "+
			"a corrupt or unreadable database cannot answer this probe, which is exactly the state this command exists for; "+
			"if you have checked that no crewshipd is running, re-run with --force",
			g.path, g.verb, err)

	case err != nil:
		// --force, and the probe could not answer. Proceed, but leave a
		// record: an override nobody can see in the transcript afterwards is
		// indistinguishable from the silent fail-open this guard replaced.
		if announce {
			fmt.Fprintf(os.Stderr,
				"WARNING: --force: could not determine whether %s is in use: %v\n"+
					"WARNING: %s it anyway, on your word that no crewshipd holds it open.\n"+
					"WARNING: If one does, %s.\n",
				g.path, err, g.verb, g.risk)
		}
		return nil

	case inUse:
		// Not overridable. --force exists for the question the probe could not
		// answer; this one it answered, and the answer is the failure mode the
		// guard was written for.
		return fmt.Errorf("%s is open by another process (most likely a running crewshipd) — stop it before %s; %s "+
			"(--force does not override this: it lifts an indeterminate probe, not a database that is definitely in use)",
			g.path, g.verb, g.risk)
	}
	return nil
}

// warnDBLocalOnly is gone, and so is the isLoopbackServerURL exemption that
// made it quiet. It printed a note only when the CLI's server was a REMOTE
// host, on the reasoning that a server on localhost must be using the local
// data directory. That inference is false wherever crewshipd runs with its own
// DATABASE_URL — every dev clone (file:./crewship.db), every container, every
// multi-instance box — and it is why a whole family of commands answered from
// the wrong database for months (#2086). Its replacement is requireLocalDB in
// local_db_target.go, which names the file every time and refuses, loopback or
// not, when the operator has named a server without saying --local.

var dbCmd = &cobra.Command{
	Use:   "db",
	Short: "Maintenance on the database FILE on this host (snapshots, restore, ledger)",
	Long: `Host-side maintenance for the Crewship SQLite file on this machine —
DATABASE_URL when set, otherwise the data directory.

Nothing here goes over HTTP, so nothing here can act on a remote
instance. Because of that, these commands refuse to run when
--server / CREWSHIP_SERVER / --profile names a server, unless you
pass --local to say you mean the file on this host. A server on
localhost is not an exemption: it may well be running against a
different database than the one in your data directory.`,
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
the restore is itself reversible.

Before overwriting, and again immediately before the swap, the command checks
whether anything still has the database open, by locking the file itself. If
the file is too damaged to answer that check — which is one of the reasons you
would be restoring a snapshot in the first place — it stops and tells you what
the check hit. "--force" continues past THAT, and only that: it is your
statement that no crewshipd is running. It does not override a database that
is definitely in use; that answer is knowable, and restoring under a live
server tears the file.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		// `db` is host-side maintenance on the LOCAL SQLite file, and this one
		// overwrites it. When the CLI is pointed at a server, that is a
		// contradiction the command cannot resolve on the operator's behalf —
		// restoring a snapshot over the wrong database is not undone by a
		// warning. (Contrast: `nuke` acts through the remote API and is the
		// model for remote-destructive commands.)
		localDB, err := requireLocalDB(cmd, "crewship db restore-snapshot", "")
		if err != nil {
			return err
		}
		dbPath := localDB.Path

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
		//
		// This first check is here, before the snapshot is chosen and the
		// confirmation asked, so an operator who left crewshipd running is
		// told immediately rather than after answering a question that could
		// never have been honoured. It is NOT the check that makes the swap
		// safe — see the re-probe below.
		guard := dbWriteGuard{
			path:  dbPath,
			verb:  "restoring",
			risk:  "a live server holds the database open and would see a torn file",
			force: restoreSnapshotForce,
		}
		if err := guard.check(true); err != nil {
			return err
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
		if !restoreSnapshotYes && !dbConfirm("Proceed?") {
			return fmt.Errorf("aborted (pass --yes to skip confirmation)")
		}

		// Ask again, immediately before the swap. The prompt above is unbounded
		// — it blocks for as long as the operator takes to read it — and that
		// is ample time for systemd's Restart=always to bring crewshipd back,
		// or for a colleague to start it. RestoreSnapshot then renames a file
		// into place under a process holding the old one open, which is
		// precisely the torn database this guard exists to prevent; the first
		// probe answered about a moment that has already passed. Unlike
		// repair-ledger, whose write is one transaction that SQLite serialises
		// against other connections, a rename has nothing underneath it to
		// catch this, so the re-probe is the only backstop there is.
		if err := guard.check(false); err != nil {
			return err
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
	// Persistent here and nowhere else: every `db` subcommand — restore-snapshot,
	// migration-status, repair-ledger — is local-only by construction, so an
	// inherited flag is advertised on exactly the commands that honour it.
	// `admin` cannot do this; it mixes local-only and HTTP-only verbs.
	// localdb_flag_guard_test.go holds both halves of that rule.
	dbCmd.PersistentFlags().Bool("local", false, localOnlyFlagHelp)

	restoreSnapshotCmd.Flags().BoolVar(&restoreSnapshotList, "list", false, "list available snapshots and exit")
	restoreSnapshotCmd.Flags().BoolVar(&restoreSnapshotYes, "yes", false, "skip the confirmation prompt")
	restoreSnapshotCmd.Flags().BoolVar(&restoreSnapshotForce, "force", false,
		"restore even though the in-use check could not answer (corrupt or unreadable database); never overrides a database that IS in use")
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
// It replaces a health-endpoint probe (localServerRunning, now deleted along
// with its last caller) that had no connection to dbPath whatsoever and was
// wrong in both directions: it
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
//   - Any other connection holding the WAL index (the -shm file) open, idle
//     or not. That is the live-crewshipd case: such a connection holds a
//     shared lock on the dead-man-switch byte until it closes, and
//     locking_mode=EXCLUSIVE needs that byte exclusively, so the probe gets
//     SQLITE_BUSY. Plain `BEGIN EXCLUSIVE` without the pragma is NOT enough
//     here — measured: against a held-open idle WAL connection it succeeds in
//     ~250µs, because in WAL mode it only contends with an in-flight writer.
//     That distinction is the whole reason for the pragma.
//   - Another writer mid-transaction, in any journal mode.
//
// What it does NOT detect, both measured rather than reasoned:
//
//   - An idle connection to a database in rollback-journal mode. It holds no
//     OS-level lock between transactions and is invisible to any lock-based
//     check. Crewship's own Open always sets WAL, so every crewshipd is
//     covered; a hand-rolled non-WAL connection from another tool is the gap.
//   - A connection that opened the file and has not executed a single
//     statement yet, in the specific case where opening it converted the
//     file from rollback-journal to WAL: the WAL index is not materialised
//     until the first read or write, so there is briefly nothing to collide
//     with. One statement — which a booting crewshipd runs long before an
//     operator reaches these commands — closes that window for good.
//
// In both gaps the "stop the server first" instruction in the command help is
// still doing real work; this probe narrows what the operator has to get
// right, it does not eliminate it.
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
