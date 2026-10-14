package main

import (
	"fmt"
	"net/url"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

type sharedIssue struct {
	ID          string  `json:"id" yaml:"id"`
	Identifier  string  `json:"identifier" yaml:"identifier"`
	Title       string  `json:"title" yaml:"title"`
	Status      string  `json:"status" yaml:"status"`
	ProjectID   string  `json:"project_id" yaml:"project_id"`
	ProjectName string  `json:"project_name" yaml:"project_name"`
	CanRead     bool    `json:"can_read" yaml:"can_read"`
	Description *string `json:"description,omitempty" yaml:"description,omitempty"`
}

var issueSharedCmd = &cobra.Command{
	Use: "shared [identifier-or-id]", Short: "List or read project issues shared with your Guest account", Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()
		workspace := client.GetWorkspaceID()
		path := "/api/v1/workspaces/" + url.PathEscape(workspace) + "/restricted-issues"
		if len(args) == 1 {
			if args[0] == "." || args[0] == ".." || args[0] == "" {
				return fmt.Errorf("invalid issue identifier")
			}
			path += "/" + url.PathEscape(args[0])
		} else {
			query := url.Values{}
			project, _ := cmd.Flags().GetString("project")
			if project != "" {
				query.Set("project_id", project)
			}
			limit, _ := cmd.Flags().GetInt("limit")
			offset, _ := cmd.Flags().GetInt("offset")
			query.Set("limit", strconv.Itoa(limit))
			query.Set("offset", strconv.Itoa(offset))
			path += "?" + query.Encode()
		}
		response, err := client.Get(path)
		if err != nil {
			return err
		}
		defer response.Body.Close()
		if err = cli.CheckError(response); err != nil {
			return err
		}
		if len(args) == 1 {
			var item sharedIssue
			if err = cli.ReadJSON(response, &item); err != nil {
				return err
			}
			return newFormatter().Auto(item, []string{"ISSUE", "TITLE", "STATUS", "PROJECT"}, [][]string{{item.Identifier, item.Title, item.Status, item.ProjectName}})
		}
		items := []sharedIssue{}
		if err = cli.ReadJSON(response, &items); err != nil {
			return err
		}
		rows := [][]string{}
		for _, item := range items {
			rows = append(rows, []string{item.Identifier, item.Title, item.Status, item.ProjectName})
		}
		return newFormatter().Auto(items, []string{"ISSUE", "TITLE", "STATUS", "PROJECT"}, rows)
	},
}

func init() {
	issueSharedCmd.Flags().String("project", "", "Filter to a shared project ID")
	issueSharedCmd.Flags().Int("limit", 100, "Maximum returned rows")
	issueSharedCmd.Flags().Int("offset", 0, "Pagination offset")
	issueCmd.AddCommand(issueSharedCmd)
}
