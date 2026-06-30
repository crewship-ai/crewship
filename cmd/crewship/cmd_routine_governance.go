package main

import (
	"encoding/json"
	"fmt"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// Routine governance CLI parity for the maker-checker + airbag endpoints:
//   approve  POST /api/v1/workspaces/{ws}/pipelines/{slug}/approve   (MANAGER+)
//   reject   POST /api/v1/workspaces/{ws}/pipelines/{slug}/reject    (MANAGER+)
//   disable  POST /api/v1/workspaces/{ws}/pipelines/{slug}/disable   (OWNER/ADMIN)
//   enable   POST /api/v1/workspaces/{ws}/pipelines/{slug}/enable    (OWNER/ADMIN)
//
// cli.CheckError renders the server's RFC 7807 Problem Details (including the
// 409 "routine is awaiting approval" / "routine is disabled" run refusals and
// the 403 role errors) so the user gets a readable message.

// postRoutineGovernance is the shared POST+decode for the four governance
// verbs. action is the URL sub-segment ("approve"/"reject"/...).
func postRoutineGovernance(slug, action string) error {
	if err := requireAuth(); err != nil {
		return err
	}
	if err := requireWorkspace(); err != nil {
		return err
	}
	client := newAPIClient()
	ws := client.GetWorkspaceID()
	resp, err := client.Post(fmt.Sprintf("/api/v1/workspaces/%s/pipelines/%s/%s", ws, slug, action), nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := cli.CheckError(resp); err != nil {
		return err
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	status, _ := out["status"].(string)
	if status == "" {
		status = action
	}
	fmt.Printf("Routine %s → %s\n", slug, status)
	if cancelled, ok := out["cancelled_runs"].(float64); ok && cancelled > 0 {
		fmt.Printf("  cancelled %d in-flight run(s)\n", int(cancelled))
	}
	return nil
}

var routineApproveCmd = &cobra.Command{
	Use:   "approve <slug>",
	Short: "Approve a proposed routine (MANAGER+) — flips it live",
	Args:  cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		return postRoutineGovernance(args[0], "approve")
	},
}

var routineRejectCmd = &cobra.Command{
	Use:   "reject <slug>",
	Short: "Reject a proposed routine (MANAGER+) — removes it",
	Args:  cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		return postRoutineGovernance(args[0], "reject")
	},
}

var routineDisableCmd = &cobra.Command{
	Use:   "disable <slug>",
	Short: "Disable a routine (OWNER/ADMIN) — airbag: cancels in-flight runs, refuses new ones",
	Args:  cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		return postRoutineGovernance(args[0], "disable")
	},
}

var routineEnableCmd = &cobra.Command{
	Use:   "enable <slug>",
	Short: "Re-enable a disabled routine (OWNER/ADMIN)",
	Args:  cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		return postRoutineGovernance(args[0], "enable")
	},
}

func init() {
	pipelineCmd.AddCommand(routineApproveCmd)
	pipelineCmd.AddCommand(routineRejectCmd)
	pipelineCmd.AddCommand(routineDisableCmd)
	pipelineCmd.AddCommand(routineEnableCmd)
}
