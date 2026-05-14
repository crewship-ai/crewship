package main

import (
	"context"
	"fmt"

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
	Long: `Crewship can optionally send anonymous crash reports to Sentry to help
the maintainer diagnose bugs. The feature is OFF by default and the prompt
runs once at first start. You can change your mind at any time:

  crewship telemetry on
  crewship telemetry off
  crewship telemetry status

What is sent (only when enabled):
  - Go stack traces and error messages
  - Crewship version, commit, OS/architecture
  - An anonymous install ID generated locally on opt-in

What is NEVER sent:
  - Workspace, user, or credential data
  - HTTP request bodies
  - Authorization headers, cookies, or query-string secrets
  - Environment variables`,
}

var telemetryOnCmd = &cobra.Command{
	Use:   "on",
	Short: "Opt in to anonymous crash reporting",
	RunE: func(cmd *cobra.Command, args []string) error {
		return setTelemetry(true)
	},
}

var telemetryOffCmd = &cobra.Command{
	Use:   "off",
	Short: "Opt out of anonymous crash reporting",
	RunE: func(cmd *cobra.Command, args []string) error {
		return setTelemetry(false)
	},
}

var telemetryStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show current telemetry consent state",
	RunE: func(cmd *cobra.Command, args []string) error {
		db, err := openLocalDB()
		if err != nil {
			return err
		}
		defer db.Close()

		enabled, asked, installID, err := crashreport.Status(context.Background(), db.DB)
		if err != nil {
			return fmt.Errorf("read telemetry status: %w", err)
		}
		switch {
		case !asked:
			fmt.Println("Telemetry: not yet configured. You'll be prompted on the next `crewship start`.")
		case enabled:
			fmt.Println("Telemetry: ENABLED (opt-in)")
			if installID != "" {
				fmt.Printf("  install_id: %s\n", installID)
			}
			if crashreport.DSN == "" {
				cli.PrintWarning("This build has no Sentry DSN compiled in. Telemetry consent is recorded but no events are sent.")
			}
		default:
			fmt.Println("Telemetry: DISABLED (opt-out)")
		}
		return nil
	},
}

func init() {
	telemetryCmd.AddCommand(telemetryOnCmd)
	telemetryCmd.AddCommand(telemetryOffCmd)
	telemetryCmd.AddCommand(telemetryStatusCmd)
}

// setTelemetry is shared by `on` and `off`. It opens the local DB the same
// way `crewship start` does so the consent state lives next to the data,
// not in a separate config file the user has to keep in sync.
func setTelemetry(enabled bool) error {
	db, err := openLocalDB()
	if err != nil {
		return err
	}
	defer db.Close()

	on, installID, err := crashreport.SetOptIn(context.Background(), db.DB, enabled)
	if err != nil {
		return fmt.Errorf("write telemetry setting: %w", err)
	}
	if on {
		cli.PrintSuccess("Telemetry enabled. Crash reports will be sent on the next server start.")
		if installID != "" {
			fmt.Printf("  install_id: %s\n", installID)
		}
		if crashreport.DSN == "" {
			cli.PrintWarning("This build has no Sentry DSN compiled in. Consent is recorded but no events will be sent until you install a release binary.")
		}
		return nil
	}
	cli.PrintSuccess("Telemetry disabled. No crash reports will be sent.")
	return nil
}

// openLocalDB opens the database at the same path `crewship start` uses
// when no --db override is provided. The CLI sub-commands here don't run a
// full server, so we open the file directly. Migrations are NOT applied
// here — the `crewship telemetry` sub-commands assume the server has been
// started at least once.
func openLocalDB() (*database.DB, error) {
	dataDir, err := database.DefaultDataDir()
	if err != nil {
		return nil, fmt.Errorf("resolve data directory: %w", err)
	}
	db, err := database.Open(dataDir.DatabaseURL())
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	return db, nil
}
