package main

// crewServicesCmd is the CLI counterpart to GET /api/v1/crews/{crewId}/services
// — the live-Docker-read inventory of a crew's sidecar containers (status,
// ports, inferred datastore type), as opposed to a snapshot of what the
// manifest last configured.

import (
	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

var crewServicesCmd = &cobra.Command{
	Use:   "services <crew-slug-or-id>",
	Short: "Show a crew's live sidecar service inventory (status / ports / type)",
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

		resp, err := client.Get("/api/v1/crews/" + crewID + "/services")
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var out struct {
			Services []struct {
				Name   string   `json:"name"`
				Image  string   `json:"image"`
				Type   string   `json:"type"`
				Status string   `json:"status"`
				Ports  []string `json:"ports"`
			} `json:"services"`
		}
		if err := cli.ReadJSON(resp, &out); err != nil {
			return err
		}

		f := newFormatter()
		headers := []string{"NAME", "TYPE", "STATUS", "IMAGE", "PORTS"}
		var rows [][]string
		for _, s := range out.Services {
			rows = append(rows, []string{s.Name, s.Type, s.Status, s.Image, formatServicePorts(s.Ports)})
		}
		return f.Auto(out.Services, headers, rows)
	},
}

// formatServicePorts joins a service's port list for the table column,
// e.g. ["5432/tcp"] -> "5432/tcp". Empty slice -> "-" so the column
// never renders blank.
func formatServicePorts(ports []string) string {
	if len(ports) == 0 {
		return "-"
	}
	out := ports[0]
	for _, p := range ports[1:] {
		out += ", " + p
	}
	return out
}
