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
	Enabled     bool                    `json:"enabled"`
	AuthConfigs []composioAuthConfig    `json:"auth_configs"`
	Users       []composioUserInventory `json:"users"`
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

type composioTool struct {
	Slug        string          `json:"slug"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Toolkit     composioToolkit `json:"toolkit"`
}

type composioToolsResponse struct {
	Enabled bool           `json:"enabled"`
	Total   int            `json:"total"`
	Tools   []composioTool `json:"tools"`
}

type composioTriggerType struct {
	Slug        string          `json:"slug"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Type        string          `json:"type"`
	Toolkit     composioToolkit `json:"toolkit"`
}

type composioTriggerTypesResponse struct {
	Enabled  bool                  `json:"enabled"`
	Total    int                   `json:"total"`
	Triggers []composioTriggerType `json:"triggers"`
}

type composioTriggerInstance struct {
	ID            string         `json:"id"`
	TriggerName   string         `json:"trigger_name"`
	UserID        string         `json:"user_id"`
	TriggerConfig map[string]any `json:"trigger_config"`
	DisabledAt    string         `json:"disabled_at"`
}

type composioActiveTriggersResponse struct {
	Enabled  bool                      `json:"enabled"`
	Triggers []composioTriggerInstance `json:"triggers"`
}

type composioCreateTriggerResponse struct {
	Enabled bool                    `json:"enabled"`
	Trigger composioTriggerInstance `json:"trigger"`
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

var composioToolsCmd = &cobra.Command{
	Use:   "tools <toolkit>",
	Short: "List the tools a Composio toolkit exposes (e.g. github has 846)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		search, _ := cmd.Flags().GetString("search")
		limit, _ := cmd.Flags().GetInt("limit")

		path := "/api/v1/integrations/composio/tools"
		q := url.Values{}
		q.Set("toolkit", args[0])
		if search != "" {
			q.Set("search", search)
		}
		if limit > 0 {
			q.Set("limit", strconv.Itoa(limit))
		}
		path += "?" + q.Encode()

		var res composioToolsResponse
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
		rows := make([][]string, 0, len(res.Tools))
		for _, t := range res.Tools {
			rows = append(rows, []string{t.Slug, t.Name, t.Description})
		}
		f.Table([]string{"SLUG", "NAME", "DESCRIPTION"}, rows)
		fmt.Printf("\nShowing %d of %d tools for %q. Narrow with --search.\n", len(res.Tools), res.Total, args[0])
		return nil
	},
}

var composioTriggersCmd = &cobra.Command{
	Use:   "triggers",
	Short: "Composio triggers (event subscriptions like GMAIL_NEW_MESSAGE)",
}

var composioTriggersTypesCmd = &cobra.Command{
	Use:   "types",
	Short: "List available Composio trigger types (filter by toolkit)",
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		toolkit, _ := cmd.Flags().GetString("toolkit")
		search, _ := cmd.Flags().GetString("search")
		limit, _ := cmd.Flags().GetInt("limit")

		path := "/api/v1/integrations/composio/triggers"
		q := url.Values{}
		if toolkit != "" {
			q.Set("toolkit", toolkit)
		}
		if search != "" {
			q.Set("search", search)
		}
		if limit > 0 {
			q.Set("limit", strconv.Itoa(limit))
		}
		if enc := q.Encode(); enc != "" {
			path += "?" + enc
		}

		var res composioTriggerTypesResponse
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
		rows := make([][]string, 0, len(res.Triggers))
		for _, t := range res.Triggers {
			rows = append(rows, []string{t.Slug, t.Toolkit.Slug, t.Type, t.Description})
		}
		f.Table([]string{"SLUG", "TOOLKIT", "TYPE", "DESCRIPTION"}, rows)
		fmt.Printf("\nShowing %d of %d trigger types. Narrow with --toolkit / --search.\n", len(res.Triggers), res.Total)
		return nil
	},
}

var composioTriggersActiveCmd = &cobra.Command{
	Use:   "active",
	Short: "List active Composio trigger instances (across all users)",
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		var res composioActiveTriggersResponse
		if err := getJSON(client, "/api/v1/integrations/composio/triggers/active", &res); err != nil {
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
		rows := make([][]string, 0, len(res.Triggers))
		for _, t := range res.Triggers {
			state := "active"
			if t.DisabledAt != "" {
				state = "disabled"
			}
			rows = append(rows, []string{t.ID, t.TriggerName, t.UserID, state})
		}
		f.Table([]string{"ID", "TRIGGER", "USER_ID", "STATE"}, rows)
		return nil
	},
}

var composioTriggersEnableCmd = &cobra.Command{
	Use:   "enable <slug>",
	Short: "Create/enable a Composio trigger instance for a user",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		user, _ := cmd.Flags().GetString("user")
		if strings.TrimSpace(user) == "" {
			return fmt.Errorf("--user is required (the Composio user_id the trigger fires for)")
		}
		var res composioCreateTriggerResponse
		if err := postJSON(client, "/api/v1/integrations/composio/triggers", map[string]any{
			"slug": args[0], "user_id": user,
		}, &res); err != nil {
			return err
		}
		fmt.Printf("Trigger %s enabled for user %q (id: %s).\n", args[0], res.Trigger.UserID, res.Trigger.ID)
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

var composioConnectCmd = &cobra.Command{
	Use:   "connect <toolkit>",
	Short: "Start an OAuth connect link to authorize an app for a Composio user",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		user, _ := cmd.Flags().GetString("user")
		if strings.TrimSpace(user) == "" {
			return fmt.Errorf("--user is required (the Composio user_id to connect the account under)")
		}
		var res struct {
			RedirectURL string `json:"redirect_url"`
			UserID      string `json:"user_id"`
		}
		if err := postJSON(client, "/api/v1/integrations/composio/connect", map[string]string{
			"toolkit": args[0], "user_id": user,
		}, &res); err != nil {
			return err
		}
		fmt.Printf("Open this URL to authorize %s for user %q:\n\n  %s\n", args[0], res.UserID, res.RedirectURL)
		return nil
	},
}

func init() {
	composioConnectCmd.Flags().String("user", "", "Composio user_id to connect the account under (required)")
	composioToolkitsCmd.Flags().String("search", "", "Filter apps by name/slug")
	composioToolkitsCmd.Flags().String("category", "", "Filter by category (e.g. email, developer-tools)")
	composioToolkitsCmd.Flags().Int("limit", 0, "Max apps to return (default server-side)")
	composioToolsCmd.Flags().String("search", "", "Filter tools by name/slug")
	composioToolsCmd.Flags().Int("limit", 0, "Max tools to return (default server-side)")

	composioTriggersTypesCmd.Flags().String("toolkit", "", "Filter trigger types by toolkit slug")
	composioTriggersTypesCmd.Flags().String("search", "", "Filter trigger types by name/slug")
	composioTriggersTypesCmd.Flags().Int("limit", 0, "Max trigger types to return (default server-side)")
	composioTriggersEnableCmd.Flags().String("user", "", "Composio user_id the trigger fires for (required)")
	composioTriggersCmd.AddCommand(composioTriggersTypesCmd, composioTriggersActiveCmd, composioTriggersEnableCmd)

	composioKeySetCmd.Flags().String("key", "", "Composio project API key (ak_…)")
	composioKeySetCmd.Flags().String("base-url", "", "Override Composio base URL (optional)")
	composioKeySetCmd.Flags().String("label", "", "Human-friendly project label (optional)")
	composioKeyCmd.AddCommand(composioKeyShowCmd, composioKeySetCmd, composioKeyRemoveCmd)

	composioCmd.AddCommand(composioInventoryCmd)
	composioCmd.AddCommand(composioToolkitsCmd)
	composioCmd.AddCommand(composioToolsCmd)
	composioCmd.AddCommand(composioTriggersCmd)
	composioCmd.AddCommand(composioKeyCmd)
	composioCmd.AddCommand(composioConnectCmd)
	integrationCmd.AddCommand(composioCmd)
}
