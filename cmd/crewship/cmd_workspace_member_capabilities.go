package main

// CLI subcommands for per-member capability admin
// (PRD-SLASH-CAPABILITIES-2026 §6.8).
//
// Mirror of the dashboard Members capability grid for operators who
// prefer the terminal. Four commands under
// `crewship workspace member capabilities`:
//
//   capabilities list <user>            — show grants for a single member
//   capabilities grant <user> <cap>...  — add capabilities incrementally
//   capabilities revoke <user> <cap>... — remove capabilities incrementally
//   capabilities preset <user> <bundle> — apply chat / power / admin
//
// All require the caller be ADMIN+ in the active workspace; the
// server enforces this via the same handler the dashboard PATCH uses.
// CLI exits non-zero on 403 so a misconfigured CI pipeline fails
// loudly instead of silently no-oping.

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/crewship-ai/crewship/internal/cli"
)

var workspaceMemberCapabilitiesCmd = &cobra.Command{
	Use:     "capabilities",
	Aliases: []string{"caps", "capability"},
	Short:   "Manage per-member capability grants",
	Long: `Per-member capabilities grant individual high-value actions
(create routines, skills, credentials, ...) without promoting the user
to a wider role. ADMIN+ required to mutate.

Capabilities are workspace-scoped — a user in multiple workspaces
configures each separately. The chat capability is always implied and
cannot be granted or revoked individually; removing chat means
removing the member entirely (use 'workspace member remove').

Bundles map to common combinations:
  chat   — chat-only baseline (default for new MEMBERs)
  power  — chat + routine + issue + memory (trusted team members)
  admin  — full set including credential lifecycle`,
}

var workspaceMemberCapsListCmd = &cobra.Command{
	Use:   "list <user-id>",
	Short: "Show capabilities granted to one member",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := requireAuth(); err != nil {
			return err
		}
		if err := requireWorkspace(); err != nil {
			return err
		}
		client := newAPIClient()
		wsID := client.GetWorkspaceID()
		userID := args[0]
		resp, err := client.Get(
			fmt.Sprintf("/api/v1/workspaces/%s/members/%s/capabilities", wsID, userID),
		)
		if err != nil {
			return err
		}
		if err := cli.CheckError(resp); err != nil {
			return err
		}
		var out struct {
			UserID       string   `json:"user_id"`
			Role         string   `json:"role"`
			Capabilities []string `json:"capabilities"`
		}
		if err := cli.ReadJSON(resp, &out); err != nil {
			return err
		}
		f := newFormatter()
		headers := []string{"USER", "ROLE", "CAPABILITIES"}
		rows := [][]string{{out.UserID, out.Role, strings.Join(out.Capabilities, ", ")}}
		return f.Auto(out, headers, rows)
	},
}

var workspaceMemberCapsGrantCmd = &cobra.Command{
	Use:   "grant <user-id> <capability> [<capability>...]",
	Short: "Grant one or more capabilities to a member",
	Long: `Grant capabilities incrementally. Existing grants are preserved.
Examples:
  crewship workspace member capabilities grant ludmila routine.create
  crewship workspace member capabilities grant ludmila routine.create issue.create memory.write

Valid capability strings: chat, routine.create, skill.create,
credential.create, credential.rotate, issue.create, memory.write.

Server rejects unknown capabilities (typo guard) with a 400.`,
	Args: cobra.MinimumNArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		return mutateCaps(args[0], "grant", args[1:])
	},
}

var workspaceMemberCapsRevokeCmd = &cobra.Command{
	Use:   "revoke <user-id> <capability> [<capability>...]",
	Short: "Revoke one or more capabilities from a member",
	Long: `Revoke capabilities incrementally. Other grants are preserved.
Revoking chat is rejected by the server — chat is always implied;
remove the member entirely with 'workspace member remove' instead.`,
	Args: cobra.MinimumNArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		return mutateCaps(args[0], "revoke", args[1:])
	},
}

var workspaceMemberCapsPresetCmd = &cobra.Command{
	Use:   "preset <user-id> <chat|power|admin>",
	Short: "Apply a named capability bundle to a member",
	Long: `Replace the member's capability set with the named bundle.
This overwrites existing grants — use 'grant' for incremental edits.

Bundles:
  chat   — chat only
  power  — chat + routine.create + issue.create + memory.write
  admin  — full set, including credential.create + credential.rotate

The OWNER target is immutable (server 403s) regardless of bundle.
Caller cannot mutate their own row (downgrade-then-restore defence).`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		userID := args[0]
		preset := strings.ToLower(args[1])
		if preset != "chat" && preset != "power" && preset != "admin" {
			return fmt.Errorf("unknown preset %q (valid: chat, power, admin)", preset)
		}
		return patchCaps(userID, map[string]string{"preset": preset})
	},
}

// mutateCaps wraps the grant / revoke incremental shape into a
// patchCaps call. Centralised so the wire-shape contract lives in
// exactly one place.
func mutateCaps(userID, op string, caps []string) error {
	if op != "grant" && op != "revoke" {
		return fmt.Errorf("internal: unknown op %q", op)
	}
	body := map[string][]string{op: caps}
	bodyBytes, _ := json.Marshal(body)
	return patchCapsRaw(userID, bodyBytes)
}

func patchCaps(userID string, body any) error {
	bodyBytes, err := json.Marshal(body)
	if err != nil {
		return err
	}
	return patchCapsRaw(userID, bodyBytes)
}

func patchCapsRaw(userID string, bodyBytes []byte) error {
	if err := requireAuth(); err != nil {
		return err
	}
	if err := requireWorkspace(); err != nil {
		return err
	}
	client := newAPIClient()
	wsID := client.GetWorkspaceID()
	path := fmt.Sprintf("/api/v1/workspaces/%s/members/%s/capabilities", wsID, userID)
	resp, err := client.Patch(path, json.RawMessage(bodyBytes))
	if err != nil {
		return err
	}
	if err := cli.CheckError(resp); err != nil {
		return err
	}
	var out struct {
		UserID       string   `json:"user_id"`
		Role         string   `json:"role"`
		Capabilities []string `json:"capabilities"`
	}
	if err := cli.ReadJSON(resp, &out); err != nil {
		return err
	}
	cli.PrintSuccess(fmt.Sprintf(
		"%s (%s): %s",
		out.UserID, out.Role, strings.Join(out.Capabilities, ", "),
	))
	return nil
}

func init() {
	workspaceMemberCapabilitiesCmd.AddCommand(workspaceMemberCapsListCmd)
	workspaceMemberCapabilitiesCmd.AddCommand(workspaceMemberCapsGrantCmd)
	workspaceMemberCapabilitiesCmd.AddCommand(workspaceMemberCapsRevokeCmd)
	workspaceMemberCapabilitiesCmd.AddCommand(workspaceMemberCapsPresetCmd)
	workspaceMemberCmd.AddCommand(workspaceMemberCapabilitiesCmd)
}
