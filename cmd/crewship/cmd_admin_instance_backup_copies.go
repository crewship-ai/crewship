//go:build !clionly

package main

// crewship admin instance backups copies — restore from an off-site copy:
//
//	copies list --destination <id>                 GET  /api/v1/admin/instance/backups/copies?destination=
//	copies fetch --destination <id> --key <key>    POST /api/v1/admin/instance/backups/copies/fetch
//	copies status <fetch-id>                       GET  /api/v1/admin/instance/backups/copies/fetch/{id}
//
// A fetch runs on the server as a job: the POST answers at once, so a large
// download is never cut by this client's 30 s request timeout; --wait follows
// the job with short status calls.

import (
	"errors"
	"fmt"
	"net/url"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

type offsiteCopyRow struct {
	Key         string    `json:"key" yaml:"key"`
	Size        int64     `json:"size" yaml:"size"`
	Modified    time.Time `json:"modified" yaml:"modified"`
	Scope       string    `json:"scope" yaml:"scope"`
	WorkspaceID *string   `json:"workspace_id" yaml:"workspace_id"`
	Local       bool      `json:"local" yaml:"local"`
	LocalPath   *string   `json:"local_path" yaml:"local_path"`
}

type offsiteCopyList struct {
	DestinationID   string           `json:"destination_id" yaml:"destination_id"`
	DestinationName string           `json:"destination_name" yaml:"destination_name"`
	Copies          []offsiteCopyRow `json:"copies" yaml:"copies"`
}

type offsiteFetchJob struct {
	ID            string     `json:"id" yaml:"id"`
	DestinationID string     `json:"destination_id" yaml:"destination_id"`
	Key           string     `json:"key" yaml:"key"`
	Status        string     `json:"status" yaml:"status"`
	Path          *string    `json:"path" yaml:"path"`
	Size          int64      `json:"size" yaml:"size"`
	Layers        int        `json:"layers" yaml:"layers"`
	Error         *string    `json:"error" yaml:"error"`
	StartedAt     time.Time  `json:"started_at" yaml:"started_at"`
	EndedAt       *time.Time `json:"ended_at" yaml:"ended_at"`
}

var adminInstanceBackupsCopiesCmd = &cobra.Command{
	Use:   "copies",
	Short: "Bundles at an off-site destination, and fetching one back to restore it",
}

var adminInstanceBackupsCopiesListCmd = &cobra.Command{
	Use:   "list --destination <id>",
	Short: "List the bundles an off-site destination holds",
	Long: `GET /api/v1/admin/instance/backups/copies?destination=<id>. Every bundle at
the destination (not its environment layers), and whether this server
already has it. Destination ids: 'admin instance backups destinations list'.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := requireInstanceAdminClient()
		if err != nil {
			return err
		}
		dest, _ := cmd.Flags().GetString("destination")
		if dest == "" {
			return cli.WithExitCode(errors.New("--destination <id> is required"), cli.ExitValidation)
		}
		var out offsiteCopyList
		if err := getJSON(client, instanceBackupsAPI+"/copies?destination="+url.QueryEscape(dest), &out); err != nil {
			return err
		}
		return resolvedFormatter(cmd).AutoHuman(out, func() {
			if len(out.Copies) == 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "No bundle at %s.\n", backupDash(out.DestinationName))
				return
			}
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "KEY\tSCOPE\tSIZE\tMODIFIED\tON THIS SERVER")
			for _, c := range out.Copies {
				scope := c.Scope
				if c.WorkspaceID != nil {
					scope += " " + *c.WorkspaceID
				}
				here := "no"
				if c.Local && c.LocalPath != nil {
					here = *c.LocalPath
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", c.Key, scope, formatBytes(c.Size), c.Modified.UTC().Format(time.RFC3339), here)
			}
			_ = tw.Flush()
		})
	},
}

func printFetchJob(cmd *cobra.Command, j offsiteFetchJob) {
	w := cmd.OutOrStdout()
	switch j.Status {
	case "done":
		path := ""
		if j.Path != nil {
			path = *j.Path
		}
		fmt.Fprintf(w, "Fetched %s (%s, %d environment layer(s)) to %s.\n", j.Key, formatBytes(j.Size), j.Layers, path)
		fmt.Fprintf(w, "Restore it: crewship recover --bundle %s --identity <key-file> --data-dir <empty dir>, or from Admin › Backups › Recovery.\n", path)
	case "failed":
		msg := ""
		if j.Error != nil {
			msg = *j.Error
		}
		fmt.Fprintf(w, "Fetch %s of %s failed: %s\n", j.ID, j.Key, msg)
	default:
		fmt.Fprintf(w, "Fetch %s of %s is running on the server; follow it with: crewship admin instance backups copies status %s\n", j.ID, j.Key, j.ID)
	}
}

func fetchJobFailed(j offsiteFetchJob) error {
	if j.Status == "failed" {
		msg := "the fetch failed"
		if j.Error != nil {
			msg = *j.Error
		}
		return errors.New(msg)
	}
	return nil
}

var adminInstanceBackupsCopiesFetchCmd = &cobra.Command{
	Use:   "fetch --destination <id> --key <key>",
	Short: "Fetch a bundle back from an off-site destination to restore it",
	Long: `POST /api/v1/admin/instance/backups/copies/fetch. Downloads the bundle and
the environment layers it needs into this server's backups directory, each
verified, so it can be restored or drilled like a local bundle. The server
runs it as a job and answers at once; --wait follows it until it ends.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := requireInstanceAdminClient()
		if err != nil {
			return err
		}
		f := cmd.Flags()
		dest, _ := f.GetString("destination")
		key, _ := f.GetString("key")
		if dest == "" || key == "" {
			return cli.WithExitCode(errors.New("--destination <id> and --key <key> are required (keys: 'copies list')"), cli.ExitValidation)
		}
		var job offsiteFetchJob
		if err := postJSON(client, instanceBackupsAPI+"/copies/fetch", map[string]any{"destination_id": dest, "key": key}, &job); err != nil {
			return err
		}
		if wait, _ := f.GetBool("wait"); wait {
			timeout, _ := f.GetDuration("timeout")
			deadline := time.Now().Add(timeout)
			for job.Status == "running" {
				if time.Now().After(deadline) {
					return fmt.Errorf("fetch %s is still running after %s; follow it with: crewship admin instance backups copies status %s", job.ID, timeout, job.ID)
				}
				time.Sleep(time.Second)
				if err := getJSON(client, instanceBackupsAPI+"/copies/fetch/"+url.PathEscape(job.ID), &job); err != nil {
					return err
				}
			}
		}
		if err := resolvedFormatter(cmd).AutoHuman(job, func() { printFetchJob(cmd, job) }); err != nil {
			return err
		}
		return fetchJobFailed(job)
	},
}

var adminInstanceBackupsCopiesStatusCmd = &cobra.Command{
	Use:   "status <fetch-id>",
	Short: "Show an off-site fetch: running, done (and where) or failed",
	Long:  `GET /api/v1/admin/instance/backups/copies/fetch/{id}. A server restart forgets its fetches.`,
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireInstanceAdminClient()
		if err != nil {
			return err
		}
		var job offsiteFetchJob
		if err := getJSON(client, instanceBackupsAPI+"/copies/fetch/"+url.PathEscape(args[0]), &job); err != nil {
			return err
		}
		if err := resolvedFormatter(cmd).AutoHuman(job, func() { printFetchJob(cmd, job) }); err != nil {
			return err
		}
		return fetchJobFailed(job)
	},
}

func init() {
	adminInstanceBackupsCopiesListCmd.Flags().String("destination", "", "Destination id (required)")
	fc := adminInstanceBackupsCopiesFetchCmd.Flags()
	fc.String("destination", "", "Destination id (required)")
	fc.String("key", "", "The bundle's key at the destination, as 'copies list' shows it (required)")
	fc.Bool("wait", false, "Wait for the fetch to finish and print where the bundle is")
	fc.Duration("timeout", 6*time.Hour, "With --wait: how long to wait")
	adminInstanceBackupsCopiesCmd.AddCommand(adminInstanceBackupsCopiesListCmd, adminInstanceBackupsCopiesFetchCmd, adminInstanceBackupsCopiesStatusCmd)
	adminInstanceBackupsCmd.AddCommand(adminInstanceBackupsCopiesCmd)
}
