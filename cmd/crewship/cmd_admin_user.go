//go:build !clionly

package main

// `crewship admin user …` — the per-person admin actions behind Admin ›
// Users, over HTTP:
//
//	sessions <email>                      GET  /api/v1/admin/users/{id}/sessions
//	revoke-session <email> <session-id>   POST /api/v1/admin/users/{id}/sessions/{sid}/revoke
//	sign-out <email>                      POST /api/v1/admin/users/{id}/sessions/revoke-all
//	unlock <email>                        POST /api/v1/admin/users/{id}/unlock
//
// People are named by email, the way an operator knows them; the id is
// resolved through GET /api/v1/admin/users, so an address the caller may
// not administer is "not found" here exactly as it is on the server.
//
// These are the server-backed siblings of the host-side recovery verbs in
// cmd_admin.go (`admin sessions list`, `admin invalidate-sessions`), which
// stay for the case where the server or the login is what is broken.

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

// adminUserSessions mirrors internal/api's adminUserSessionsResponse.
type adminUserSessions struct {
	Sessions []struct {
		ID         string `json:"id" yaml:"id"`
		CreatedAt  string `json:"created_at" yaml:"created_at"`
		LastUsedAt string `json:"last_used_at" yaml:"last_used_at"`
		ExpiresAt  string `json:"expires_at" yaml:"expires_at"`
		UserAgent  string `json:"user_agent" yaml:"user_agent"`
		IP         string `json:"ip" yaml:"ip"`
		Current    bool   `json:"current" yaml:"current"`
	} `json:"sessions" yaml:"sessions"`
	CLITokens []struct {
		ID         string   `json:"id" yaml:"id"`
		Name       string   `json:"name" yaml:"name"`
		Scopes     []string `json:"scopes" yaml:"scopes"`
		CreatedAt  string   `json:"created_at" yaml:"created_at"`
		LastUsedAt *string  `json:"last_used_at" yaml:"last_used_at"`
		ExpiresAt  *string  `json:"expires_at" yaml:"expires_at"`
	} `json:"cli_tokens" yaml:"cli_tokens"`
}

var adminUserCmd = &cobra.Command{
	Use:   "user",
	Short: "Per-person admin actions: signed-in devices, sign-out, unlock",
	Long: `Act on one person's account on the server the CLI targets. People are named
by email. You can act on members of your current workspace; the instance
owner can act on any account. An ADMIN cannot act on an OWNER.

  crewship admin user sessions jana@example.com
  crewship admin user revoke-session jana@example.com <session-id>
  crewship admin user sign-out jana@example.com
  crewship admin user unlock jana@example.com`,
}

var adminUserSessionsCmd = &cobra.Command{
	Use:   "sessions <email>",
	Short: "List a person's signed-in devices and CLI tokens",
	Long: `GET /api/v1/admin/users/{id}/sessions: every active sign-in (device, IP,
last use) and every live CLI token (name, scopes, last use — never the
secret). The session marked * is the one you are using now.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, id, err := adminUserTarget(args[0])
		if err != nil {
			return err
		}
		var out adminUserSessions
		if err := getJSON(client, "/api/v1/admin/users/"+url.PathEscape(id)+"/sessions", &out); err != nil {
			return err
		}
		return resolvedFormatter(cmd).AutoHuman(out, func() {
			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "KIND\tID\tDEVICE\tIP\tLAST USED\tCREATED")
			for _, s := range out.Sessions {
				id := s.ID
				if s.Current {
					id += " *"
				}
				fmt.Fprintf(w, "session\t%s\t%s\t%s\t%s\t%s\n", id, dashIfBlank(s.UserAgent), dashIfBlank(s.IP), shortAdminTime(s.LastUsedAt), shortAdminTime(s.CreatedAt))
			}
			for _, t := range out.CLITokens {
				last := "-"
				if t.LastUsedAt != nil {
					last = shortAdminTime(*t.LastUsedAt)
				}
				scopes := "all"
				if len(t.Scopes) > 0 {
					scopes = strings.Join(t.Scopes, ",")
				}
				fmt.Fprintf(w, "cli-token\t%s\t%s (%s)\t-\t%s\t%s\n", t.ID, t.Name, scopes, last, shortAdminTime(t.CreatedAt))
			}
			_ = w.Flush()
			if len(out.Sessions)+len(out.CLITokens) == 0 {
				fmt.Fprintln(os.Stderr, "(no active sessions or CLI tokens)")
			}
		})
	},
}

var adminUserRevokeSessionCmd = &cobra.Command{
	Use:   "revoke-session <email> <session-id>",
	Short: "Sign a person out of one device",
	Long: `POST /api/v1/admin/users/{id}/sessions/{session-id}/revoke. The device has to
sign in again; the person's other devices and CLI tokens are untouched.
Session ids come from 'crewship admin user sessions <email>'.`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, id, err := adminUserTarget(args[0])
		if err != nil {
			return err
		}
		path := "/api/v1/admin/users/" + url.PathEscape(id) + "/sessions/" + url.PathEscape(args[1]) + "/revoke"
		if err := postJSON(client, path, map[string]any{}, nil); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Signed %s out of session %s.\n", args[0], args[1])
		return nil
	},
}

var adminUserSignOutCmd = &cobra.Command{
	Use:   "sign-out <email>",
	Short: "Sign a person out of every device",
	Long: `POST /api/v1/admin/users/{id}/sessions/revoke-all. Every browser session
ends; the password stays as it is, and CLI tokens are separate credentials
that stay valid (revoke those in Settings › Access & Secrets). Logged as
admin_invalidate, the same reason the host-side 'admin invalidate-sessions'
writes.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, id, err := adminUserTarget(args[0])
		if err != nil {
			return err
		}
		var out struct {
			Revoked int `json:"revoked" yaml:"revoked"`
		}
		if err := postJSON(client, "/api/v1/admin/users/"+url.PathEscape(id)+"/sessions/revoke-all", map[string]any{}, &out); err != nil {
			return err
		}
		return resolvedFormatter(cmd).AutoHuman(out, func() {
			fmt.Printf("Signed %s out of %d session(s).\n", args[0], out.Revoked)
		})
	},
}

var adminUserUnlockCmd = &cobra.Command{
	Use:   "unlock <email>",
	Short: "Lift a sign-in lockout after too many failed passwords",
	Long: `POST /api/v1/admin/users/{id}/unlock. Clears the lock and the failed-attempt
counter so the person can sign in straight away instead of waiting for the
lock to expire. 'crewship admin list-users --locked-only' lists who is locked.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		client, id, err := adminUserTarget(args[0])
		if err != nil {
			return err
		}
		if err := postJSON(client, "/api/v1/admin/users/"+url.PathEscape(id)+"/unlock", map[string]any{}, nil); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Unlocked %s.\n", args[0])
		return nil
	},
}

// adminUserTarget resolves an email to the account id the admin routes take.
func adminUserTarget(email string) (*cli.Client, string, error) {
	email = strings.TrimSpace(email)
	if email == "" {
		return nil, "", cli.WithExitCode(errors.New("an email is required"), cli.ExitValidation)
	}
	client, err := requireAuthAndWorkspace()
	if err != nil {
		return nil, "", err
	}
	users, _, err := fetchAdminUsers(client)
	if err != nil {
		return nil, "", err
	}
	for _, u := range users {
		if strings.EqualFold(u.Email, email) {
			if u.ID == "" {
				return nil, "", errors.New("the server did not return user ids; upgrade it to use 'crewship admin user'")
			}
			return client, u.ID, nil
		}
	}
	return nil, "", cli.WithExitCode(
		fmt.Errorf("no account %s among the people you administer (members of the current workspace, or every account for an instance administrator)", email),
		cli.ExitNotFound)
}

func dashIfBlank(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

func init() {
	adminUserCmd.AddCommand(adminUserSessionsCmd, adminUserRevokeSessionCmd, adminUserSignOutCmd, adminUserUnlockCmd)
	adminCmd.AddCommand(adminUserCmd)
}
