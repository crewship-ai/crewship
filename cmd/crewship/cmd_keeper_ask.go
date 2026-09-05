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

--credential takes either spelling: the credential's NAME as 'credential list'
prints it, or the env-var SLOT that agent reads it under, as
'crewship agent credentials <agent>' prints it. Whichever you pass, the
credential has to be reachable BY THAT AGENT — the judge rules on a request an
agent could actually make, so an unassigned credential is refused here rather
than judged.

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

		// --credential is resolved HERE, against what this agent can actually
		// reach, rather than passed through for the server to match.
		//
		// The server matches the string against agent_credentials.env_var_name
		// — the per-agent ENV-VAR SLOT — and nothing else. So the two spellings
		// this command's own help documents behaved differently:
		// `--credential PROD_DB_DSN` (a slot) worked and `--credential
		// prod-db-dsn` (the credential's NAME, the one `credential list`
		// prints) came back "credential not found for name: prod-db-dsn" about
		// a credential that plainly exists. Resolving to an id here makes both
		// spellings work and lets the failure say which of the three possible
		// things actually went wrong.
		body := map[string]any{
			"requesting_agent_id": agentID,
			"requesting_crew_id":  crewID,
			"intent":              strings.TrimSpace(flagKeeperAskIntent),
		}
		credRef := strings.TrimSpace(flagKeeperAskCredential)
		credID, err := resolveKeeperCredential(client, agentID, flagKeeperAskAgent, credRef)
		if err != nil {
			return err
		}
		if credID != "" {
			body["credential_id"] = credID
		} else {
			// Nothing to resolve against (the agent's credential list was
			// unreadable). Send the string as before rather than refusing:
			// the server's own slot lookup is still a working path.
			body["credential_name"] = credRef
		}

		// The workspace is taken from the session server-side; sending it here
		// would be ignored, and pretending otherwise would invite somebody to
		// think they could target another one.
		var out keeperAskResult
		if err := postJSON(client, "/api/v1/admin/keeper/ask", body, &out); err != nil {
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

// agentCredentialRef is one row of GET /api/v1/agents/{id}/credentials: what
// this agent can actually reach, under both names it goes by.
type agentCredentialRef struct {
	CredentialID   string `json:"credential_id"`
	CredentialName string `json:"credential_name"`
	EnvVarName     string `json:"env_var_name"`
	GrantSource    string `json:"grant_source"`
}

// resolveKeeperCredential turns --credential into a credential id.
//
// It accepts BOTH spellings the command's help documents, because both are
// real and neither is wrong: an agent asks for the env-var slot it was going
// to read (PROD_DB_ADMIN), an operator types the name `credential list`
// prints (prod-db-dsn). The slot is tried first — that is what the server
// matched before, and a slot the agent is about to use is the more specific
// answer when a workspace happens to contain both spellings.
//
// Returns ("", nil) only when the agent's credential list could not be read
// at all; the caller then falls back to the server-side lookup rather than
// refusing a request over a failed enrichment.
//
// The error paths are the point. "credential not found for name: X" about a
// credential that `credential list` plainly shows is worse than no message:
// it sends the operator to look for a missing row instead of at the
// assignment that is actually absent.
func resolveKeeperCredential(client *cli.Client, agentID, agentRef, credRef string) (string, error) {
	resp, err := client.Get(fmt.Sprintf("/api/v1/agents/%s/credentials", agentID))
	if err != nil {
		return "", nil
	}
	if err := cli.CheckError(resp); err != nil {
		return "", nil
	}
	var reachable []agentCredentialRef
	if err := cli.ReadJSON(resp, &reachable); err != nil {
		return "", nil
	}

	for _, c := range reachable {
		if c.EnvVarName == credRef {
			return c.CredentialID, nil
		}
	}
	for _, c := range reachable {
		if c.CredentialName == credRef || c.CredentialID == credRef {
			return c.CredentialID, nil
		}
	}

	// Not reachable by this agent. Say which of the two situations it is,
	// because they need different next commands.
	if id, lookupErr := resolveCredentialID(client, credRef); lookupErr == nil && id != "" {
		return "", cli.NotFoundf(
			"credential %q exists in this workspace but is not assigned to agent %s, "+
				"so the judge has nothing to rule on.\n"+
				"  assign it: crewship credential assign %s %s\n"+
				"  what this agent can reach: crewship agent credentials %s",
			credRef, agentRef, credRef, agentRef, agentRef)
	}
	return "", cli.NotFoundf(
		"no credential named %q, and no agent env-var slot by that name either.\n"+
			"  workspace credentials: crewship credential list\n"+
			"  this agent's slots:    crewship agent credentials %s",
		credRef, agentRef)
}

func init() {
	f := keeperAskCmd.Flags()
	f.StringVar(&flagKeeperAskAgent, "agent", "", "agent slug or id the request is made on behalf of")
	f.StringVar(&flagKeeperAskCrew, "crew", "", "crew slug or id that agent belongs to")
	f.StringVar(&flagKeeperAskCredential, "credential", "",
		"credential name (as `credential list` shows it) or the agent's env-var slot (as `crewship agent credentials <agent>` shows it)")
	f.StringVar(&flagKeeperAskIntent, "intent", "", "what the credential is for — the tier's minimum length applies")
}
