package main

import (
	"fmt"
	"net/http"
	"net/url"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

// runSkillLifecycle resolves the skill and sends the one POST its command owns.
// Each command passes its own call, with the route spelled out in full, so the
// route-contract test can see which endpoint it hits.
func runSkillLifecycle(args []string, done string, post func(c *cli.Client, ws, skillID string) (*http.Response, error)) error {
	if err := requireAuth(); err != nil {
		return err
	}
	if err := requireWorkspace(); err != nil {
		return err
	}
	client := newAPIClient()
	skillID, err := resolveSkillID(client, args[0])
	if err != nil {
		return err
	}
	resp, err := post(client, url.PathEscape(client.GetWorkspaceID()), url.PathEscape(skillID))
	if err != nil {
		return err
	}
	if err := cli.CheckError(resp); err != nil {
		return err
	}
	_ = resp.Body.Close()
	cli.PrintSuccess(fmt.Sprintf("Skill %s %s", args[0], done))
	return nil
}

var skillArchiveCmd = &cobra.Command{
	Use:   "archive <skill-slug-or-id>",
	Short: "Move a skill to the Archived view",
	Long: `Archive a skill by hand. An archived skill is hidden from the library and
from agent pickers; agents that already hold it keep it. Needs the OWNER or
ADMIN role. Archiving an archived skill changes nothing.

Examples:
  crewship skill archive my-old-skill`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runSkillLifecycle(args, "archived", func(c *cli.Client, ws, id string) (*http.Response, error) {
			return c.Post("/api/v1/workspaces/"+ws+"/skills/"+id+"/archive", nil)
		})
	},
}

var skillRestoreCmd = &cobra.Command{
	Use:   "restore <skill-slug-or-id>",
	Short: "Bring an archived skill back",
	Long: `Restore an archived skill to active. The keeper sweep still judges it by use
and assignment: a restored skill that no agent holds or calls is stale again
after 30 days and archived after 90. Needs the OWNER or ADMIN role.

Examples:
  crewship skill restore my-old-skill`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runSkillLifecycle(args, "restored", func(c *cli.Client, ws, id string) (*http.Response, error) {
			return c.Post("/api/v1/workspaces/"+ws+"/skills/"+id+"/restore", nil)
		})
	},
}

func init() {
	skillCmd.AddCommand(skillArchiveCmd)
	skillCmd.AddCommand(skillRestoreCmd)
}
