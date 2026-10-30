package main

// crewship agent learning — GET /api/v1/agents/{agentId}/learning, the read
// side of `agent update --self-learning`.

import (
	"net/url"
	"strconv"

	"github.com/spf13/cobra"
)

var agentLearningCmd = &cobra.Command{
	Use:               "learning <agent-slug-or-id>",
	Short:             "Show an agent's self-learning flag and its audit trail",
	Args:              cobra.ExactArgs(1),
	ValidArgsFunction: completeAgentSlug,
	Long: `Show whether self-learning is enabled for the agent, who last changed it,
when, and the recorded reason. To change it, use
'crewship agent update <agent> --self-learning --learning-reason "..."'.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		agentID, err := resolveAgentID(client, args[0])
		if err != nil {
			return err
		}
		var res struct {
			AgentID     string  `json:"agent_id" yaml:"agent_id"`
			Enabled     bool    `json:"enabled" yaml:"enabled"`
			SetByUserID *string `json:"set_by_user_id,omitempty" yaml:"set_by_user_id,omitempty"`
			SetAt       *string `json:"set_at,omitempty" yaml:"set_at,omitempty"`
			Reason      *string `json:"reason,omitempty" yaml:"reason,omitempty"`
		}
		if err := getJSON(client, "/api/v1/agents/"+url.PathEscape(agentID)+"/learning", &res); err != nil {
			return err
		}
		str := func(p *string) string {
			if p == nil {
				return "-"
			}
			return *p
		}
		return newFormatter().AutoDetail(res, [][]string{
			{"Agent", res.AgentID},
			{"Enabled", strconv.FormatBool(res.Enabled)},
			{"Set by", str(res.SetByUserID)},
			{"Set at", str(res.SetAt)},
			{"Reason", str(res.Reason)},
		})
	},
}

func init() {
	agentCmd.AddCommand(agentLearningCmd)
}
