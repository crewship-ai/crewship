package main

import (
	"fmt"
	"net/url"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

func newAuditHeadCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "head", Short: "Read the current audit tail for independent OWNER review", Args: cobra.NoArgs}
	cmd.Flags().String("scope", "workspace", "workspace or instance")
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		scope, _ := cmd.Flags().GetString("scope")
		if scope != "workspace" && scope != "instance" {
			return fmt.Errorf("scope must be workspace or instance")
		}
		client := newAPIClient()
		query := url.Values{"scope": {scope}}
		if scope == "workspace" {
			if err := requireWorkspace(); err != nil {
				return err
			}
			id, err := client.ResolveWorkspaceIDStrict(cmd.Context())
			if err != nil {
				return err
			}
			query.Set("workspace_id", id)
		} else {
			client.WorkspaceID = ""
		}
		response, err := client.Get("/api/v1/admin/audit/head?" + query.Encode())
		if err != nil {
			return err
		}
		if err := cli.CheckError(response); err != nil {
			return err
		}
		var result struct {
			Seq  int64  `json:"tail_seq" yaml:"tail_seq"`
			Hash string `json:"tail_hash" yaml:"tail_hash"`
		}
		if err := cli.ReadJSON(response, &result); err != nil {
			return err
		}
		return newFormatter().AutoHuman(result, func() {
			fmt.Fprintf(cmd.OutOrStdout(), "Audit tail sequence: %d\nAudit tail hash: %s\nThis read does not accept or reseal history.\n", result.Seq, result.Hash)
		})
	}
	return cmd
}

func init() { auditCmd.AddCommand(newAuditHeadCommand()) }
