package main

import (
	"fmt"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

var workspaceCmd = &cobra.Command{
	Use:     "workspace",
	Aliases: []string{"ws"},
	Short:   "Manage workspaces",
}

var workspaceListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all workspaces",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}

		client := newAPIClient()
		// Workspace list doesn't need workspace_id param
		client.WorkspaceID = ""
		resp, err := client.Get("/api/v1/workspaces")
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var workspaces []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
			Slug string `json:"slug"`
			Role string `json:"currentUserRole"`
		}
		if err := cli.ReadJSON(resp, &workspaces); err != nil {
			return err
		}

		f := newFormatter()
		headers := []string{"SLUG", "NAME", "ID", "ROLE"}
		var rows [][]string
		activeWS := cli.ResolveWorkspace(flagWorkspace, cliCfg)
		for _, ws := range workspaces {
			marker := ""
			if ws.Slug == activeWS || ws.ID == activeWS {
				marker = " *"
			}
			rows = append(rows, []string{ws.Slug + marker, ws.Name, ws.ID, ws.Role})
		}
		return f.Auto(workspaces, headers, rows)
	},
}

var workspaceUseCmd = &cobra.Command{
	Use:   "use <slug-or-id>",
	Short: "Set the default workspace",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		// Validate workspace exists if user is logged in
		localCfg, err := cli.LoadConfig()
		if err != nil {
			localCfg = &cli.CLIConfig{}
		}
		if localCfg.Token != "" {
			client := cli.NewClient(
				cli.ResolveServer(flagServer, localCfg),
				localCfg.Token, "",
			)
			resp, err := client.Get("/api/v1/workspaces")
			if err == nil && resp.StatusCode == 200 {
				var workspaces []struct {
					ID   string `json:"id"`
					Slug string `json:"slug"`
					Name string `json:"name"`
				}
				if cli.ReadJSON(resp, &workspaces) == nil {
					found := false
					for _, ws := range workspaces {
						if ws.Slug == args[0] || ws.ID == args[0] {
							found = true
							break
						}
					}
					if !found {
						return fmt.Errorf("workspace %q not found or not accessible", args[0])
					}
				}
			}
		}

		localCfg.Workspace = args[0]
		if err := cli.SaveConfig(localCfg); err != nil {
			return err
		}
		cli.PrintSuccess(fmt.Sprintf("Default workspace set to: %s", args[0]))
		return nil
	},
}

var workspaceCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a new workspace",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}

		name, _ := cmd.Flags().GetString("name")
		slug, _ := cmd.Flags().GetString("slug")

		if name == "" {
			return fmt.Errorf("--name is required")
		}

		lang, _ := cmd.Flags().GetString("language")

		body := map[string]interface{}{"name": name}
		if slug != "" {
			body["slug"] = slug
		}
		if lang != "" {
			body["preferred_language"] = lang
		}

		client := newAPIClient()
		client.WorkspaceID = ""
		resp, err := client.Post("/api/v1/workspaces", body)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var created struct {
			ID   string `json:"id"`
			Name string `json:"name"`
			Slug string `json:"slug"`
		}
		if err := cli.ReadJSON(resp, &created); err != nil {
			return err
		}

		cli.PrintSuccess(fmt.Sprintf("Workspace created: %s (%s)", created.Slug, created.ID))
		return nil
	},
}

var workspaceGetCmd = &cobra.Command{
	Use:   "get [slug-or-id]",
	Short: "Show workspace details",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}

		wsID := cli.ResolveWorkspace(flagWorkspace, cliCfg)
		if len(args) > 0 {
			wsID = args[0]
		}
		if wsID == "" {
			return fmt.Errorf("no workspace specified")
		}

		client := newAPIClient()
		client.WorkspaceID = wsID
		resp, err := client.Get("/api/v1/workspaces/" + wsID)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var ws struct {
			ID                string  `json:"id"`
			Name              string  `json:"name"`
			Slug              string  `json:"slug"`
			CreatedAt         string  `json:"created_at"`
			LogoURL           *string `json:"logo_url"`
			PreferredLanguage *string `json:"preferred_language"`
		}
		if err := cli.ReadJSON(resp, &ws); err != nil {
			return err
		}

		lang := "-"
		if ws.PreferredLanguage != nil {
			lang = *ws.PreferredLanguage
		}

		f := newFormatter()
		pairs := [][]string{
			{"Name", ws.Name},
			{"Slug", ws.Slug},
			{"Language", lang},
			{"ID", ws.ID},
			{"Created", ws.CreatedAt},
		}
		return f.AutoDetail(ws, pairs)
	},
}

var workspaceUpdateCmd = &cobra.Command{
	Use:   "update",
	Short: "Update the current workspace",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		body := map[string]interface{}{}
		flags := cmd.Flags()

		if flags.Changed("name") {
			v, _ := flags.GetString("name")
			body["name"] = v
		}
		if flags.Changed("slug") {
			v, _ := flags.GetString("slug")
			body["slug"] = v
		}
		if flags.Changed("language") {
			v, _ := flags.GetString("language")
			body["preferred_language"] = v
		}

		if len(body) == 0 {
			return fmt.Errorf("no fields to update")
		}

		client := newAPIClient()
		wsID := client.GetWorkspaceID()
		if wsID == "" {
			return fmt.Errorf("no workspace selected")
		}
		resp, err := client.Patch("/api/v1/workspaces/"+wsID, body)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		resp.Body.Close()

		cli.PrintSuccess("Workspace updated.")
		return nil
	},
}

func init() {
	workspaceCreateCmd.Flags().String("name", "", "Workspace name (required)")
	workspaceCreateCmd.Flags().String("slug", "", "Workspace slug (auto-generated from name)")
	workspaceCreateCmd.Flags().String("language", "", "Preferred language (e.g. cs, en)")

	workspaceUpdateCmd.Flags().String("name", "", "Workspace name")
	workspaceUpdateCmd.Flags().String("slug", "", "Workspace slug")
	workspaceUpdateCmd.Flags().String("language", "", "Preferred language (e.g. cs, en)")

	workspaceCmd.AddCommand(workspaceListCmd)
	workspaceCmd.AddCommand(workspaceUseCmd)
	workspaceCmd.AddCommand(workspaceGetCmd)
	workspaceCmd.AddCommand(workspaceCreateCmd)
	workspaceCmd.AddCommand(workspaceUpdateCmd)
}
