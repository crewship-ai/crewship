package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"
)

// Composio managed-integration commands live under `crewship integration
// composio`. Slice 2a ships the read-only inventory mirror of
// GET /api/v1/integrations/composio/inventory (API↔CLI parity).

// composioInventoryResponse mirrors the server's wire shape.
type composioInventoryResponse struct {
	Enabled     bool                     `json:"enabled"`
	AuthConfigs []composioAuthConfig     `json:"auth_configs"`
	Users       []composioUserInventory  `json:"users"`
}

type composioToolkit struct {
	Slug string `json:"slug"`
	Logo string `json:"logo,omitempty"`
}

type composioAuthConfig struct {
	ID      string          `json:"id"`
	Name    string          `json:"name"`
	Status  string          `json:"status"`
	Toolkit composioToolkit `json:"toolkit"`
}

type composioConnectedAccount struct {
	ID      string          `json:"id"`
	UserID  string          `json:"user_id"`
	Status  string          `json:"status"`
	Toolkit composioToolkit `json:"toolkit"`
}

type composioUserInventory struct {
	UserID            string                     `json:"user_id"`
	ConnectedAccounts []composioConnectedAccount `json:"connected_accounts"`
}

var composioCmd = &cobra.Command{
	Use:   "composio",
	Short: "Composio managed integrations (catalog + connected users)",
}

var composioInventoryCmd = &cobra.Command{
	Use:   "inventory",
	Short: "Show the Composio connector catalog and connected accounts per user",
	Long: "Lists the project's auth-config catalog (connectable apps) and every " +
		"connected account grouped by Composio user_id. This is the operator " +
		"inventory; agents are scoped to a single user_id and never see the full list.",
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}

		var inv composioInventoryResponse
		if err := getJSON(client, "/api/v1/integrations/composio/inventory", &inv); err != nil {
			return err
		}

		f := newFormatter()
		// Structured output: dump the whole response once.
		if f.Format == "json" || f.Format == "yaml" {
			return f.Auto(inv, nil, nil)
		}

		if !inv.Enabled {
			fmt.Println("Composio is not configured on this server (set COMPOSIO_API_KEY).")
			return nil
		}

		fmt.Println("Connector catalog (auth configs):")
		catRows := make([][]string, 0, len(inv.AuthConfigs))
		for _, ac := range inv.AuthConfigs {
			catRows = append(catRows, []string{ac.Toolkit.Slug, ac.Name, ac.Status})
		}
		f.Table([]string{"TOOLKIT", "NAME", "STATUS"}, catRows)

		fmt.Println("\nConnected users:")
		userRows := make([][]string, 0, len(inv.Users))
		for _, u := range inv.Users {
			userRows = append(userRows, []string{u.UserID, strings.Join(distinctToolkits(u), ","), fmt.Sprintf("%d", len(u.ConnectedAccounts))})
		}
		f.Table([]string{"USER_ID", "APPS", "ACCOUNTS"}, userRows)
		return nil
	},
}

// distinctToolkits returns the sorted unique toolkit slugs a user has
// connected, for the compact "APPS" column.
func distinctToolkits(u composioUserInventory) []string {
	seen := map[string]struct{}{}
	for _, a := range u.ConnectedAccounts {
		seen[a.Toolkit.Slug] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for s := range seen {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

func init() {
	composioCmd.AddCommand(composioInventoryCmd)
	integrationCmd.AddCommand(composioCmd)
}
