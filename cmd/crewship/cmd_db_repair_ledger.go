//go:build !clionly

package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/crewship-ai/crewship/internal/database"
	"github.com/spf13/cobra"
)

var (
	repairLedgerDryRun bool
	repairLedgerYes    bool
)

// cmdContext returns the command's context, or Background when there is
// none. Cobra only attaches a context in Execute, so a RunE invoked directly
// — which is how every command in this package is tested — hands you a nil
// one. Passing that to database/sql panics inside (*DB).conn, and the panic
// path then deadlocks on the connection mutex, so the symptom is a hung test
// rather than a stack trace pointing here.
func cmdContext(cmd *cobra.Command) context.Context {
	if ctx := cmd.Context(); ctx != nil {
		return ctx
	}
	return context.Background()
}

var repairLedgerCmd = &cobra.Command{
	Use:   "repair-ledger",
	Short: "Reconcile the migration ledger with this binary after a renumbering",
	Long: `Fix a database that refuses to start with a "migration version N collision".

That error means the database recorded a migration under one version number
while this binary declares it under another. It happens when a migration is
renumbered after it has already been applied somewhere — typically a machine
that ran a feature branch before the branch merged and the version was moved
to avoid clashing with another PR.

The repair moves the ledger row to the version this binary declares. Nothing
in the schema changes: the migration already ran, only the number recording it
was wrong. Any version freed up is then applied normally on the next start.

It refuses when the ledger names a migration this binary does not have at all.
That is not a renumbering — the database was migrated by a different Crewship —
and the fix there is a newer binary or "crewship db restore-snapshot".

  crewship db repair-ledger --dry-run   # show the plan, change nothing
  crewship db repair-ledger             # apply it, after confirming

Stop crewshipd first: a running server holds the database open.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		dd, err := database.DefaultDataDir()
		if err != nil {
			return fmt.Errorf("resolve data dir: %w", err)
		}
		dbPath := dd.DatabasePath()
		warnDBLocalOnly(dbPath)

		// Read-only inspection is safe against a live server; writing is not.
		// The guard therefore sits between the plan and the apply, so
		// --dry-run stays useful while the server is still up.
		inspect, err := sql.Open("sqlite", dbPath)
		if err != nil {
			return fmt.Errorf("open %s: %w", dbPath, err)
		}
		defer func() { _ = inspect.Close() }()

		ctx := cmdContext(cmd)

		applied, err := database.ReadLedger(ctx, inspect)
		if errors.Is(err, database.ErrNoLedger) {
			// Almost always the wrong directory rather than a corrupt file, so
			// lead with that instead of the driver's "no such table".
			return fmt.Errorf(
				"there is no Crewship database at %s (no migration ledger in it) — "+
					"check CREWSHIP_DATA_DIR, or run this on the host that owns the database",
				dbPath)
		}
		if err != nil {
			return err
		}
		if len(applied) == 0 {
			fmt.Printf("%s has no applied migrations — nothing to repair.\n", dbPath)
			return nil
		}

		plan, err := database.PlanLedgerRepair(applied)
		if err != nil {
			return err
		}
		if plan.Empty() {
			fmt.Printf("The ledger in %s already agrees with this binary (%d migrations applied).\n",
				dbPath, len(applied))
			return nil
		}

		fmt.Printf("Ledger repair for %s\n\n", dbPath)
		for _, r := range plan.Renumbers {
			fmt.Printf("  move  %-40s  v%d → v%d\n", r.Name, r.From, r.To)
		}
		if len(plan.PendingAfter) > 0 {
			fmt.Printf("\nThen applied normally on the next start:\n")
			for _, p := range plan.PendingAfter {
				fmt.Printf("  apply %-40s  v%d\n", p.Name, p.Version)
			}
		}
		fmt.Println("\nNo table data is touched — only the version numbers recording what already ran.")

		if repairLedgerDryRun {
			fmt.Println("\nDry run: nothing was changed.")
			return nil
		}

		// Everything above this line only read. Everything below rewrites the
		// ledger, so this is where "is anyone else using this database" has to
		// be answered — and answered about the FILE. The health-endpoint probe
		// that used to sit here knew nothing about dbPath: it blocked a repair
		// on a sandbox database because an unrelated instance answered on the
		// probed port, and waved one through against a crewshipd running on any
		// other port. Renumbering the ledger under a server that has already
		// booted against the old numbers is the failure that guard existed to
		// prevent, and it is the one it did not prevent.
		//
		// Close our own handle first. Measured, on the fixture in
		// cmd_db_repair_ledger_test.go: `sql.Open` alone leaves inUse false
		// (database/sql is lazy — no connection exists yet), but after
		// ReadLedger has run a query the pooled idle connection keeps the WAL
		// dead-man-switch lock and databaseInUse reports true. ReadLedger
		// always runs by the time we get here, so probing without closing
		// would refuse every repair, every time, in our own name.
		//
		// Nothing carries over from that handle: `plan` is a plain value, and
		// ApplyLedgerRepair opens its own transaction which re-reads every row
		// it is about to move and aborts with ErrLedgerChanged if the ledger
		// drifted. A fresh handle is not a compromise here — that in-transaction
		// re-check is the same consistency story the apply already relied on.
		if err := inspect.Close(); err != nil {
			return fmt.Errorf("close inspection handle on %s: %w", dbPath, err)
		}

		if inUse, err := databaseInUse(dbPath); err != nil {
			// Fail closed, but honestly: "we could not open the file" is not
			// "a server is running", and sending the operator to hunt for a
			// process that does not exist is how the old message wasted time.
			return fmt.Errorf("cannot determine whether %s is in use, so not repairing: %w", dbPath, err)
		} else if inUse {
			return fmt.Errorf("%s is open by another process (most likely a running crewshipd) — stop it before repairing; a server that has already booted holds the old version numbers in memory", dbPath)
		}

		if !repairLedgerYes && !confirmInteractive("Apply this repair?") {
			return fmt.Errorf("aborted (pass --yes to skip confirmation)")
		}

		// Reopen for the write. The probe deliberately does not hold its lock
		// (see databaseInUse), so there is nothing here to inherit — and the
		// prompt above is an unbounded window in which the operator could start
		// the server anyway, which no lock held across it could close either.
		// ApplyLedgerRepair's in-transaction re-check is what actually covers
		// that window.
		apply, err := sql.Open("sqlite", dbPath)
		if err != nil {
			return fmt.Errorf("reopen %s for the repair: %w", dbPath, err)
		}
		defer func() { _ = apply.Close() }()

		if err := database.ApplyLedgerRepair(ctx, apply, plan); err != nil {
			return fmt.Errorf("repair failed — the ledger is unchanged (the whole repair runs in one transaction): %w", err)
		}

		fmt.Printf("\nLedger repaired. Start crewshipd; it will apply %d pending migration(s) and boot.\n",
			len(plan.PendingAfter))
		return nil
	},
}

func init() {
	repairLedgerCmd.Flags().BoolVar(&repairLedgerDryRun, "dry-run", false, "show the plan without changing anything")
	repairLedgerCmd.Flags().BoolVar(&repairLedgerYes, "yes", false, "skip the confirmation prompt")
	dbCmd.AddCommand(repairLedgerCmd)
}
