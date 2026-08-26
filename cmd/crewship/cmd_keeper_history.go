package main

import (
	"fmt"
	"net/url"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// keeperRequestEvent mirrors one row of GET
// /api/v1/admin/keeper/requests/{requestId}/events — the append-only keeper
// transition ledger (issue #1369).
type keeperRequestEvent struct {
	Seq         int     `json:"seq"`
	State       string  `json:"state"`
	RequestType *string `json:"request_type,omitempty"`
	AgentName   string  `json:"agent_name,omitempty"`
	CredName    string  `json:"credential_name,omitempty"`
	Intent      *string `json:"intent,omitempty"`
	Command     *string `json:"command,omitempty"`
	Reason      *string `json:"reason,omitempty"`
	RiskScore   *int    `json:"risk_score,omitempty"`
	ExitCode    *int    `json:"exit_code,omitempty"`
	ActorType   string  `json:"actor_type"`
	ActorID     *string `json:"actor_id,omitempty"`
	RecordedAt  string  `json:"recorded_at"`
}

var keeperHistoryCmd = &cobra.Command{
	Use:   "history <request-id>",
	Short: "Show the append-only decision history for one Keeper request (requires ADMIN or OWNER)",
	Long: `Print every recorded state TRANSITION for a Keeper request, oldest first.

'crewship keeper requests' shows what was DECIDED. This shows how the request got
there — the history that used to be destroyed by an in-place UPDATE:

  * that the request was PENDING at all, and for how long
  * who caused each transition (the agent asking, the Keeper deciding, an
    operator resolving, or the system suppressing a duplicate)
  * the exit code of a command an ALLOW actually ran

The ledger is append-only at the database level: a decision cannot be rewritten,
only superseded by a further transition — which shows up here as an extra row
rather than a silently changed value.

Backed by GET /api/v1/admin/keeper/requests/{requestId}/events.

Examples:
  crewship keeper history kpr_exe_ab12cd34ef56
  crewship keeper history kpr_exe_ab12cd34ef56 --format json | jq '.[].state'`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}

		var events []keeperRequestEvent
		path := "/api/v1/admin/keeper/requests/" + url.PathEscape(args[0]) + "/events"
		if err := getJSON(client, path, &events); err != nil {
			return keeperPermissionHint(err)
		}

		if len(events) == 0 {
			// An empty list is also what a foreign-workspace request id returns —
			// the endpoint deliberately does not distinguish "no such request" from
			// "not yours", so the message must not claim it does not exist.
			//
			// Both of these lines are guidance for a person and neither is a
			// transition, so under a machine format the answer is `[]`. The
			// command's own help documents `--format json | jq '.[].state'`,
			// which this used to break on exactly the ambiguous case the note
			// exists to explain.
			return resolvedFormatter(cmd).AutoHuman(events, func() {
				fmt.Println("No recorded transitions for this request in the current workspace.")
				fmt.Printf("%sNote:%s requests raised before the ledger migration have their current\n"+
					"state backfilled but no intermediate history.\n", cli.Yellow, cli.Reset)
			})
		}

		headers := []string{"SEQ", "STATE", "ACTOR", "RISK", "EXIT", "RECORDED AT", "REASON"}
		var rows [][]string
		for _, e := range events {
			actor := e.ActorType
			if e.ActorID != nil && *e.ActorID != "" {
				actor += ":" + truncateString(*e.ActorID, 12)
			}
			risk := "-"
			if e.RiskScore != nil {
				risk = fmt.Sprintf("%d", *e.RiskScore)
			}
			exit := "-"
			if e.ExitCode != nil {
				exit = fmt.Sprintf("%d", *e.ExitCode)
			}
			reason := "-"
			if e.Reason != nil && *e.Reason != "" {
				reason = truncateString(*e.Reason, 48)
			}
			rows = append(rows, []string{
				fmt.Sprintf("%d", e.Seq), e.State, actor, risk, exit, e.RecordedAt, reason,
			})
		}
		return newFormatter().Auto(events, headers, rows)
	},
}

func init() {
	keeperCmd.AddCommand(keeperHistoryCmd)
}
