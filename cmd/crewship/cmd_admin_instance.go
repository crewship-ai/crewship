//go:build !clionly

package main

// `crewship admin instance …` — Admin › People & workspaces from the
// terminal: what an instance administrator does across every workspace.
//
//	add-person <email>                  POST   /api/v1/admin/instance/people
//	grant <email> --workspace <slug>    PUT    /api/v1/admin/instance/workspaces/{ws}/members/{user}
//	revoke <email> --workspace <slug>   DELETE /api/v1/admin/instance/workspaces/{ws}/members/{user}
//	create-workspace <name>             POST   /api/v1/admin/instance/workspaces
//	transfer <slug> --to <email>        POST   /api/v1/admin/instance/workspaces/{ws}/transfer-ownership
//	delete-workspace <slug>             DELETE /api/v1/admin/instance/workspaces/{ws}
//	suspend | reactivate <email>        POST   /api/v1/admin/instance/people/{user}/suspend|reactivate
//	setup-link <email> [--revoke]       POST | DELETE /api/v1/admin/instance/people/{user}/setup-link
//	add-admin | remove-admin <email>    PUT | DELETE /api/v1/admin/instance/admins/{user}
//	audit                               GET    /api/v1/admin/instance/audit
//
// People are named by email and workspaces by slug; both resolve through the
// admin lists, which answer for the whole instance when the caller is an
// instance administrator. `add-admin --local` is the break-glass path: it
// writes the database file on this host when nobody can sign in to do it.

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

var adminInstanceCmd = &cobra.Command{
	Use:   "instance",
	Short: "Instance administration: people, access and workspaces across the server",
	Long: `Act on the whole instance: add people, give or take access in any workspace,
create, hand over and delete workspaces, suspend accounts, reissue setup
links and name the other instance administrators.

Only an instance administrator may do this. That is CREWSHIP_OWNER_EMAIL on
the server, anyone named with 'add-admin', or — while neither is set — the
owners of the oldest workspace. A workspace ADMIN or OWNER is not enough.

  crewship admin instance add-person lucie@acme.example --name "Lucie H" --access acme-ops=MEMBER
  crewship admin instance grant jana@acme.example --workspace research-lab --role ADMIN
  crewship admin instance transfer research-lab --to eva@lab.example
  crewship admin instance suspend tomas@acme.example --reason "left the company"
  crewship admin instance add-admin jana@acme.example`,
}

// instanceUser resolves an email to an account id through GET /admin/users.
func instanceUser(client *cli.Client, email string) (string, error) {
	email = strings.TrimSpace(email)
	if email == "" {
		return "", cli.WithExitCode(errors.New("an email is required"), cli.ExitValidation)
	}
	users, _, err := fetchAdminUsers(client)
	if err != nil {
		return "", err
	}
	for _, u := range users {
		if strings.EqualFold(u.Email, email) && u.ID != "" {
			return u.ID, nil
		}
	}
	return "", cli.WithExitCode(fmt.Errorf("no account %s on this instance", email), cli.ExitNotFound)
}

// instanceWorkspace resolves a slug (or an id) through GET /admin/workspaces.
func instanceWorkspace(client *cli.Client, ref string) (adminWorkspaceRow, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return adminWorkspaceRow{}, cli.WithExitCode(errors.New("a workspace slug is required"), cli.ExitValidation)
	}
	var rows []adminWorkspaceRow
	if err := getJSON(client, "/api/v1/admin/workspaces", &rows); err != nil {
		return adminWorkspaceRow{}, err
	}
	for _, w := range rows {
		if w.Slug == ref || w.ID == ref {
			return w, nil
		}
	}
	return adminWorkspaceRow{}, cli.WithExitCode(fmt.Errorf("no workspace %s on this instance", ref), cli.ExitNotFound)
}

type instanceMembership struct {
	WorkspaceID string `json:"workspace_id" yaml:"workspace_id"`
	Role        string `json:"role" yaml:"role"`
}

type instancePersonCreated struct {
	UserID      string               `json:"user_id" yaml:"user_id"`
	Email       string               `json:"email" yaml:"email"`
	Memberships []instanceMembership `json:"memberships" yaml:"memberships"`
	SetupURL    string               `json:"setup_url" yaml:"setup_url"`
	ExpiresAt   string               `json:"expires_at" yaml:"expires_at"`
}

var adminInstanceAddPersonCmd = &cobra.Command{
	Use:   "add-person <email>",
	Short: "Create an account, give it access, and print its setup link",
	Long: `POST /api/v1/admin/instance/people. Creates the account and puts it in the
workspaces named by --access <slug>=<ROLE> (repeatable; ROLE is OWNER, ADMIN,
MANAGER, MEMBER or VIEWER). No email is sent: pass the printed setup link on
yourself. It is valid for 7 days, and the person chooses their own password.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthInstance()
		if err != nil {
			return err
		}
		name, _ := cmd.Flags().GetString("name")
		access, _ := cmd.Flags().GetStringArray("access")
		body := map[string]any{"email": args[0], "full_name": name}
		ms := []instanceMembership{}
		for _, a := range access {
			slug, role, ok := strings.Cut(a, "=")
			if !ok {
				return cli.WithExitCode(fmt.Errorf("--access %q: want <workspace-slug>=<ROLE>", a), cli.ExitValidation)
			}
			ws, err := instanceWorkspace(client, slug)
			if err != nil {
				return err
			}
			ms = append(ms, instanceMembership{WorkspaceID: ws.ID, Role: strings.ToUpper(role)})
		}
		body["memberships"] = ms
		var out instancePersonCreated
		if err := postJSON(client, "/api/v1/admin/instance/people", body, &out); err != nil {
			return err
		}
		return resolvedFormatter(cmd).AutoHuman(out, func() {
			fmt.Fprintf(cmd.OutOrStdout(), "Created %s in %d workspace(s).\nSetup link (valid until %s):\n  %s\n",
				out.Email, len(out.Memberships), out.ExpiresAt, out.SetupURL)
		})
	},
}

var adminInstanceGrantCmd = &cobra.Command{
	Use:   "grant <email> --workspace <slug>",
	Short: "Give a person access to a workspace, or change their role there",
	Long: `PUT /api/v1/admin/instance/workspaces/{ws}/members/{user}. Adds the person
with --role (default MEMBER), or changes the role they have. OWNER is allowed;
the last owner of a workspace cannot be moved down — use 'transfer'.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthInstance()
		if err != nil {
			return err
		}
		wsRef, _ := cmd.Flags().GetString("workspace")
		role, _ := cmd.Flags().GetString("role")
		ws, err := instanceWorkspace(client, wsRef)
		if err != nil {
			return err
		}
		userID, err := instanceUser(client, args[0])
		if err != nil {
			return err
		}
		var out struct {
			Role    string `json:"role" yaml:"role"`
			Created bool   `json:"created" yaml:"created"`
		}
		if err := putJSON(client, "/api/v1/admin/instance/workspaces/"+url.PathEscape(ws.ID)+"/members/"+url.PathEscape(userID),
			map[string]string{"role": strings.ToUpper(role)}, &out); err != nil {
			return err
		}
		verb := "is now"
		if out.Created {
			verb = "joined as"
		}
		cli.PrintSuccess(fmt.Sprintf("%s %s %s in %s.", args[0], verb, out.Role, ws.Name))
		return nil
	},
}

var adminInstanceRevokeCmd = &cobra.Command{
	Use:   "revoke <email> --workspace <slug>",
	Short: "Take a person out of a workspace",
	Long: `DELETE /api/v1/admin/instance/workspaces/{ws}/members/{user}. Their pages
move first, exactly as when a workspace admin removes them. The last owner
stays — use 'transfer' first.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthInstance()
		if err != nil {
			return err
		}
		wsRef, _ := cmd.Flags().GetString("workspace")
		ws, err := instanceWorkspace(client, wsRef)
		if err != nil {
			return err
		}
		userID, err := instanceUser(client, args[0])
		if err != nil {
			return err
		}
		if err := deleteJSON(client, "/api/v1/admin/instance/workspaces/"+url.PathEscape(ws.ID)+"/members/"+url.PathEscape(userID)); err != nil {
			return err
		}
		cli.PrintSuccess(fmt.Sprintf("Removed %s from %s.", args[0], ws.Name))
		return nil
	},
}

var adminInstanceCreateWorkspaceCmd = &cobra.Command{
	Use:   "create-workspace <name> --slug <slug> --owner <email>",
	Short: "Create a workspace owned by someone else",
	Long: `POST /api/v1/admin/instance/workspaces. The workspace starts empty and is
owned by --owner. You are not added to it.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthInstance()
		if err != nil {
			return err
		}
		slug, _ := cmd.Flags().GetString("slug")
		owner, _ := cmd.Flags().GetString("owner")
		lang, _ := cmd.Flags().GetString("language")
		ownerID, err := instanceUser(client, owner)
		if err != nil {
			return err
		}
		body := map[string]any{"name": args[0], "slug": slug, "owner_user_id": ownerID}
		if lang != "" {
			body["preferred_language"] = lang
		}
		var out struct {
			ID   string `json:"id" yaml:"id"`
			Slug string `json:"slug" yaml:"slug"`
		}
		if err := postJSON(client, "/api/v1/admin/instance/workspaces", body, &out); err != nil {
			return err
		}
		return resolvedFormatter(cmd).AutoHuman(out, func() {
			fmt.Fprintf(cmd.OutOrStdout(), "Created %s (%s), owned by %s.\n", args[0], out.Slug, owner)
		})
	},
}

var adminInstanceTransferCmd = &cobra.Command{
	Use:   "transfer <workspace-slug> --to <email>",
	Short: "Hand a workspace to another member; the previous owner becomes ADMIN",
	Long: `POST /api/v1/admin/instance/workspaces/{ws}/transfer-ownership. The new owner
must already be a member ('grant' them first); every previous owner stays on
as ADMIN.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthInstance()
		if err != nil {
			return err
		}
		to, _ := cmd.Flags().GetString("to")
		ws, err := instanceWorkspace(client, args[0])
		if err != nil {
			return err
		}
		userID, err := instanceUser(client, to)
		if err != nil {
			return err
		}
		if err := postJSON(client, "/api/v1/admin/instance/workspaces/"+url.PathEscape(ws.ID)+"/transfer-ownership",
			map[string]string{"user_id": userID}, nil); err != nil {
			return err
		}
		cli.PrintSuccess(fmt.Sprintf("%s now owns %s.", to, ws.Name))
		return nil
	},
}

var adminInstanceDeleteWorkspaceCmd = &cobra.Command{
	Use:   "delete-workspace <slug> --confirm <slug>",
	Short: "Delete a workspace, its crews and agents",
	Long: `DELETE /api/v1/admin/instance/workspaces/{ws}. --confirm must repeat the slug.
Crews and agents are removed with it; the instance audit keeps the record.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		confirm, _ := cmd.Flags().GetString("confirm")
		if confirm != args[0] {
			return cli.WithExitCode(errors.New("--confirm must repeat the workspace slug"), cli.ExitValidation)
		}
		client, err := requireAuthInstance()
		if err != nil {
			return err
		}
		ws, err := instanceWorkspace(client, args[0])
		if err != nil {
			return err
		}
		resp, err := client.Do("DELETE", "/api/v1/admin/instance/workspaces/"+url.PathEscape(ws.ID), map[string]string{"confirm_slug": confirm})
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		_ = resp.Body.Close()
		cli.PrintSuccess(fmt.Sprintf("Deleted %s.", ws.Name))
		return nil
	},
}

var adminInstanceSuspendCmd = &cobra.Command{
	Use:   "suspend <email>",
	Short: "Block an account: no sign-in, sessions and CLI tokens end now",
	Long: `POST /api/v1/admin/instance/people/{user}/suspend. Every session and CLI token
ends; the account cannot sign in until 'reactivate'. Workspace access is kept.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthInstance()
		if err != nil {
			return err
		}
		reason, _ := cmd.Flags().GetString("reason")
		userID, err := instanceUser(client, args[0])
		if err != nil {
			return err
		}
		var out struct {
			Sessions int `json:"sessions_revoked" yaml:"sessions_revoked"`
			Tokens   int `json:"cli_tokens_revoked" yaml:"cli_tokens_revoked"`
		}
		if err := postJSON(client, "/api/v1/admin/instance/people/"+url.PathEscape(userID)+"/suspend",
			map[string]string{"reason": reason}, &out); err != nil {
			return err
		}
		cli.PrintSuccess(fmt.Sprintf("Suspended %s: %d session(s) and %d CLI token(s) ended.", args[0], out.Sessions, out.Tokens))
		return nil
	},
}

var adminInstanceReactivateCmd = &cobra.Command{
	Use:   "reactivate <email>",
	Short: "Lift a suspension",
	Long:  `POST /api/v1/admin/instance/people/{user}/reactivate. The person signs in again; ended sessions stay ended.`,
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthInstance()
		if err != nil {
			return err
		}
		userID, err := instanceUser(client, args[0])
		if err != nil {
			return err
		}
		if err := postJSON(client, "/api/v1/admin/instance/people/"+url.PathEscape(userID)+"/reactivate", map[string]any{}, nil); err != nil {
			return err
		}
		cli.PrintSuccess(fmt.Sprintf("Reactivated %s.", args[0]))
		return nil
	},
}

var adminInstanceSetupLinkCmd = &cobra.Command{
	Use:   "setup-link <email>",
	Short: "Issue a new setup link for an account nobody has signed in to yet",
	Long: `POST /api/v1/admin/instance/people/{user}/setup-link prints a fresh link and
voids the previous one. --revoke voids the pending link instead (DELETE). An
account somebody already signs in to gets no link: they reset their own password.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthInstance()
		if err != nil {
			return err
		}
		userID, err := instanceUser(client, args[0])
		if err != nil {
			return err
		}
		path := "/api/v1/admin/instance/people/" + url.PathEscape(userID) + "/setup-link"
		if revoke, _ := cmd.Flags().GetBool("revoke"); revoke {
			if err := deleteJSON(client, path); err != nil {
				return err
			}
			cli.PrintSuccess(fmt.Sprintf("Revoked the setup link for %s.", args[0]))
			return nil
		}
		var out struct {
			SetupURL  string `json:"setup_url" yaml:"setup_url"`
			ExpiresAt string `json:"expires_at" yaml:"expires_at"`
		}
		if err := postJSON(client, path, map[string]any{}, &out); err != nil {
			return err
		}
		return resolvedFormatter(cmd).AutoHuman(out, func() {
			fmt.Fprintf(cmd.OutOrStdout(), "Setup link for %s (valid until %s):\n  %s\n", args[0], out.ExpiresAt, out.SetupURL)
		})
	},
}

var adminInstanceAddAdminCmd = &cobra.Command{
	Use:   "add-admin <email>",
	Short: "Make a person an instance administrator",
	Long: `PUT /api/v1/admin/instance/admins/{user}. Naming anyone turns off the
"owners of the oldest workspace" fallback: from then on, the named admins and
CREWSHIP_OWNER_EMAIL are the whole list.

--local writes the database file on this host instead — the way in when
nobody who could do it can sign in.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if local, _ := cmd.Flags().GetBool("local"); local {
			return addInstanceAdminLocal(cmd, args[0])
		}
		client, err := requireAuthInstance()
		if err != nil {
			return err
		}
		userID, err := instanceUser(client, args[0])
		if err != nil {
			return err
		}
		if err := putJSON(client, "/api/v1/admin/instance/admins/"+url.PathEscape(userID), map[string]any{}, nil); err != nil {
			return err
		}
		cli.PrintSuccess(fmt.Sprintf("%s is now an instance administrator.", args[0]))
		return nil
	},
}

// addInstanceAdminLocal is the break-glass grant: straight into the file.
func addInstanceAdminLocal(cmd *cobra.Command, email string) error {
	db, err := openGatedLocalDB(cmd, "crewship admin instance add-admin", "crewship admin instance add-admin "+email)
	if err != nil {
		return err
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res, err := db.ExecContext(ctx,
		`UPDATE users SET instance_role = 'ADMIN' WHERE lower(email) = lower(?)`, strings.TrimSpace(email))
	if err != nil {
		return fmt.Errorf("grant instance admin: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return cli.WithExitCode(fmt.Errorf("no user with email %q", email), cli.ExitNotFound)
	}
	cli.PrintSuccess(fmt.Sprintf("%s is now an instance administrator (written to the local database).", email))
	return nil
}

var adminInstanceRemoveAdminCmd = &cobra.Command{
	Use:   "remove-admin <email>",
	Short: "Remove a named instance administrator",
	Long: `DELETE /api/v1/admin/instance/admins/{user}. Nobody removes themselves, and
CREWSHIP_OWNER_EMAIL is changed in the server's environment, not here.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := requireAuthInstance()
		if err != nil {
			return err
		}
		userID, err := instanceUser(client, args[0])
		if err != nil {
			return err
		}
		if err := deleteJSON(client, "/api/v1/admin/instance/admins/"+url.PathEscape(userID)); err != nil {
			return err
		}
		cli.PrintSuccess(fmt.Sprintf("%s is no longer an instance administrator.", args[0]))
		return nil
	},
}

type instanceAuditEntry struct {
	ID                string  `json:"id" yaml:"id"`
	UserEmail         *string `json:"user_email" yaml:"user_email"`
	Action            string  `json:"action" yaml:"action"`
	EntityType        string  `json:"entity_type" yaml:"entity_type"`
	EntityID          *string `json:"entity_id" yaml:"entity_id"`
	TargetWorkspaceID *string `json:"target_workspace_id" yaml:"target_workspace_id"`
	Metadata          string  `json:"metadata" yaml:"metadata"`
	CreatedAt         string  `json:"created_at" yaml:"created_at"`
}

var adminInstanceAuditCmd = &cobra.Command{
	Use:   "audit",
	Short: "List instance-level actions, newest first",
	Long: `GET /api/v1/admin/instance/audit. Everything done through 'admin instance':
accounts created and suspended, access granted and removed, workspaces
created, handed over and deleted. --for narrows it to one person (email) or
workspace (slug).`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		client, err := requireAuthInstance()
		if err != nil {
			return err
		}
		limit, _ := cmd.Flags().GetInt("limit")
		forRef, _ := cmd.Flags().GetString("for")
		q := url.Values{}
		q.Set("limit", fmt.Sprint(limit))
		if forRef != "" {
			if strings.Contains(forRef, "@") {
				id, err := instanceUser(client, forRef)
				if err != nil {
					return err
				}
				q.Set("entity_id", id)
			} else {
				ws, err := instanceWorkspace(client, forRef)
				if err != nil {
					return err
				}
				q.Set("entity_id", ws.ID)
			}
		}
		var rows []instanceAuditEntry
		if err := getJSON(client, "/api/v1/admin/instance/audit?"+q.Encode(), &rows); err != nil {
			return err
		}
		return resolvedFormatter(cmd).AutoHuman(rows, func() {
			tw := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "WHEN\tWHO\tACTION\tTARGET")
			for _, a := range rows {
				who, target := "-", "-"
				if a.UserEmail != nil {
					who = *a.UserEmail
				}
				if a.EntityID != nil {
					target = *a.EntityID
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", a.CreatedAt, who, a.Action, target)
			}
			_ = tw.Flush()
		})
	},
}

func init() {
	adminInstanceAddPersonCmd.Flags().String("name", "", "Full name")
	adminInstanceAddPersonCmd.Flags().StringArray("access", nil, "Workspace access as <slug>=<ROLE> (repeatable)")

	adminInstanceGrantCmd.Flags().String("workspace", "", "Workspace slug (required)")
	adminInstanceGrantCmd.Flags().String("role", "MEMBER", "OWNER | ADMIN | MANAGER | MEMBER | VIEWER")
	_ = adminInstanceGrantCmd.MarkFlagRequired("workspace")

	adminInstanceRevokeCmd.Flags().String("workspace", "", "Workspace slug (required)")
	_ = adminInstanceRevokeCmd.MarkFlagRequired("workspace")

	adminInstanceCreateWorkspaceCmd.Flags().String("slug", "", "Workspace slug (required)")
	adminInstanceCreateWorkspaceCmd.Flags().String("owner", "", "Email of the owner (required)")
	adminInstanceCreateWorkspaceCmd.Flags().String("language", "", "Agent language, e.g. cs or en")
	_ = adminInstanceCreateWorkspaceCmd.MarkFlagRequired("slug")
	_ = adminInstanceCreateWorkspaceCmd.MarkFlagRequired("owner")

	adminInstanceTransferCmd.Flags().String("to", "", "Email of the new owner (required)")
	_ = adminInstanceTransferCmd.MarkFlagRequired("to")

	adminInstanceDeleteWorkspaceCmd.Flags().String("confirm", "", "Repeat the workspace slug to confirm (required)")
	_ = adminInstanceDeleteWorkspaceCmd.MarkFlagRequired("confirm")

	adminInstanceSuspendCmd.Flags().String("reason", "", "Why, for the audit trail")

	adminInstanceSetupLinkCmd.Flags().Bool("revoke", false, "Void the pending link instead of issuing a new one")

	adminInstanceAddAdminCmd.Flags().Bool("local", false, localOnlyFlagHelp)

	adminInstanceAuditCmd.Flags().Int("limit", 100, "Rows to return (max 500)")
	adminInstanceAuditCmd.Flags().String("for", "", "One person (email) or workspace (slug)")

	adminInstanceCmd.AddCommand(
		adminInstanceAddPersonCmd, adminInstanceGrantCmd, adminInstanceRevokeCmd,
		adminInstanceCreateWorkspaceCmd, adminInstanceTransferCmd, adminInstanceDeleteWorkspaceCmd,
		adminInstanceSuspendCmd, adminInstanceReactivateCmd, adminInstanceSetupLinkCmd,
		adminInstanceAddAdminCmd, adminInstanceRemoveAdminCmd, adminInstanceAuditCmd,
	)
	adminCmd.AddCommand(adminInstanceCmd)
}
