package main

import (
	"fmt"
	"net/url"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

type sharedProjectMilestone struct {
	ID          string `json:"id" yaml:"id"`
	ProjectID   string `json:"project_id" yaml:"project_id"`
	Name        string `json:"name" yaml:"name"`
	Description string `json:"description" yaml:"description"`
	TargetDate  string `json:"target_date" yaml:"target_date"`
	Status      string `json:"status" yaml:"status"`
	Position    int    `json:"position" yaml:"position"`
	Version     string `json:"version" yaml:"version"`
	CanManage   bool   `json:"can_manage" yaml:"can_manage"`
	CanDelete   bool   `json:"can_delete" yaml:"can_delete"`
}

func newSharedProjectMilestonesCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "milestones <project-id> [milestone-id]", Short: "Read milestones through current project Read and Manage shares", Args: cobra.RangeArgs(1, 2)}
	cmd.Flags().Int("limit", 100, "Maximum rows (1–500)")
	cmd.Flags().Int("offset", 0, "Pagination offset")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		limit, _ := cmd.Flags().GetInt("limit")
		offset, _ := cmd.Flags().GetInt("offset")
		if limit < 1 || limit > 500 || offset < 0 {
			return fmt.Errorf("provide limit 1–500 and nonnegative offset")
		}
		return sharedProjectMilestoneRequest("GET", args, nil, url.Values{"limit": {strconv.Itoa(limit)}, "offset": {strconv.Itoa(offset)}})
	}
	for _, verb := range []string{"create", "update", "delete"} {
		verb := verb
		count := 1
		if verb != "create" {
			count = 2
		}
		use := verb + " <project-id>"
		if count == 2 {
			use += " <milestone-id>"
		}
		child := &cobra.Command{Use: use, Short: map[string]string{"create": "Create a project milestone", "update": "Update a reviewed milestone snapshot", "delete": "Delete an empty milestone with explicit confirmation"}[verb], Args: cobra.ExactArgs(count)}
		if verb != "delete" {
			child.Flags().String("name", "", "Milestone name")
			child.Flags().String("description", "", "Complete description; empty clears it")
			child.Flags().String("target-date", "", "Target YYYY-MM-DD; empty clears it")
			child.Flags().String("status", "active", "active, completed or cancelled")
			_ = child.MarkFlagRequired("name")
		}
		if verb != "create" {
			child.Flags().String("version", "", "Reviewed milestone version; conflicts are not retried")
			_ = child.MarkFlagRequired("version")
		}
		if verb == "update" {
			child.Flags().Int("position", 0, "Reviewed ordering position")
			for _, flag := range []string{"description", "target-date", "status", "position"} {
				_ = child.MarkFlagRequired(flag)
			}
		}
		if verb == "delete" {
			child.Flags().Bool("confirm", false, "Confirm removal of this empty milestone")
		}
		child.RunE = func(cmd *cobra.Command, args []string) error {
			payload := map[string]any{}
			if verb != "create" {
				version, _ := cmd.Flags().GetString("version")
				if len(version) != 64 {
					return fmt.Errorf("provide the exact reviewed --version")
				}
				payload["version"] = version
			}
			if verb == "delete" {
				confirmed, _ := cmd.Flags().GetBool("confirm")
				if !confirmed {
					return fmt.Errorf("review the milestone and provide --confirm to remove it")
				}
			} else {
				for _, field := range []string{"name", "description", "status"} {
					payload[field], _ = cmd.Flags().GetString(field)
				}
				payload["target_date"], _ = cmd.Flags().GetString("target-date")
				if verb == "update" {
					payload["position"], _ = cmd.Flags().GetInt("position")
				}
			}
			return sharedProjectMilestoneRequest(map[string]string{"create": "POST", "update": "PUT", "delete": "DELETE"}[verb], args, payload, nil)
		}
		cmd.AddCommand(child)
	}
	return cmd
}

func sharedProjectMilestoneRequest(method string, args []string, body map[string]any, query url.Values) error {
	for _, id := range args {
		if id == "" || id == "." || id == ".." {
			return fmt.Errorf("provide valid resource IDs")
		}
	}
	if err := requireAuth(); err != nil {
		return err
	}
	if err := requireWorkspace(); err != nil {
		return err
	}
	client := newAPIClient()
	workspace := client.GetWorkspaceID()
	if workspace == "." || workspace == ".." {
		return fmt.Errorf("invalid workspace ID")
	}
	path := "/api/v1/workspaces/" + url.PathEscape(workspace) + "/restricted-projects/" + url.PathEscape(args[0]) + "/milestones"
	if len(args) > 1 {
		path += "/" + url.PathEscape(args[1])
	} else if len(query) > 0 {
		path += "?" + query.Encode()
	}
	var payload any
	if body != nil {
		payload = body
	}
	response, err := client.Do(method, path, payload)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if err = cli.CheckError(response); err != nil {
		return err
	}
	if method == "DELETE" {
		return newFormatter().Auto(map[string]any{"deleted": true}, []string{"STATUS"}, [][]string{{"Milestone removed"}})
	}
	headers := []string{"MILESTONE", "NAME", "STATUS", "TARGET", "POSITION", "MANAGE", "DELETE", "VERSION"}
	row := func(item sharedProjectMilestone) []string {
		return []string{item.ID, item.Name, item.Status, item.TargetDate, strconv.Itoa(item.Position), fmt.Sprint(item.CanManage), fmt.Sprint(item.CanDelete), item.Version}
	}
	if method == "GET" && len(args) == 1 {
		items := []sharedProjectMilestone{}
		if err = cli.ReadJSON(response, &items); err != nil {
			return err
		}
		rows := [][]string{}
		for _, item := range items {
			rows = append(rows, row(item))
		}
		return newFormatter().Auto(items, headers, rows)
	}
	var item sharedProjectMilestone
	if err = cli.ReadJSON(response, &item); err != nil {
		return err
	}
	return newFormatter().Auto(item, headers, [][]string{row(item)})
}
