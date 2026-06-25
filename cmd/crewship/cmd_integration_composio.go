package main

import (
	"fmt"
	"net/url"
	"sort"
	"strconv"
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

type composioToolkitCategory struct {
	Name string `json:"name"`
}

type composioToolkitMeta struct {
	Description string                    `json:"description"`
	ToolsCount  int                       `json:"tools_count"`
	Categories  []composioToolkitCategory `json:"categories"`
}

type composioToolkitInfo struct {
	Slug string              `json:"slug"`
	Name string              `json:"name"`
	Meta composioToolkitMeta `json:"meta"`
}

type composioToolkitsResponse struct {
	Enabled  bool                  `json:"enabled"`
	Total    int                   `json:"total"`
	Toolkits []composioToolkitInfo `json:"toolkits"`
}

type composioSettingsResponse struct {
	Configured bool   `json:"configured"`
	Source     string `json:"source"`
	Label      string `json:"label"`
	BaseURL    string `json:"base_url"`
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

var composioToolkitsCmd = &cobra.Command{
	Use:   "toolkits",
	Short: "Browse the Composio app catalog (1000+ connectable apps)",
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		search, _ := cmd.Flags().GetString("search")
		category, _ := cmd.Flags().GetString("category")
		limit, _ := cmd.Flags().GetInt("limit")

		path := "/api/v1/integrations/composio/toolkits"
		q := url.Values{}
		if search != "" {
			q.Set("search", search)
		}
		if category != "" {
			q.Set("category", category)
		}
		if limit > 0 {
			q.Set("limit", strconv.Itoa(limit))
		}
		if enc := q.Encode(); enc != "" {
			path += "?" + enc
		}

		var res composioToolkitsResponse
		if err := getJSON(client, path, &res); err != nil {
			return err
		}

		f := newFormatter()
		if f.Format == "json" || f.Format == "yaml" {
			return f.Auto(res, nil, nil)
		}
		if !res.Enabled {
			fmt.Println("Composio is not configured on this server (set COMPOSIO_API_KEY).")
			return nil
		}
		rows := make([][]string, 0, len(res.Toolkits))
		for _, t := range res.Toolkits {
			cat := ""
			if len(t.Meta.Categories) > 0 {
				cat = t.Meta.Categories[0].Name
			}
			rows = append(rows, []string{t.Slug, t.Name, cat, fmt.Sprintf("%d", t.Meta.ToolsCount)})
		}
		f.Table([]string{"SLUG", "NAME", "CATEGORY", "TOOLS"}, rows)
		fmt.Printf("\nShowing %d of %d apps. Narrow with --search / --category.\n", len(res.Toolkits), res.Total)
		return nil
	},
}

var composioKeyCmd = &cobra.Command{
	Use:   "key",
	Short: "Manage the workspace Composio API key",
}

var composioKeyShowCmd = &cobra.Command{
	Use:   "show",
	Short: "Show whether/how Composio is configured (never prints the key)",
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		var s composioSettingsResponse
		if err := getJSON(client, "/api/v1/integrations/composio/settings", &s); err != nil {
			return err
		}
		f := newFormatter()
		if f.Format == "json" || f.Format == "yaml" {
			return f.Auto(s, nil, nil)
		}
		if !s.Configured {
			fmt.Println("Composio: not configured. Set a key with: crewship integration composio key set --key <ak_…>")
			return nil
		}
		fmt.Printf("Composio: configured (source: %s)\n", s.Source)
		if s.Label != "" {
			fmt.Printf("  Label:    %s\n", s.Label)
		}
		if s.BaseURL != "" {
			fmt.Printf("  Base URL: %s\n", s.BaseURL)
		}
		return nil
	},
}

var composioKeySetCmd = &cobra.Command{
	Use:   "set",
	Short: "Set & validate the workspace Composio API key (stored encrypted)",
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		key, _ := cmd.Flags().GetString("key")
		baseURL, _ := cmd.Flags().GetString("base-url")
		label, _ := cmd.Flags().GetString("label")
		if strings.TrimSpace(key) == "" {
			return fmt.Errorf("--key is required")
		}
		var s composioSettingsResponse
		if err := putJSON(client, "/api/v1/integrations/composio/settings", map[string]string{
			"api_key": key, "base_url": baseURL, "label": label,
		}, &s); err != nil {
			return err
		}
		fmt.Printf("Composio key saved & validated (source: %s).\n", s.Source)
		return nil
	},
}

var composioKeyRemoveCmd = &cobra.Command{
	Use:   "remove",
	Short: "Remove the workspace Composio API key (reverts to the server env, if any)",
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		if err := deleteJSON(client, "/api/v1/integrations/composio/settings"); err != nil {
			return err
		}
		fmt.Println("Composio workspace key removed.")
		return nil
	},
}

func init() {
	composioToolkitsCmd.Flags().String("search", "", "Filter apps by name/slug")
	composioToolkitsCmd.Flags().String("category", "", "Filter by category (e.g. email, developer-tools)")
	composioToolkitsCmd.Flags().Int("limit", 0, "Max apps to return (default server-side)")

	composioKeySetCmd.Flags().String("key", "", "Composio project API key (ak_…)")
	composioKeySetCmd.Flags().String("base-url", "", "Override Composio base URL (optional)")
	composioKeySetCmd.Flags().String("label", "", "Human-friendly project label (optional)")
	composioKeyCmd.AddCommand(composioKeyShowCmd, composioKeySetCmd, composioKeyRemoveCmd)

	composioCmd.AddCommand(composioInventoryCmd)
	composioCmd.AddCommand(composioToolkitsCmd)
	composioCmd.AddCommand(composioKeyCmd)
	integrationCmd.AddCommand(composioCmd)
}
