package main

import (
	"fmt"
	"net/url"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

type sharedRoutine struct {
	ID             string         `json:"id" yaml:"id"`
	Slug           string         `json:"slug" yaml:"slug"`
	Name           string         `json:"name" yaml:"name"`
	Status         string         `json:"status" yaml:"status"`
	CanView        bool           `json:"can_view" yaml:"can_view"`
	CanEdit        bool           `json:"can_edit" yaml:"can_edit"`
	CanRun         bool           `json:"can_run" yaml:"can_run"`
	RuntimeReady   bool           `json:"runtime_ready" yaml:"runtime_ready"`
	Definition     map[string]any `json:"definition,omitempty" yaml:"definition,omitempty"`
	DefinitionHash string         `json:"definition_hash,omitempty" yaml:"definition_hash,omitempty"`
	Version        int            `json:"version,omitempty" yaml:"version,omitempty"`
}

func newRoutineSharedCommand() *cobra.Command {
	return &cobra.Command{Use: "shared [routine-id-or-slug]", Short: "Inspect routines explicitly shared with your Guest account", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) > 0 && (args[0] == "." || args[0] == ".." || args[0] == "") {
			return fmt.Errorf("invalid routine identifier")
		}
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()
		workspace := client.GetWorkspaceID()
		if workspace == "." || workspace == ".." || workspace == "" {
			return fmt.Errorf("invalid workspace identifier")
		}
		path := "/api/v1/workspaces/" + url.PathEscape(workspace) + "/restricted-routine-library"
		if len(args) > 0 {
			path += "/" + url.PathEscape(args[0])
		}
		response, err := client.Get(path)
		if err != nil {
			return err
		}
		defer response.Body.Close()
		if err = cli.CheckError(response); err != nil {
			return err
		}
		columns := []string{"ROUTINE", "NAME", "VIEW", "EDIT DRAFT", "RUN", "RUNTIME READY"}
		row := func(item sharedRoutine) []string {
			return []string{item.Slug, item.Name, fmt.Sprint(item.CanView), fmt.Sprint(item.CanEdit), fmt.Sprint(item.CanRun), fmt.Sprint(item.RuntimeReady)}
		}
		if len(args) > 0 {
			var item sharedRoutine
			if err = cli.ReadJSON(response, &item); err != nil {
				return err
			}
			return newFormatter().Auto(item, columns, [][]string{row(item)})
		}
		items := []sharedRoutine{}
		if err = cli.ReadJSON(response, &items); err != nil {
			return err
		}
		rows := [][]string{}
		for _, item := range items {
			rows = append(rows, row(item))
		}
		return newFormatter().Auto(items, columns, rows)
	}}
}
func init() { pipelineCmd.AddCommand(newRoutineSharedCommand()) }
