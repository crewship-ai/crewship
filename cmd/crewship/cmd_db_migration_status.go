//go:build !clionly

package main

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/crewship-ai/crewship/internal/database"
	"github.com/spf13/cobra"
)

// noCrewshipDatabaseAt is the one wording for "this path is not a Crewship
// database", shared by the two ways of finding that out: the file is not there
// at all, and the file is there but holds no migration ledger. They used to
// differ, and the first of them used to be the driver's
// "unable to open database file (14)".
func noCrewshipDatabaseAt(dbPath string) string {
	return fmt.Sprintf(
		"there is no Crewship database at %s — check DATABASE_URL / CREWSHIP_DATA_DIR, "+
			"or run this on the host that owns the database", dbPath)
}

var migrationStatusCmd = &cobra.Command{
	Use:   "migration-status",
	Short: "Show the schema version and any outstanding post-deployment work",
	Long: `Report what this database has applied and what is still outstanding.

Two kinds of pending work look different and matter differently:

  Schema migrations run at boot. If any are pending, the server has not
  started — start it and they apply.

  Post-deployment migrations run in the background AFTER the server starts
  serving, in batches, because their cost grows with row count. While one is
  outstanding the schema change is only partly applied; that is by design and
  the running code tolerates it. If one is stuck, this is where you find out.

Reads the database file on this host directly, so it works with the server
down. That also means it cannot answer for a remote instance: with a server
named, it refuses unless you pass --local.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		// Previously this resolved the data dir and nothing else — it did not
		// even honour DATABASE_URL, so on a clone whose server runs against
		// file:./crewship.db it reported a completely unrelated schema version
		// under the heading "Database:" (#2086). requireLocalDB does both:
		// resolves the file the operator's environment actually names, and
		// refuses when that file is not plausibly the targeted server's.
		target, err := requireLocalDB(cmd, "crewship db migration-status", "")
		if err != nil {
			return err
		}
		dbPath := target.Path
		// Before sql.Open, because the resolver no longer creates the data
		// directory: on a box that has never run crewshipd, opening a path
		// inside a missing directory answers with the driver's
		// "unable to open database file (14)" instead of the message below,
		// and this command is one an operator runs precisely when the box is
		// in that state.
		if err := target.mustExist(noCrewshipDatabaseAt(dbPath)); err != nil {
			return err
		}

		db, err := sql.Open("sqlite", dbPath)
		if err != nil {
			return fmt.Errorf("open %s: %w", dbPath, err)
		}
		defer db.Close()
		ctx := cmdContext(cmd)

		applied, err := database.ReadLedger(ctx, db)
		if errors.Is(err, database.ErrNoLedger) {
			return errors.New(noCrewshipDatabaseAt(dbPath))
		}
		if err != nil {
			return err
		}

		fmt.Printf("Database: %s\n", dbPath)
		if len(applied) == 0 {
			fmt.Println("Schema:   empty (nothing applied yet)")
			return nil
		}
		highest := applied[len(applied)-1]
		fmt.Printf("Schema:   v%d (%s), %d migration(s) applied\n",
			highest.Version, highest.Name, len(applied))

		pending, err := database.PostDeployPending(ctx, db)
		if err != nil {
			return err
		}
		var outstanding []database.PostDeployStatus
		for _, p := range pending {
			if !p.Applied {
				outstanding = append(outstanding, p)
			}
		}
		if len(outstanding) == 0 {
			fmt.Println("Post-deployment: nothing outstanding")
			return nil
		}

		fmt.Printf("\nPost-deployment migrations outstanding (%d):\n", len(outstanding))
		for _, p := range outstanding {
			fmt.Printf("  v%-14d %s\n", p.Version, p.Name)
		}
		fmt.Println("\nThese run in the background while the server serves. If they are not")
		fmt.Println("progressing, check the server log for \"post-deployment migration\" lines.")
		return nil
	},
}

func init() {
	dbCmd.AddCommand(migrationStatusCmd)
}
