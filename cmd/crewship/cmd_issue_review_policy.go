package main

// `crewship issue review-policy` — the CLI half of
// PUT /api/v1/crews/{crewId}/issues/{identifier}/review-policy.
//
// Whether a person has to accept the work before an issue can be called done
// is a decision an operator (and an agent driving the CLI) must be able to
// take without a browser — every endpoint gets a command, and this one had
// none: the web issue panel was its only door.
//
// The flag is deliberately explicit. `--required` and `--required=false` are
// two different instructions, and a bare `issue review-policy ENG-4` is a
// refusal rather than a guess, because both directions are consequential:
// turning acceptance off removes a gate somebody put there on purpose.

import (
	"fmt"
	"net/url"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

var issueReviewPolicyCmd = &cobra.Command{
	Use:   "review-policy <identifier>",
	Short: "Require or drop human acceptance before an issue's work counts as done",
	Long: `Set whether this issue needs a person to accept the work.

The policy can only change while the issue is still TODO or BACKLOG and has
no live assignment: once work starts, the server answers 409 rather than
moving the goalposts under the worker.

The revision defaults to the issue's current work revision, so a plain call
is safe; pass --revision to pin the value you actually read, and a concurrent
change is refused instead of silently overwritten.`,
	Example: `  crewship issue review-policy ENG-4 --required
  crewship issue review-policy ENG-4 --required=false
  crewship issue review-policy ENG-4 --required --revision 3 -f json`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if !cmd.Flags().Changed("required") {
			return fmt.Errorf("--required is required: pass --required to ask for acceptance, --required=false to drop it")
		}
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()
		issue, err := fetchIssue(client, args[0])
		if err != nil {
			return err
		}
		required, _ := cmd.Flags().GetBool("required")
		revision := issue.WorkRevision
		if cmd.Flags().Changed("revision") {
			revision, _ = cmd.Flags().GetInt("revision")
		}
		resp, err := client.Put(
			fmt.Sprintf("/api/v1/crews/%s/issues/%s/review-policy",
				issue.CrewID, url.PathEscape(derefStr(issue.Identifier, issue.ID))),
			map[string]any{"revision": revision, "client_review_required": required})
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var out map[string]any
		if err := cli.ReadJSON(resp, &out); err != nil {
			return err
		}
		return resolvedFormatter(cmd).AutoHuman(out, func() {
			if required {
				cli.PrintSuccess(args[0] + " now needs your acceptance before it is done")
				return
			}
			cli.PrintSuccess(args[0] + " no longer needs your acceptance")
		})
	},
}

func init() {
	issueReviewPolicyCmd.Flags().Bool("required", false, "Require human acceptance (--required=false drops it)")
	issueReviewPolicyCmd.Flags().Int("revision", 0, "Expected work revision (defaults to the issue's current one)")
	issueCmd.AddCommand(issueReviewPolicyCmd)
}
