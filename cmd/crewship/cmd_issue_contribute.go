package main

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

type sharedIssueProject struct {
	CanManage     bool   `json:"can_manage" yaml:"can_manage"`
	ID            string `json:"id" yaml:"id"`
	Name          string `json:"name" yaml:"name"`
	CanContribute bool   `json:"can_contribute" yaml:"can_contribute"`
	IntakeReady   bool   `json:"intake_ready" yaml:"intake_ready"`
}
type sharedIssueComment struct {
	ID        string `json:"id" yaml:"id"`
	Body      string `json:"body" yaml:"body"`
	Author    string `json:"author" yaml:"author"`
	CreatedAt string `json:"created_at" yaml:"created_at"`
}
type projectGuestIntake struct {
	ProjectID string `json:"project_id" yaml:"project_id"`
	CrewID    string `json:"crew_id" yaml:"crew_id"`
	CrewName  string `json:"crew_name" yaml:"crew_name"`
	Revision  int64  `json:"revision" yaml:"revision"`
}

func guestCLIPathSegment(value string) (string, error) {
	if value == "" || value == "." || value == ".." {
		return "", fmt.Errorf("invalid resource identifier")
	}
	return url.PathEscape(value), nil
}

func guestIssueClient() (*cli.Client, string, error) {
	if err := requireAuth(); err != nil {
		return nil, "", err
	}
	if err := requireWorkspace(); err != nil {
		return nil, "", err
	}
	client := newAPIClient()
	workspace, err := guestCLIPathSegment(client.GetWorkspaceID())
	if err != nil {
		return nil, "", err
	}
	return client, "/api/v1/workspaces/" + workspace, nil
}

func newSharedIssueProjectsCmd() *cobra.Command {
	return &cobra.Command{Use: "projects", Short: "List shared projects and your contribution/intake availability", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		client, _, err := guestIssueClient()
		if err != nil {
			return err
		}
		response, err := client.Get(fmt.Sprintf("/api/v1/workspaces/%s/restricted-issue-projects", url.PathEscape(client.GetWorkspaceID())))
		if err != nil {
			return err
		}
		defer response.Body.Close()
		if err = cli.CheckError(response); err != nil {
			return err
		}
		items := []sharedIssueProject{}
		if err = cli.ReadJSON(response, &items); err != nil {
			return err
		}
		rows := [][]string{}
		for _, item := range items {
			rows = append(rows, []string{item.ID, item.Name, fmt.Sprint(item.CanContribute), fmt.Sprint(item.CanManage), fmt.Sprint(item.IntakeReady)})
		}
		return newFormatter().Auto(items, []string{"PROJECT", "NAME", "CONTRIBUTE", "MANAGE", "INTAKE READY"}, rows)
	}}
}

func newSharedIssueCreateCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "create", Short: "Create an unassigned Backlog request in a shared project", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		project, _ := cmd.Flags().GetString("project")
		title, _ := cmd.Flags().GetString("title")
		description, _ := cmd.Flags().GetString("description")
		if strings.TrimSpace(project) == "" || strings.TrimSpace(title) == "" || len(title) > 4096 || len(description) > 65536 {
			return fmt.Errorf("project and title are required; title maximum 4096 bytes, description maximum 65536 bytes")
		}
		client, base, err := guestMutationClient(cmd)
		if err != nil {
			return err
		}
		response, err := client.Post(base+"/restricted-issues", map[string]any{"project_id": project, "title": title, "description": description})
		if err != nil {
			return err
		}
		defer response.Body.Close()
		if err = cli.CheckError(response); err != nil {
			return err
		}
		var item sharedIssue
		if err = cli.ReadJSON(response, &item); err != nil {
			return err
		}
		return newFormatter().Auto(item, []string{"ISSUE", "TITLE", "STATUS", "PROJECT"}, [][]string{{item.Identifier, item.Title, item.Status, item.ProjectName}})
	}}
	cmd.Flags().String("project", "", "Shared project ID (requires Read and Contribute)")
	_ = cmd.MarkFlagRequired("project")
	cmd.Flags().String("title", "", "Issue title")
	_ = cmd.MarkFlagRequired("title")
	cmd.Flags().String("description", "", "Issue description")
	cmd.Flags().String("idempotency-key", "", "Stable key for retrying this exact mutation (1–200 bytes)")
	return cmd
}

func newSharedIssueCommentCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "comment <identifier-or-id>", Short: "Post a comment shared with the issue's project", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		id, err := guestCLIPathSegment(args[0])
		if err != nil {
			return err
		}
		body, _ := cmd.Flags().GetString("body")
		if strings.TrimSpace(mustSharedProject(cmd)) == "" {
			return fmt.Errorf("--project is required to confirm this comment audience")
		}
		if strings.TrimSpace(body) == "" || len(body) > 32768 {
			return fmt.Errorf("comment must contain 1–32768 bytes")
		}
		client, base, err := guestMutationClient(cmd)
		if err != nil {
			return err
		}
		response, err := client.Post(base+"/restricted-issues/"+id+"/comments", map[string]string{"body": body, "project_id": mustSharedProject(cmd)})
		if err != nil {
			return err
		}
		defer response.Body.Close()
		if err = cli.CheckError(response); err != nil {
			return err
		}
		var item sharedIssueComment
		if err = cli.ReadJSON(response, &item); err != nil {
			return err
		}
		return newFormatter().Auto(item, []string{"COMMENT", "AUTHOR", "BODY"}, [][]string{{item.ID, item.Author, item.Body}})
	}}
	cmd.Flags().String("project", "", "Expected project ID; confirms the audience for this draft")
	_ = cmd.MarkFlagRequired("project")
	cmd.Flags().String("body", "", "Shared comment text")
	_ = cmd.MarkFlagRequired("body")
	cmd.Flags().String("idempotency-key", "", "Stable key for retrying this exact mutation (1–200 bytes)")
	return cmd
}

func newSharedIssueCommentsCmd() *cobra.Command {
	return &cobra.Command{Use: "comments <identifier-or-id>", Short: "Read only comments explicitly shared with the current project", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		id, err := guestCLIPathSegment(args[0])
		if err != nil {
			return err
		}
		client, base, err := guestIssueClient()
		if err != nil {
			return err
		}
		response, err := client.Get(base + "/restricted-issues/" + id + "/comments")
		if err != nil {
			return err
		}
		defer response.Body.Close()
		if err = cli.CheckError(response); err != nil {
			return err
		}
		items := []sharedIssueComment{}
		if err = cli.ReadJSON(response, &items); err != nil {
			return err
		}
		rows := [][]string{}
		for _, item := range items {
			rows = append(rows, []string{item.ID, item.Author, item.Body, item.CreatedAt})
		}
		return newFormatter().Auto(items, []string{"COMMENT", "AUTHOR", "BODY", "CREATED"}, rows)
	}}
}

func newSharedIssueMoveCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "move <identifier-or-id>", Short: "Arrange shared intake work between BACKLOG and TODO", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		id, err := guestCLIPathSegment(args[0])
		if err != nil {
			return err
		}
		status, _ := cmd.Flags().GetString("status")
		if status != "BACKLOG" && status != "TODO" {
			return fmt.Errorf("status must be BACKLOG or TODO; trusted execution/review states require the existing workflow")
		}
		client, base, err := guestMutationClient(cmd)
		if err != nil {
			return err
		}
		response, err := client.Patch(base+"/restricted-issues/"+id, map[string]string{"status": status})
		if err != nil {
			return err
		}
		defer response.Body.Close()
		if err = cli.CheckError(response); err != nil {
			return err
		}
		var item sharedIssue
		if err = cli.ReadJSON(response, &item); err != nil {
			return err
		}
		return newFormatter().Auto(item, []string{"ISSUE", "STATUS", "PROJECT"}, [][]string{{item.Identifier, item.Status, item.ProjectName}})
	}}
	cmd.Flags().String("status", "", "BACKLOG or TODO")
	_ = cmd.MarkFlagRequired("status")
	cmd.Flags().String("idempotency-key", "", "Stable key for retrying this exact mutation (1–200 bytes)")
	return cmd
}

func newProjectGuestIntakeCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "guest-intake <project-id>", Short: "Read or configure a project's Guest request crew (ADMIN/OWNER)", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		project, err := guestCLIPathSegment(args[0])
		if err != nil {
			return err
		}
		client, _, err := guestIssueClient()
		if err != nil {
			return err
		}
		path := fmt.Sprintf("/api/v1/workspaces/%s/projects/%s/guest-intake", url.PathEscape(client.GetWorkspaceID()), project)
		response, err := client.Get(path)
		if err != nil {
			return err
		}
		if err = cli.CheckError(response); err != nil {
			response.Body.Close()
			return err
		}
		var item projectGuestIntake
		err = cli.ReadJSON(response, &item)
		response.Body.Close()
		if err != nil {
			return err
		}
		if item.ProjectID != args[0] || item.Revision < 0 {
			return fmt.Errorf("project intake snapshot mismatch")
		}
		if cmd.Flags().Changed("crew") {
			crew, _ := cmd.Flags().GetString("crew")
			if strings.TrimSpace(crew) == "" {
				return fmt.Errorf("crew ID is required")
			}
			response, err = client.Put(path, map[string]any{"crew_id": crew, "revision": item.Revision})
			if err != nil {
				return err
			}
			defer response.Body.Close()
			if err = cli.CheckError(response); err != nil {
				return err
			}
			previous := item.Revision
			if err = cli.ReadJSON(response, &item); err != nil {
				return err
			}
			if item.ProjectID != args[0] || item.Revision <= previous {
				return fmt.Errorf("project intake save snapshot mismatch")
			}
		}
		return newFormatter().Auto(item, []string{"PROJECT", "CREW", "NAME", "REVISION"}, [][]string{{item.ProjectID, item.CrewID, item.CrewName, fmt.Sprint(item.Revision)}})
	}}
	cmd.Flags().String("crew", "", "Set the active workspace crew receiving new Guest requests; reads once and never retries a conflict")
	return cmd
}

func init() {
	issueSharedCmd.AddCommand(newSharedIssueProjectsCmd(), newSharedIssueCreateCmd(), newSharedIssueCommentCmd(), newSharedIssueCommentsCmd(), newSharedIssueMoveCmd())
	projectCmd.AddCommand(newProjectGuestIntakeCmd())
}

func mustSharedProject(cmd *cobra.Command) string {
	project, _ := cmd.Flags().GetString("project")
	return project
}

func guestMutationClient(cmd *cobra.Command) (*cli.Client, string, error) {
	key, _ := cmd.Flags().GetString("idempotency-key")
	if cmd.Flags().Changed("idempotency-key") && (strings.TrimSpace(key) == "" || len(key) > 200) {
		return nil, "", fmt.Errorf("--idempotency-key must contain 1–200 bytes")
	}
	client, base, err := guestIssueClient()
	if err != nil {
		return nil, "", err
	}
	if key != "" {
		client = client.WithHeader("Idempotency-Key", key)
	}
	return client, base, nil
}
