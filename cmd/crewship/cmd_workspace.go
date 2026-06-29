package main

import (
	"fmt"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

func truncateID(id string, n int) string {
	if len(id) < n {
		return id
	}
	return id[:n]
}

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
		// Validate workspace exists if user is logged in. Resolve auth through
		// the active-profile overlay so a profile-authenticated user is
		// validated against the right server/token, not the empty top-level.
		localCfg, err := cli.LoadConfig()
		if err != nil {
			// LoadConfig returns an empty config for a missing file, so a real
			// error means an unreadable / malformed file — don't continue with
			// an empty config and clobber the user's saved profiles on save.
			return fmt.Errorf("load CLI config: %w", err)
		}
		eff := localCfg.WithActiveProfile(flagProfile)
		if eff.Token != "" {
			client := cli.NewClient(
				cli.EffectiveServer(flagServer, flagProfile, localCfg),
				eff.Token, "",
			)
			// Bind the token to the configured server host so `workspace use`
			// never leaks it to a mismatched --server/CREWSHIP_SERVER target
			// (issue #571 / CLI2).
			client.TokenHost = serverHost(eff.Server)
			client.AllowHostMismatch = flagAllowServerMismatch || envAllowServerMismatch()
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

		// Write to the active target (profile when one is active, else
		// top-level) so the selection isn't masked by the overlay on the next
		// command.
		localCfg.SetWorkspaceTarget(flagProfile, args[0])
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

// workspaceMemberCmd groups member management subcommands.
var workspaceMemberCmd = &cobra.Command{
	Use:     "member",
	Aliases: []string{"members"},
	Short:   "Manage workspace members",
}

var workspaceMemberListCmd = &cobra.Command{
	Use:   "list",
	Short: "List workspace members",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		client := newAPIClient()
		wsID := client.GetWorkspaceID()
		resp, err := client.Get("/api/v1/workspaces/" + wsID + "/members")
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var members []struct {
			ID        string `json:"id"`
			UserID    string `json:"user_id"`
			Email     string `json:"email"`
			FullName  string `json:"full_name"`
			Role      string `json:"role"`
			CreatedAt string `json:"created_at"`
		}
		if err := cli.ReadJSON(resp, &members); err != nil {
			return err
		}

		f := newFormatter()
		headers := []string{"ID", "EMAIL", "NAME", "ROLE", "JOINED"}
		var rows [][]string
		for _, m := range members {
			rows = append(rows, []string{truncateID(m.UserID, 12), m.Email, m.FullName, m.Role, m.CreatedAt})
		}
		return f.Auto(members, headers, rows)
	},
}

var workspaceMemberAddCmd = &cobra.Command{
	Use:   "add <user-id>",
	Short: "Add a member to the workspace",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		role, _ := cmd.Flags().GetString("role")
		if role == "" {
			role = "MEMBER"
		}

		client := newAPIClient()
		wsID := client.GetWorkspaceID()
		resp, err := client.Post("/api/v1/workspaces/"+wsID+"/members", map[string]string{
			"user_id": args[0],
			"role":    role,
		})
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		resp.Body.Close()

		cli.PrintSuccess(fmt.Sprintf("Member added with role %s.", role))
		return nil
	},
}

var workspaceMemberRemoveCmd = &cobra.Command{
	Use:   "remove <user-id>",
	Short: "Remove a member from the workspace",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		if err := confirmAction(cmd, fmt.Sprintf("Remove member %q from workspace?", args[0])); err != nil {
			return err
		}

		client := newAPIClient()
		wsID := client.GetWorkspaceID()
		resp, err := client.Delete("/api/v1/workspaces/" + wsID + "/members/" + args[0])
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		resp.Body.Close()

		cli.PrintSuccess("Member removed.")
		return nil
	},
}

// workspaceInviteCmd groups invitation subcommands. It also acts as a
// shortcut: `crewship workspace invite <email>` invites a user directly
// without requiring the `create` subcommand. Both paths call
// sendWorkspaceInvitation so there is no Cobra flag-delegation hack.
var workspaceInviteCmd = &cobra.Command{
	Use:     "invite [email]",
	Aliases: []string{"invitation", "invitations"},
	Short:   "Manage workspace invitations",
	Args:    cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		// No positional arg — fall back to help (group mode).
		if len(args) == 0 {
			return cmd.Help()
		}
		role, _ := cmd.Flags().GetString("role")
		return sendWorkspaceInvitation(args[0], role)
	},
}

// sendWorkspaceInvitation is the shared implementation for both the
// `workspace invite <email>` shortcut and `workspace invite create
// <email>`. Keeping it a plain function avoids relying on Cobra flag
// inheritance across delegated RunE calls.
func sendWorkspaceInvitation(email, role string) error {
	if err := requireAuth(); err != nil {
		return err
	}
	if err := requireWorkspace(); err != nil {
		return err
	}
	if role == "" {
		role = "MEMBER"
	}

	client := newAPIClient()
	wsID := client.GetWorkspaceID()
	resp, err := client.Post("/api/v1/workspaces/"+wsID+"/invitations", map[string]string{
		"email": email,
		"role":  role,
	})
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if err := cli.CheckError(resp); err != nil {
		return err
	}

	var inv struct {
		ID    string `json:"id"`
		Email string `json:"email"`
		Role  string `json:"role"`
	}
	if err := cli.ReadJSON(resp, &inv); err != nil {
		return err
	}

	cli.PrintSuccess(fmt.Sprintf("Invitation sent to %s (%s role).", inv.Email, inv.Role))
	return nil
}

var workspaceInviteListCmd = &cobra.Command{
	Use:   "list",
	Short: "List pending workspace invitations",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		client := newAPIClient()
		wsID := client.GetWorkspaceID()
		resp, err := client.Get("/api/v1/workspaces/" + wsID + "/invitations")
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var invitations []struct {
			ID        string `json:"id"`
			Email     string `json:"email"`
			Role      string `json:"role"`
			ExpiresAt string `json:"expires_at"`
			CreatedAt string `json:"created_at"`
		}
		if err := cli.ReadJSON(resp, &invitations); err != nil {
			return err
		}

		f := newFormatter()
		headers := []string{"ID", "EMAIL", "ROLE", "EXPIRES", "CREATED"}
		var rows [][]string
		for _, inv := range invitations {
			rows = append(rows, []string{truncateID(inv.ID, 12), inv.Email, inv.Role, inv.ExpiresAt, inv.CreatedAt})
		}
		return f.Auto(invitations, headers, rows)
	},
}

var workspaceInviteCreateCmd = &cobra.Command{
	Use:   "create <email>",
	Short: "Invite a user to the workspace by email",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		role, _ := cmd.Flags().GetString("role")
		return sendWorkspaceInvitation(args[0], role)
	},
}

func init() {
	workspaceCreateCmd.Flags().String("name", "", "Workspace name (required)")
	workspaceCreateCmd.Flags().String("slug", "", "Workspace slug (auto-generated from name)")
	workspaceCreateCmd.Flags().String("language", "", "Preferred language (e.g. cs, en)")

	workspaceUpdateCmd.Flags().String("name", "", "Workspace name")
	workspaceUpdateCmd.Flags().String("slug", "", "Workspace slug")
	workspaceUpdateCmd.Flags().String("language", "", "Preferred language (e.g. cs, en)")

	workspaceMemberAddCmd.Flags().String("role", "MEMBER", "Role: MEMBER|ADMIN")
	workspaceMemberRemoveCmd.Flags().BoolP("yes", "y", false, "Skip confirmation")

	workspaceInviteCreateCmd.Flags().String("role", "MEMBER", "Role: MEMBER|ADMIN")
	// Mirror the role flag on the parent so `workspace invite <email> --role ADMIN` works.
	workspaceInviteCmd.Flags().String("role", "MEMBER", "Role: MEMBER|ADMIN")

	workspaceMemberCmd.AddCommand(workspaceMemberListCmd)
	workspaceMemberCmd.AddCommand(workspaceMemberAddCmd)
	workspaceMemberCmd.AddCommand(workspaceMemberRemoveCmd)

	workspaceInviteCmd.AddCommand(workspaceInviteListCmd)
	workspaceInviteCmd.AddCommand(workspaceInviteCreateCmd)

	workspaceCmd.AddCommand(workspaceListCmd)
	workspaceCmd.AddCommand(workspaceUseCmd)
	workspaceCmd.AddCommand(workspaceGetCmd)
	workspaceCmd.AddCommand(workspaceCreateCmd)
	workspaceCmd.AddCommand(workspaceUpdateCmd)
	workspaceCmd.AddCommand(workspaceMemberCmd)
	workspaceCmd.AddCommand(workspaceInviteCmd)
}
