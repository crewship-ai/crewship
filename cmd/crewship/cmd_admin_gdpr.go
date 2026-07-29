//go:build !clionly

package main

// `crewship admin gdpr export|delete` — the two operations that answer a
// data-subject request: a copy of everything held about a person, or its
// erasure.
//
// Both existed only as buttons (CLAUDE.md rule 3), and those buttons were
// broken: the admin API is workspace-scoped by middleware and the panel sent
// no workspace_id, so every press came back 400. A CLI is also how a queue of
// requests gets handled without forty clicks, and how the handling gets
// scripted and logged somewhere other than a browser.

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

// gdprDeleteResult is the receipt: the audit row's id plus what went.
type gdprDeleteResult struct {
	ActionID string         `json:"action_id"`
	Deleted  map[string]int `json:"deleted"`
}

// gdprUserPath builds the workspace-scoped endpoint for one subject.
func gdprUserPath(client *cli.Client, userID string) string {
	return fmt.Sprintf("/api/v1/admin/users/%s/data?workspace_id=%s",
		url.PathEscape(userID), url.QueryEscape(client.GetWorkspaceID()))
}

// resolveGDPRSubject accepts a user id or an email, the same courtesy
// `crewship audit --user` extends — an operator holding a request from a
// person has their email, not their cuid.
func resolveGDPRSubject(client *cli.Client, raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("a user id or email is required")
	}
	if !strings.Contains(raw, "@") {
		return raw, nil
	}
	id, err := findWorkspaceMemberUserIDByEmail(client, client.GetWorkspaceID(), raw)
	if err != nil {
		return "", fmt.Errorf("resolve %q: %w", raw, err)
	}
	if id == "" {
		return "", fmt.Errorf("no workspace member with the email %q", raw)
	}
	return id, nil
}

var adminGDPRCmd = &cobra.Command{
	Use:   "gdpr",
	Short: "Answer a data-subject request: export or erase one person's data (admin)",
	Long: `Export everything this workspace holds about one person, or erase it.

Both write an append-only audit row (who, why, what it touched) — that record
is the answer to "prove you handled that request".`,
}

var adminGDPRExportCmd = &cobra.Command{
	Use:   "export <user-id-or-email>",
	Short: "Export every row referencing a user, as JSON (admin)",
	Long: `Right of access: a read-only snapshot of everything referencing this user.

Writes the JSON to stdout by default so it can be piped or redirected; use
--out to write a file instead.

Examples:
  crewship admin gdpr export someone@example.com
  crewship admin gdpr export usr_123 --out subject-access.json`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()
		userID, err := resolveGDPRSubject(client, args[0])
		if err != nil {
			return err
		}

		resp, err := client.Get(gdprUserPath(client, userID))
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var payload map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
		pretty, err := json.MarshalIndent(payload, "", "  ")
		if err != nil {
			return fmt.Errorf("encode export: %w", err)
		}

		out, _ := cmd.Flags().GetString("out")
		if out != "" {
			// 0600: this file is every row about a person.
			if err := os.WriteFile(out, append(pretty, '\n'), 0o600); err != nil {
				return fmt.Errorf("write %s: %w", out, err)
			}
			cli.PrintSuccess(fmt.Sprintf("Export written to %s", out))
			return nil
		}
		fmt.Println(string(pretty))
		return nil
	},
}

var adminGDPRDeleteCmd = &cobra.Command{
	Use:     "delete <user-id-or-email>",
	Aliases: []string{"erase"},
	Short:   "Irreversibly erase every row referencing a user (admin)",
	Long: `Right to erasure: removes every row referencing this user in this workspace.

The reason is not paperwork — it is the audit trail, and it is what answers
"why was this person's data removed" a year from now. This cannot be undone.

Examples:
  crewship admin gdpr delete someone@example.com --reason "SAR #1234" --yes`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		reason, _ := cmd.Flags().GetString("reason")
		if strings.TrimSpace(reason) == "" {
			return fmt.Errorf("--reason is required: it is the audit trail for an irreversible erasure")
		}
		yes, _ := cmd.Flags().GetBool("yes")
		if !yes {
			return fmt.Errorf("this erases every row referencing the user and cannot be undone — re-run with --yes to confirm")
		}
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()
		userID, err := resolveGDPRSubject(client, args[0])
		if err != nil {
			return err
		}

		// Do("DELETE", …) rather than Delete(): this endpoint takes a body,
		// and the reason in it is the whole point of the audit row.
		resp, err := client.Do("DELETE", gdprUserPath(client, userID),
			map[string]string{"reason": strings.TrimSpace(reason)})
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var result gdprDeleteResult
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}

		return resolvedFormatter(cmd).AutoHuman(result, func() {
			// The action id is the receipt. Print it first: it is what an
			// operator records against the request they were answering.
			cli.PrintSuccess(fmt.Sprintf("Erased. Audit action: %s", result.ActionID))
			for table, n := range result.Deleted {
				if n > 0 {
					fmt.Printf("  %-28s %d\n", table, n)
				}
			}
		})
	},
}

func init() {
	adminGDPRExportCmd.Flags().String("out", "", "Write the export to this file instead of stdout")
	adminGDPRDeleteCmd.Flags().String("reason", "", "Why this data is being erased (recorded in the audit trail; required)")
	adminGDPRDeleteCmd.Flags().Bool("yes", false, "Confirm the irreversible erasure")
	adminGDPRCmd.AddCommand(adminGDPRExportCmd)
	adminGDPRCmd.AddCommand(adminGDPRDeleteCmd)
	adminCmd.AddCommand(adminGDPRCmd)
}
