package main

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

// recipeCmd exposes the curated recipe catalogue (1-click crew templates
// baked into the binary — internal/recipes) over the CLI. The four
// endpoints existed for the dashboard install Sheet only; agents install
// recipes through these commands instead of hand-rolled HTTP.

// recipeRow mirrors the wire shape of GET /api/v1/recipes entries; only
// the fields the CLI renders are typed.
type recipeRow struct {
	Slug        string `json:"slug"`
	Name        string `json:"name"`
	Description string `json:"description"`
	CrewSlug    string `json:"crew_slug"`
	Credentials []struct {
		EnvVarName string `json:"env_var_name"`
	} `json:"credentials"`
	MCPServers []struct {
		Name string `json:"name"`
	} `json:"mcp_servers"`
}

var recipeCmd = &cobra.Command{
	Use:   "recipe",
	Short: "Browse and install crew recipes (1-click crew + credentials + MCP templates)",
	Long: `Recipes are curated crew templates baked into the server: one install
creates the crew, its credentials, and its MCP servers atomically.

Examples:
  crewship recipe list
  crewship recipe get code-review-crew
  crewship recipe preview code-review-crew
  crewship recipe install code-review-crew --credential GITHUB_TOKEN=ghp_xxx`,
}

var recipeListCmd = &cobra.Command{
	Use:   "list",
	Short: "List the recipe catalogue",
	RunE: func(cmd *cobra.Command, _ []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		client := newAPIClient()
		resp, err := client.Get("/api/v1/recipes")
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var rows []recipeRow
		if err := cli.ReadJSON(resp, &rows); err != nil {
			return err
		}
		f := newFormatter()
		headers := []string{"SLUG", "NAME", "CREW SLUG", "CREDENTIALS", "MCP", "DESCRIPTION"}
		table := make([][]string, 0, len(rows))
		for _, r := range rows {
			creds := make([]string, 0, len(r.Credentials))
			for _, c := range r.Credentials {
				creds = append(creds, c.EnvVarName)
			}
			mcp := make([]string, 0, len(r.MCPServers))
			for _, m := range r.MCPServers {
				mcp = append(mcp, m.Name)
			}
			table = append(table, []string{
				r.Slug, r.Name, r.CrewSlug,
				strings.Join(creds, ","), strings.Join(mcp, ","), r.Description,
			})
		}
		return f.Auto(rows, headers, table)
	},
}

var recipeGetCmd = &cobra.Command{
	Use:   "get <slug>",
	Short: "Show one recipe's full definition",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		client := newAPIClient()
		resp, err := client.Get("/api/v1/recipes/" + url.PathEscape(args[0]))
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		// Full manifest passthrough: the recipe shape (credentials with
		// labels/types, MCP server definitions) is richer than the table
		// row, so detail mode decodes into a generic map.
		var detail map[string]interface{}
		if err := cli.ReadJSON(resp, &detail); err != nil {
			return err
		}
		f := newFormatter()
		pairs := [][]string{
			{"Slug", str(detail["slug"])},
			{"Name", str(detail["name"])},
			{"Description", str(detail["description"])},
			{"Crew slug", str(detail["crew_slug"])},
		}
		return f.AutoDetail(detail, pairs)
	},
}

var recipePreviewCmd = &cobra.Command{
	Use:   "preview <slug>",
	Short: "Dry-run an install: which credentials are needed vs already present",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()
		resp, err := client.Get("/api/v1/recipes/" + url.PathEscape(args[0]) + "/preview")
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var preview struct {
			NeededCredentials   []string        `json:"needed_credentials"`
			ExistingCredentials map[string]bool `json:"existing_credentials"`
			CrewSlugAvailable   bool            `json:"crew_slug_available"`
			ResolvedCrewSlug    string          `json:"resolved_crew_slug"`
		}
		if err := cli.ReadJSON(resp, &preview); err != nil {
			return err
		}
		f := newFormatter()
		existing := make([]string, 0, len(preview.ExistingCredentials))
		for name, has := range preview.ExistingCredentials {
			if has {
				existing = append(existing, name)
			}
		}
		pairs := [][]string{
			{"Crew slug", preview.ResolvedCrewSlug},
			{"Slug free", fmt.Sprintf("%t", preview.CrewSlugAvailable)},
			{"Credentials needed", strings.Join(preview.NeededCredentials, ", ")},
			{"Credentials reused", strings.Join(existing, ", ")},
		}
		if err := f.AutoDetail(preview, pairs); err != nil {
			return err
		}
		if f.Format == "table" || f.Format == "" {
			if len(preview.NeededCredentials) > 0 {
				fmt.Printf("\nInstall with:\n  crewship recipe install %s", args[0])
				for _, c := range preview.NeededCredentials {
					fmt.Printf(" --credential %s=<value>", c)
				}
				fmt.Println()
			} else {
				fmt.Printf("\nAll credentials present. Install with:\n  crewship recipe install %s\n", args[0])
			}
		}
		return nil
	},
}

var recipeInstallCmd = &cobra.Command{
	Use:   "install <slug>",
	Short: "Install a recipe: crew + credentials + MCP servers in one transaction",
	Long: `Install a recipe into the current workspace. Credentials the workspace
already has (matched by env var name) are reused; new ones are supplied
with repeated --credential flags.

Examples:
  crewship recipe install code-review-crew --credential GITHUB_TOKEN=ghp_xxx
  crewship recipe install standup-crew            # all credentials already present`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		credPairs, _ := cmd.Flags().GetStringArray("credential")
		labelPairs, _ := cmd.Flags().GetStringArray("label")
		creds, err := parseKeyValuePairs(credPairs, "--credential")
		if err != nil {
			return err
		}
		labels, err := parseKeyValuePairs(labelPairs, "--label")
		if err != nil {
			return err
		}

		client := newAPIClient()
		resp, err := client.Post("/api/v1/recipes/"+url.PathEscape(args[0])+"/install", map[string]any{
			"credential_values": creds,
			"account_labels":    labels,
		})
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var out struct {
			CrewID            string   `json:"crew_id"`
			CrewSlug          string   `json:"crew_slug"`
			CredentialsAdded  []string `json:"credentials_added"`
			CredentialsReused []string `json:"credentials_reused"`
			MCPServersAdded   []string `json:"mcp_servers_added"`
		}
		if err := cli.ReadJSON(resp, &out); err != nil {
			return err
		}
		f := newFormatter()
		pairs := [][]string{
			{"Crew", fmt.Sprintf("%s (%s)", out.CrewSlug, out.CrewID)},
			{"Credentials added", strings.Join(out.CredentialsAdded, ", ")},
			{"Credentials reused", strings.Join(out.CredentialsReused, ", ")},
			{"MCP servers", strings.Join(out.MCPServersAdded, ", ")},
		}
		if err := f.AutoDetail(out, pairs); err != nil {
			return err
		}
		if f.Format == "table" || f.Format == "" {
			cli.PrintSuccess(fmt.Sprintf("Recipe installed — crew %q is ready.", out.CrewSlug))
		}
		return nil
	},
}

// parseKeyValuePairs parses repeated KEY=VALUE flag values into a map.
// The value may itself contain '=' (tokens often do); only the first
// '=' splits. flagName is used in error messages.
func parseKeyValuePairs(pairs []string, flagName string) (map[string]string, error) {
	out := map[string]string{}
	for _, p := range pairs {
		key, value, found := strings.Cut(p, "=")
		if !found || strings.TrimSpace(key) == "" {
			return nil, fmt.Errorf("%s %q must be KEY=VALUE", flagName, p)
		}
		out[strings.TrimSpace(key)] = value
	}
	return out, nil
}

func init() {
	recipeInstallCmd.Flags().StringArray("credential", nil, "credential value as ENV_VAR_NAME=value (repeatable)")
	recipeInstallCmd.Flags().StringArray("label", nil, "account label as ENV_VAR_NAME=label (repeatable, optional)")

	recipeCmd.AddCommand(recipeListCmd)
	recipeCmd.AddCommand(recipeGetCmd)
	recipeCmd.AddCommand(recipePreviewCmd)
	recipeCmd.AddCommand(recipeInstallCmd)
	rootCmd.AddCommand(recipeCmd)
}
