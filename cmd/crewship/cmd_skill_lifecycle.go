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
func runSkillLifecycle(cmd *cobra.Command, args []string, done string, post func(c *cli.Client, ws, skillID string) (*http.Response, error)) error {
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
	raw, err := readServerDocument(resp)
	if err != nil {
		return err
	}
	// -f json: the server's {skill_id, lifecycle_state} — the state the
	// skill is actually in, which for a no-op archive is not "archived by
	// this call".
	if handled, err := machineMutationResult(cmd, raw, map[string]any{"skill_id": skillID}); handled {
		return err
	}
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
		return runSkillLifecycle(cmd, args, "archived", func(c *cli.Client, ws, id string) (*http.Response, error) {
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
		return runSkillLifecycle(cmd, args, "restored", func(c *cli.Client, ws, id string) (*http.Response, error) {
			return c.Post("/api/v1/workspaces/"+ws+"/skills/"+id+"/restore", nil)
		})
	},
}

var skillLockCmd = &cobra.Command{
	Use:   "lock <skill-slug-or-id>",
	Short: "Lock a skill against autonomous rewrites",
	Long: `Lock a skill against autonomous rewrites. While it is locked no agent, sidecar tool,
memory consolidation, generation or proposed-skill approval can change its
content, bundled re-seeds leave it alone, and the lifecycle sweep never moves
it to stale or archived. A blocked write fails with an error naming the lock
and is recorded in the journal. Skills are shared across the instance, so this
needs an instance administrator who is OWNER or ADMIN of the workspace, using a
session or a full-access CLI token. Assigning a skill grants nothing, and scoped
tokens, run tokens and agent identities are refused.

Examples:
  crewship skill lock my-skill`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runSkillLifecycle(cmd, args, "locked", func(c *cli.Client, ws, id string) (*http.Response, error) {
			return c.Post("/api/v1/workspaces/"+ws+"/skills/"+id+"/lock", nil)
		})
	},
}

var skillUnlockCmd = &cobra.Command{
	Use:   "unlock <skill-slug-or-id>",
	Short: "Lift a skill's lock",
	Long: `Unlock a skill so imports, proposals and the lifecycle sweep may change it
again. Needs an instance administrator who is OWNER or ADMIN of the workspace,
using a session or a full-access CLI token.

Examples:
  crewship skill unlock my-skill`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runSkillLifecycle(cmd, args, "unlocked", func(c *cli.Client, ws, id string) (*http.Response, error) {
			return c.Post("/api/v1/workspaces/"+ws+"/skills/"+id+"/unlock", nil)
		})
	},
}

func init() {
	skillCmd.AddCommand(skillLockCmd)
	skillCmd.AddCommand(skillUnlockCmd)
	skillCmd.AddCommand(skillArchiveCmd)
	skillCmd.AddCommand(skillRestoreCmd)
}
