package main

import (
	"fmt"
	"net/url"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

// agentChannelsCmd is the mirror of `notifychannel agents list`.
//
// That command answers "who may post to THIS CHANNEL". This one answers "what
// can THIS AGENT reach". Both directions matter when auditing: the first tells
// you a channel is over-shared, the second tells you an agent is
// over-privileged, and neither is derivable from the other without walking
// every channel in the workspace.
//
// Read-only on purpose. Granting stays on the channel — the authority to let
// an agent speak belongs to whoever owns the destination, not to whoever is
// looking at the agent.
var agentChannelsCmd = &cobra.Command{
	Use:   "channels <agent-id>",
	Short: "List the notification channels an agent may send to",
	Long: `Show which notification channels this agent is allowed to post to on its
own initiative.

Default-deny: an agent has no channels until a human pairs it with one, so an
empty list is the normal state rather than a problem.

The destination is deliberately not shown — this says THAT a channel exists
and of what kind, never where it points. To grant or revoke:

  crewship notifychannel agents allow <channel-id> --agent <agent-id>
  crewship notifychannel agents deny  <channel-id> --agent <agent-id>`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()
		resp, err := client.Get(
			"/api/v1/agents/" + url.PathEscape(args[0]) + "/notification-channels",
		)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var body struct {
			Channels []struct {
				ID       string `json:"id"`
				Type     string `json:"type"`
				Provider string `json:"provider"`
				Enabled  bool   `json:"enabled"`
			} `json:"channels"`
		}
		if err := cli.ReadJSON(resp, &body); err != nil {
			return err
		}
		f := newFormatter()
		headers := []string{"CHANNEL", "TYPE", "PROVIDER", "ENABLED"}
		rows := make([][]string, 0, len(body.Channels))
		for _, c := range body.Channels {
			rows = append(rows, []string{c.ID, c.Type, c.Provider, fmt.Sprintf("%v", c.Enabled)})
		}
		return f.Auto(body.Channels, headers, rows)
	},
}

func init() {
	agentCmd.AddCommand(agentChannelsCmd)
}
