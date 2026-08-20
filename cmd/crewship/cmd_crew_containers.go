package main

// crewContainersCmd is the CLI counterpart to
// GET /api/v1/crews/{crewId}/containers — the live inventory of every
// container a crew has on the runtime (its agent runtime and its sidecars),
// with state and usage.
//
// `crew container-status` answers about the crew's ONE runtime container via
// crewshipd's IPC socket; `crew services` answers about the sidecars under
// their manifest names. This is the docker-shaped view: real container names,
// one row each, which is what an operator about to run `docker logs` needs.

import (
	"fmt"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

var crewContainersCmd = &cobra.Command{
	Use:   "containers <crew-slug-or-id>",
	Short: "Show a crew's live containers (runtime + sidecars) with state and usage",
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

		resp, err := client.Get("/api/v1/crews/" + crewID + "/containers")
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		// Pointers, so an absent reading stays absent. A runtime that cannot
		// report stats sends null, and printing that as "0.0%" would draw an
		// idle container where nothing was measured.
		var out struct {
			Containers []struct {
				Name       string   `json:"name"`
				Image      string   `json:"image"`
				Kind       string   `json:"kind"`
				Status     string   `json:"status"`
				CPUPercent *float64 `json:"cpu_percent"`
				MemoryMB   *int     `json:"memory_mb"`
				AgentCount *int     `json:"agent_count"`
			} `json:"containers"`
		}
		if err := cli.ReadJSON(resp, &out); err != nil {
			return err
		}

		f := newFormatter()
		headers := []string{"NAME", "KIND", "STATUS", "IMAGE", "CPU", "MEM", "AGENTS"}
		var rows [][]string
		for _, c := range out.Containers {
			rows = append(rows, []string{
				c.Name, c.Kind, c.Status, c.Image,
				formatContainerCPU(c.CPUPercent),
				formatContainerMemory(c.MemoryMB),
				formatContainerAgents(c.AgentCount),
			})
		}
		return f.Auto(out.Containers, headers, rows)
	},
}

// The three formatters below share one rule: an unmeasured number prints as
// "-", never as a zero.
func formatContainerCPU(pct *float64) string {
	if pct == nil {
		return "-"
	}
	return strconv.FormatFloat(*pct, 'f', 1, 64) + "%"
}

func formatContainerMemory(mb *int) string {
	if mb == nil {
		return "-"
	}
	return fmt.Sprintf("%d MB", *mb)
}

func formatContainerAgents(n *int) string {
	if n == nil {
		return "-"
	}
	return strconv.Itoa(*n)
}
