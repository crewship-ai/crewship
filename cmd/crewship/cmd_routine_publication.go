package main

import (
	"fmt"
	"net/url"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

// Reuse the existing draft tree. Legacy `draft publish <file>` retains its
// validation-receipt flow; explicit snapshot flags select shared publication.
func addRoutinePublicationFlags(cmd *cobra.Command) {
	cmd.Flags().String("draft-id", "", "Reviewed shared draft ID from routine shared-draft")
	cmd.Flags().Int("revision", 0, "Reviewed shared draft revision")
	cmd.Flags().Int("base-revision", 0, "Reviewed base publication revision")
}

func runRoutinePublication(cmd *cobra.Command, args []string, action string) error {
	draft, _ := cmd.Flags().GetString("draft-id")
	revision, _ := cmd.Flags().GetInt("revision")
	base, _ := cmd.Flags().GetInt("base-revision")
	if draft == "" || revision < 1 || base < 1 || args[0] == "." || args[0] == ".." {
		return fmt.Errorf("supply a routine and the exact draft ID, positive revision and base revision")
	}
	if err := requireAuth(); err != nil {
		return err
	}
	if err := requireWorkspace(); err != nil {
		return err
	}
	client := newAPIClient()
	workspace := client.GetWorkspaceID()
	if workspace == "" || workspace == "." || workspace == ".." {
		return fmt.Errorf("invalid workspace identifier")
	}
	path := "/api/v1/workspaces/" + url.PathEscape(workspace) + "/restricted-routine-library/" + url.PathEscape(args[0]) + "/draft/" + action
	// No hidden draft GET, refresh or retry: a conflict leaves review with
	// the human rather than silently selecting a newer draft.
	response, err := client.Post(path, map[string]any{"draft_id": draft, "revision": revision, "base_revision": base})
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if err := cli.CheckError(response); err != nil {
		return err
	}
	var result map[string]any
	if err := cli.ReadJSON(response, &result); err != nil {
		return err
	}
	if action == "validate" {
		return newFormatter().Auto(result, []string{"VALID", "SCOPE", "DRAFT", "REVISION", "BASE"}, [][]string{{fmt.Sprint(result["valid"]), fmt.Sprint(result["scope"]), draft, fmt.Sprint(revision), fmt.Sprint(base)}})
	}
	return newFormatter().Auto(result, []string{"ROUTINE", "VERSION", "PUBLICATION"}, [][]string{{fmt.Sprint(result["id"]), fmt.Sprint(result["version"]), fmt.Sprint(result["publication_revision"])}})
}
