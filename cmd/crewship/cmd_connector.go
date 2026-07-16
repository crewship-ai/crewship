package main

import (
	"fmt"
	"net/url"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

// connectorCmd exposes the generic connector catalogue (manifest-driven
// integrations under internal/connectors) over the CLI. Previously only
// the Composio-specific subtree had commands; the generic catalogue's
// list/get/verify/install endpoints had no CLI caller.

var connectorCmd = &cobra.Command{
	Use:   "connector",
	Short: "Browse, verify, and install generic connectors (manifest-driven integrations)",
	Long: `Connectors are manifest-driven integrations from the built-in catalogue.
Verify checks credentials against the provider without persisting them;
install creates the integration at workspace scope (or crew scope with
--crew).

Examples:
  crewship connector list
  crewship connector get slack
  crewship connector verify slack --field bot_token=xoxb-…
  crewship connector install slack --field bot_token=xoxb-… --crew backend`,
}

var connectorListCmd = &cobra.Command{
	Use:   "list",
	Short: "List the connector catalogue",
	RunE: func(cmd *cobra.Command, _ []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		client := newAPIClient()
		resp, err := client.Get("/api/v1/connectors")
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var rows []struct {
			ID          string `json:"id"`
			Name        string `json:"name"`
			Description string `json:"description"`
			Category    string `json:"category"`
			AuthMode    string `json:"auth_mode"`
		}
		if err := cli.ReadJSON(resp, &rows); err != nil {
			return err
		}
		f := newFormatter()
		headers := []string{"ID", "NAME", "CATEGORY", "AUTH", "DESCRIPTION"}
		table := make([][]string, 0, len(rows))
		for _, r := range rows {
			table = append(table, []string{r.ID, r.Name, r.Category, r.AuthMode, r.Description})
		}
		return f.Auto(rows, headers, table)
	},
}

var connectorGetCmd = &cobra.Command{
	Use:   "get <id>",
	Short: "Show one connector's full manifest",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		client := newAPIClient()
		resp, err := client.Get("/api/v1/connectors/" + url.PathEscape(args[0]))
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var manifest map[string]interface{}
		if err := cli.ReadJSON(resp, &manifest); err != nil {
			return err
		}
		f := newFormatter()
		pairs := [][]string{
			{"ID", str(manifest["id"])},
			{"Name", str(manifest["name"])},
			{"Category", str(manifest["category"])},
			{"Auth mode", str(manifest["auth_mode"])},
			{"Description", str(manifest["description"])},
		}
		return f.AutoDetail(manifest, pairs)
	},
}

var connectorVerifyCmd = &cobra.Command{
	Use:   "verify <id>",
	Short: "Probe credentials against the provider without persisting them",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		fieldPairs, _ := cmd.Flags().GetStringArray("field")
		fields, err := parseKeyValuePairs(fieldPairs, "--field")
		if err != nil {
			return err
		}
		client := newAPIClient()
		resp, err := client.Post("/api/v1/connectors/"+url.PathEscape(args[0])+"/verify",
			map[string]any{"fields": fields})
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var out struct {
			OK      bool   `json:"ok"`
			Message string `json:"message"`
		}
		if err := cli.ReadJSON(resp, &out); err != nil {
			return err
		}
		// stdout is success-only: a failed probe reports exclusively via
		// the returned error (structured envelope on stderr in machine
		// formats) so JSON consumers never see two competing documents.
		f := newFormatter()
		if !out.OK {
			msg := out.Message
			if msg == "" {
				msg = "provider rejected the credentials"
			}
			return fmt.Errorf("verify failed: %s", msg)
		}
		switch f.Format {
		case "json", "yaml", "ndjson":
			return f.Auto(out, nil, nil)
		default:
			cli.PrintSuccess("Credentials verified.")
		}
		return nil
	},
}

var connectorInstallCmd = &cobra.Command{
	Use:   "install <id>",
	Short: "Install a connector as a workspace (or crew) integration",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		fieldPairs, _ := cmd.Flags().GetStringArray("field")
		fields, err := parseKeyValuePairs(fieldPairs, "--field")
		if err != nil {
			return err
		}
		client := newAPIClient()
		body := map[string]any{"fields": fields}
		if crewRef, _ := cmd.Flags().GetString("crew"); crewRef != "" {
			crewID, err := resolveCrewID(client, crewRef)
			if err != nil {
				return err
			}
			body["crew_id"] = crewID
		}
		if name, _ := cmd.Flags().GetString("name"); name != "" {
			body["name"] = name
		}
		resp, err := client.Post("/api/v1/connectors/"+url.PathEscape(args[0])+"/install", body)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var out struct {
			IntegrationID string `json:"integration_id"`
			NextStep      string `json:"next_step"`
			OAuthURL      string `json:"oauth_url"`
		}
		if err := cli.ReadJSON(resp, &out); err != nil {
			return err
		}
		f := newFormatter()
		pairs := [][]string{{"Integration", out.IntegrationID}}
		if out.NextStep != "" {
			pairs = append(pairs, []string{"Next step", out.NextStep})
		}
		if out.OAuthURL != "" {
			pairs = append(pairs, []string{"OAuth URL", out.OAuthURL})
		}
		if err := f.AutoDetail(out, pairs); err != nil {
			return err
		}
		if (f.Format == "table" || f.Format == "") && out.NextStep != "" {
			cli.PrintWarning("Install needs a browser step — open the OAuth URL above to finish.")
		}
		return nil
	},
}

func init() {
	connectorVerifyCmd.Flags().StringArray("field", nil, "credential field as NAME=value (repeatable)")
	connectorInstallCmd.Flags().StringArray("field", nil, "credential field as NAME=value (repeatable)")
	connectorInstallCmd.Flags().String("crew", "", "install at crew scope (crew slug or id); default is workspace scope")
	connectorInstallCmd.Flags().String("name", "", "user-facing label for the integration (default: connector name)")

	connectorCmd.AddCommand(connectorListCmd)
	connectorCmd.AddCommand(connectorGetCmd)
	connectorCmd.AddCommand(connectorVerifyCmd)
	connectorCmd.AddCommand(connectorInstallCmd)
	rootCmd.AddCommand(connectorCmd)
}
