package main

import (
	"fmt"
	"syscall"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// authCmd groups self-service account commands. Login/logout/whoami stay
// top-level for muscle memory; this parent hosts the newer account
// mutations like password change (#867.1).
var authCmd = &cobra.Command{
	Use:   "auth",
	Short: "Manage your own account (password, ...)",
}

var authPasswdCmd = &cobra.Command{
	Use:   "passwd",
	Short: "Change your account password",
	Long: `Change your own account password.

Prompts for the current and new password when run interactively. For
scripting, pass --current and --new (the new password must be at least 8
characters). Changing your password revokes your OTHER active sessions;
the session you run this from stays signed in.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}

		current, _ := cmd.Flags().GetString("current")
		newPw, _ := cmd.Flags().GetString("new")

		if current == "" {
			pw, err := promptPassword("Current password: ")
			if err != nil {
				return err
			}
			current = pw
		}
		if newPw == "" {
			pw, err := promptPassword("New password: ")
			if err != nil {
				return err
			}
			confirm, err := promptPassword("Confirm new password: ")
			if err != nil {
				return err
			}
			if pw != confirm {
				return fmt.Errorf("passwords do not match")
			}
			newPw = pw
		}

		if len(newPw) < 8 {
			return fmt.Errorf("new password must be at least 8 characters")
		}

		client := newAPIClient()
		// User-scoped endpoint — no workspace context.
		client.WorkspaceID = ""
		resp, err := client.Post("/api/v1/users/me/password", map[string]string{
			"current_password": current,
			"new_password":     newPw,
		})
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		resp.Body.Close()

		cli.PrintSuccess("Password changed. Your other sessions have been signed out.")
		return nil
	},
}

// promptPassword reads a password from the TTY without echo.
func promptPassword(label string) (string, error) {
	fmt.Print(label)
	b, err := term.ReadPassword(int(syscall.Stdin))
	fmt.Println()
	if err != nil {
		return "", fmt.Errorf("read password: %w", err)
	}
	return string(b), nil
}

func init() {
	authPasswdCmd.Flags().String("current", "", "Current password (prompts if omitted)")
	authPasswdCmd.Flags().String("new", "", "New password, min 8 chars (prompts if omitted)")
	authCmd.AddCommand(authPasswdCmd)
}
