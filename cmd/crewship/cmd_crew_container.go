package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

// crewContainerStatusCmd surfaces a crew's runtime container state — the CLI
// counterpart to GET /api/v1/crews/{crewId}/container-status. It's the quick
// way to confirm a crew's container came back up after a network-policy change
// (which stops the container so it's recreated with the new policy).
var crewContainerStatusCmd = &cobra.Command{
	Use:   "container-status <slug-or-id>",
	Short: "Show a crew's runtime container status (running / stopped / …)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		client := newAPIClient()

		crewID, err := resolveCrewID(client, args[0])
		if err != nil {
			return err
		}

		resp, err := client.Get("/api/v1/crews/" + crewID + "/container-status")
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var status struct {
			CrewID string `json:"crew_id"`
			Status string `json:"status"`
			Uptime string `json:"uptime"`
		}
		if err := cli.ReadJSON(resp, &status); err != nil {
			return err
		}

		fmt.Printf("%sContainer:%s %s%s%s\n", cli.Bold, cli.Reset,
			containerStatusColor(status.Status), sanitizeTerminal(status.Status), cli.Reset)
		if status.Uptime != "" {
			fmt.Printf("%sStarted:%s   %s\n", cli.Bold, cli.Reset, sanitizeTerminal(status.Uptime))
		}
		return nil
	},
}

// containerStatusColor maps a container state to a terminal colour.
func containerStatusColor(state string) string {
	switch state {
	case "running":
		return cli.Green
	case "creating":
		return cli.Blue
	case "stopped", "not_configured":
		return cli.Yellow
	case "error", "unknown":
		return cli.Red
	default:
		return ""
	}
}
