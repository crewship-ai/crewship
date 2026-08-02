package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

var (
	flagKeeperResolveDecision    string
	flagKeeperResolveReason      string
	flagKeeperResolveAdjudicator string
)

type keeperResolveResult struct {
	RequestID string `json:"request_id"`
	Decision  string `json:"decision"`
	Reason    string `json:"reason"`
	DecidedAt string `json:"decided_at"`
}

var keeperResolveCmd = &cobra.Command{
	Use:   "resolve <request-id> --decision allow|deny [--reason <text>]",
	Short: "Rule on an escalated credential request (requires OWNER or ADMIN)",
	Long: `Answer an escalation: the judge asked for a person, and this is the person.

An L4 credential is never granted by the model alone, and L3 can be put behind a
human too ('keeper profile set --escalate-from 3'). Those requests wait here.

Only ALLOW or DENY — this command IS the escalation being answered, so there is
nothing left to escalate to.

Refused when the workspace or the tier requires a second approver and the
escalation was raised by an agent you own. That is not a mistake to work around:
approving your own agent's production request is the case four-eyes exists for,
and the attempt is recorded.

Find the waiting ones with 'crewship inbox list'.

--adjudicator names an AI model that made the judgement instead of you. Use it
when you are applying a model's verdicts at scale to give 'keeper eval' labels:
the ledger then records the decision as a reference adjudication rather than
as yours, and the eval reports it separately from decisions a PERSON made.

That separation is the point. An AI adjudication is a useful label — it answers
"can a small model match a frontier one?", which scales where human rulings do
not — but it is not ground truth, and recorded as one it would make the eval
report agreement with a person about a number that measured agreement with a
model. Omit the flag and the decision is yours, which is the default.

Examples:
  crewship keeper resolve cmsb… --decision deny --reason "no change window declared"
  crewship keeper resolve cmsb… --decision allow`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		decision := strings.ToUpper(strings.TrimSpace(flagKeeperResolveDecision))
		if decision != "ALLOW" && decision != "DENY" {
			return fmt.Errorf("--decision must be allow or deny")
		}
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		var out keeperResolveResult
		if err := postJSON(client,
			"/api/v1/admin/keeper/requests/"+strings.TrimSpace(args[0])+"/resolve",
			map[string]any{
				"decision":    decision,
				"reason":      strings.TrimSpace(flagKeeperResolveReason),
				"adjudicator": strings.TrimSpace(flagKeeperResolveAdjudicator),
			},
			&out); err != nil {
			return keeperPermissionHint(err)
		}
		return newFormatter().AutoHuman(out, func() {
			colour := cli.Green
			if out.Decision == "DENY" {
				colour = cli.Red
			}
			cli.PrintSuccess(fmt.Sprintf("Resolved as %s%s%s.", colour, out.Decision, cli.Reset))
			if out.Reason != "" {
				fmt.Printf("  %s\n", out.Reason)
			}
		})
	},
}

func init() {
	f := keeperResolveCmd.Flags()
	f.StringVar(&flagKeeperResolveDecision, "decision", "", "allow or deny")
	f.StringVar(&flagKeeperResolveReason, "reason", "", "why — recorded on the decision and in the journal")
	f.StringVar(&flagKeeperResolveAdjudicator, "adjudicator", "",
		"name the AI model that made this judgement, when one did instead of you (e.g. reference-model-v1)")
}
