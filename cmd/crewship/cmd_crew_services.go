package main

// crewServicesCmd is the CLI counterpart to GET /api/v1/crews/{crewId}/services
// — the live-Docker-read inventory of a crew's sidecar containers (status,
// ports, inferred datastore type), as opposed to a snapshot of what the
// manifest last configured.

import (
	"fmt"
	"github.com/spf13/cobra"
	"net/url"
	"strconv"

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
				Name   string   `json:"name" yaml:"name"`
				Image  string   `json:"image" yaml:"image"`
				Type   string   `json:"type" yaml:"type"`
				Status string   `json:"status" yaml:"status"`
				Ports  []string `json:"ports" yaml:"ports"`
			} `json:"services" yaml:"services"`
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

var crewServiceStatesCmd = &cobra.Command{
	Use: "service-states <crew-slug-or-id>", Short: "Show requested and reconciled service state", Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()
		id, err := resolveCrewID(client, args[0])
		if err != nil {
			return err
		}
		resp, err := client.Get("/api/v1/crews/" + url.PathEscape(id) + "/service-states")
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var out struct {
			Services []struct {
				Name          string `json:"name" yaml:"name"`
				DesiredState  string `json:"desired_state" yaml:"desired_state"`
				ObservedState string `json:"observed_state" yaml:"observed_state"`
				Version       int64  `json:"version" yaml:"version"`
				LastError     string `json:"last_error,omitempty" yaml:"last_error,omitempty"`
			} `json:"services" yaml:"services"`
		}
		if err := cli.ReadJSON(resp, &out); err != nil {
			return err
		}
		rows := [][]string{}
		for _, s := range out.Services {
			rows = append(rows, []string{s.Name, s.DesiredState, s.ObservedState, strconv.FormatInt(s.Version, 10), s.LastError})
		}
		return newFormatter().Auto(out.Services, []string{"NAME", "REQUESTED", "OBSERVED", "VERSION", "ERROR"}, rows)
	},
}
var crewServiceStateCmd = &cobra.Command{
	Use: "service-state <crew-slug-or-id> <service> <running|stopped> <expected-version>", Short: "Persist a service's requested state across restart", Args: cobra.ExactArgs(4),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		version, err := strconv.ParseInt(args[3], 10, 64)
		if err != nil || version < 0 || (args[2] != "running" && args[2] != "stopped") {
			return fmt.Errorf("state must be running/stopped and version must be nonnegative")
		}
		client := newAPIClient()
		id, err := resolveCrewID(client, args[0])
		if err != nil {
			return err
		}
		resp, err := client.Put("/api/v1/crews/"+url.PathEscape(id)+"/services/"+url.PathEscape(args[1])+"/state", map[string]any{"desired_state": args[2], "expected_version": version})
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		cli.PrintSuccess("Service intent saved; use crew service-states to check reconciliation.")
		return nil
	},
}
