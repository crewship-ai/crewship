//go:build !clionly

package main

// init registers the subcommands that are only available in the full
// "crewship" binary — the ones that touch the local SQLite database,
// the embedded server, the container runtime, or the crashreport
// telemetry surface. None of these make sense against a remote server
// the way the rest of the CLI does, so they're stripped from the
// `clionly` build (which ships as `crewship-cli` in the Homebrew tap).
//
// Anything you add here will SILENTLY disappear from `crewship-cli`.
// If a new command should be available in BOTH binaries, register it
// in main.go's init() instead.
func init() {
	rootCmd.AddCommand(startCmd)
	rootCmd.AddCommand(doctorCmd)
	rootCmd.AddCommand(telemetryCmd)
}
