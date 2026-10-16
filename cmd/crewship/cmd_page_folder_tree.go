package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

func newPageFolderReparentCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "reparent <slug>",
		Short: "Move a folder in the tree while preserving Page identity and leaf sharing",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			parent, _ := cmd.Flags().GetString("parent")
			root, _ := cmd.Flags().GetBool("root")
			version, _ := cmd.Flags().GetInt64("tree-version")
			acl, _ := cmd.Flags().GetInt64("acl-version")
			hasParent := strings.TrimSpace(parent) != ""
			if hasParent == root {
				return fmt.Errorf("choose --parent SLUG or --root")
			}
			if !cmd.Flags().Changed("tree-version") || version < 0 {
				return fmt.Errorf("nonnegative --tree-version is required")
			}
			body := map[string]any{"parent": nil, "tree_version": version}
			if !root {
				if !cmd.Flags().Changed("acl-version") || acl < 0 {
					return fmt.Errorf("nonnegative parent --acl-version is required")
				}
				body["parent"] = parent
				body["acl_version"] = acl
			}
			client, err := pageClient()
			if err != nil {
				return err
			}
			resp, err := client.Post("/api/v1/page-folders/"+pagePathEscape(args[0])+"/parent", body)
			if err != nil {
				return err
			}
			return printPageArtifactResponse(cmd, resp)
		},
	}
	cmd.Flags().String("parent", "", "New parent folder slug")
	cmd.Flags().Bool("root", false, "Move to workspace root")
	cmd.Flags().Int64("tree-version", 0, "Current workspace folder tree version (required)")
	cmd.Flags().Int64("acl-version", 0, "Current parent ACL version (required with --parent)")
	return cmd
}

func init() { pageFolderCmd.AddCommand(newPageFolderReparentCommand()) }
