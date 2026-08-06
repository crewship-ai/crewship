package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/crewship-ai/crewship/internal/provider"
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
			// RuntimeContract is absent when the provider has no opinion, and
			// absent is not "current" — nothing is printed in that case.
			RuntimeContract string `json:"runtime_contract"`
			// ConfigDrift names the per-crew settings the RUNNING container
			// does not carry — its memory / CPU limits, which are applied at
			// container create and nowhere else (#1681). Empty whenever the
			// container matches its crew, and whenever the provider reported
			// no limits to compare against.
			ConfigDrift []struct {
				Field      string  `json:"field"`
				Configured float64 `json:"configured"`
				Effective  float64 `json:"effective"`
			} `json:"config_drift"`
		}
		if err := cli.ReadJSON(resp, &status); err != nil {
			return err
		}

		fmt.Printf("%sContainer:%s %s%s%s\n", cli.Bold, cli.Reset,
			containerStatusColor(status.Status), sanitizeTerminal(status.Status), cli.Reset)
		if status.Uptime != "" {
			fmt.Printf("%sStarted:%s   %s\n", cli.Bold, cli.Reset, sanitizeTerminal(status.Uptime))
		}
		// The crew container is created once and reused for days, so a merged
		// change to how it is created — the init process, the core-dump
		// ulimit, supplementary groups, swap, /dev/shm — reaches only crews
		// whose container is recreated afterwards. This is where an operator
		// finds out which those are (#1642).
		switch status.RuntimeContract {
		case provider.RuntimeContractCurrent:
			fmt.Printf("%sConfig:%s    %scurrent%s\n", cli.Bold, cli.Reset, cli.Green, cli.Reset)
		case provider.RuntimeContractStale:
			fmt.Printf("%sConfig:%s    %sfrom an older build%s\n", cli.Bold, cli.Reset, cli.Yellow, cli.Reset)
			fmt.Printf("           This container was created before the current container configuration and does not\n")
			fmt.Printf("           carry the settings added since — including hardening that is applied at create time\n")
			fmt.Printf("           and nowhere else. It picks them up the next time the container is recreated: an\n")
			fmt.Printf("           idle-TTL stop, or `crewship crew restart-agents %s`.\n", sanitizeTerminal(args[0]))
		}
		// The same shape of gap, one level down: not "this container is older
		// than the build" but "this container is older than THIS CREW'S
		// configuration". A memory or CPU edit is written to the row, reported
		// by `crew get`, and applied to the cgroup only at create time — so
		// until the container is recreated the crew runs under the old
		// figure, and this is the surface that says so (#1681).
		if len(status.ConfigDrift) > 0 {
			fmt.Printf("%sLimits:%s    %sconfigured, not yet in effect%s\n", cli.Bold, cli.Reset, cli.Yellow, cli.Reset)
			for _, d := range status.ConfigDrift {
				fmt.Printf("           %s: %s configured, container running with %s\n",
					sanitizeTerminal(d.Field), formatLimit(d.Configured), formatLimit(d.Effective))
			}
			fmt.Printf("           Container limits are applied when the container is created and cannot be changed\n")
			fmt.Printf("           on a running one. The crew picks these up the next time it is recreated: an\n")
			fmt.Printf("           idle-TTL stop, or `crewship crew restart-agents %s`.\n", sanitizeTerminal(args[0]))
		}
		return nil
	},
}

// formatLimit renders a limit figure the way it was written: 8192 rather than
// 8192.000000, and 1.5 rather than 2, because container_cpus is fractional and
// rounding it in the report would make a real drift look like a display bug.
func formatLimit(v float64) string {
	return fmt.Sprintf("%g", v)
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
