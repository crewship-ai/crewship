package main

import (
	"fmt"
	"net/http"
	"net/url"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

type sharedProjectDetails struct {
	ID          string `json:"id" yaml:"id"`
	Name        string `json:"name" yaml:"name"`
	Description string `json:"description" yaml:"description"`
	Version     string `json:"version" yaml:"version"`
	CanManage   bool   `json:"can_manage" yaml:"can_manage"`
}

func newSharedProjectCommand() *cobra.Command {
	command := &cobra.Command{Use: "shared <project-id>", Short: "Read basic project details shared with your Guest account", Args: cobra.ExactArgs(1)}
	command.RunE = func(cmd *cobra.Command, args []string) error { return sharedProjectRequest(args[0], nil) }
	update := &cobra.Command{Use: "update <project-id>", Short: "Update shared project name and description with Manage and Read grants", Args: cobra.ExactArgs(1)}
	update.Flags().String("name", "", "Project name")
	update.Flags().String("description", "", "Complete project description; empty clears it")
	update.Flags().String("version", "", "Snapshot version returned by project shared; conflicts are not retried")
	for _, flag := range []string{"name", "description", "version"} {
		_ = update.MarkFlagRequired(flag)
	}
	update.RunE = func(cmd *cobra.Command, args []string) error {
		name, _ := cmd.Flags().GetString("name")
		description, _ := cmd.Flags().GetString("description")
		version, _ := cmd.Flags().GetString("version")
		if name == "" || len(version) != 64 {
			return fmt.Errorf("provide --name and the exact --version returned by project shared")
		}
		return sharedProjectRequest(args[0], map[string]any{"name": name, "description": description, "version": version})
	}
	command.AddCommand(update, newSharedProjectMilestonesCommand())
	return command
}

func sharedProjectRequest(id string, body map[string]any) error {
	if id == "" || id == "." || id == ".." {
		return fmt.Errorf("provide a project ID")
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
	path := "/api/v1/workspaces/" + url.PathEscape(workspace) + "/restricted-projects/" + url.PathEscape(id)
	var response *http.Response
	var err error
	if body == nil {
		response, err = client.Get(path)
	} else {
		response, err = client.Put(path, body)
	}
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if err = cli.CheckError(response); err != nil {
		return err
	}
	var details sharedProjectDetails
	if err = cli.ReadJSON(response, &details); err != nil {
		return err
	}
	return newFormatter().Auto(details, []string{"PROJECT", "MANAGE", "VERSION", "DESCRIPTION"}, [][]string{{details.Name, fmt.Sprint(details.CanManage), details.Version, details.Description}})
}

func init() { projectCmd.AddCommand(newSharedProjectCommand()) }
