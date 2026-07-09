//go:build !clionly

package main

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/crewship-ai/crewship/internal/database"
	"github.com/spf13/cobra"
)

var (
	restoreSnapshotList bool
	restoreSnapshotYes  bool
)

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

		// Refuse if a local server appears to be running against this DB — a
		// live crewshipd holds the database open and would see a torn file.
		if running, where := localServerRunning(); running {
			return fmt.Errorf("a Crewship server appears to be running (%s) — stop it before restoring (the DB is held open)", where)
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

// localServerRunning best-effort detects a local crewshipd by probing the
// health endpoint on the configured/default port. It is a courtesy guard, not
// a lock — RestoreSnapshot also stashes the current DB aside — so a server on
// a non-standard port is caught by the operator's own "stop it first" step.
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
