package main

// crewship crew member set-role — PATCH /api/v1/crews/{crewId}/members/{memberId}.

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

var crewMemberSetRoleCmd = &cobra.Command{
	Use:   "set-role <crew-slug-or-id> <member-id> [role]",
	Short: "Set or clear a crew member's per-crew role override",
	Long: `Pin a crew member to a role inside this crew (OWNER, ADMIN, MANAGER,
MEMBER or VIEWER), or clear the override with --clear so the member inherits
their workspace role again. Requires workspace ADMIN/OWNER, and the role cannot
exceed your own.

Examples:
  crewship crew member set-role core mem_123 MANAGER
  crewship crew member set-role core mem_123 --clear`,
	Args: cobra.RangeArgs(2, 3),
	RunE: func(cmd *cobra.Command, args []string) error {
		clear, _ := cmd.Flags().GetBool("clear")
		role := ""
		if len(args) == 3 {
			// The server ranks roles by their upper-case names.
			role = strings.ToUpper(strings.TrimSpace(args[2]))
		}
		switch {
		case clear && role != "":
			return fmt.Errorf("pass either a role or --clear, not both")
		case !clear && role == "":
			return fmt.Errorf("a role is required (OWNER, ADMIN, MANAGER, MEMBER, VIEWER), or pass --clear")
		}
		member, err := guestCLIPathSegment(args[1])
		if err != nil {
			return err
		}
		client, err := requireAuthAndWorkspace()
		if err != nil {
			return err
		}
		crewID, err := resolveCrewID(client, args[0])
		if err != nil {
			return err
		}
		path := "/api/v1/crews/" + url.PathEscape(crewID) + "/members/" + member
		if err := patchJSON(client, path, map[string]string{"role": role}, nil); err != nil {
			return err
		}
		if clear {
			cli.PrintSuccess("Crew role override cleared; member inherits the workspace role.")
		} else {
			cli.PrintSuccess("Crew role set to " + role + ".")
		}
		return nil
	},
}

func init() {
	crewMemberSetRoleCmd.Flags().Bool("clear", false, "Clear the per-crew override (member inherits the workspace role)")
	crewMemberCmd.AddCommand(crewMemberSetRoleCmd)
}
