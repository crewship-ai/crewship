package main

// agentCredentialReadinessCmd is the CLI counterpart to
// GET /api/v1/agents/{agentId}/credential-readiness — the read-only report of
// whether a credential the runtime will actually deliver authenticates this
// agent's model (#2183).
//
// It exists because `agent credentials` answers a different question. That
// listing is the union of everything that reaches the agent, so one crew
// GH_TOKEN binding makes it non-empty for every agent in the crew — and the
// agent whose first run then fails at the first model call looks, on that
// listing, exactly like one that works (#2169). The classification here is
// the orchestrator's own: the same functions that decide what the sidecar
// CredStore holds and what the env carries.
//
// Report only. It assigns nothing; the output ends in the command that
// would.

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

type agentModelCredentialOut struct {
	State          string `json:"state"`
	CredentialName string `json:"credential_name,omitempty"`
	CredentialID   string `json:"credential_id,omitempty"`
	Source         string `json:"source,omitempty"`
	Delivery       string `json:"delivery,omitempty"`
	Provider       string `json:"provider,omitempty"`
}

type agentCredentialReadinessOut struct {
	AgentID         string                  `json:"agent_id"`
	AgentSlug       string                  `json:"agent_slug"`
	Adapter         string                  `json:"adapter"`
	ModelCredential agentModelCredentialOut `json:"model_credential"`
	Notes           []string                `json:"notes"`
}

var agentCredentialReadinessCmd = &cobra.Command{
	Use:   "credential-readiness <agent>",
	Short: "Report whether a delivered credential authenticates the agent's model",
	Long: `Report whether a credential the runtime will actually deliver authenticates this
agent's model provider.

'agent credentials' lists everything that reaches the agent; this command asks the
narrower question its first run will ask. The answer is computed by the runtime's
own delivery rules — a Claude Code agent is served by the sidecar proxy (its env
holds a dummy key by design), a Codex login lands in a file, an OpenCode key is
written to the env — so "ready" here means the run's own selector finds it.

States: ready, missing, unknown. Unknown is not a failure: the server says
nothing for an adapter or provider it has no opinion about.

This command only reports. Granting a credential is a separate step:

  crewship credential assign <credential> <agent>`,
	Example: `  crewship agent credential-readiness casey
  crewship agent credential-readiness casey --format json`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		client := newAPIClient()
		agentID, err := resolveAgentID(client, args[0])
		if err != nil {
			return err
		}

		resp, err := client.Get("/api/v1/agents/" + agentID + "/credential-readiness")
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var out agentCredentialReadinessOut
		if err := cli.ReadJSON(resp, &out); err != nil {
			return err
		}

		f := newFormatter()
		return f.AutoHuman(out, func() {
			label := out.AgentSlug
			if label == "" {
				label = args[0]
			}
			mc := out.ModelCredential
			provider := mc.Provider
			if provider == "" {
				provider = "-"
			}
			switch mc.State {
			case "ready":
				fmt.Printf("Agent %s (%s): model credential ready.\n\n", label, out.Adapter)
				name := mc.CredentialName
				if name == "" {
					name = "(name withheld)"
				}
				f.Table([]string{"PROVIDER", "CREDENTIAL", "SOURCE", "DELIVERY"},
					[][]string{{provider, name, mc.Source, mc.Delivery}})
			case "missing":
				fmt.Printf("Agent %s (%s): NO model credential reaches this agent for %s.\n", label, out.Adapter, provider)
				fmt.Printf("Its first run will fail at the first model call.\n")
			default:
				fmt.Printf("Agent %s (%s): no opinion on the model credential.\n", label, out.Adapter)
			}
			if len(out.Notes) > 0 {
				fmt.Printf("\n%s\n", strings.Join(out.Notes, "\n"))
			}
			if mc.State == "missing" {
				// Point at the fix without performing it.
				fmt.Printf("\nGrant one, then re-check:\n"+
					"  crewship credential list\n"+
					"  crewship credential assign <credential> %s\n", label)
			}
		})
	},
}
