package main

// crewship persona suggest — POST /api/v1/agents/{agentId}/persona/suggest.

import (
	"fmt"
	"net/url"
	"strconv"

	"github.com/spf13/cobra"
)

var personaSuggestCmd = &cobra.Command{
	Use:   "suggest <agent-slug>",
	Short: "Propose a PERSONA.md change for an agent (policy-gated)",
	Long: `Submit a candidate persona for the agent. Depending on the crew's autonomy
policy and the agent's self-learning flag, the proposal is queued for operator
approval or applied immediately.

Example:
  crewship persona suggest viktor --content "$(cat PERSONA.md)" --rationale "tighter tone"`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		content, _ := cmd.Flags().GetString("content")
		rationale, _ := cmd.Flags().GetString("rationale")
		if content == "" {
			return fmt.Errorf("--content is required")
		}
		client, err := requireAPIClientWithWorkspace()
		if err != nil {
			return err
		}
		agentID, err := resolveAgentID(client, args[0])
		if err != nil {
			return err
		}
		var res struct {
			AgentID  string `json:"agent_id" yaml:"agent_id"`
			Decision string `json:"decision" yaml:"decision"`
			Bytes    int    `json:"bytes" yaml:"bytes"`
			Applied  bool   `json:"applied" yaml:"applied"`
			Gate     string `json:"self_learning_gate,omitempty" yaml:"self_learning_gate,omitempty"`
		}
		body := map[string]string{"content": content, "rationale": rationale}
		if err := postJSON(client, "/api/v1/agents/"+url.PathEscape(agentID)+"/persona/suggest", body, &res); err != nil {
			return err
		}
		pairs := [][]string{
			{"Decision", res.Decision},
			{"Applied", strconv.FormatBool(res.Applied)},
			{"Bytes", strconv.Itoa(res.Bytes)},
		}
		if res.Gate != "" {
			pairs = append(pairs, []string{"Self-learning gate", res.Gate})
		}
		return newFormatter().AutoDetail(res, pairs)
	},
}

func init() {
	personaSuggestCmd.Flags().String("content", "", "Proposed PERSONA.md content (required)")
	personaSuggestCmd.Flags().String("rationale", "", "Why the change is proposed")
	personaCmd.AddCommand(personaSuggestCmd)
}
