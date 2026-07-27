package main

import (
	"fmt"

	"github.com/crewship-ai/crewship/internal/cli"
	"github.com/spf13/cobra"
)

// `crewship workspace member invite <email>` — the CLI half of the
// provisioning endpoint (API↔CLI parity, CLAUDE.md core rule #3).
//
// Distinct from `member add <user-id>`, which needs the person to already
// have an account, and from `workspace invite`, which writes an invitation
// row nobody delivers because no mailer is wired. This one creates the
// account if the email is new, adds the membership, and prints a setup link
// to hand over.
var workspaceMemberInviteCmd = &cobra.Command{
	Use:   "invite <email>",
	Short: "Add someone by email, creating their account if needed",
	Long: "Creates the account when the email is new, adds it to the current\n" +
		"workspace, and prints a one-time setup link. Send the link however you\n" +
		"like — the person sets their own password with it. No mail is sent.",
	Args: cobra.ExactArgs(1),
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
		resp, err := client.Post("/api/v1/workspaces/"+wsID+"/members/provision", map[string]string{
			"email": args[0],
			"role":  role,
		})
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}

		var out struct {
			Email       string `json:"email"`
			Role        string `json:"role"`
			CreatedUser bool   `json:"created_user"`
			SetupURL    string `json:"setup_url"`
			ExpiresAt   string `json:"expires_at"`
		}
		if err := cli.ReadJSON(resp, &out); err != nil {
			return err
		}

		if out.CreatedUser {
			cli.PrintSuccess(fmt.Sprintf("Created %s as %s.", out.Email, out.Role))
		} else {
			// No link for an account somebody already controls — one would
			// reset their password, and this command prints it to whoever
			// ran it. See ProvisionMember.
			cli.PrintSuccess(fmt.Sprintf("Added existing account %s as %s. They sign in with their existing password.", out.Email, out.Role))
			return nil
		}
		// Printed on its own line, unadorned, so it survives a copy-paste and
		// a pipe into another command. It is the only time it is shown.
		fmt.Println()
		fmt.Println(out.SetupURL)
		fmt.Println()
		fmt.Printf("Send that link to %s — it expires %s and is shown only once.\n", out.Email, out.ExpiresAt)
		return nil
	},
}

func init() {
	workspaceMemberInviteCmd.Flags().String("role", "MEMBER", "Role to grant: ADMIN, MANAGER, MEMBER, VIEWER")
	workspaceMemberCmd.AddCommand(workspaceMemberInviteCmd)
}
