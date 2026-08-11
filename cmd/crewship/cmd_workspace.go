package main

import (
	"fmt"
	"strings"

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
						return cli.NotFoundf("workspace %q not found or not accessible", args[0])
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
			ID                         string  `json:"id"`
			Name                       string  `json:"name"`
			Slug                       string  `json:"slug"`
			CreatedAt                  string  `json:"created_at"`
			LogoURL                    *string `json:"logo_url"`
			PreferredLanguage          *string `json:"preferred_language"`
			AllowPrivilegedCredentials bool    `json:"allow_privileged_credentials"`
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
			{"Allow privileged credentials", fmt.Sprintf("%t", ws.AllowPrivilegedCredentials)},
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
		if flags.Changed("allow-privileged-credentials") {
			v, _ := flags.GetBool("allow-privileged-credentials")
			body["allow_privileged_credentials"] = v
		}
		// Audit retention windows (#1887). Gated on flags.Changed so an
		// unset flag never sends a 0 — which the server reads as an explicit
		// "keep forever" and would silently switch pruning off for anyone who
		// ran `workspace update --name x`.
		if flags.Changed("credential-audit-retention-days") {
			v, _ := flags.GetInt("credential-audit-retention-days")
			body["credential_audit_retention_days"] = v
		}
		if flags.Changed("audit-log-retention-days") {
			v, _ := flags.GetInt("audit-log-retention-days")
			body["audit_log_retention_days"] = v
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

// workspaceDeleteCmd deletes a workspace. Owner-only and irreversible
// (soft-delete cascade over crews + agents), so it requires the operator
// to re-type the slug via --confirm, mirroring the type-the-slug UI
// confirm (#866.2). The typed slug is sent as confirm_slug; the server
// re-validates it, the last-workspace guard, and OWNER role.
var workspaceDeleteCmd = &cobra.Command{
	Use:   "delete [slug-or-id]",
	Short: "Delete a workspace (owner only, irreversible)",
	Args:  cobra.MaximumNArgs(1),
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

		confirm, _ := cmd.Flags().GetString("confirm")
		if confirm == "" {
			return fmt.Errorf("--confirm <slug> is required: re-type the workspace slug to confirm deletion")
		}

		if err := confirmAction(cmd, fmt.Sprintf(
			"Permanently delete workspace %q and ALL its crews & agents? This cannot be undone.", wsID)); err != nil {
			return err
		}

		client := newAPIClient()
		client.WorkspaceID = wsID
		resp, err := client.Do("DELETE", "/api/v1/workspaces/"+wsID, map[string]string{"confirm_slug": confirm})
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		resp.Body.Close()

		cli.PrintSuccess("Workspace deleted.")
		return nil
	},
}

// workspaceMemberCmd groups member management subcommands.
var workspaceMemberCmd = &cobra.Command{
	Use:     "member",
	Aliases: []string{"members"},
	Short:   "Manage workspace members",
}

// workspaceMemberRow is one row of GET /workspaces/{id}/members.
//
// The server nests the person under `user` (memberResponse in
// internal/api/workspaces_membership.go). This used to be read as a flat
// `email`, so the EMAIL and NAME columns printed empty and
// `crewship audit --user <email>` could never resolve anyone — the very
// convenience that flag exists for. The flat fields stay as a fallback:
// reading one shape should not mean refusing the other.
type workspaceMemberRow struct {
	ID        string `json:"id"`
	UserID    string `json:"user_id"`
	Email     string `json:"email"`
	FullName  string `json:"full_name"`
	Role      string `json:"role"`
	CreatedAt string `json:"created_at"`
	User      *struct {
		ID       string `json:"id"`
		Email    string `json:"email"`
		FullName string `json:"full_name"`
	} `json:"user,omitempty"`
}

func (m workspaceMemberRow) email() string {
	if m.User != nil && m.User.Email != "" {
		return m.User.Email
	}
	return m.Email
}

func (m workspaceMemberRow) fullName() string {
	if m.User != nil && m.User.FullName != "" {
		return m.User.FullName
	}
	return m.FullName
}

// normalized copies the resolved email/name back onto the flat fields so the
// machine formats carry what the table prints.
//
// Without this, `--format json` re-marshalled the struct exactly as decoded:
// a top-level `"email": ""` sitting next to a populated `user.email`, because
// the server (memberResponse, internal/api/workspaces_membership.go) only ever
// sends the nested shape. A field that is present and always empty is worse
// than an absent one — an absent key sends a consumer looking for the real
// one, an empty key makes `select(.email==$e)` come back with nothing and read
// as "no such member". scripts/test-harness/test-run-stream.sh drew exactly
// that conclusion and skipped its cross-tenant assertion with a false reason
// for the whole life of the case (#1829).
//
// The nested object is left intact: anything already written against
// `.user.email` keeps working.
func (m workspaceMemberRow) normalized() workspaceMemberRow {
	m.Email = m.email()
	m.FullName = m.fullName()
	return m
}

// resolveWorkspaceMemberID maps a caller-supplied id onto the
// workspace_members ROW id that PATCH/DELETE /workspaces/{id}/members/{id}
// consume.
//
// `member add` takes a USER id (the API deliberately refuses an email — see
// addMemberRequest) while `member role` / `member remove` take a MEMBERSHIP
// id. That asymmetry is invisible at the call site and the server's answer for
// getting it wrong is a bare 404 "Member not found" — indistinguishable from
// "that person is not in this workspace". Accept either form here so the
// obvious call works, and name both columns when neither matches.
//
// Best-effort by construction: if the roster cannot be read (permissions, a
// dead server, an older build) the argument goes through untouched and the
// server answers as it always did. Resolution is a convenience, never a new
// failure mode.
func resolveWorkspaceMemberID(client *cli.Client, wsID, arg string) (string, error) {
	resp, err := client.Get("/api/v1/workspaces/" + wsID + "/members")
	if err != nil {
		return arg, nil
	}
	if err := cli.CheckError(resp); err != nil {
		return arg, nil
	}
	var members []workspaceMemberRow
	if err := cli.ReadJSON(resp, &members); err != nil {
		return arg, nil
	}
	if len(members) == 0 {
		return arg, nil
	}
	for _, m := range members {
		if m.ID == arg {
			return arg, nil
		}
	}
	for _, m := range members {
		if m.UserID != "" && m.UserID == arg {
			return m.ID, nil
		}
	}
	return "", fmt.Errorf(
		"no workspace member matches %q: it is neither a MEMBER ID nor a USER ID in 'crewship workspace member list'",
		arg)
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

		var members []workspaceMemberRow
		if err := cli.ReadJSON(resp, &members); err != nil {
			return err
		}

		f := newFormatter()
		// MEMBER ID is the workspace_members row id — the identifier the
		// `member role` / `member remove` commands PATCH/DELETE by. Show it
		// first so the CLI advertises the same id the API consumes; USER ID
		// stays for cross-referencing user-scoped commands.
		headers := []string{"MEMBER ID", "USER ID", "EMAIL", "NAME", "ROLE", "JOINED"}
		var rows [][]string
		for i, m := range members {
			// Normalise in place so json/yaml/ndjson render the same values
			// the table below prints — see normalized().
			members[i] = m.normalized()
			rows = append(rows, []string{m.ID, truncateID(m.UserID, 12), m.email(), m.fullName(), m.Role, m.CreatedAt})
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

var workspaceMemberRoleCmd = &cobra.Command{
	Use:   "role <member-id-or-user-id> <ROLE>",
	Short: "Change a member's workspace role",
	Long: `Change a workspace member's role.

MANAGER+ only, subject to the ladder: you can only grant a role below your
own, you cannot modify a member ranked above you, and the last OWNER
cannot be demoted. The first argument takes either column from
'workspace member list' — the MEMBER ID (the membership row the API
consumes) or the USER ID of the person. <ROLE> is one of OWNER, ADMIN,
MANAGER, MEMBER, VIEWER.`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}

		role := strings.ToUpper(args[1])

		client := newAPIClient()
		wsID := client.GetWorkspaceID()
		memberID, err := resolveWorkspaceMemberID(client, wsID, args[0])
		if err != nil {
			return err
		}
		resp, err := client.Patch("/api/v1/workspaces/"+wsID+"/members/"+memberID, map[string]string{
			"role": role,
		})
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		resp.Body.Close()

		cli.PrintSuccess(fmt.Sprintf("Member role changed to %s.", role))
		return nil
	},
}

var workspaceMemberRemoveCmd = &cobra.Command{
	Use:   "remove <member-id-or-user-id>",
	Short: "Remove a member from the workspace",
	Long: `Remove a member from the workspace.

Takes either column from 'crewship workspace member list': the MEMBER ID
(the membership row, which is what the API consumes) or the USER ID of the
person. Both resolve to the same removal.

Accepting both exists to defuse an asymmetry that is invisible at the call
site: 'member add' takes a USER id, while the endpoint behind 'member
remove' and 'member role' matches only a membership row. Passing the user id
used to answer a bare "Member not found", which reads as "that person is not
in this workspace" rather than "wrong id".`,
	Args: cobra.ExactArgs(1),
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
		memberID, err := resolveWorkspaceMemberID(client, wsID, args[0])
		if err != nil {
			return err
		}
		resp, err := client.Delete("/api/v1/workspaces/" + wsID + "/members/" + memberID)
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

	// Deliberately not "Invitation sent": no mail is sent. CreateInvitation
	// holds no mailer, so this writes a row and nothing reaches the invitee.
	// `workspace member invite` is the command that actually gets someone in.
	cli.PrintSuccess(fmt.Sprintf("Invitation recorded for %s (%s role).", inv.Email, inv.Role))
	cli.PrintWarning("No email was sent — no mailer is wired. Use `crewship workspace member invite` to create the account and get a setup link.")
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
	workspaceUpdateCmd.Flags().Bool("allow-privileged-credentials", false,
		"Load credentials into a --privileged crew's sidecar despite the collapsed UID 1001/1002 isolation boundary (#1032, default false — fails closed)")
	workspaceUpdateCmd.Flags().Int("credential-audit-retention-days", 0,
		"Days of credential_audit history to keep; 0 means keep forever. Unset uses the 90-day default (#1887)")
	workspaceUpdateCmd.Flags().Int("audit-log-retention-days", 0,
		"Days of audit_logs history to keep; 0 means keep forever, which is the default — audit_logs is the compliance trail (#1887)")

	workspaceMemberAddCmd.Flags().String("role", "MEMBER", "Role: MEMBER|ADMIN")
	workspaceMemberRemoveCmd.Flags().BoolP("yes", "y", false, "Skip confirmation")

	workspaceDeleteCmd.Flags().String("confirm", "", "Workspace slug, re-typed to confirm deletion (required)")
	workspaceDeleteCmd.Flags().BoolP("yes", "y", false, "Skip the interactive confirmation prompt")

	workspaceInviteCreateCmd.Flags().String("role", "MEMBER", "Role: MEMBER|ADMIN")
	// Mirror the role flag on the parent so `workspace invite <email> --role ADMIN` works.
	workspaceInviteCmd.Flags().String("role", "MEMBER", "Role: MEMBER|ADMIN")

	workspaceMemberCmd.AddCommand(workspaceMemberListCmd)
	workspaceMemberCmd.AddCommand(workspaceMemberAddCmd)
	workspaceMemberCmd.AddCommand(workspaceMemberRoleCmd)
	workspaceMemberCmd.AddCommand(workspaceMemberRemoveCmd)

	workspaceInviteCmd.AddCommand(workspaceInviteListCmd)
	workspaceInviteCmd.AddCommand(workspaceInviteCreateCmd)

	workspaceCmd.AddCommand(workspaceListCmd)
	workspaceCmd.AddCommand(workspaceUseCmd)
	workspaceCmd.AddCommand(workspaceGetCmd)
	workspaceCmd.AddCommand(workspaceCreateCmd)
	workspaceCmd.AddCommand(workspaceUpdateCmd)
	workspaceCmd.AddCommand(workspaceDeleteCmd)
	workspaceCmd.AddCommand(workspaceMemberCmd)
	workspaceCmd.AddCommand(workspaceInviteCmd)
}
