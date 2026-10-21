package main

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

type sharedIssue struct {
	CanDeleteIssues bool    `json:"can_delete_issues" yaml:"can_delete_issues"`
	ID              string  `json:"id" yaml:"id"`
	Identifier      string  `json:"identifier" yaml:"identifier"`
	Title           string  `json:"title" yaml:"title"`
	Status          string  `json:"status" yaml:"status"`
	ProjectID       string  `json:"project_id" yaml:"project_id"`
	ProjectName     string  `json:"project_name" yaml:"project_name"`
	CanRead         bool    `json:"can_read" yaml:"can_read"`
	CanContribute   bool    `json:"can_contribute" yaml:"can_contribute"`
	Description     *string `json:"description,omitempty" yaml:"description,omitempty"`
}

var issueSharedCmd = &cobra.Command{
	Use: "shared [identifier-or-id]", Short: "List or read project issues shared with your Guest account", Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		search, _ := cmd.Flags().GetString("search")
		if !utf8.ValidString(search) || utf8.RuneCountInString(search) > 256 {
			return fmt.Errorf("search must contain at most 256 valid Unicode characters")
		}
		search = strings.TrimSpace(search)
		if len(args) == 1 && cmd.Flags().Changed("search") {
			return fmt.Errorf("--search is only available when listing shared issues")
		}
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
			if search != "" {
				query.Set("q", search)
			}
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
	issueSharedCmd.Flags().String("search", "", "Search shared issue titles and identifiers (maximum 256 characters)")
	issueSharedCmd.Flags().Int("limit", 100, "Maximum returned rows")
	issueSharedCmd.Flags().Int("offset", 0, "Pagination offset")
	issueCmd.AddCommand(issueSharedCmd)
}
