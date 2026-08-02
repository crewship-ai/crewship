package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

var (
	flagKeeperAskAgent      string
	flagKeeperAskCrew       string
	flagKeeperAskCredential string
	flagKeeperAskIntent     string
)

type keeperAskResult struct {
	RequestID string `json:"request_id"`
	Decision  string `json:"decision"`
	Reason    string `json:"reason"`
	RiskScore int    `json:"risk_score"`
}

var keeperAskCmd = &cobra.Command{
	Use:   "ask --agent <id> --crew <id> --credential <name> --intent <text>",
	Short: "Put a credential request to the judge yourself (requires OWNER or ADMIN)",
	Long: `Ask this instance's judge how it would rule on a credential request, without
waiting for an agent to want one.

'keeper judge test' proves the judge answers, but it asks ONE fixed scenario — an
L1 npm token for a CI bot. This asks yours: your credential, your tier, your
wording.

It is the SAME path an agent's request travels: the tier floors apply, the
decision lands on the audit trail, an escalation reaches the inbox, and the
health window records it. So the verdict you see is the verdict an agent would
have got, not an approximation of it.

Two things it is good for beyond curiosity:

  Tuning. Change the judge profile, re-ask the same question, compare. The
  profile in force is stamped on each decision, so 'keeper requests' tells you
  which regime produced which answer.

  Ground truth. 'keeper eval' scores candidate models against decisions a HUMAN
  ruled on, and it needs about twenty before it will quote a rate. Escalations
  and high-risk denials land in the inbox; resolving them there is what creates
  those decisions. This is how you produce varied cases to resolve instead of
  waiting for them to happen.

Examples:
  crewship keeper ask --agent agt_riley --crew crew_ops \
    --credential PROD_DB_ADMIN \
    --intent "Run the approved schema migration on the orders table during tonight's change window"

  crewship keeper ask --agent agt_riley --crew crew_ops --credential npm-token --intent "publish"`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		for _, f := range []struct{ name, val string }{
			{"--agent", flagKeeperAskAgent},
			{"--crew", flagKeeperAskCrew},
			{"--credential", flagKeeperAskCredential},
			{"--intent", flagKeeperAskIntent},
		} {
			if strings.TrimSpace(f.val) == "" {
				return fmt.Errorf("%s is required", f.name)
			}
		}

		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}

		// Slug or id, both accepted. An operator types "riley", not a cuid, and
		// the credential lookup joins agent_credentials on the ID — so passing the
		// slug straight through produced "credential not found", which points at
		// the wrong thing entirely.
		agentID, err := resolveAgentID(client, strings.TrimSpace(flagKeeperAskAgent))
		if err != nil {
			return err
		}
		crewID, err := resolveCrewID(client, strings.TrimSpace(flagKeeperAskCrew))
		if err != nil {
			return err
		}

		// The workspace is taken from the session server-side; sending it here
		// would be ignored, and pretending otherwise would invite somebody to
		// think they could target another one.
		var out keeperAskResult
		if err := postJSON(client, "/api/v1/admin/keeper/ask", map[string]any{
			"requesting_agent_id": agentID,
			"requesting_crew_id":  crewID,
			"credential_name":     strings.TrimSpace(flagKeeperAskCredential),
			"intent":              strings.TrimSpace(flagKeeperAskIntent),
		}, &out); err != nil {
			return keeperPermissionHint(err)
		}

		return newFormatter().AutoHuman(out, func() {
			colour := cli.Green
			switch out.Decision {
			case "DENY":
				colour = cli.Red
			case "ESCALATE":
				colour = cli.Yellow
			}
			fmt.Printf("%s%s%s  risk %d\n", colour, out.Decision, cli.Reset, out.RiskScore)
			if out.Reason != "" {
				fmt.Printf("  %s\n", out.Reason)
			}
			if out.RequestID != "" {
				fmt.Printf("  %sid %s — full record: crewship keeper history %s%s\n",
					cli.Dim, out.RequestID, out.RequestID, cli.Reset)
			}
			if out.Decision == "ESCALATE" {
				fmt.Printf("  %sWaiting for a person: crewship inbox list%s\n", cli.Dim, cli.Reset)
			}
		})
	},
}

func init() {
	f := keeperAskCmd.Flags()
	f.StringVar(&flagKeeperAskAgent, "agent", "", "agent slug or id the request is made on behalf of")
	f.StringVar(&flagKeeperAskCrew, "crew", "", "crew slug or id that agent belongs to")
	f.StringVar(&flagKeeperAskCredential, "credential", "", "credential name, as `credential list` shows it")
	f.StringVar(&flagKeeperAskIntent, "intent", "", "what the credential is for — the tier's minimum length applies")
}
