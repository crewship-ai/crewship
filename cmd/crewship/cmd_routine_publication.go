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
	path := "/api/v1/workspaces/" + url.PathEscape(workspace) + "/restricted-routine-library/" + url.PathEscape(args[0]) + "/draft/validate"
	if action == "publish" {
		path = "/api/v1/workspaces/" + url.PathEscape(workspace) + "/restricted-routine-library/" + url.PathEscape(args[0]) + "/draft/publish"
	}
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

var routineUnpublishCmd = &cobra.Command{
	Use:   "unpublish <routine-id-or-slug>",
	Short: "Withdraw a Guest publication; new runs are refused and running ones stop before their next step",
	Long: `Withdraw the current Guest publication of a routine.

The published version and its authority record stay in history, and nothing is
deleted. New runs are refused, and runs already in progress stop before their
next step. Republishing a reviewed shared draft makes the routine runnable
again. Requires Publish on the routine, or workspace OWNER/ADMIN.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if args[0] == "" || args[0] == "." || args[0] == ".." {
			return fmt.Errorf("supply a routine ID or slug")
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
		response, err := client.Post("/api/v1/workspaces/"+url.PathEscape(workspace)+"/restricted-routine-library/"+url.PathEscape(args[0])+"/unpublish", nil)
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
		return newFormatter().Auto(result, []string{"ROUTINE", "VERSION", "WITHDRAWN"}, [][]string{{fmt.Sprint(result["id"]), fmt.Sprint(result["version"]), fmt.Sprint(result["withdrawn_at"])}})
	},
}

func init() { pipelineCmd.AddCommand(routineUnpublishCmd) }
