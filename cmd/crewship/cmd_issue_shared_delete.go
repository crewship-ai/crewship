package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

type sharedIssueDeletion struct {
	ID         string `json:"id" yaml:"id"`
	Identifier string `json:"identifier" yaml:"identifier"`
	Title      string `json:"title" yaml:"title"`
	ProjectID  string `json:"project_id" yaml:"project_id"`
	Status     string `json:"status" yaml:"status"`
	Version    string `json:"version" yaml:"version"`
	CanDelete  bool   `json:"can_delete" yaml:"can_delete"`
}

func newSharedIssueDeletionCmd(write bool) *cobra.Command {
	use, short := "shared-delete-preview <identifier-or-id>", "Review an issue for explicitly granted permanent deletion"
	if write {
		use, short = "shared-delete <identifier-or-id>", "Delete reviewed Backlog or Cancelled issue work with explicit confirmation"
	}
	cmd := &cobra.Command{Use: use, Short: short, Args: cobra.ExactArgs(1)}
	if write {
		cmd.Flags().String("project", "", "Reviewed project ID")
		cmd.Flags().String("version", "", "Exact version from shared-delete-preview; conflicts are not retried")
		cmd.Flags().Bool("confirm", false, "Confirm permanent deletion of the issue, comments and attachments")
		_ = cmd.MarkFlagRequired("project")
		_ = cmd.MarkFlagRequired("version")
	}
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		id, err := guestCLIPathSegment(args[0])
		if err != nil {
			return err
		}
		var payload any
		method := "GET"
		if write {
			project, _ := cmd.Flags().GetString("project")
			version, _ := cmd.Flags().GetString("version")
			confirmed, _ := cmd.Flags().GetBool("confirm")
			if project == "" || len(version) != 64 || !confirmed {
				return fmt.Errorf("review the issue and supply --project, --version and --confirm")
			}
			payload = map[string]any{"project_id": project, "version": version, "confirm": true}
			method = "DELETE"
		}
		client, base, err := guestIssueClient()
		if err != nil {
			return err
		}
		path := base + "/restricted-issues/" + id
		if !write {
			path += "/deletion"
		}
		response, err := client.Do(method, path, payload)
		if err != nil {
			return err
		}
		defer response.Body.Close()
		if err = cli.CheckError(response); err != nil {
			return err
		}
		if write {
			return newFormatter().Auto(map[string]bool{"deleted": true}, []string{"DELETED"}, [][]string{{"true"}})
		}
		var item sharedIssueDeletion
		if err = cli.ReadJSON(response, &item); err != nil {
			return err
		}
		return newFormatter().Auto(item, []string{"ISSUE", "PROJECT", "STATUS", "DELETE", "VERSION"}, [][]string{{item.Identifier, item.ProjectID, item.Status, fmt.Sprint(item.CanDelete), item.Version}})
	}
	return cmd
}
func init() { issueCmd.AddCommand(newSharedIssueDeletionCmd(false), newSharedIssueDeletionCmd(true)) }
