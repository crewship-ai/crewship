//go:build !clionly

package main

// `crewship admin instance backups environments land` — load the complete
// container environments an offline `crewship recover` staged.
//
//	backups environments land [--dry-run]   POST /api/v1/admin/instance/backups/environments/land

import (
	"fmt"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

// landEnvironmentsTimeout clears the server's own ceiling for a landing
// (backup.LandTimeout, 30 minutes) so the CLI never cancels it halfway.
const landEnvironmentsTimeout = 35 * time.Minute

type landedEnvironmentRow struct {
	Workspace string   `json:"workspace" yaml:"workspace"`
	Crew      string   `json:"crew" yaml:"crew"`
	Result    string   `json:"result" yaml:"result"`
	Reason    string   `json:"reason,omitempty" yaml:"reason,omitempty"`
	Image     string   `json:"image,omitempty" yaml:"image,omitempty"`
	Container string   `json:"container,omitempty" yaml:"container,omitempty"`
	Volumes   []string `json:"volumes,omitempty" yaml:"volumes,omitempty"`
	Unsafe    []string `json:"unsafe" yaml:"unsafe"`
	Notes     []string `json:"notes,omitempty" yaml:"notes,omitempty"`
}

type landEnvironmentsView struct {
	Dir          string                 `json:"dir" yaml:"dir"`
	Staged       bool                   `json:"staged" yaml:"staged"`
	DryRun       bool                   `json:"dry_run" yaml:"dry_run"`
	Environments []landedEnvironmentRow `json:"environments" yaml:"environments"`
}

var adminInstanceBackupsEnvironmentsCmd = &cobra.Command{
	Use:   "environments",
	Short: "Complete container environments: land the ones a recover staged",
}

var adminInstanceBackupsEnvironmentsLandCmd = &cobra.Command{
	Use:   "land",
	Short: "Load the crew environments 'crewship recover' staged, now that Docker is here",
	Long: `POST /api/v1/admin/instance/backups/environments/land. 'crewship recover'
runs offline and keeps the crews' complete environments under
<data dir>/restore-staging/environments. Run this on the recovered server once
it runs with Docker: each image is loaded back (a Crewship crew's under its
crew image tag, so the crew is recreated from the captured environment), a
container Crewship does not manage is recreated with its sanitized settings,
and every environment is reported restored, rebuilt (architecture or missing
layers: its data is back, its environment is rebuilt from the crew image) or
skipped with the reason. Settings not carried over for safety (Docker socket,
privileged mode, host paths, added capabilities, devices) stay off.
Processes start fresh. --dry-run only checks.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		dry, _ := cmd.Flags().GetBool("dry-run")
		timeout, _ := cmd.Flags().GetDuration("timeout")
		var out landEnvironmentsView
		if err := postJSON(client.WithTimeout(timeout), "/api/v1/admin/instance/backups/environments/land", map[string]any{"dry_run": dry}, &out); err != nil {
			return err
		}
		return resolvedFormatter(cmd).AutoHuman(out, func() {
			w := cmd.OutOrStdout()
			if !out.Staged {
				fmt.Fprintf(w, "No staged environments under %s.\n", out.Dir)
				return
			}
			tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "WORKSPACE\tCREW\tRESULT\tDETAIL")
			for _, e := range out.Environments {
				detail := e.Reason
				if detail == "" {
					detail = e.Image
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", e.Workspace, e.Crew, e.Result, detail)
			}
			_ = tw.Flush()
			for _, e := range out.Environments {
				if len(e.Unsafe) > 0 {
					fmt.Fprintf(w, "not carried over for safety (%s): %s\n", e.Crew, strings.Join(e.Unsafe, ", "))
				}
			}
			if out.DryRun {
				fmt.Fprintln(w, "Dry run: nothing was loaded.")
			}
		})
	},
}

func init() {
	adminInstanceBackupsEnvironmentsLandCmd.Flags().Bool("dry-run", false, "Check each staged environment without loading anything")
	adminInstanceBackupsEnvironmentsLandCmd.Flags().Duration("timeout", landEnvironmentsTimeout, "How long to wait for the server (loading images takes a while)")
	adminInstanceBackupsEnvironmentsCmd.AddCommand(adminInstanceBackupsEnvironmentsLandCmd)
	adminInstanceBackupsCmd.AddCommand(adminInstanceBackupsEnvironmentsCmd)
}
