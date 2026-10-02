//go:build !clionly

package main

import (
	"fmt"
	"github.com/spf13/cobra"
)

var adminInstanceBackupsServicesCmd = &cobra.Command{Use: "services", Short: "Persistent quota-service data staged by instance recovery"}
var adminInstanceBackupsServicesLandCmd = &cobra.Command{
	Use: "land", Short: "Import verified recovered service disks on this host and release maintenance", Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := requireInstanceAdminClient()
		if err != nil {
			return err
		}
		dry, _ := cmd.Flags().GetBool("dry-run")
		var out struct {
			Images int  `json:"images" yaml:"images"`
			DryRun bool `json:"dry_run" yaml:"dry_run"`
		}
		if err := postJSON(client.WithTimeout(landEnvironmentsTimeout), "/api/v1/admin/instance/backups/services/land", map[string]any{"dry_run": dry}, &out); err != nil {
			return err
		}
		return resolvedFormatter(cmd).AutoHuman(out, func() {
			if out.DryRun {
				fmt.Fprintf(cmd.OutOrStdout(), "Verified %d staged service image(s); nothing imported.\n", out.Images)
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "Imported %d service image(s); maintenance released.\n", out.Images)
			}
		})
	},
}

func init() {
	adminInstanceBackupsServicesLandCmd.Flags().Bool("dry-run", false, "Verify staged disks, current ownership and maintenance without importing")
	adminInstanceBackupsServicesCmd.AddCommand(adminInstanceBackupsServicesLandCmd)
	adminInstanceBackupsCmd.AddCommand(adminInstanceBackupsServicesCmd)
}
