package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
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

// routineGovernanceRoles names who may run each verb, matching the route
// registration in internal/api/router_pipelines.go (roleCreate / roleManage).
var routineGovernanceRoles = map[string]string{
	"approve": "MANAGER, ADMIN or OWNER",
	"reject":  "MANAGER, ADMIN or OWNER",
	"disable": "ADMIN or OWNER",
	"enable":  "ADMIN or OWNER",
}

// routineGovernanceForbidden turns the route middleware's bare "Forbidden"
// into a message that names the role the verb needs. A 403 whose detail says
// something more specific passes through untouched (see isBareForbidden). The
// APIError stays in the chain, so the exit code (4) and the --format json
// error envelope keep their status and detail.
func routineGovernanceForbidden(err error, action string) error {
	var apiErr *cli.APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusForbidden || !isBareForbidden(apiErr.Detail) {
		return err
	}
	return fmt.Errorf("%w: routine %s needs the %s role in this workspace (or a CLI token whose scopes allow routine changes) — ask a workspace admin", err, action, routineGovernanceRoles[action])
}

// postRoutineGovernance is the shared POST+decode for the four governance
// verbs. action is the URL sub-segment ("approve"/"reject"/...).
func postRoutineGovernance(cmd *cobra.Command, slug, action string) error {
	if err := requireAuth(); err != nil {
		return err
	}
	if err := requireWorkspace(); err != nil {
		return err
	}
	client := newAPIClient()
	ws := client.GetWorkspaceID()
	resp, err := client.Post(fmt.Sprintf("/api/v1/workspaces/%s/pipelines/%s/%s", url.PathEscape(ws), url.PathEscape(slug), action), nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := cli.CheckError(resp); err != nil {
		return routineGovernanceForbidden(err, action)
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return resolvedFormatter(cmd).AutoHuman(out, func() {
		status, _ := out["status"].(string)
		if status == "" {
			status = action
		}
		fmt.Printf("Routine %s → %s\n", slug, status)
		if cancelled, ok := out["cancelled_runs"].(float64); ok && cancelled > 0 {
			fmt.Printf("  cancelled %d in-flight run(s)\n", int(cancelled))
		}
	})
}

var routineApproveCmd = &cobra.Command{
	Use:   "approve <slug>",
	Short: "Approve a proposed routine (MANAGER+) — flips it live",
	Long: `Approve a routine that a save left as "proposed" because it added a risk
factor (http/egress, code runtime, credentials, unmet integrations). The routine
becomes active and its inbox review item is resolved.

Needs the MANAGER, ADMIN or OWNER role. A routine that is not awaiting approval
(already active, or disabled) is refused with exit code 5; use "routine enable"
to lift a disable.`,
	Example: `  crewship routine approve email-fetch
  crewship routine approve email-fetch -f json   # {"slug":"email-fetch","status":"active"}`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return postRoutineGovernance(cmd, args[0], "approve")
	},
}

var routineRejectCmd = &cobra.Command{
	Use:   "reject <slug>",
	Short: "Reject a proposed routine (MANAGER+) — removes it",
	Long: `Reject a routine that is awaiting approval. The proposal is removed and its
inbox review item is resolved.

Needs the MANAGER, ADMIN or OWNER role. Only a "proposed" routine can be
rejected (exit code 5 otherwise); remove a live routine with "routine delete".`,
	Example: `  crewship routine reject email-fetch
  crewship routine reject email-fetch -f json    # {"slug":"email-fetch","status":"rejected"}`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return postRoutineGovernance(cmd, args[0], "reject")
	},
}

var routineDisableCmd = &cobra.Command{
	Use:   "disable <slug>",
	Short: "Disable a routine (OWNER/ADMIN) — airbag: cancels in-flight runs, refuses new ones",
	Long: `Disable a routine: in-flight runs are cancelled and new runs are refused until
an admin runs "routine enable". A later save does not re-enable it.

Needs the ADMIN or OWNER role. The JSON output reports cancelled_runs.`,
	Example: `  crewship routine disable nightly-sweep
  crewship routine disable nightly-sweep -f json  # {"slug":"nightly-sweep","status":"disabled","cancelled_runs":0}`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return postRoutineGovernance(cmd, args[0], "disable")
	},
}

var routineEnableCmd = &cobra.Command{
	Use:   "enable <slug>",
	Short: "Re-enable a disabled routine (OWNER/ADMIN)",
	Long:  `Return a routine to "active". Needs the ADMIN or OWNER role.`,
	Example: `  crewship routine enable nightly-sweep
  crewship routine enable nightly-sweep -f json   # {"slug":"nightly-sweep","status":"active"}`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return postRoutineGovernance(cmd, args[0], "enable")
	},
}

func init() {
	pipelineCmd.AddCommand(routineApproveCmd)
	pipelineCmd.AddCommand(routineRejectCmd)
	pipelineCmd.AddCommand(routineDisableCmd)
	pipelineCmd.AddCommand(routineEnableCmd)
}
